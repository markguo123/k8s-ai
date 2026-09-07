package diagnosis

import (
	"fmt"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

const validOutput = `{
  "summary": "内存限制过低",
  "rootCause": "容器内存 limit 300Mi 过低导致 OOMKilled",
  "confidence": 0.9,
  "evidenceChain": ["E1", "E99"],
  "impact": "服务间歇不可用",
  "possibleCauses": ["内存不足"],
  "investigation": ["kubectl -n prod get pod web-0", "kubectl -n prod delete pod web-0"],
  "remediation": ["kubectl -n prod set resources deployment web --containers=web --limits=memory=512Mi"],
  "remediationText": "调高内存限制并观察 Pod 是否恢复",
  "verification": ["kubectl -n prod get pod web-0"],
  "risk": "MEDIUM"
}`

func testFinding() model.Finding {
	return model.Finding{
		ID: "f1", Rule: "OOMKilled", Severity: model.SeverityHigh, Title: "OOM",
		Resource: model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-0"},
		Evidence: []model.Evidence{{ID: "E1", Key: "lastReason", Value: "OOMKilled"}, {ID: "E2", Key: "exitCode", Value: "137"}},
	}
}

func TestParseLLMOutput(t *testing.T) {
	out, err := parseLLMOutput(validOutput)
	if err != nil {
		t.Fatal(err)
	}
	if out.RootCause == "" || out.Confidence != 0.9 {
		t.Fatalf("parse = %+v", out)
	}
	// 代码块包裹
	fenced := "```json\n" + validOutput + "\n```"
	if _, err := parseLLMOutput(fenced); err != nil {
		t.Fatalf("fenced parse: %v", err)
	}
	// 坏 JSON
	if _, err := parseLLMOutput("not json"); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
	// 缺 rootCause
	if _, err := parseLLMOutput(`{"summary":"x","confidence":0.5}`); err == nil {
		t.Fatal("缺 rootCause 应报错")
	}
	// 置信度越界
	if _, err := parseLLMOutput(`{"summary":"x","rootCause":"y","confidence":1.5}`); err == nil {
		t.Fatal("置信度越界应报错")
	}
}

func TestBuildDiagnosisValidation(t *testing.T) {
	out, err := parseLLMOutput(validOutput)
	if err != nil {
		t.Fatal(err)
	}
	d := buildDiagnosis(testFinding(), out)
	// Evidence ID：E1 保留，E99 丢弃。
	if len(d.EvidenceChain) != 1 || d.EvidenceChain[0] != "E1" {
		t.Fatalf("evidence chain = %v", d.EvidenceChain)
	}
	// 排查命令：只读 get 通过；delete 属写动词被丢弃。
	if len(d.Investigation) != 1 || d.Investigation[0].Text != "kubectl -n prod get pod web-0" {
		t.Fatalf("investigation = %+v", d.Investigation)
	}
	// 修复命令保留并带风险；文字说明必须映射。
	if len(d.Remediation) != 1 || d.Remediation[0].Risk != model.RiskMedium {
		t.Fatalf("remediation = %+v", d.Remediation)
	}
	if d.RemediationText != "调高内存限制并观察 Pod 是否恢复" {
		t.Fatalf("remediationText = %q", d.RemediationText)
	}
}

func TestValidateCommand(t *testing.T) {
	cases := []struct {
		text     string
		category model.CommandCategory
		ns       string
		wantOK   bool
	}{
		{"kubectl -n prod get pod web-0", model.CmdInvestigation, "prod", true},
		{"kubectl get pod web-0", model.CmdInvestigation, "", false},                // 缺 namespace
		{"kubectl -n prod delete pod web-0", model.CmdInvestigation, "prod", false}, // 排查命令不允许写动词
		{"kubectl -n prod delete pod web-0", model.CmdRemediation, "prod", true},
		{"kubectl get node node-1", model.CmdInvestigation, "", true}, // node 集群级
		{"kubectl -n prod delete pod", model.CmdRemediation, "prod", false},
		{"kubectl -n prod get pvc data", model.CmdRemediation, "prod", true},                                  // 只读确认命令允许进入 remediation（命令化修复方案）                                   // 缺资源名
		{"kubectl -n prod rollout restart deployment <deployment-name>", model.CmdRemediation, "prod", false}, // 占位符命令
		{"echo rm -rf /", model.CmdRemediation, "prod", false},
		// P1 体验：修复命令必须非交互（system.md §三十五 规则 4）。
		{"kubectl -n prod edit configmap nginx-config", model.CmdRemediation, "prod", false},
		{"kubectl -n prod attach nginx-pod", model.CmdRemediation, "prod", false},
		{"kubectl -n prod port-forward svc/web 8080:80", model.CmdRemediation, "prod", false},
		{"kubectl -n prod exec -it nginx-pod -- sh", model.CmdRemediation, "prod", false},
		{"kubectl -n prod exec nginx-pod -- ls", model.CmdRemediation, "prod", false}, // exec 不作为修复命令（风险不可控且非修复），只能用于 investigation
		{"kubectl -n prod run tmp --image=busybox -i -- sleep 1", model.CmdRemediation, "prod", false},
		{"kubectl -n prod patch configmap nginx-config --type merge -p '{\"data\":{\"k\":\"v\"}}'", model.CmdRemediation, "prod", true},
	}
	for _, tc := range cases {
		_, ok := validateCommand(tc.text, tc.category, tc.ns)
		if ok != tc.wantOK {
			t.Errorf("validateCommand(%q, %s) = %v, want %v", tc.text, tc.category, ok, tc.wantOK)
		}
	}
}

// TestValidateCommandNamespace 验证命名空间校验真正生效 + 缺省自动补全：
// 命令自带 -n 必须与 Finding 命名空间一致（不一致丢弃），缺省时补全 Finding 命名空间。
func TestValidateCommandNamespace(t *testing.T) {
	cases := []struct {
		text     string
		ns       string
		wantOK   bool
		wantText string // 补全后的命令文本；空表示不校验文本
	}{
		// 缺省 -n：自动补全 Finding 命名空间（命令可独立执行）。
		{"kubectl get pod web-0", "prod", true, "kubectl -n prod get pod web-0"},
		{"kubectl get pvc data -o yaml", "prod", true, "kubectl -n prod get pvc data -o yaml"},
		{"kubectl logs web-0 --tail=200", "prod", true, "kubectl -n prod logs web-0 --tail=200"},
		{"kubectl get secret my-secret", "prod", true, "kubectl -n prod get secret my-secret"}, // secret 命名空间级
		// 已带且一致：原样保留。
		{"kubectl -n prod get pod web-0", "prod", true, "kubectl -n prod get pod web-0"},
		{"kubectl --namespace=prod get pod web-0", "prod", true, "kubectl --namespace=prod get pod web-0"},
		{"kubectl -n=prod get pod web-0", "prod", true, "kubectl -n=prod get pod web-0"},
		{"kubectl get pod web-0 -n prod", "prod", true, "kubectl get pod web-0 -n prod"},
		{"kubectl get pod web-0 --namespace=prod", "prod", true, "kubectl get pod web-0 --namespace=prod"},
		// 命令自带 ns 与 Finding 不一致：视为编造/笔误，整体丢弃。
		{"kubectl -n staging get pod web-0", "prod", false, ""},
		{"kubectl --namespace=staging get pod web-0", "prod", false, ""},
		{"kubectl get pod web-0 -n staging", "prod", false, ""},
		{"kubectl get pod web-0 --namespace=staging", "prod", false, ""},
		// Finding 无命名空间可补全（如集群级场景误引用命名空间资源）：丢弃。
		{"kubectl get pod web-0", "", false, ""},
		// 显式跨命名空间查询：不补全、不校验。
		{"kubectl get pod web-0 -A", "prod", true, "kubectl get pod web-0 -A"},
		{"kubectl -A get pod web-0", "prod", true, "kubectl -A get pod web-0"},
		// 集群级资源：不要求 -n。
		{"kubectl get node node-1", "", true, "kubectl get node node-1"},
		{"kubectl -n prod get node node-1", "", true, "kubectl -n prod get node node-1"},
	}
	for _, tc := range cases {
		cmd, ok := validateCommand(tc.text, model.CmdInvestigation, tc.ns)
		if ok != tc.wantOK {
			t.Errorf("validateCommand(%q, ns=%q) ok = %v, want %v", tc.text, tc.ns, ok, tc.wantOK)
			continue
		}
		if tc.wantText != "" && cmd.Text != tc.wantText {
			t.Errorf("validateCommand(%q, ns=%q) text = %q, want %q", tc.text, tc.ns, cmd.Text, tc.wantText)
		}
	}
}

// TestExtractVerbNamespace 验证动词前的 -n/--namespace（含 = 形式）被正确提取。
func TestExtractVerbNamespace(t *testing.T) {
	cases := []struct {
		tokens []string
		verb   string
		ns     string
	}{
		{[]string{"-n", "prod", "get", "pod", "web-0"}, "get", "prod"},
		{[]string{"--namespace", "prod", "get", "pod", "web-0"}, "get", "prod"},
		{[]string{"-n=prod", "get", "pod", "web-0"}, "get", "prod"},
		{[]string{"--namespace=prod", "get", "pod", "web-0"}, "get", "prod"},
		{[]string{"-o", "yaml", "get", "pod", "web-0"}, "get", ""},
		{[]string{"get", "pod", "web-0"}, "get", ""},
		{[]string{"-n"}, "", ""},
	}
	for _, tc := range cases {
		verb, _, ns := extractVerb(tc.tokens)
		if verb != tc.verb || ns != tc.ns {
			t.Errorf("extractVerb(%v) = (%q, ns=%q), want (%q, ns=%q)", tc.tokens, verb, ns, tc.verb, tc.ns)
		}
	}
}

func TestFilterEvidenceRefs(t *testing.T) {
	refs := filterEvidenceRefs(testFinding().Evidence, []string{"E1", "E99", "E2"})
	if len(refs) != 2 || strings.Join(refs, ",") != "E1,E2" {
		t.Fatalf("refs = %v", refs)
	}
}

// TestSchemaNewFields 验证置信度等级/因果链/不确定性字段解析与校验（含归一化兜底）。
func TestSchemaNewFields(t *testing.T) {
	out, err := parseLLMOutput(`{"summary":"s","rootCause":"r","confidence":0.7,"confidenceLevel":"HIGH_CONFIDENCE","causalChain":"c","evidenceChain":[],"impact":"i","possibleCauses":[],"investigation":[],"remediation":[],"verification":[],"risk":"LOW","uncertainty":"u"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out.ConfidenceLevel != "HIGH_CONFIDENCE" || out.CausalChain != "c" || out.Uncertainty != "u" {
		t.Fatalf("新字段解析异常: %+v", out)
	}
	if out.Risk != "LOW" {
		t.Fatalf("LOW risk 未映射: %q", out.Risk)
	}
	// #4 修复：HIGH_CONF0.9 这类变体 + 其他乱拼法 不再报错，而是归一化。
	variants := []struct {
		raw        string
		confidence float64
		want       string
	}{
		{"HIGH_CONF0.9", 0.9, "HIGH_CONFIDENCE"},
		{"HIGH_CONF", 0.7, "HIGH_CONFIDENCE"},
		{"confirmed", 0.95, "CONFIRMED"},
		{"Confidence: High", 0.7, "HIGH_CONFIDENCE"},
		{"possible", 0.4, "POSSIBLE"},
		{"BOGUS", 0.9, "CONFIRMED"},        // 无法识别但 confidence=0.9 → CONFIRMED (≥0.85)
		{"BOGUS", 0.5, "POSSIBLE"},          // confidence=0.5 → POSSIBLE (0.3~0.59)
		{"BOGUS", 0.65, "HIGH_CONFIDENCE"},  // confidence=0.65 → HIGH_CONFIDENCE (0.6~0.84)
		{"", 0.95, "CONFIRMED"},             // 空值按 confidence 高值
		{"", 0.2, "UNKNOWN"},                // 空值按 confidence 低值
		{"uncertain", 0.5, "UNKNOWN"},
	}
	for _, v := range variants {
		json := `{"summary":"s","rootCause":"r","confidence":` + fmtFloat(v.confidence) + `,"confidenceLevel":"` + v.raw + `"}`
		out, err := parseLLMOutput(json)
		if err != nil {
			t.Fatalf("confidenceLevel 变体 %q 归一化不应报错: %v", v.raw, err)
		}
		if out.ConfidenceLevel != v.want {
			t.Errorf("confidenceLevel 变体 %q (conf=%v)= %q, want %q", v.raw, v.confidence, out.ConfidenceLevel, v.want)
		}
	}
	// risk 归一化
	riskCases := []struct {
		raw  string
		want string
	}{
		{"LOW", "LOW"}, {"low", "LOW"}, {"SAFE", "SAFE"},
		{"HIGH_RISK", "HIGH"}, {"SEVERE", "CRITICAL"}, {"whatever", "MEDIUM"},
	}
	for _, r := range riskCases {
		json := `{"summary":"s","rootCause":"r","confidence":0.7,"risk":"` + r.raw + `"}`
		out, err := parseLLMOutput(json)
		if err != nil {
			t.Fatalf("risk 变体 %q 归一化不应报错: %v", r.raw, err)
		}
		if out.Risk != r.want {
			t.Errorf("risk 变体 %q = %q, want %q", r.raw, out.Risk, r.want)
		}
	}
	d := buildDiagnosis(testFinding(), out)
	if d.ConfidenceLevel != "HIGH_CONFIDENCE" || d.CausalChain != "c" || d.Uncertainty != "u" {
		t.Fatalf("Diagnosis 映射异常: %+v", d)
	}
}

func fmtFloat(f float64) string {
	// 测试中 confidence 范围固定 0.2/0.5/0.7/0.9/0.95，用通用 sprintf 即可
	return fmt.Sprintf("%g", f)
}
