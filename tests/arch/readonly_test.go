// Package arch_test guards architecture rules with static analysis
// (TESTING.md §3): read/write surface, forbidden imports, dependency directions.
package arch_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/k8s-ai/k8s-ai"

var forbiddenImports = []string{
	"k8s.io/client-go/kubernetes/fake",
	"k8s.io/client-go/dynamic",
}

// packageScopedImports 只允许指定生产包出现的 import（相对 internal/ 的包路径）。
var packageScopedImports = map[string][]string{
	"executor": {"os/exec"},
}

// forbiddenMethodCalls 是 client-go 写方法调用；仅 internal/executor 允许。
var forbiddenMethodCalls = []string{
	"Create", "Update", "Patch", "Delete", "Apply", "Replace", "Scale", "Exec", "PortForward",
}

// forbiddenSecretsCall 在任何包都禁止：Secret 数据读取只能走 kubectl 二进制
// （executor 内），client-go 的 Secrets() 方法永不使用（AGENTS.md LLM Safety）。
const forbiddenSecretsCall = "Secrets"

// mutationAllowedPkg 是允许 client-go 写方法的生产包（相对 internal/ 的包路径）。
const mutationAllowedPkg = "executor"

// forbiddenShellBins 是 exec.Command 首参的禁止 shell 解释器字面量。
var forbiddenShellBins = map[string]bool{"bash": true, "sh": true, "cmd": true}

func walkProductionGoFiles(t *testing.T, fn func(path string)) {
	t.Helper()
	root := filepath.Join("..", "..", "internal")
	absRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(absRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fn(path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func parseFile(t *testing.T, path string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return f
}

// relPkg 返回文件相对 internal/ 的包路径（POSIX 风格，如 "executor"）。
func relPkg(path string) string {
	root, err := filepath.Abs(filepath.Join("..", "..", "internal"))
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(filepath.Dir(rel))
}

// isShellCommandCall 检测 exec.Command/CommandContext 是否以 shell 解释器执行。
// 只捕获字面量形式（exec.Command("bash", "-c", ...)），变量拼接由代码评审兜底。
func isShellCommandCall(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		lit, ok := arg.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			continue
		}
		if s == "-c" || forbiddenShellBins[s] {
			return true
		}
	}
	return false
}

func TestProductionCodeReadOnly(t *testing.T) {
	walkProductionGoFiles(t, func(path string) {
		pkg := relPkg(path)
		inExecutor := pkg == mutationAllowedPkg || strings.HasPrefix(pkg, mutationAllowedPkg+"/")
		f := parseFile(t, path)
		importsClientGo := false
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(p, "k8s.io/client-go/") || strings.HasPrefix(p, "k8s.io/api/") {
				importsClientGo = true
			}
			for _, forb := range forbiddenImports {
				if p == forb {
					t.Errorf("%s: forbidden import %s", path, p)
				}
			}
			if allow, ok := packageScopedImports[p]; ok {
				allowed := false
				for _, a := range allow {
					if pkg == a || strings.HasPrefix(pkg, a+"/") {
						allowed = true
						break
					}
				}
				if !allowed {
					t.Errorf("%s: import %s is only allowed in internal/%s", path, p, strings.Join(allow, ", internal/"))
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			name := sel.Sel.Name
			if name == forbiddenSecretsCall {
				t.Errorf("%s: forbidden method call Secrets (Secret data never via client-go)", path)
			}
			// 只对 import client-go/api 的文件做写方法检查，避免误伤自有接口
			// （如 session.Store 的 Create/Update/Delete）。
			if importsClientGo {
				for _, m := range forbiddenMethodCalls {
					if name == m && !inExecutor {
						t.Errorf("%s: forbidden method call %s", path, m)
					}
				}
			}
			// 执行层外禁止 shell 解释器执行（bash/sh/cmd -c）；
			// 执行层内允许 policy-gated shell 路径（AGENTS.md: chat 模式下含 shell 语法的命令经人工审批后执行）。
			if !inExecutor && (name == "Command" || name == "CommandContext") && isShellCommandCall(call) {
				t.Errorf("%s: shell interpreter execution is only allowed in internal/executor (%s)", path, name)
			}
			return true
		})
	})
}

func TestDependencyDirections(t *testing.T) {
	graph := map[string]map[string]bool{}
	walkProductionGoFiles(t, func(path string) {
		f := parseFile(t, path)
		base, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			t.Fatal(err)
		}
		pkg := module + "/" + filepath.ToSlash(filepath.Dir(rel))
		if graph[pkg] == nil {
			graph[pkg] = map[string]bool{}
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(p, module) {
				graph[pkg][p] = true
			}
		}
	})

	cliPkg := module + "/internal/cli"
	for pkg := range graph {
		if pkg != cliPkg && graph[pkg][cliPkg] {
			t.Errorf("%s must not import %s (business logic must not depend on CLI)", pkg, cliPkg)
		}
	}

	modelPkg := module + "/internal/model"
	for pkg := range graph {
		if pkg != modelPkg && graph[modelPkg][pkg] {
			t.Errorf("%s must not import internal packages", modelPkg)
		}
	}

	k8sPkg := module + "/internal/kubernetes"
	for _, to := range []string{"scanner", "rule", "evidence", "diagnosis", "llm", "report", "cli"} {
		if graph[k8sPkg][module+"/internal/"+to] {
			t.Errorf("kubernetes must not import internal/%s", to)
		}
	}

	for _, to := range []string{"scanner", "kubernetes", "llm", "diagnosis"} {
		if graph[cliPkg][module+"/internal/"+to] {
			t.Errorf("cli must not import internal/%s directly (must go through service)", to)
		}
	}

	// 二期新增业务包禁止 CLI 直接依赖（经 service 组合根）；
	// webui 与 server 同属展示/基础设施层，允许 cli 直接装配。
	for _, to := range []string{"agent", "tools", "approval", "executor", "session", "audit", "mcp"} {
		if graph[cliPkg][module+"/internal/"+to] {
			t.Errorf("cli must not import internal/%s directly (must go through service)", to)
		}
	}
	// kubernetes 基础设施不得反向依赖任何业务/领域包。
	for _, to := range []string{"agent", "tools", "approval", "executor", "session", "audit", "webui", "mcp"} {
		if graph[k8sPkg][module+"/internal/"+to] {
			t.Errorf("kubernetes must not import internal/%s", to)
		}
	}
}
