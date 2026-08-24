package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestIsStreaming(t *testing.T) {
	cases := []struct {
		argv []string
		want bool
	}{
		{[]string{"kubectl", "logs", "-f", "pod"}, true},
		{[]string{"kubectl", "logs", "--follow", "pod"}, true},
		{[]string{"kubectl", "logs", "pod", "--tail=10"}, false},
		{[]string{"kubectl", "get", "pods", "-w"}, true},
		{[]string{"kubectl", "get", "pods", "--watch"}, true},
		{[]string{"kubectl", "get", "pods"}, false},
		{[]string{"kubectl", "describe", "pod", "x"}, false},
		{[]string{"kubectl"}, false},
	}
	for _, c := range cases {
		if got := IsStreaming(c.argv); got != c.want {
			t.Errorf("IsStreaming(%v) = %v, want %v", c.argv, got, c.want)
		}
	}
}

// streamingFakeKubectl 生成先输出一行标记再阻塞的 kubectl.bat，模拟 logs -f 持续输出。
func streamingFakeKubectl(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubectl.bat")
	// echo 标记后用 ping 阻塞（Windows 惯用睡眠手法），输出重定向到 nul 不污染 stdout。
	content := "@echo off\r\necho stream-marker\r\nping -n 5 127.0.0.1 >nul\r\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunKubectlStreamingTimeout(t *testing.T) {
	bin := streamingFakeKubectl(t)
	r := NewRunner(Options{KubectlBin: bin, MaxBytes: 4096, StreamTimeout: 800 * time.Millisecond})
	res, err := r.RunKubectl(context.Background(), "kubectl logs -f pod -n default")
	if err != nil {
		t.Fatalf("流式超时不应返回错误: %v", err)
	}
	if !res.StreamTimeout {
		t.Fatal("流式超时应标记 StreamTimeout=true")
	}
	if !strings.Contains(res.Stdout, "stream-marker") {
		t.Fatalf("应捕获部分输出: %q", res.Stdout)
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
