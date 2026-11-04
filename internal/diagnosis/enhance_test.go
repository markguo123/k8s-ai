package diagnosis

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/relations"
)

// 四期 P9：LLM 增强（架构图第 7 阶段）与结果校验（第 8 阶段）。
//
// 两条底线必须可回归：
//  1. 增强上下文只搬运「程序已经知道的事实」——确定性结论、真实关系边、真实状态、
//     采集时间、编号化证据。模型拿到的是事实，不是又一次自由发挥的机会。
//  2. 校验必须拦住编造的证据编号与资源名，并退回确定性结论；报告要能区分
//     「模型没答」与「模型越界」。

// enhSnapshot 是「快照里真实存在的东西」：Namespace prod、Pod prod/web-0、
// Node node-1、StatefulSet prod/web。校验层只在这些对象里判定模型是否编造。
func enhSnapshot() *model.ClusterSnapshot {
	return &model.ClusterSnapshot{
		CollectedAt: "2026-11-04T10:00:00Z",
		Namespaces:  []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "prod"}}},
		Pods: []model.PodInfo{{
			Ref:       model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-0"},
			Phase:     "Running",
			NodeName:  "node-1",
			OwnerRefs: []model.ResourceRef{{Kind: "StatefulSet", Namespace: "prod", Name: "web"}},
			Containers: []model.ContainerInfo{{
				Name: "web", State: "Waiting", Reason: "OOMKilled", ExitCode: 137, RestartCount: 3,
			}},
		}},
		Nodes: []model.NodeInfo{{
			Ref:        model.ResourceRef{Kind: "Node", Name: "node-1"},
			Conditions: []model.ConditionInfo{{Type: "Ready", Status: "True"}},
		}},
		Workloads: []model.WorkloadInfo{{
			Ref: model.ResourceRef{Kind: "StatefulSet", Namespace: "prod", Name: "web"},
		}},
	}
}

// enhIndex 带一条 OOM 事件：事件是「为什么」的主要来源，必须进上下文。
func enhIndex(snap *model.ClusterSnapshot) *model.CorrelationIndex {
	return &model.CorrelationIndex{
		EventsByKey: map[string][]model.EventInfo{
			snap.Pods[0].Ref.Key(): {{
				Type: "Warning", Reason: "OOMKilling", Count: 4,
				Message:        "System OOM encountered, victim process: web",
				InvolvedObject: snap.Pods[0].Ref,
			}},
		},
	}
}

// enhIncident 一条真实故障链：根因 Pod OOM（f1）+ 派生症状 Service 无就绪端点（f2）。
func enhIncident() (model.Incident, map[string]model.Finding) {
	root := testFinding()
	member := model.Finding{
		ID: "f2", Rule: "ServiceNoEndpoint", Severity: model.SeverityHigh, Title: "Service 无 Endpoint",
		Resource: model.ResourceRef{Kind: "Service", Namespace: "prod", Name: "web"},
		Evidence: []model.Evidence{{ID: "E1", Key: "readyEndpoints", Value: "0"}},
	}
	inc := model.Incident{
		ID: "inc-1", Title: "prod/web 服务不可用", Severity: model.SeverityCritical,
		Root: refOfTest(root), Members: []model.FindingRef{refOfTest(member)},
	}
	return inc, map[string]model.Finding{root.ID: root, member.ID: member}
}

// enhDet 确定性诊断层给 inc-1 的结论（confirmed，带可信度依据与承认的缺口）。
func enhDet() map[string]model.Diagnosis {
	return map[string]model.Diagnosis{"inc-1": {
		FindingID:       "f1",
		Source:          "deterministic",
		Status:          model.DiagStatusDeterministic,
		RootCauseStatus: model.RootCauseConfirmed,
		RootCause:       "prod/web-0 容器内存 limit 过低，反复 OOMKilled",
		Diagnoser:       "workload-oom",
		Confidence:      0.9,
		ConfidenceBasis: "lastReason=OOMKilled 与 exitCode=137 两条证据互证",
		EvidenceGaps:    []string{"未采集到 limit 的变更记录"},
		EvidenceChain:   []string{"E1", "E2"},
	}}
}

