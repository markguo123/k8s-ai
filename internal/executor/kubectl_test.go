package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeKubectl 生成一个回显参数的可执行 kubectl.bat（Windows 测试用）。
func fakeKubectl(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubectl.bat")
	content := "@echo off\r\n" + body + "\r\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunKubectlSuccess(t *testing.T) {
	bin := fakeKubectl(t, "echo ARGS:%*")
	r := NewRunner(Options{KubectlBin: bin, MaxBytes: 4096})
	res, err := r.RunKubectl(context.Background(), "kubectl get pods -n default")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "ARGS:") || !strings.Contains(res.Stdout, "get pods -n default") {
		t.Fatalf("result = %+v", res)
	}
}

func TestRunKubectlNonZeroExit(t *testing.T) {
	bin := fakeKubectl(t, "echo boom\r\nexit /b 3")
	r := NewRunner(Options{KubectlBin: bin, MaxBytes: 4096})
	res, err := r.RunKubectl(context.Background(), "kubectl get pods")
	if err != nil {
		t.Fatal("非零退出码不应作为 Go error 返回")
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit code = %d", res.ExitCode)
	}
}

func TestRunKubectlRejectsShellMeta(t *testing.T) {
	bin := fakeKubectl(t, "echo ok")
	r := NewRunner(Options{KubectlBin: bin})
	if _, err := r.RunKubectl(context.Background(), "kubectl get pods | grep x"); err == nil {
		t.Fatal("管道命令应被拒绝")
	}
	if _, err := r.RunKubectl(context.Background(), "kubectl exec -it pod -- ls"); err == nil {
		t.Fatal("交互式 exec 应被拒绝")
	}
}

func TestRunKubectlDenyOps(t *testing.T) {
	bin := fakeKubectl(t, "echo ok")
	r := NewRunner(Options{KubectlBin: bin, DenyOps: []string{"delete namespace"}})
	if _, err := r.RunKubectl(context.Background(), "kubectl delete namespace production"); err == nil {
		t.Fatal("deny_ops 应拦截")
	}
	if _, err := r.RunKubectl(context.Background(), "kubectl get namespaces"); err != nil {
		t.Fatalf("只读命令不应被拦截: %v", err)
	}
}

func TestCappedBuffer(t *testing.T) {
	c := newCappedBuffer(10)
	_, _ = c.Write([]byte("0123456789"))
	_, _ = c.Write([]byte("abcdef"))
	if c.String() != "0123456789" || !c.truncated {
		t.Fatalf("buf=%q truncated=%v", c.String(), c.truncated)
	}
}
