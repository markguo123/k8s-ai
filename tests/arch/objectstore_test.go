package arch_test

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 本文件守 P8.2 的四条存储边界（方案 15 §4.4 / §11.1）。这些约束都只在「以后有人加
// 第二个后端 / 加第二个调用方」时才会被破坏，而那时最先被改的往往就是这几处，
// 靠评审记住不现实。

// objectStoreSDKImportPrefixes 是对象存储厂商 SDK 的 import 前缀。
var objectStoreSDKImportPrefixes = []string{"github.com/minio/", "github.com/aws/aws-sdk-go"}

// objectStoreSDKAllowedFile 是唯一允许出现厂商 SDK 的文件：SDK 类型一旦泄漏出这个文件，
// 「业务只认 objectstore.Store」就成了空话。
const objectStoreSDKAllowedFile = "objectstore/s3.go"

// objectsSQLAllowedFiles 是 objects 表 SQL 的允许出处（方案 15 §4.4）。
//
// 单源的理由很实际：状态机、引用计数、正文读写三处都要改这张表，同一条 UPDATE 出现在
// 两个包里，早晚有一处漏掉 CAS 条件。元数据在 MySQL、正文可能在别的后端，但登记表只有
// 这一份事实源。
var objectsSQLAllowedFiles = map[string]bool{
	"store/objects.go":       true,
	"store/objectcontent.go": true,
	"store/tx.go":            true,
	"store/migrate.go":       true,
}

// objectsSQLRe 匹配真正打到 objects 表上的语句片段。只匹配 SQL 关键字形式，避免把注释、
// 文档里的 "objects 表" 也算成违规。
var objectsSQLRe = regexp.MustCompile(`(?i)\b(from|into|update|table)\s+objects\b`)

func relInternalPath(t *testing.T, path string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "internal"))
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(rel)
}

// importPaths 返回文件的 import 路径集合。
func importPaths(t *testing.T, path string) []string {
	t.Helper()
	f := parseFile(t, path)
	var out []string
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// stringLiterals 返回文件里所有字符串字面量（含原始字符串），已去引号。
func stringLiterals(t *testing.T, path string) []string {
	t.Helper()
	f := parseFile(t, path)
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if s, err := strconv.Unquote(lit.Value); err == nil {
			out = append(out, s)
		}
		return true
	})
	return out
}

// TestObjectStorageSDKImportBoundary 保证厂商 SDK 只出现在 s3 适配器里。
func TestObjectStorageSDKImportBoundary(t *testing.T) {
	violations := 0
	walkProductionGoFiles(t, func(path string) {
		rel := relInternalPath(t, path)
		for _, imp := range importPaths(t, path) {
			for _, prefix := range objectStoreSDKImportPrefixes {
				if strings.HasPrefix(imp, prefix) && rel != objectStoreSDKAllowedFile {
					t.Errorf("%s 直接 import 了对象存储厂商 SDK %q；业务侧只准用 internal/objectstore 抽象", rel, imp)
					violations++
				}
			}
		}
	})
	if violations == 0 {
		t.Log("尚无厂商 SDK import（P8.4 落地 s3.go 后本条仍须成立）")
	}
}

// TestObjectsTableSQLSingleSource 保证 objects 表的 SQL 只长在允许的四个文件里。
func TestObjectsTableSQLSingleSource(t *testing.T) {
	hits := 0
	walkProductionGoFiles(t, func(path string) {
		rel := relInternalPath(t, path)
		for _, lit := range stringLiterals(t, path) {
			touches := objectsSQLRe.MatchString(lit) ||
				(strings.Contains(lit, "objects") && strings.Contains(lit, "payload"))
			if !touches {
				continue
			}
			hits++
			if !objectsSQLAllowedFiles[rel] {
				t.Errorf("%s 出现了 objects 表的 SQL，登记表读写必须收口在 %v", rel, sortedKeys(objectsSQLAllowedFiles))
			}
		}
	})
	if hits == 0 {
		t.Error("一条 objects 表 SQL 都没扫到：检测规则或表名已经变了，门禁本身失效比漏报更危险")
	}
}

// TestReadyzDoesNotDependOnObjectStore 钉住「对象存储故障不影响基础业务」：就绪端点的实现
// 里不许出现 objectstore。/readyz 只看平台库，桶不可达只降级文件相关能力。
func TestReadyzDoesNotDependOnObjectStore(t *testing.T) {
	const importPath = module + "/internal/objectstore"
	readyzFiles := 0
	walkProductionGoFiles(t, func(path string) {
		f := parseFile(t, path)
		defines := false
		references := false
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Name.Name == "handleReadyz" {
				defines = true
			}
		}
		if !defines {
			return
		}
		readyzFiles++
		for _, imp := range f.Imports {
			if p, err := strconv.Unquote(imp.Path.Value); err == nil && p == importPath {
				references = true
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "objectstore" {
				references = true
			}
			return true
		})
		if references {
			t.Errorf("%s 定义 handleReadyz 却引用了 objectstore：对象存储不得进入就绪判据（方案 15 §11.1）",
				relInternalPath(t, path))
		}
	})
	if readyzFiles == 0 {
		t.Error("没找到 handleReadyz 的定义，门禁失去保护对象")
	}
}

// TestObjectStoreDoesNotDependOnStore 保证抽象层不反向依赖持久层（也不写 SQL）：
// 元数据与内容后端的耦合是单向的——store 认识 objectstore.Store，objectstore 不认识 store。
func TestObjectStoreDoesNotDependOnStore(t *testing.T) {
	forbidden := map[string]bool{
		module + "/internal/store": true,
		"database/sql":             true,
	}
	scanned := 0
	walkProductionGoFiles(t, func(path string) {
		if relPkg(path) != "objectstore" {
			return
		}
		scanned++
		for _, imp := range importPaths(t, path) {
			if forbidden[imp] {
				t.Errorf("%s 引入了 %q：抽象层不能反向依赖持久层", relInternalPath(t, path), imp)
			}
		}
	})
	if scanned == 0 {
		t.Error("没有扫到 internal/objectstore 下的生产文件")
	}
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