func enhContext(snap *model.ClusterSnapshot, idx *model.CorrelationIndex,
	det map[string]model.Diagnosis) *Context {
	return &Context{Snapshot: snap, Index: idx, Graph: relations.Build(snap, idx), Deterministic: det}
}

// enhCleanOutput 只引用真实证据（含故障链成员证据 f2#E1）与真实资源。
const enhCleanOutput = `{
  "summary": "内存限制过低",
  "rootCause": "prod/web-0 容器内存 limit 过低，反复 OOMKilled 后 prod/web 无就绪端点",
  "confidence": 0.9,
  "confidenceLevel": "HIGH_CONFIDENCE",
  "causalChain": "limit 过低导致 OOMKilled，Pod 反复重启使 Service 失去就绪端点",
  "evidenceChain": ["E1", "E2", "f2#E1"],
  "impact": "prod/web 服务间歇不可用",
  "possibleCauses": ["内存不足"],
  "investigation": ["kubectl -n prod describe pod web-0"],
  "remediation": ["kubectl -n prod set resources statefulset web --containers=web --limits=memory=512Mi"],
  "remediationText": "调高内存限制并观察 prod/web-0 是否恢复",
  "verification": ["kubectl -n prod get pod web-0"],
  "risk": "MEDIUM"
}`

// enhInventedOutput 引用了快照里不存在的资源；同段的 linux/amd64、apps/v1
// 是版本号样式文本，前缀不是真实命名空间，不得被误判成编造资源。
const enhInventedOutput = `{
  "summary": "缺少前置资源",
  "rootCause": "prod/ghost-pod 从未创建（镜像 linux/amd64，接口 apps/v1）",
  "confidence": 0.95,
  "confidenceLevel": "CONFIRMED",
  "causalChain": "前置对象缺失导致 prod/web-0 无法就绪",
  "evidenceChain": ["E1"],
  "impact": "prod/web 不可用",
  "possibleCauses": ["配置缺失"],
  "investigation": ["kubectl -n prod get pod ghost-pod"],
  "remediation": ["kubectl -n prod get pod web-0"],
  "remediationText": "补齐缺失对象后观察 prod/web-0",
  "verification": ["kubectl -n prod get pod web-0"],
  "risk": "LOW"
}`

// enhHangingOutput 根因不引用任何证据：不算越界，但不允许维持高置信度。
const enhHangingOutput = `{
  "summary": "内存限制过低",
  "rootCause": "prod/web-0 容器内存 limit 过低",
  "confidence": 0.9,
  "confidenceLevel": "CONFIRMED",
  "evidenceChain": [],
  "impact": "prod/web 间歇不可用",
  "investigation": ["kubectl -n prod get pod web-0"],
  "remediation": ["kubectl -n prod set resources statefulset web --containers=web --limits=memory=512Mi"],
  "remediationText": "调高内存限制",
  "verification": ["kubectl -n prod get pod web-0"],
  "risk": "MEDIUM"
}`

func detRootCause() string { return enhDet()["inc-1"].RootCause }

func hasRef(refs []string, want string) bool {
	for _, r := range refs {
		if r == want {
			return true
		}
	}
	return false
}

