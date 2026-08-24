package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/executor"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// fakeWriteRunner 记录调用并返回固定结果。
type fakeWriteRunner struct {
	lastCommand  string
	lastReplicas int32
}

func (f *fakeWriteRunner) RunKubectl(_ context.Context, command string) (executor.Result, error) {
	f.lastCommand = command
	return executor.Result{Command: command, Stdout: "ok", ExitCode: 0}, nil
}
func (f *fakeWriteRunner) ScaleDeployment(_ context.Context, ns, name string, replicas int32) (executor.Result, error) {
	_ = ns
	_ = name
	f.lastReplicas = replicas
	return executor.Result{Stdout: "scaled"}, nil
}
func (f *fakeWriteRunner) PatchDeployment(_ context.Context, ns, name, patch string) (executor.Result, error) {
	return executor.Result{Stdout: "patched"}, nil
}
func (f *fakeWriteRunner) PatchConfigMap(_ context.Context, ns, name, patch string) (executor.Result, error) {
	return executor.Result{Stdout: "configmap patched"}, nil
}
func (f *fakeWriteRunner) RestartRollout(_ context.Context, ns, name string) (executor.Result, error) {
	return executor.Result{Stdout: "restarted"}, nil
}
func (f *fakeWriteRunner) LabelResource(_ context.Context, kind, ns, name, key, value string) (executor.Result, error) {
	return executor.Result{Stdout: "labeled"}, nil
}
func (f *fakeWriteRunner) DeletePod(_ context.Context, ns, name string, grace *int64) (executor.Result, error) {
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
}

func TestScaleDeploymentTool(t *testing.T) {
	r := &fakeWriteRunner{}
	ts := NewWriteTools(r)
	tr, err := ts[1].Execute(context.Background(), json.RawMessage(`{"namespace":"production","name":"payment-api","replicas":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.lastReplicas != 5 {
		t.Fatalf("replicas = %d", r.lastReplicas)
	}
	if ts[1].IsReadOnly(nil) || ts[1].Risk(nil) != model.RiskMedium {
		t.Fatal("scale 应为非只读 MEDIUM")
	}
	if tr.Data.(executor.Result).Stdout != "scaled" {
		t.Fatalf("result = %+v", tr.Data)
	}
	// 非法副本数。
	tr, err = ts[1].Execute(context.Background(), json.RawMessage(`{"namespace":"n","name":"d","replicas":"abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Data.(map[string]any)["error"] == nil {
		t.Fatal("非法 replicas 应返回错误")
	}
}

func TestWriteToolsCount(t *testing.T) {
	ts := NewWriteTools(&fakeWriteRunner{})
	if len(ts) != 7 {
		t.Fatalf("写工具数 = %d, want 7", len(ts))
	}
}

func TestWriteToolDescribe(t *testing.T) {
	ts := NewWriteTools(&fakeWriteRunner{})
	// ts[1]=scale_deployment
	cmd, change := ts[1].(CommandDescriber).Describe(json.RawMessage(`{"namespace":"production","name":"payment-api","replicas":5}`))
	if !strings.Contains(cmd, "kubectl -n production scale deployment/payment-api --replicas=5") ||
		!strings.Contains(change, "调整 Deployment production/payment-api 副本数为 5") {
		t.Fatalf("scale describe: %q / %q", cmd, change)
	}
	// ts[3]=patch_configmap
	cmd, change = ts[3].(CommandDescriber).Describe(json.RawMessage(`{"namespace":"yanshou-nginx","name":"nginx-config","patch":"{\"data\":{\"default.conf\":\"listen 80;\\nserver_name localhost;\"}}"}`))
	if !strings.Contains(cmd, "patch configmap/nginx-config") ||
		!strings.Contains(change, "default.conf") || !strings.Contains(change, "listen 80;") {
		t.Fatalf("patch_configmap describe: %q / %q", cmd, change)
	}
	// ts[4]=restart_rollout
	cmd, change = ts[4].(CommandDescriber).Describe(json.RawMessage(`{"namespace":"yanshou-nginx","name":"nginx-deployment"}`))
	if !strings.Contains(cmd, "rollout restart deployment/nginx-deployment") ||
		!strings.Contains(change, "滚动重启") {
		t.Fatalf("restart describe: %q / %q", cmd, change)
	}
}
