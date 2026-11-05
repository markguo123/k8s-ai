package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/executor"
	"github.com/k8s-ai/k8s-ai/internal/manifestx"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/skills"
)

// fakeCreateRunner 记录提交请求，并按表返回映射/存在性结论。
type fakeCreateRunner struct {
	mappings  map[string]executor.ApplyMapping
	statuses  map[string]executor.ObjectStatus
	requests  []executor.ApplyRequest
	statusQ   []string // 记录存在性查询的对象标识
	applyFail map[string]string
}

func newFakeCreateRunner() *fakeCreateRunner {
	return &fakeCreateRunner{
		mappings: map[string]executor.ApplyMapping{
			"v1/ConfigMap":       {Resource: "configmaps", Namespaced: true},
			"v1/Secret":          {Resource: "secrets", Namespaced: true},
			"v1/Namespace":       {Resource: "namespaces"},
			"apps/v1/Deployment": {Resource: "deployments", Namespaced: true},
			"apps/v1/DaemonSet":  {Resource: "daemonsets", Namespaced: true},
		},
		statuses:  map[string]executor.ObjectStatus{},
		applyFail: map[string]string{},
	}
}

func (f *fakeCreateRunner) MappingFor(apiVersion, kind string) (executor.ApplyMapping, error) {
	if m, ok := f.mappings[apiVersion+"/"+kind]; ok {
		return m, nil
	}
	return executor.ApplyMapping{}, &noMatchError{}
}

type noMatchError struct{}

func (*noMatchError) Error() string { return "no matches for kind" }

func (f *fakeCreateRunner) GetObjectStatus(_ context.Context, ref executor.ObjectRef) (executor.ObjectStatus, error) {
	key := ref.Kind + "/" + ref.Namespace + "/" + ref.Name
	f.statusQ = append(f.statusQ, key)
	if s, ok := f.statuses[key]; ok {
		return s, nil
	}
	return executor.ObjectStatus{}, nil
}

func (f *fakeCreateRunner) Apply(_ context.Context, req executor.ApplyRequest) (executor.ApplyResult, error) {
	f.requests = append(f.requests, req)
	kind, _ := req.Object["kind"].(string)
	name := ""
	if meta, ok := req.Object["metadata"].(map[string]any); ok {
		name, _ = meta["name"].(string)
	}
	// applyFail 模拟「dry run 通过、apply 才被准入/配额拒绝」的失败：
	// 服务端 dry run 只复现提交前就能判定的错误，所以这里只对真实提交生效。
	if msg, ok := f.applyFail[kind+"/"+name]; ok && !req.DryRun {
		return executor.ApplyResult{}, &failedError{msg: msg}
	}
	res := executor.ApplyResult{
		Kind: kind, Name: name, Namespace: req.Namespace, DryRun: req.DryRun,
		UID: "uid-" + name, Namespaced: true,
	}
	if strings.HasPrefix(name, "exists") {
		res.Existed = true
	}
	if kind == "Secret" {
		res.SecretKeys, res.GeneratedKeys, res.SecretKeyStatuses = fakeSecretOutcome(req)
	}
	if req.Verify && !req.DryRun {
		res.Verify = &executor.VerifyResult{Kind: kind, Name: name, Ready: true, Reason: "available"}
	}
	return res, nil
}

type failedError struct{ msg string }

func (e *failedError) Error() string { return e.msg }

const cmManifest = `apiVersion: v1
kind: ConfigMap
metadata:
  name: web
  namespace: default
data:
  mode: prod
`

func newTestCreateTool(runner CreateRunner, store *ProposalStore) Tool {
	return NewCreateTool(CreateOptions{
		Limits:           manifestx.Limits{MaxObjects: 3, MaxBytes: 8192},
		DefaultNamespace: "default",
		SecretAccess:     "create_only",
		VerifyTimeout:    30 * time.Second,
	}, runner, store, "sess-1")
}