// TestEnhancedPromptCarriesDeterministicFacts 增强上下文必须把程序已知的事实搬齐。
func TestEnhancedPromptCarriesDeterministicFacts(t *testing.T) {
	snap := enhSnapshot()
	inc, byID := enhIncident()
	f := &fakeLLM{responses: []string{enhCleanOutput}}

	ds, summary, err := NewWithContext(diagOpts(), enhContext(snap, enhIndex(snap), enhDet())).
		DiagnoseIncidents(context.Background(), []model.Incident{inc}, byID, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 || ds[0].IncidentID != inc.ID || !ds[0].LLMUsed {
		t.Fatalf("diagnoses = %+v", ds)
	}
	if len(f.lastMsgs) != 3 {
		t.Fatalf("消息结构异常: %+v", f.lastMsgs)
	}
	prompt := f.lastMsgs[1].Content
	for _, want := range []string{
		"[程序已给出的确定性结论]",
		"根因状态：confirmed",
		"诊断器：workload-oom",
		"可信度依据：lastReason=OOMKilled 与 exitCode=137 两条证据互证",
		"程序明确承认的证据缺口：未采集到 limit 的变更记录",
		"该根因已确认证据充分",
		"[同一故障链的其他异常（引用其证据时写 findingID#证据ID）]",
		"f2#E1",
		"[资源关系（来自 OwnerReference/Selector/Spec 字段，非推测）]",
		"ownedBy",
		"StatefulSet/prod/web",
		"[资源真实状态（只读采集，不得改写）]",
		"Pod prod/web-0：phase=Running node=node-1",
		"OOMKilling",
		"数据采集时间：2026-11-04T10:00:00Z",
		"[禁止事项]",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("增强上下文缺少 %q\n---\n%s", want, prompt)
		}
	}
	// 增强内容只能追加在根因上下文之后，不能挤掉证据本身。
	if strings.Index(prompt, "Evidence:") > strings.Index(prompt, "[程序已给出的确定性结论]") {
		t.Error("确定性结论排在证据之前，预算紧张时会挤掉根因上下文")
	}
	if summary.Rejected != 0 || summary.Validated != 1 || summary.Degraded != 0 {
		t.Fatalf("summary = %+v", summary)
	}
}

