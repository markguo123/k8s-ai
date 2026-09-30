// Package creationtest 覆盖三期 R8.1：创建链路集成测试。
//
// 这里串起 service.Chat → Agent 审批 → create_resource Tool → executor SSA 创建/校验，
// 使用 fake dynamic client + SSA reactor 模拟 apiserver，不依赖真实集群。
package creationtest

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/executor"
	"github.com/k8s-ai/k8s-ai/internal/kubernetes"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/scanner"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

const (
	configMapManifest = `apiVersion: v1
kind: ConfigMap
metadata:
  name: web-config
  namespace: default
data:
  mode: prod
`

	secretAskManifest = `apiVersion: v1
kind: Secret
metadata:
  name: db-secret
  namespace: default
type: Opaque
stringData:
  password: $ASK
`
)

// scriptLLM 是 creationtest 的脚本化 LLM：按顺序返回预置响应，多次 Chat 调用共享同一实例。
type scriptLLM struct {
	responses []*llm.ChatResponse
	idx       int
}

func (s *scriptLLM) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	if s.idx >= len(s.responses) {
		return &llm.ChatResponse{Content: "done"}, nil
	}
	r := s.responses[s.idx]
	s.idx++
	return r, nil
}

// stubScanRunner 只满足 ChatService 的扫描工具装配要求。
type stubScanRunner struct{}

func (f *stubScanRunner) Run(_ context.Context, _ model.ScanOptions) (*model.ScanResult, error) {
	panic("creationtest 不执行扫描工具")
}

// collected 汇总一次 Chat 里的工具结果与审批事件。
type collected struct {
	toolResponses []service.ChatToolCallView
	choiceEvents  int
}

// runChat 跑一轮 Chat；遇到审批选择时自动回 yes，遇到 Secret 索取时回 secretValue。
func runChat(t *testing.T, svc service.ChatService, sessionID, query, secretValue string) *collected {
	t.Helper()
	respond := make(chan string, 1)
	events, err := svc.Chat(context.Background(), service.ChatRequest{SessionID: sessionID, Query: query}, respond)
	if err != nil {
		t.Fatal(err)
	}
	c := &collected{}
	for ev := range events {
		switch ev.Type {
		case service.ChatEventChoice:
			c.choiceEvents++
			if ev.Choice != nil && ev.Choice.Secret {
				respond <- secretValue
			} else {
				respond <- "yes"
			}
		case service.ChatEventToolResponse:
			if ev.Tool != nil {
				c.toolResponses = append(c.toolResponses, *ev.Tool)
			}
		}
	}
	return c
}

func lastCreateResponse(t *testing.T, c *collected) *service.ChatToolCallView {
	t.Helper()
	for i := len(c.toolResponses) - 1; i >= 0; i-- {
		if c.toolResponses[i].Name == "create_resource" {
			return &c.toolResponses[i]
		}
	}
	t.Fatal("没有 create_resource 工具响应")
	return nil
}

func dataMap(t *testing.T, view *service.ChatToolCallView) map[string]any {
	t.Helper()
	m, ok := view.Data.(map[string]any)
	if !ok {
		t.Fatalf("工具 Data 不是 map：%#v", view.Data)
	}
	return m
}