func createInput(t *testing.T, manifest, mode string, extra map[string]any) json.RawMessage {
	t.Helper()
	view := map[string]any{"manifest": manifest}
	if mode != "" {
		view["mode"] = mode
	}
	for k, v := range extra {
		view[k] = v
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func dataOf(t *testing.T, tr ToolResult) map[string]any {
	t.Helper()
	data, ok := tr.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data 类型 = %T", tr.Data)
	}
	return data
}

func errorCode(t *testing.T, tr ToolResult) string {
	t.Helper()
	return stringOf(dataOf(t, tr)["code"])
}

func TestCreateToolDryRunRegistersProposal(t *testing.T) {
	runner, store := newFakeCreateRunner(), NewProposalStore()
	tool := newTestCreateTool(runner, store)
	input := createInput(t, cmManifest, "", nil)
	if !tool.IsReadOnly(input) {
		t.Fatal("dry run 必须判定为只读")
	}
	if tool.Risk(input) != model.RiskSafe {
		t.Fatal("dry run 风险必须是 SAFE")
	}
	tr, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	data := dataOf(t, tr)
	if data["status"] != statusValidated {
		t.Fatalf("status = %v", data["status"])
	}
	if _, hasErr := data["error"]; hasErr {
		t.Fatalf("dry run 不该带 error：%v", data["error"])
	}
	if store.Count("sess-1") != 1 {
		t.Fatalf("提案未登记：%d", store.Count("sess-1"))
	}
	if len(runner.requests) != 1 || !runner.requests[0].DryRun {
		t.Fatalf("必须走服务端 dry run：%+v", runner.requests)
	}
	if data["next"] == nil || !strings.Contains(stringOf(data["next"]), "mode=apply") {
		t.Fatal("dry run 结果必须告诉模型下一步")
	}
}

// TestCreateToolApplyRequiresProposal 是 D-09 的核心：没预演过就不许创建。
func TestCreateToolApplyRequiresProposal(t *testing.T) {
	runner, store := newFakeCreateRunner(), NewProposalStore()
	tool := newTestCreateTool(runner, store)
	tr, err := tool.Execute(context.Background(), createInput(t, cmManifest, CreateModeApply, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := errorCode(t, tr); got != errCreateProposalRequired {
		t.Fatalf("code = %q", got)
	}
	if len(runner.requests) != 0 {
		t.Fatal("未预演的清单绝不能提交")
	}
}

// TestCreateToolApplyRejectsChangedManifest 防止「预演一份、提交另一份」。
func TestCreateToolApplyRejectsChangedManifest(t *testing.T) {
	runner, store := newFakeCreateRunner(), NewProposalStore()
	tool := newTestCreateTool(runner, store)
	if _, err := tool.Execute(context.Background(), createInput(t, cmManifest, "", nil)); err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(cmManifest, "mode: prod", "mode: dev", 1)
	tr, err := tool.Execute(context.Background(), createInput(t, tampered, CreateModeApply, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := errorCode(t, tr); got != errCreateProposalRequired {
		t.Fatalf("code = %q", got)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("被拒绝的 apply 不应提交：%+v", runner.requests)
	}
}

func TestCreateToolApplyHappyPathWithVerify(t *testing.T) {
	runner, store := newFakeCreateRunner(), NewProposalStore()
	tool := newTestCreateTool(runner, store)
	input := createInput(t, cmManifest, "", nil)
	if _, err := tool.Execute(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	applyInput := createInput(t, cmManifest, CreateModeApply, map[string]any{"verify": true, "timeout_seconds": 5})
	if tool.IsReadOnly(applyInput) {
		t.Fatal("apply 不是只读")
	}
	if tool.Risk(applyInput) != model.RiskMedium {
		t.Fatalf("创建 ConfigMap 风险应为 MEDIUM，实际 %v", tool.Risk(applyInput))
	}
	tr, err := tool.Execute(context.Background(), applyInput)
	if err != nil {
		t.Fatal(err)
	}
	data := dataOf(t, tr)
	if data["status"] != statusApplied || data["applied"] != 1 {
		t.Fatalf("status = %v applied = %v objects = %v", data["status"], data["applied"], data["objects"])
	}
	last := runner.requests[len(runner.requests)-1]
	if last.DryRun || !last.Verify || last.VerifyTimeout != 5*time.Second {
		t.Fatalf("apply 参数不对：%+v", last)
	}
}

func TestCreateToolApplyRejectsExistingAndMissingNamespace(t *testing.T) {
	runner, store := newFakeCreateRunner(), NewProposalStore()
	runner.statuses["ConfigMap/default/web"] = executor.ObjectStatus{Found: true, UID: "u1"}
	tool := newTestCreateTool(runner, store)
	if _, err := tool.Execute(context.Background(), createInput(t, cmManifest, "", nil)); err != nil {
		t.Fatal(err)
	}
	tr, _ := tool.Execute(context.Background(), createInput(t, cmManifest, CreateModeApply, nil))
	data := dataOf(t, tr)
	if data["status"] != statusRejected {
		t.Fatalf("status = %v", data["status"])
	}
	text := tr.Text
	if !strings.Contains(text, errCreateAlreadyExists) || !strings.Contains(text, "patch") {
		t.Fatalf("已存在时要给出 patch 建议：%q", text)
	}

	nsMissing := newFakeCreateRunner()
	nsMissing.statuses["ConfigMap/kube/web"] = executor.ObjectStatus{NamespaceMissing: true}
	tool2 := newTestCreateTool(nsMissing, NewProposalStore())
	manifest := strings.Replace(cmManifest, "namespace: default", "namespace: kube", 1)
	if _, err := tool2.Execute(context.Background(), createInput(t, manifest, "", nil)); err != nil {
		t.Fatal(err)
	}
	tr2, _ := tool2.Execute(context.Background(), createInput(t, manifest, CreateModeApply, nil))
	if !strings.Contains(tr2.Text, errCreateNamespaceMissing) {
		t.Fatalf("缺少命名空间时要提示先建 ns：%q", tr2.Text)
	}
	if len(nsMissing.requests) != 1 {
		t.Fatal("命名空间缺失的对象不应提交")
	}
}

func TestCreateToolPartialSuccessKeepsAppliedObjects(t *testing.T) {
	runner, store := newFakeCreateRunner(), NewProposalStore()
	runner.applyFail["ConfigMap/web"] = "admission webhook denied the request: quota exceeded for configmaps"
	tool := newTestCreateTool(runner, store)
	manifest := cmManifest + "---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: web2\n  namespace: default\n"
	if _, err := tool.Execute(context.Background(), createInput(t, manifest, "", nil)); err != nil {
		t.Fatal(err)
	}
	tr, _ := tool.Execute(context.Background(), createInput(t, manifest, CreateModeApply, nil))
	data := dataOf(t, tr)
	if data["status"] != statusPartial || data["applied"] != 1 || data["refused"] != 1 {
		t.Fatalf("部分成功要如实汇报：%v", data)
	}
	if _, hasErr := data["error"]; hasErr {
		t.Fatal("部分成功不算整体失败，否则模型会重放整份清单")
	}
}

func TestCreateToolValidationFailureListsEveryFinding(t *testing.T) {
	runner := newFakeCreateRunner()
	tool := newTestCreateTool(runner, nil)
	manifest := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: Bad_Name\n"
	tr, _ := tool.Execute(context.Background(), createInput(t, manifest, "", nil))
	if got := errorCode(t, tr); got != errCreateValidationFailed {
		t.Fatalf("code = %q", got)
	}
	if !strings.Contains(tr.Text, "metadata.name") || !strings.Contains(tr.Text, "建议") {
		t.Fatalf("要一次给全字段级结论与修法：%q", tr.Text)
	}
	if len(runner.requests) != 0 {
		t.Fatal("静态校验失败不应打到 apiserver")
	}
}

func TestCreateToolRejectsHelmTemplateAndBadMode(t *testing.T) {
	tool := newTestCreateTool(newFakeCreateRunner(), nil)
	tpl := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Values.name }}\n"
	tr, _ := tool.Execute(context.Background(), createInput(t, tpl, "", nil))
	if got := errorCode(t, tr); got != errCreateManifestRejected {
		t.Fatalf("code = %q", got)
	}
	badMode := createInput(t, cmManifest, "force", nil)
	tr2, _ := tool.Execute(context.Background(), badMode)
	if got := errorCode(t, tr2); got != errCreateInvalidArguments {
		t.Fatalf("code = %q", got)
	}
	if tool.IsReadOnly(badMode) {
		t.Fatal("mode 非法时按非只读处理")
	}
	if tool.Risk(badMode) != model.RiskHigh {
		t.Fatal("入参非法时按高风险走人工审批")
	}
}

// TestCreateToolRiskFollowsKindScopeAndFields 对齐 R0.3 分级表在工具层的落点。
func TestCreateToolRiskFollowsKindScopeAndFields(t *testing.T) {
	runner := newFakeCreateRunner()
	runner.mappings["rbac.authorization.k8s.io/v1/ClusterRole"] = executor.ApplyMapping{Resource: "clusterroles"}
	tool := newTestCreateTool(runner, nil)
	cases := []struct {
		kind     string
		mode     string
		manifest string
		want     model.RiskLevel
	}{
		{"ConfigMap", CreateModeApply, cmManifest, model.RiskMedium},
		{"ConfigMap", CreateModeDryRun, cmManifest, model.RiskSafe},
		{"ClusterRole", CreateModeApply, "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: reader\nrules: []\n", model.RiskHigh},
		{"DaemonSet", CreateModeApply, "apiVersion: apps/v1\nkind: DaemonSet\nmetadata:\n  name: d\n  namespace: default\nspec:\n  template:\n    spec:\n      hostPID: true\n      containers:\n      - name: c\n        image: example.com/c:1.0\n", model.RiskHigh},
	}
	for _, tc := range cases {
		t.Run(tc.kind+"/"+tc.mode, func(t *testing.T) {
			// apply 路径必须先有同内容的 dry run 提案，Risk 才能复用该清单。
			if tc.mode == CreateModeApply {
				if _, err := tool.Execute(context.Background(), createInput(t, tc.manifest, "", nil)); err != nil {
					t.Fatal(err)
				}
			}
			if got := tool.Risk(createInput(t, tc.manifest, tc.mode, nil)); got != tc.want {
				t.Fatalf("risk = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestCreateToolNeverLeaksSecretValues 保证 Secret 明文值不进 Data/Text。
func TestCreateToolNeverLeaksSecretValues(t *testing.T) {
	runner := newFakeCreateRunner()
	tool := newTestCreateTool(runner, NewProposalStore())
	const secretValue = "sup3r-s3cret-token-value"
	manifest := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: web\n  namespace: default\nstringData:\n  token: " + secretValue + "\n"
	tr, _ := tool.Execute(context.Background(), createInput(t, manifest, "", nil))
	if got := errorCode(t, tr); got != errCreateValidationFailed {
		t.Fatalf("明文凭据必须被拒：%v", tr.Data)
	}
	encoded, err := json.Marshal(tr.Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, blob := range []string{string(encoded), tr.Text} {
		if strings.Contains(blob, secretValue) {
			t.Fatalf("结果里出现了 Secret 明文：%q", blob)
		}
	}
	// 占位符路径：创建成功，只回显 key 名。
	generated := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: web\n  namespace: default\nstringData:\n  token: " + manifestx.PlaceholderGenerate + "\n"
	store := NewProposalStore()
	tool2 := newTestCreateTool(runner, store)
	if _, err := tool2.Execute(context.Background(), createInput(t, generated, "", nil)); err != nil {
		t.Fatal(err)
	}
	tr2, _ := tool2.Execute(context.Background(), createInput(t, generated, CreateModeApply, nil))
	data := dataOf(t, tr2)
	if data["status"] != statusApplied {
		t.Fatalf("占位符 Secret 应创建成功：%v", data)
	}
	if !strings.Contains(tr2.Text, "web") || strings.Contains(tr2.Text, secretValue) {
		t.Fatalf("apply 结果只应包含 key 名：%q", tr2.Text)
	}
	if len(runner.statusQ) != 0 {
		t.Fatalf("Secret 不得做存在性查询（会把已有值读回进程）：%v", runner.statusQ)
	}
}

func TestCreateToolSecretAccessOffIsRejected(t *testing.T) {
	tool := NewCreateTool(CreateOptions{SecretAccess: "off", DefaultNamespace: "default"}, newFakeCreateRunner(), nil, "s")
	manifest := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: web\n  namespace: default\nstringData:\n  token: " + manifestx.PlaceholderGenerate + "\n"
	tr, _ := tool.Execute(context.Background(), createInput(t, manifest, "", nil))
	if !strings.Contains(tr.Text, "secret.access") {
		t.Fatalf("off 档要给出配置层面的解释：%q", tr.Text)
	}
}

func TestCreateToolGrantTargetOnlyForSingleObject(t *testing.T) {
	tool := newTestCreateTool(newFakeCreateRunner(), nil)
	gt := tool.(interface {
		GrantTarget(json.RawMessage) (string, string, string, bool)
	})
	if _, err := tool.Execute(context.Background(), createInput(t, cmManifest, "", nil)); err != nil {
		t.Fatal(err)
	}
	kind, name, ns, ok := gt.GrantTarget(createInput(t, cmManifest, CreateModeApply, nil))
	if !ok || kind != "ConfigMap" || name != "web" || ns != "default" {
		t.Fatalf("单对象应给出精确授权目标：%s %s %s %v", kind, name, ns, ok)
	}
	two := cmManifest + "---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: web2\n  namespace: default\n"
	if _, err := tool.Execute(context.Background(), createInput(t, two, "", nil)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := gt.GrantTarget(createInput(t, two, CreateModeApply, nil)); ok {
		t.Fatal("多对象不得产生可复用授权")
	}
	if _, _, _, ok := gt.GrantTarget(createInput(t, cmManifest, "", nil)); ok {
		t.Fatal("dry run 不得产生授权目标")
	}
}

func TestCreateToolDescribeMasksSecrets(t *testing.T) {
	tool := newTestCreateTool(newFakeCreateRunner(), nil)
	desc := tool.(interface {
		Describe(json.RawMessage) (string, string)
	})
	manifest := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: web\n  namespace: default\nstringData:\n  token: " + manifestx.PlaceholderGenerate + "\n"
	if _, err := tool.Execute(context.Background(), createInput(t, manifest, "", nil)); err != nil {
		t.Fatal(err)
	}
	command, change := desc.Describe(createInput(t, manifest, CreateModeApply, nil))
	if !strings.Contains(command, "create_resource mode=apply") || !strings.Contains(command, "Secret/default/web") {
		t.Fatalf("command = %q", command)
	}
	if !strings.Contains(change, "风险等级") {
		t.Fatalf("change = %q", change)
	}
}

func TestCreateToolDryRunIncludesSkillFindings(t *testing.T) {
	reg, err := skills.NewBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	runner := newFakeCreateRunner()
	tool := NewCreateTool(CreateOptions{
		Limits:           manifestx.Limits{MaxObjects: 3, MaxBytes: 8192},
		DefaultNamespace: "default",
		SecretAccess:     "create_only",
		VerifyTimeout:    30 * time.Second,
		Skills:           reg,
	}, runner, NewProposalStore(), "sess-1")
	tr, err := tool.Execute(context.Background(), createInput(t, cmManifest, "", nil))
	if err != nil {
		t.Fatal(err)
	}
	data := dataOf(t, tr)
	objects, _ := data["objects"].([]map[string]any)
	if len(objects) != 1 {
		t.Fatalf("objects = %v", objects)
	}
	item := objects[0]
	finds, _ := item["skills"].([]skills.Finding)
	if len(finds) == 0 {
		t.Fatalf("未输出 Skill findings: %v", item)
	}
	found := false
	for _, f := range finds {
		if f.Rule == "metadata_labels" {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺 metadata_labels finding: %+v", finds)
	}
}

// TestCreateToolSkillEscalationRaisesRisk 验证 P3.8 的风险升档闭环：
// 内置 create-workload Skill 的 elevateWhen 命中（hostNetwork=true）后，
// dry-run 输出的 skills 附带 HIGH 风险 finding，apply 风险随之抬到 HIGH。
func TestCreateToolSkillEscalationRaisesRisk(t *testing.T) {
	reg, err := skills.NewBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	runner := newFakeCreateRunner()
	tool := NewCreateTool(CreateOptions{
		Limits:           manifestx.Limits{MaxObjects: 3, MaxBytes: 8192},
		DefaultNamespace: "default",
		SecretAccess:     "create_only",
		VerifyTimeout:    30 * time.Second,
		Skills:           reg,
	}, runner, NewProposalStore(), "sess-1")

	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: risky
  namespace: default
spec:
  hostNetwork: true
  template:
    spec:
      containers:
      - name: web
        image: example.com/web:1.0
`
	if _, err := tool.Execute(context.Background(), createInput(t, manifest, "", nil)); err != nil {
		t.Fatal(err)
	}
	tr, err := tool.Execute(context.Background(), createInput(t, manifest, CreateModeApply, nil))
	if err != nil {
		t.Fatal(err)
	}
	data := dataOf(t, tr)
	objects, _ := data["objects"].([]map[string]any)
	if len(objects) != 1 {
		t.Fatalf("objects = %v", objects)
	}
	finds, _ := objects[0]["skills"].([]skills.Finding)
	var hostRisk skills.RiskLevel
	for _, f := range finds {
		if f.Rule == "host_network" {
			hostRisk = f.Risk
		}
	}
	if hostRisk != skills.RiskHigh {
		t.Fatalf("host_network finding 风险应为 HIGH, got %q (finds=%+v)", hostRisk, finds)
	}
	if got := tool.Risk(createInput(t, manifest, CreateModeApply, nil)); got != model.RiskHigh {
		t.Fatalf("apply risk = %s, want HIGH", got)
	}
}

func TestProposalStoreExpiryAndCap(t *testing.T) {
	store := NewProposalStoreWith(time.Millisecond, 2)
	store.Record("s1", []string{"fp1"}, []string{"ConfigMap/default/a"})
	if ok, _ := store.Match("s1", []string{"fp1"}); !ok {
		t.Fatal("刚记录的提案应命中")
	}
	store.Record("s2", []string{"fp-empty"}, nil)
	if ok, reason := store.Match("s2", []string{""}); ok {
		t.Fatalf("空指纹不得命中：%q", reason)
	}
	capped := NewProposalStoreWith(time.Hour, 2)
	for _, fp := range []string{"a", "b", "c"} {
		capped.Record("s3", []string{fp}, nil)
	}
	if capped.Count("s3") != 2 {
		t.Fatalf("单会话提案数未受限：%d", capped.Count("s3"))
	}
	if ok, _ := capped.Match("s3", []string{"a"}); ok {
		t.Fatal("最旧的提案应被淘汰")
	}
	capped.Forget("s3")
	if capped.Count("s3") != 0 {
		t.Fatal("Forget 未清空")
	}
}
