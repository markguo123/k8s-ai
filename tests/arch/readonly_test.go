// Package arch_test guards architecture rules with static analysis
// (TESTING.md §3): read-only surface, forbidden imports, dependency directions.
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

const module = "github.com/k8s-ai/k8s-ai"

var forbiddenImports = []string{
	"os/exec",
	"k8s.io/client-go/kubernetes/fake",
	"k8s.io/client-go/dynamic",
}

var forbiddenMethodCalls = []string{
	"Create", "Update", "Patch", "Delete", "Apply", "Replace", "Scale", "Exec", "PortForward",
	"Secrets", // 一期永不访问 Secret 资源（AGENTS.md / SECURITY.md）
}

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

func TestProductionCodeReadOnly(t *testing.T) {
	walkProductionGoFiles(t, func(path string) {
		f := parseFile(t, path)
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, forb := range forbiddenImports {
				if p == forb {
					t.Errorf("%s: forbidden import %s", path, p)
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
			for _, m := range forbiddenMethodCalls {
				if sel.Sel.Name == m {
					t.Errorf("%s: forbidden method call %s", path, m)
				}
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
}