func toolArgs(t *testing.T, args map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func dryRunResp(t *testing.T, manifest string) *llm.ChatResponse {
	t.Helper()
	return &llm.ChatResponse{ToolCalls: []llm.ToolCall{{
		ID:        "dry",
		Name:      "create_resource",
		Arguments: toolArgs(t, map[string]any{"manifest": manifest, "mode": "dry_run"}),
	}}}
}

func applyResp(t *testing.T, manifest string) *llm.ChatResponse {
	t.Helper()
	return &llm.ChatResponse{ToolCalls: []llm.ToolCall{{
		ID:        "apply",
		Name:      "create_resource",
		Arguments: toolArgs(t, map[string]any{"manifest": manifest, "mode": "apply", "verify": true}),
	}}}
}

func doneResp() *llm.ChatResponse {
	return &llm.ChatResponse{Content: "done"}
}

func newCreationChat(t *testing.T, runner *executor.Runner, llmClient *scriptLLM) (service.ChatService, *config.Config) {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent.SessionDir = t.TempDir()
	cfg.Agent.AuditDir = t.TempDir()
	cfg.Agent.Create.Enabled = true
	reader := kubernetes.NewClientWithClientset(fake.NewSimpleClientset())
	svc := service.NewChat(cfg,
		func() (scanner.Reader, error) { return reader, nil },
		func(llm.Options) llm.LLMClient { return llmClient },
		func() tools.ScanRunner { return &stubScanRunner{} },
		func() (tools.WriteRunner, error) { return runner, nil })
	return svc, cfg
}

// TestServiceCreateConfigMap 验证 dry_run → 人工确认 → SSA 创建 → verify 的完整链路。
func TestServiceCreateConfigMap(t *testing.T) {
	defaultNS := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": "default"},
	}}
	runner, fake := newSSARunner(t, executor.Options{}, defaultNS)
	llmClient := &scriptLLM{responses: []*llm.ChatResponse{
		dryRunResp(t, configMapManifest),
		doneResp(),
		applyResp(t, configMapManifest),
		doneResp(),
	}}
	svc, _ := newCreationChat(t, runner, llmClient)
	if _, err := svc.GetOrCreateSession("s1", "", "qwen-plus"); err != nil {
		t.Fatal(err)
	}

	if c := runChat(t, svc, "s1", "预演 ConfigMap", ""); c.choiceEvents != 0 {
		t.Fatalf("dry_run 不应触发审批，choice=%d", c.choiceEvents)
	}
	c := runChat(t, svc, "s1", "确认创建 ConfigMap", "")
	if c.choiceEvents == 0 {
		t.Fatal("apply 应触发人工审批")
	}
	resp := lastCreateResponse(t, c)
	if resp.Ok == nil || !*resp.Ok {
		t.Fatalf("apply 工具应成功：result=%q", resp.Result)
	}
	data := dataMap(t, resp)
	if data["mode"] != "apply" || data["status"] != "applied" {
		t.Fatalf("apply 状态不对：%#v", data)
	}
	obj := getUnstructured(t, fake, gvr("", "v1", "configmaps"), "default", "web-config")
	if obj.GetName() != "web-config" {
		t.Fatalf("对象未创建：%+v", obj.Object)
	}
}

// TestServiceCreateSecretAsksUserValue silently 验证 Secret 值通过审批通道带外收集，
// 指纹落审计但不泄露明文。
func TestServiceCreateSecretAsksUserValue(t *testing.T) {
	const plain = "Gientech@123"
	runner, fake := newSSARunner(t, executor.Options{})
	llmClient := &scriptLLM{responses: []*llm.ChatResponse{
		dryRunResp(t, secretAskManifest),
		doneResp(),
		applyResp(t, secretAskManifest),
		doneResp(),
	}}
	svc, cfg := newCreationChat(t, runner, llmClient)
	if _, err := svc.GetOrCreateSession("sec", "", "qwen-plus"); err != nil {
		t.Fatal(err)
	}
	runChat(t, svc, "sec", "预演 Secret", "")
	c := runChat(t, svc, "sec", "确认创建 Secret", plain)
	if c.choiceEvents < 2 {
		t.Fatalf("Secret apply 应经历审批 + 取值两个交互，实际 choice=%d", c.choiceEvents)
	}
	resp := lastCreateResponse(t, c)
	data := dataMap(t, resp)
	if data["status"] != "applied" {
		t.Fatalf("Secret 创建未成功：%#v", data)
	}
	// 集群侧确实拿到了用户值（执行器只在 obj 里物化一次）。
	obj := getUnstructured(t, fake, gvr("", "v1", "secrets"), "default", "db-secret")
	if got, _, _ := unstructured.NestedString(obj.Object, "stringData", "password"); got != plain {
		t.Fatalf("Secret 群集侧值不对：%q", got)
	}
	// 工具结果与会话/审计不得出现明文。
	encoded, err := json.Marshal(resp.Data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resp.Result, plain) || strings.Contains(string(encoded), plain) {
		t.Fatal("工具结果泄露了 Secret 明文")
	}
	auditContent := readAudit(t, cfg, "sec")
	if strings.Contains(auditContent, plain) {
		t.Fatal("审计文件泄露了 Secret 明文")
	}
	if !strings.Contains(string(encoded), "sha256") || !strings.Contains(auditContent, "SECRET_TOUCHED") {
		t.Fatalf("缺少 Secret 指纹审计：data=%s audit=%s", encoded, auditContent)
	}
}

// TestServiceApplyWithoutDryRunRejected 验证绕过预演的 apply 会被拒绝。
func TestServiceApplyWithoutDryRunRejected(t *testing.T) {
	runner, _ := newSSARunner(t, executor.Options{})
	llmClient := &scriptLLM{responses: []*llm.ChatResponse{
		applyResp(t, configMapManifest),
		doneResp(),
	}}
	svc, _ := newCreationChat(t, runner, llmClient)
	if _, err := svc.GetOrCreateSession("rej", "", "qwen-plus"); err != nil {
		t.Fatal(err)
	}
	c := runChat(t, svc, "rej", "直接创建 ConfigMap", "")
	resp := lastCreateResponse(t, c)
	if resp.Ok == nil || *resp.Ok {
		t.Fatalf("未经预演直接 apply 应被拒绝：%s", resp.Result)
	}
	data := dataMap(t, resp)
	if _, ok := data["error"]; !ok {
		t.Fatalf("拒绝结果缺少 error：%#v", data)
	}
}

