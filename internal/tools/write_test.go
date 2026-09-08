package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/executor"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// fakeWriteRunner 记录调用并返回固定结果。
type fakeWriteRunner struct {
	lastCommand string
}

func (f *fakeWriteRunner) RunKubectl(_ context.Context, command string) (executor.Result, error) {
	f.lastCommand = command
	return executor.Result{Command: command, Stdout: "ok", ExitCode: 0}, nil
}
func (f *fakeWriteRunner) ScaleDeployment(context.Context, string, string, int32) (executor.Result, error) {
	return executor.Result{Stdout: "scaled"}, nil
}
func (f *fakeWriteRunner) PatchDeployment(context.Context, string, string, string) (executor.Result, error) {
	return executor.Result{Stdout: "patched"}, nil
}
func (f *fakeWriteRunner) PatchConfigMap(context.Context, string, string, string) (executor.Result, error) {
	return executor.Result{Stdout: "configmap patched"}, nil
}
func (f *fakeWriteRunner) RestartRollout(context.Context, string, string) (executor.Result, error) {
	return executor.Result{Stdout: "restarted"}, nil
}
func (f *fakeWriteRunner) LabelResource(context.Context, string, string, string, string, string) (executor.Result, error) {
	return executor.Result{Stdout: "labeled"}, nil
}
func (f *fakeWriteRunner) DeletePod(context.Context, string, string, *int64) (executor.Result, error) {
	return executor.Result{Stdout: "deleted"}, nil
}

func TestKubectlToolReadOnlyAndRisk(t *testing.T) {
	r := &fakeWriteRunner{}
	ts := NewWriteTools(r)
	kubectl := ts[0]
	if !kubectl.IsReadOnly(json.RawMessage(`{"command":"kubectl get pods"}`)) {
		t.Fatal("get 应为只读")
	}
	if kubectl.IsReadOnly(json.RawMessage(`{"command":"kubectl delete pod x"}`)) {
		t.Fatal("delete 不应为只读")
	}
	if kubectl.Risk(json.RawMessage(`{"command":"kubectl scale deployment/d --replicas=2"}`)) != model.RiskMedium {
		t.Fatal("scale 风险应为 MEDIUM")
	}
	if kubectl.Risk(json.RawMessage(`{"command":"kubectl delete namespace x"}`)) != model.RiskCritical {
		t.Fatal("delete namespace 风险应为 CRITICAL")
	}
}

func TestKubectlToolExecute(t *testing.T) {
	r := &fakeWriteRunner{}
	ts := NewWriteTools(r)
	tr, err := ts[0].Execute(context.Background(), json.RawMessage(`{"command":"kubectl get pods -n default"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.lastCommand != "kubectl get pods -n default" {
		t.Fatalf("command = %q", r.lastCommand)
	}
	if tr.Data.(executor.Result).Stdout != "ok" {
		t.Fatalf("result = %+v", tr.Data)
	}
	if !strings.Contains(tr.Text, "ok") {
		t.Fatalf("Text 未填充: %q", tr.Text)
	}
}

func TestWriteToolsCount(t *testing.T) {
	ts := NewWriteTools(&fakeWriteRunner{})
	if len(ts) != 1 {
		t.Fatalf("写工具数 = %d, want 1（只有 kubectlTool）", len(ts))
	}
	if ts[0].Name() != "kubectl" {
		t.Fatalf("唯一写工具 = %q, want kubectl", ts[0].Name())
	}
}

// errorWriteRunner 返回预设错误。
type errorWriteRunner struct{ err error }

func (f *errorWriteRunner) RunKubectl(context.Context, string) (executor.Result, error) {
	return executor.Result{}, f.err
}
func (f *errorWriteRunner) ScaleDeployment(context.Context, string, string, int32) (executor.Result, error) {
	return executor.Result{}, nil
}
func (f *errorWriteRunner) PatchDeployment(context.Context, string, string, string) (executor.Result, error) {
	return executor.Result{}, nil
}
func (f *errorWriteRunner) PatchConfigMap(context.Context, string, string, string) (executor.Result, error) {
	return executor.Result{}, nil
}
func (f *errorWriteRunner) RestartRollout(context.Context, string, string) (executor.Result, error) {
	return executor.Result{}, nil
}
func (f *errorWriteRunner) LabelResource(context.Context, string, string, string, string, string) (executor.Result, error) {
	return executor.Result{}, nil
}
func (f *errorWriteRunner) DeletePod(context.Context, string, string, *int64) (executor.Result, error) {
	return executor.Result{}, nil
}

// TestKubectlMissingError 确保 kubectl 二进制不存在时返回 SYSTEM_UNAVAILABLE，
// LLM 能区分"环境缺依赖"和"命令写错"，不会反复重试。
func TestKubectlMissingError(t *testing.T) {
	missing := "kubectl: kubectl not found in PATH: exec: kubectl: executable file not found in PATH"
	r := &errorWriteRunner{err: errors.New(missing)}
	ts := NewWriteTools(r)

	// 1) kubectl 二进制缺失 → SYSTEM_UNAVAILABLE 友好提示。
	tr, err := ts[0].Execute(context.Background(), json.RawMessage(`{"command":"kubectl scale deployment x --replicas=2"}`))
	if err != nil {
		t.Fatalf("kubectl missing 不应返回 error: %v", err)
	}
	if tr.Text != "SYSTEM_UNAVAILABLE" {
		t.Fatalf("Text = %q, want SYSTEM_UNAVAILABLE", tr.Text)
	}
	d, ok := tr.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data 类型错误: %T", tr.Data)
	}
	if d["kind"] != "kubectl_binary_missing" {
		t.Fatalf("Data.kind = %v, want kubectl_binary_missing", d["kind"])
	}
	msg, _ := d["message"].(string)
	if !strings.Contains(msg, "未检测到 kubectl 二进制") {
		t.Fatalf("message 缺少友好提示: %q", msg)
	}
	if !strings.Contains(msg, "scan_cluster") || !strings.Contains(msg, "get_pod") {
		t.Fatalf("message 应提只读工具继续可用: %q", msg)
	}

	// 2) 普通命令错误（集群/命令本身问题）→ 正常 ResultError，不是 SYSTEM_UNAVAILABLE。
	r.err = errors.New("error: deployment x not found")
	tr, err = ts[0].Execute(context.Background(), json.RawMessage(`{"command":"kubectl scale deployment x --replicas=2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Text == "SYSTEM_UNAVAILABLE" {
		t.Fatal("普通命令错误不应标记为 SYSTEM_UNAVAILABLE")
	}
	if !strings.Contains(tr.Text, "deployment x not found") {
		t.Fatalf("Text = %q, want error 信息", tr.Text)
	}
}

func TestIsKubectlMissingError(t *testing.T) {
	wantTrue := []string{
		"kubectl not found in PATH: exec:",
		"executable file not found in C:\\Windows\\System32",
		"kubectl: kubectl not found",
	}
	for _, s := range wantTrue {
		if !isKubectlMissingError(s) {
			t.Fatalf("should match: %q", s)
		}
	}
	wantFalse := []string{
		"exit code 1: error: deployment not found",
		"kubectl: failed to create pod",
		"kubectl: error: must be authenticated",
	}
	for _, s := range wantFalse {
		if isKubectlMissingError(s) {
			t.Fatalf("should NOT match: %q", s)
		}
	}
}
