// Package arch_test 的执行通道红线（P6.3 / D-33 / ADR-044 v1.1）。
//
// 默认交付镜像内置 bash 与 kubectl（能力对齐本地 CLI），安全不再依赖"镜像里没有
// 解释器"，因此必须用测试把两件事钉死：
//  1. 全仓 shell 解释器执行点**只有一个**，且位于 internal/executor；
//  2. 这个执行点前面必须有 platform.exec.shell_enabled 的策略闸门（先返回、后执行），
//     不允许出现"闸门之外再起一个没闸门的执行点"。
//
// 另外锁装配完整性：生产代码构造 executor.Options 时必须显式带 ShellEnabled，
// 漏装配在零值语义下是"静默关掉排障能力"，正是要防的漂移。
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

// shellGateField 是 Runner 上的闸门字段（来自 Options.ShellEnabled）。
const shellGateField = "shellEnabled"

// shellExecPoint 是一个 shell 解释器执行点（exec.Command/CommandContext）。
type shellExecPoint struct {
	path string
	pos  token.Position
}

// isShellExecCall 判断 exec.Command/CommandContext 调用是否以 shell 解释器执行：
// 首参是 bash/sh/cmd 字面量，或参数里出现 "-c"（解释器名走常量时仍能识别）。
func isShellExecCall(call *ast.CallExpr) bool {
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

// isExecCall 判断是否为 os/exec 的 Command/CommandContext 调用（不校验接收者名）。
func isExecCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return recv.Name == "exec" && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext")
}

// hasShellGateOperand 在条件表达式树里找 `!r.shellEnabled`。
// 允许 `pc.IsShell && !r.shellEnabled` 这类组合条件：闸门只要求"关闭态一定进分支"，
// 分支里再收窄是允许的写法。
func hasShellGateOperand(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		unary, ok := n.(*ast.UnaryExpr)
		if !ok || unary.Op != token.NOT {
			return true
		}
		sel, ok := unary.X.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if ok && recv.Name == "r" && sel.Sel.Name == shellGateField {
			found = true
			return false
		}
		return true
	})
	return found
}

// isDisabledGuard 判断 if 语句是否为含 `!r.shellEnabled` 且真的提前 return 的闸门。
func isDisabledGuard(ifStmt *ast.IfStmt) bool {
	if !hasShellGateOperand(ifStmt.Cond) {
		return false
	}
	// 闸门必须真的把执行挡住：分支里要有 return。
	returns := false
	ast.Inspect(ifStmt.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.ReturnStmt); ok {
			returns = true
			return false
		}
		return true
	})
	return returns
}

// collectShellExecPoints 收集生产代码里的 shell 执行点，并为每个执行点判定
// 是否存在"位置在其之前且带 return 的关闭态闸门"。
func collectShellExecPoints(t *testing.T, fset *token.FileSet, file *ast.File, path string) (points []shellExecPoint, guarded map[string]bool) {
	t.Helper()
	points = []shellExecPoint{}
	guarded = map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		fn, ok := node.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		var calls []*ast.CallExpr
		var guards []ast.Node
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch stmt := n.(type) {
			case *ast.IfStmt:
				if isDisabledGuard(stmt) {
					guards = append(guards, stmt)
				}
			case *ast.CallExpr:
				if isExecCall(stmt) && isShellExecCall(stmt) {
					calls = append(calls, stmt)
				}
			}
			return true
		})
		for _, call := range calls {
			pos := fset.Position(call.Pos())
			key := path + ":" + strconv.Itoa(pos.Line)
			points = append(points, shellExecPoint{path: path, pos: pos})
			guarded[key] = false
			for _, g := range guards {
				// 闸门必须在执行点之前结束（即先返回、后执行）。
				if fn.End() > 0 && g.End() < call.Pos() {
					guarded[key] = true
				}
			}
		}
		return true
	})
	return points, guarded
}

// TestShellExecPointIsUniqueAndGated 是 P6.3 的核心红线。
func TestShellExecPointIsUniqueAndGated(t *testing.T) {
	var all []shellExecPoint
	guarded := map[string]bool{}
	root := filepath.Join("..", "..", "internal")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		// relPkg 需要绝对路径才能算出相对 internal/ 的包名。
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		points, g := collectShellExecPoints(t, fset, file, abs)
		all = append(all, points...)
		for k, v := range g {
			guarded[k] = v
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(all) != 1 {
		lines := make([]string, 0, len(all))
		for _, p := range all {
			lines = append(lines, p.pos.String())
		}
		t.Fatalf("全仓 shell 解释器执行点必须唯一，实际 %d 个: %v", len(all), lines)
	}
	point := all[0]
	if pkg := relPkg(point.path); pkg != "executor" {
		t.Errorf("shell 执行点必须位于 internal/executor，实际在 %s（%s）", pkg, point.pos)
	}
	if !guarded[point.path+":"+strconv.Itoa(point.pos.Line)] {
		t.Errorf("shell 执行点 %s 缺少前置策略闸门：需要 `if !r.%s { return ... }` 且在执行之前返回",
			point.pos, shellGateField)
	}
}

// TestExecutorOptionsWiredWithShellSwitch 锁装配完整性：生产代码构造 executor.Options
// 时必须显式传 ShellEnabled（零值是关闭，漏装配等于静默丢失 shell 排障能力）。
func TestExecutorOptionsWiredWithShellSwitch(t *testing.T) {
	root := filepath.Join("..", "..", "internal")
	found := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Options" {
				return true
			}
			if recv, ok := sel.X.(*ast.Ident); !ok || recv.Name != "executor" {
				return true
			}
			found++
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "ShellEnabled" {
					return true
				}
			}
			t.Errorf("%s: 构造 executor.Options 未显式传 ShellEnabled（装配漏传会静默关闭 shell 通道）",
				fset.Position(lit.Pos()))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("没在任何生产文件里找到 executor.Options 构造点，规则可能已失效")
	}
}