func readAudit(t *testing.T, cfg *config.Config, sessionID string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(cfg.Agent.AuditDir, sessionID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// --- SSA 测试夹具（等价 executor 单测里的 fake dynamic + SSA reactor） ------

// ssaKinds 注册到 RESTMapper 的类型（覆盖本次集成测试用到的资源）。
var ssaKinds = []struct {
	apiVersion string
	kind       string
	namespaced bool
}{
	{"v1", "Pod", true},
	{"v1", "Service", true},
	{"v1", "ConfigMap", true},
	{"v1", "Secret", true},
	{"v1", "ServiceAccount", true},
	{"v1", "PersistentVolumeClaim", true},
	{"v1", "Event", true},
	{"v1", "Namespace", false},
	{"apps/v1", "Deployment", true},
}

func newSSARunner(t *testing.T, opts executor.Options, objects ...runtime.Object) (*executor.Runner, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	fake := dynamicfake.NewSimpleDynamicClient(clientgoscheme.Scheme, objects...)
	mapper := meta.NewDefaultRESTMapper(nil)
	for _, k := range ssaKinds {
		gvk := executor.ParseGVK(k.apiVersion, k.kind)
		scope := meta.RESTScopeRoot
		if k.namespaced {
			scope = meta.RESTScopeNamespace
		}
		mapper.Add(gvk, scope)
	}
	installSSAReaction(fake)
	opts.Dynamic = fake
	opts.Mapper = mapper
	return executor.NewRunner(opts), fake
}

func installSSAReaction(fake *dynamicfake.FakeDynamicClient) {
	fake.PrependReactor("patch", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		impl, ok := action.(k8stesting.PatchActionImpl)
		if !ok || impl.PatchType != types.ApplyPatchType {
			return false, nil, nil
		}
		patch := &unstructured.Unstructured{Object: map[string]any{}}
		if err := json.Unmarshal(impl.Patch, &patch.Object); err != nil {
			return true, nil, err
		}
		normalizeNumbers(patch.Object)
		patch.SetName(impl.Name)
		if impl.Namespace != "" {
			patch.SetNamespace(impl.Namespace)
		}
		tracker := fake.Tracker()
		dryRun := containsString(impl.PatchOptions.DryRun, metav1.DryRunAll)
		existing, err := tracker.Get(impl.Resource, impl.Namespace, impl.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if dryRun {
				return true, patch, nil
			}
			if cerr := tracker.Create(impl.Resource, patch, impl.Namespace); cerr != nil {
				return true, nil, cerr
			}
			got, gerr := tracker.Get(impl.Resource, impl.Namespace, impl.Name, metav1.GetOptions{})
			return true, got, gerr
		}
		if err != nil {
			return true, nil, err
		}
		existingObj, ok := existing.(*unstructured.Unstructured)
		if !ok {
			return true, nil, apierrors.NewInternalError(errNotUnstructured)
		}
		merged := &unstructured.Unstructured{Object: mergeMaps(existingObj.Object, patch.Object)}
		if dryRun {
			return true, merged, nil
		}
		if uerr := tracker.Update(impl.Resource, merged, impl.Namespace); uerr != nil {
			return true, nil, uerr
		}
		got, gerr := tracker.Get(impl.Resource, impl.Namespace, impl.Name, metav1.GetOptions{})
		return true, got, gerr
	})
}

func gvr(group, version, resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
}

func getUnstructured(t *testing.T, fake *dynamicfake.FakeDynamicClient, resource schema.GroupVersionResource, ns, name string) *unstructured.Unstructured {
	t.Helper()
	var obj *unstructured.Unstructured
	var err error
	if ns == "" {
		obj, err = fake.Resource(resource).Get(context.Background(), name, metav1.GetOptions{})
	} else {
		obj, err = fake.Resource(resource).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	}
	if err != nil {
		t.Fatalf("读取 %s/%s: %v", resource.Resource, name, err)
	}
	return obj
}

func mergeMaps(base, patch map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(patch))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range patch {
		if sub, ok := v.(map[string]any); ok {
			if existing, ok := out[k].(map[string]any); ok {
				out[k] = mergeMaps(existing, sub)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func normalizeNumbers(v any) any {
	switch value := v.(type) {
	case map[string]any:
		for k, item := range value {
			value[k] = normalizeNumbers(item)
		}
		return value
	case []any:
		for i, item := range value {
			value[i] = normalizeNumbers(item)
		}
		return value
	case float64:
		if value == math.Trunc(value) && math.Abs(value) < 1<<53 {
			return int64(value)
		}
		return value
	default:
		return v
	}
}

var errNotUnstructured = errors.New("fixture: tracker 返回了非 unstructured 对象")
