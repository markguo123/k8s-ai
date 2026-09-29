package arch_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var forbiddenSkillModelFields = map[string]bool{
	"Exec":    true,
	"Script":  true,
	"Command": true,
	"Run":     true,
	"Shell":   true,
}

// TestSkillModelNoExecutionFields 锁定 R5.9：Skill 模型只能描述方法论与校验规则，
// 严禁出现执行动作/写能力字段。所有写操作仍必须走 create_resource -> internal/executor。
func TestSkillModelNoExecutionFields(t *testing.T) {
	path := filepath.Join("..", "..", "internal", "skills", "model.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				if forbiddenSkillModelFields[name.Name] {
					t.Errorf("%s: Skill 模型出现禁止字段 %s", path, name.Name)
				}
			}
		}
		return true
	})
}

// TestSkillPackageNoExecutorOrClientWrite 防止 skills 反向依赖执行层或直接拿 dynamic
// 客户端。skills 侧只做声明式模型、校验与 registry，提交仍由创建工具交给 executor。
func TestSkillPackageNoExecutorOrClientWrite(t *testing.T) {
	root := filepath.Join("..", "..", "internal", "skills")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(p, module+"/internal/executor") || p == "k8s.io/client-go/dynamic" {
				t.Errorf("%s: forbidden import %s (write path must remain internal/executor)", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
