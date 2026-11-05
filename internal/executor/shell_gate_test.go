package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunKubectlShellDisabledGuidance 锁 P6.3 关闭态行为（方案 16 §8.1）：
// 含 shell 构造的命令必须拿到可指导文案，且**不进 exec**。
// 这里故意用不存在的 kubectl 与不存在的解释器环境：真去 fork 就会得到
// "executable file not found" 这类 Go error，而断言要求的是 err == nil + 文案。
func TestRunKubectlShellDisabledGuidance(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "forked.marker")
	bin := fakeKubectl(t, "echo touched > "+marker)
	r := NewRunner(Options{KubectlBin: bin, MaxBytes: 4096, ShellEnabled: false})

	res, err := r.RunKubectl(context.Background(), "kubectl get pods -A -o json | jq -r .items")
	if err != nil {
		t.Fatalf("关闭态应返回工具级结果而不是 Go error: %v", err)
	}
	if res.ExitCode != exitShellDisabled {
		t.Fatalf("退出码应为 %d，实际 %d", exitShellDisabled, res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "platform.exec.shell_enabled") {
		t.Fatalf("文案必须点出开关键名，实际: %q", res.Stderr)
	}
	if !strings.Contains(res.Stderr, "kubectl get pods -A -o json") {
		t.Fatalf("文案必须给出可执行的替代写法，实际: %q", res.Stderr)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("关闭态不应该 fork 子进程（marker 被写入说明进了 exec）")
	}
}

// TestRunKubectlShellDisabledKeepsArgv 证明闸门只切 shell 通道：argv 直调与写通道
// 不受影响（否则等于把「平台已有能力」砍得更狠）。
func TestRunKubectlShellDisabledKeepsArgv(t *testing.T) {
	bin := fakeKubectl(t, "echo ARGS:%*")
	r := NewRunner(Options{KubectlBin: bin, MaxBytes: 4096, ShellEnabled: false})

	res, err := r.RunKubectl(context.Background(), "kubectl get pods -n default")
	if err != nil {
		t.Fatalf("argv 路径不应受 shell 开关影响: %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "get pods -n default") {
		t.Fatalf("argv 结果异常: %+v", res)
	}
}

// requireUsableShell 确认本机解释器真能跑再进正向用例。
// Windows 上 LookPath 能命中 WSL 存根 bash.exe（没装发行版时一跑就是退出码 1），
// 只判 LookPath 会让用例把环境问题误报成实现问题。
func requireUsableShell(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(shellBinary); err != nil {
		t.Skipf("本机无 %s，跳过 shell 通道正向用例（容器内冒烟覆盖）", shellBinary)
	}
	out, err := exec.Command(shellBinary, "-c", "printf ok").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Skipf("本机 %s 不可用（可能是无发行版的 WSL 存根）：err=%v out=%q", shellBinary, err, string(out))
	}
}

// TestRunKubectlShellEnabledRunsShellPath 证明开启态的 shell 通道真的可用
// （不依赖 kubectl：重定向由解释器自己完成）。没有可用 bash 的机器跳过。
func TestRunKubectlShellEnabledRunsShellPath(t *testing.T) {
	requireUsableShell(t)
	out := filepath.Join(t.TempDir(), "shellpath.txt")
	r := NewRunner(Options{KubectlBin: "kubectl-does-not-need-to-exist", MaxBytes: 4096, ShellEnabled: true})

	res, err := r.RunKubectl(context.Background(), "printf ok > "+filepath.ToSlash(out))
	if err != nil {
		t.Fatalf("开启态 shell 命令应执行: %v (stderr=%q)", err, res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Fatalf("shell 命令退出码应为 0，实际 %d stderr=%q", res.ExitCode, res.Stderr)
	}
	if got := strings.TrimSpace(readFileString(t, out)); got != "ok" {
		t.Fatalf("shell 重定向结果 = %q，want ok", got)
	}
}

// TestRunKubectlMissingKubectlGuidance 锁缺 kubectl 时的文案：既要给现场可执行的
// 处置动作，也必须保留 "kubectl not found" 关键字（tools 层据此回 SYSTEM_UNAVAILABLE，
// 不让模型把环境问题当命令写错反复重试）。
func TestRunKubectlMissingKubectlGuidance(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := NewRunner(Options{MaxBytes: 4096, ShellEnabled: true})

	_, err := r.RunKubectl(context.Background(), "kubectl get pods -n default")
	if err == nil {
		t.Fatal("PATH 里没有 kubectl 时应报错")
	}
	msg := err.Error()
	for _, want := range []string{"kubectl not found", "kubernetes.kubectl_path"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("报错应包含 %q，实际: %q", want, msg)
		}
	}
}

// TestProbeToolchainReportsMissingTools 锁启动探测语义：缺失只反映在状态里，
// 不 panic、不返回 error（缺工具不该阻塞服务启动）。
func TestProbeToolchainReportsMissingTools(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "definitely-not-here")
	r := NewRunner(Options{KubectlBin: missing, ShellEnabled: true})
	st := r.ToolchainStatus()
	if st.KubectlFound {
		t.Fatalf("不存在的 kubectl 不应报告找到: %+v", st)
	}
	if !st.ShellEnabled {
		t.Fatal("状态应回显 shell 开关，供启动日志与冒烟核对")
	}

	rOff := NewRunner(Options{KubectlBin: missing, ShellEnabled: false})
	if st := rOff.ToolchainStatus(); st.ShellEnabled {
		t.Fatal("关闭态状态应回显 shell_enabled=false（日志按 INFO 而非 WARN 播报）")
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