// TestEnhancementSkippedWhenBudgetTight 预算不足时宁可不增强，也不能截掉证据。
func TestEnhancementSkippedWhenBudgetTight(t *testing.T) {
	snap := enhSnapshot()
	inc, byID := enhIncident()
	opts := diagOpts()
	opts.MaxInputTokens = 120
	f := &fakeLLM{responses: []string{enhCleanOutput}}

	if _, _, err := NewWithContext(opts, enhContext(snap, enhIndex(snap), enhDet())).
		DiagnoseIncidents(context.Background(), []model.Incident{inc}, byID, f); err != nil {
		t.Fatal(err)
	}
	prompt := f.lastMsgs[1].Content
	if strings.Contains(prompt, "[程序已给出的确定性结论]") {
		t.Errorf("预算不足时仍注入了增强内容:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Finding: OOM") || !strings.Contains(prompt, "E1 lastReason") {
		t.Errorf("根因上下文被截没了:\n%s", prompt)
	}
}

// TestNewWithContextNilKeepsLegacyBehaviour dc=nil 时不得注入任何增强段落。
func TestNewWithContextNilKeepsLegacyBehaviour(t *testing.T) {
	inc, byID := enhIncident()
	f := &fakeLLM{responses: []string{validOutput}}

	ds, summary, err := NewWithContext(diagOpts(), nil).
		DiagnoseIncidents(context.Background(), []model.Incident{inc}, byID, f)
	if err != nil {
		t.Fatal(err)
	}
	prompt := f.lastMsgs[1].Content
	for _, unwanted := range []string{"[程序已给出的确定性结论]", "[资源关系", "[资源真实状态", "[禁止事项]"} {
		if strings.Contains(prompt, unwanted) {
			t.Errorf("dc=nil 不应注入 %q:\n%s", unwanted, prompt)
		}
	}
	// 没有确定性结论可退回：越界输出留在结果里，但必须带着未通过校验的标记。
	if len(ds) != 1 || !ds[0].LLMUsed {
		t.Fatalf("diagnoses = %+v", ds)
	}
	if ds[0].Validation == nil || ds[0].Validation.Passed {
		t.Fatalf("应记录未通过的校验: %+v", ds[0].Validation)
	}
	if ds[0].RootCauseStatus != model.RootCauseProbable || ds[0].Confidence > 0.5 ||
		ds[0].ConfidenceLevel != "POSSIBLE" {
		t.Fatalf("越界结论未降级: %+v", ds[0])
	}
	if summary.Rejected != 1 || summary.Validated != 0 {
		t.Fatalf("summary = %+v", summary)
	}
}

// TestIncidentDiagnosisRejectsFabricatedEvidence 编造证据编号：退回确定性结论并留原因。
func TestIncidentDiagnosisRejectsFabricatedEvidence(t *testing.T) {
	snap := enhSnapshot()
	inc, byID := enhIncident()
	f := &fakeLLM{responses: []string{validOutput}} // validOutput 引用了不存在的 E99

	ds, summary, err := NewWithContext(diagOpts(), enhContext(snap, enhIndex(snap), enhDet())).
		DiagnoseIncidents(context.Background(), []model.Incident{inc}, byID, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 {
		t.Fatalf("diagnoses = %+v", ds)
	}
	got := ds[0]
	if got.LLMUsed || got.Status != model.LLMStatusEvidenceInvalid {
		t.Fatalf("应退回确定性结论: %+v", got)
	}
	if got.RootCause != detRootCause() || got.RootCauseStatus != model.RootCauseConfirmed {
		t.Errorf("确定性根因被改写: %+v", got)
	}
	v := got.Validation
	if v == nil || v.Passed || v.Rule != validationRule || len(v.FabricatedEvidence) != 1 ||
		v.FabricatedEvidence[0] != "E99" {
		t.Errorf("校验结果异常: %+v", v)
	}
	if summary.Rejected != 1 || summary.Validated != 0 {
		t.Errorf("summary = %+v", summary)
	}
	if summary.StatusCounts[model.LLMStatusEvidenceInvalid] != 1 {
		t.Errorf("statusCounts = %v", summary.StatusCounts)
	}
	if len(summary.Degradations) != 1 || !summary.Degradations[0].KeptDeterministic ||
		summary.Degradations[0].IncidentID != inc.ID {
		t.Errorf("degradations = %+v", summary.Degradations)
	}
}

// TestIncidentDiagnosisRejectsInventedResources 提到快照里没有的资源：判越界并降级。
func TestIncidentDiagnosisRejectsInventedResources(t *testing.T) {
	snap := enhSnapshot()
	inc, byID := enhIncident()
	f := &fakeLLM{responses: []string{enhInventedOutput}}

	ds, summary, err := NewWithContext(diagOpts(), enhContext(snap, enhIndex(snap), nil)).
		DiagnoseIncidents(context.Background(), []model.Incident{inc}, byID, f)
	if err != nil {
		t.Fatal(err)
	}
	v := ds[0].Validation
	if v == nil || v.Passed {
		t.Fatalf("应判为越界: %+v", v)
	}
	if len(v.UnknownResources) != 1 || v.UnknownResources[0] != "prod/ghost-pod" {
		t.Errorf("未知资源判定异常（版本号样式文本不应误判）: %+v", v)
	}
	if ds[0].RootCauseStatus != model.RootCauseProbable || ds[0].Confidence > 0.5 ||
		!v.Downgraded || ds[0].ConfidenceLevel != "POSSIBLE" {
		t.Errorf("越界结论未降级: %+v validation=%+v", ds[0], v)
	}
	if summary.Rejected != 1 || !ds[0].LLMUsed {
		t.Errorf("summary = %+v", summary)
	}
}

// TestIncidentDiagnosisAcceptsMemberEvidenceRef 上下文要求的 findingID#E-ID
// 引用不能在 schema 层被当成编造剥掉，否则模型越按规矩写越会被判越界。
func TestIncidentDiagnosisAcceptsMemberEvidenceRef(t *testing.T) {
	snap := enhSnapshot()
	inc, byID := enhIncident()
	f := &fakeLLM{responses: []string{enhCleanOutput}}

	ds, summary, err := NewWithContext(diagOpts(), enhContext(snap, enhIndex(snap), enhDet())).
		DiagnoseIncidents(context.Background(), []model.Incident{inc}, byID, f)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRef(ds[0].EvidenceChain, "f2#E1") {
		t.Errorf("成员证据被剥掉了: %v", ds[0].EvidenceChain)
	}
	if ds[0].Validation == nil || !ds[0].Validation.Passed {
		t.Errorf("校验应通过: %+v", ds[0].Validation)
	}
	if ds[0].Source != "llm" || ds[0].Status != model.LLMStatusSuccess {
		t.Errorf("诊断来源/状态异常: %+v", ds[0])
	}
	if summary.Validated != 1 || summary.Rejected != 0 || summary.Degraded != 0 {
		t.Errorf("summary = %+v", summary)
	}
	if summary.StatusCounts[model.LLMStatusSuccess] != 1 {
		t.Errorf("statusCounts = %v", summary.StatusCounts)
	}
}

// TestFindingPathStillDropsForeignEvidenceRef 单 Finding 送诊没有故障链，
// 跨对象引用仍按编造处理（增强语义只在按 Incident 送诊时成立）。
func TestFindingPathStillDropsForeignEvidenceRef(t *testing.T) {
	out := strings.Replace(enhCleanOutput, `"evidenceChain": ["E1", "E2", "f2#E1"]`,
		`"evidenceChain": ["E1", "f2#E1"]`, 1)
	f := &fakeLLM{responses: []string{out}}

	ds, _, err := New(diagOpts()).Diagnose(context.Background(), []model.Finding{testFinding()}, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 || len(ds[0].EvidenceChain) != 1 || ds[0].EvidenceChain[0] != "E1" {
		t.Fatalf("evidenceChain = %v", ds[0].EvidenceChain)
	}
}

// TestIncidentDiagnosisDowngradesUnreferencedRootCause 根因不引用证据：
// 不算越界（不能据此说模型胡说），但必须下调置信度并写明原因。
func TestIncidentDiagnosisDowngradesUnreferencedRootCause(t *testing.T) {
	snap := enhSnapshot()
	inc, byID := enhIncident()
	f := &fakeLLM{responses: []string{enhHangingOutput}}

	ds, summary, err := NewWithContext(diagOpts(), enhContext(snap, enhIndex(snap), nil)).
		DiagnoseIncidents(context.Background(), []model.Incident{inc}, byID, f)
	if err != nil {
		t.Fatal(err)
	}
	v := ds[0].Validation
	if v == nil || !v.Passed {
		t.Fatalf("没有编造内容时不应判越界: %+v", v)
	}
	if !v.Downgraded || ds[0].Confidence > 0.5 || ds[0].ConfidenceLevel != "POSSIBLE" {
		t.Errorf("悬空根因未下调置信度: %+v", ds[0])
	}
	if len(v.Issues) == 0 {
		t.Errorf("必须留下下调原因: %+v", v)
	}
	if summary.Rejected != 0 || summary.Validated != 1 {
		t.Errorf("summary = %+v", summary)
	}
}

// TestIncidentDiagnosisRepairsBadJSON 首轮输出不是 JSON：带修复指令重试一次。
func TestIncidentDiagnosisRepairsBadJSON(t *testing.T) {
	snap := enhSnapshot()
	inc, byID := enhIncident()
	f := &fakeLLM{responses: []string{"这不是 JSON", enhCleanOutput}}

	ds, summary, err := NewWithContext(diagOpts(), enhContext(snap, enhIndex(snap), enhDet())).
		DiagnoseIncidents(context.Background(), []model.Incident{inc}, byID, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 || !ds[0].LLMUsed {
		t.Fatalf("修复重试应成功: %+v", ds)
	}
	if summary.Calls != 2 || summary.Rejected != 0 || summary.Validated != 1 || summary.Degraded != 0 {
		t.Fatalf("summary = %+v", summary)
	}
}

// TestIncidentDiagnosisRequestFailureKeepsDeterministic 请求失败要保留确定性结论，
// 并把失败类别记成 request_failed（报告要能区分「模型没答」和「程序没调」）。
func TestIncidentDiagnosisRequestFailureKeepsDeterministic(t *testing.T) {
	snap := enhSnapshot()
	inc, byID := enhIncident()
	f := &fakeLLM{errs: []error{errors.New("boom")}}

	ds, summary, err := NewWithContext(diagOpts(), enhContext(snap, enhIndex(snap), enhDet())).
		DiagnoseIncidents(context.Background(), []model.Incident{inc}, byID, f)
	if err != nil {
		t.Fatal(err)
	}
	if ds[0].LLMUsed || ds[0].Status != model.LLMStatusRequestFailed ||
		ds[0].RootCause != detRootCause() || !strings.Contains(ds[0].Error, "调用失败") {
		t.Fatalf("应保留确定性结论: %+v", ds[0])
	}
	if summary.Calls != 1 || summary.Failed != 1 || summary.Degraded != 1 || summary.Rejected != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(summary.Degradations) != 1 || !summary.Degradations[0].KeptDeterministic {
		t.Fatalf("degradations = %+v", summary.Degradations)
	}
}

// TestIncidentDiagnosisTimeoutKeepsDeterministic 超时是降级，不是整次体检失败。
func TestIncidentDiagnosisTimeoutKeepsDeterministic(t *testing.T) {
	snap := enhSnapshot()
	inc, byID := enhIncident()
	f := &fakeLLM{responses: []string{enhCleanOutput}, delay: time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	ds, summary, err := NewWithContext(diagOpts(), enhContext(snap, enhIndex(snap), enhDet())).
		DiagnoseIncidents(ctx, []model.Incident{inc}, byID, f)
	if err != nil {
		t.Fatalf("超时应降级而不是失败: %v", err)
	}
	if ds[0].LLMUsed || ds[0].Status != model.LLMStatusTimeout || ds[0].RootCause != detRootCause() {
		t.Fatalf("超时未保留确定性结论: %+v", ds[0])
	}
	if summary.Degraded != 1 || summary.StatusCounts[model.LLMStatusTimeout] != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

// TestIncidentDiagnosisWithoutDeterministicKeepsRuleConclusion 确定性诊断没覆盖时
// 仍要留「规则判定 + 修复方向」，且不得谎称保留了确定性结论。
func TestIncidentDiagnosisWithoutDeterministicKeepsRuleConclusion(t *testing.T) {
	snap := enhSnapshot()
	inc, byID := enhIncident()
	f := &fakeLLM{errs: []error{errors.New("boom")}}

	ds, summary, err := NewWithContext(diagOpts(), enhContext(snap, enhIndex(snap), nil)).
		DiagnoseIncidents(context.Background(), []model.Incident{inc}, byID, f)
	if err != nil {
		t.Fatal(err)
	}
	if ds[0].LLMUsed || ds[0].Source != "rule" || ds[0].RootCause == "" || ds[0].RemediationDirection == "" {
		t.Fatalf("降级结论缺内容: %+v", ds[0])
	}
	if len(summary.Degradations) != 1 || summary.Degradations[0].KeptDeterministic {
		t.Fatalf("degradations = %+v", summary.Degradations)
	}
}

// TestCapTextTruncatesOnRuneBoundary 增强文本按 rune 收口，保住预算也保住汉字。
func TestCapTextTruncatesOnRuneBoundary(t *testing.T) {
	s := strings.Repeat("内存限制过低导致反复重启，", 60)
	if got := capText(s, 0); got != s {
		t.Error("maxTokens<=0 时不应截断")
	}
	if got := capText("short text", 1000); got != "short text" {
		t.Error("未超预算时不应截断")
	}
	got := capText(s, 20)
	if !utf8.ValidString(got) {
		t.Error("截断切坏了 UTF-8 边界")
	}
	if !strings.HasPrefix(got, "内存限制") {
		t.Error("应优先保住开头（确定性结论段）")
	}
	if !strings.HasSuffix(got, "…（增强上下文已达预算上限）\n") {
		t.Errorf("缺少预算上限标记: %q", got)
	}
	if len(got) >= len(s) {
		t.Errorf("未生效截断: %d >= %d", len(got), len(s))
	}
}
