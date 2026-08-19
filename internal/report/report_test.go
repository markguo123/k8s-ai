package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func testResult() *model.ScanResult {
	return &model.ScanResult{
		Meta:    model.ScanMeta{ServerVersion: "v1.28.13", Duration: "1s"},
		Summary: model.ClusterSummary{Pods: 3, Namespaces: 1},
		Findings: []model.Finding{{
			ID:       "f1",
			Rule:     "CrashLoopBackOff",
			Severity: model.SeverityCritical,
			Title:    "容器反复崩溃",
			Resource: model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-0"},
			Evidence: []model.Evidence{{ID: "E1", Kind: model.EvObjectField, Key: "restartCount", Value: "17"}},
		}},
		HealthScore: ComputeHealthScore([]model.Finding{{
			ID: "f1", Rule: "CrashLoopBackOff", Severity: model.SeverityCritical,
			Resource: model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-0"},
		}}),
		Components: []model.ComponentInfo{{Name: "CoreDNS", Present: true, Ready: true, Detail: "pods=1"}},
		Diagnoses: []model.Diagnosis{{
			FindingID:     "f1",
			Summary:       "内存限制过低",
			RootCause:     "容器内存 limit 过低导致 OOMKilled",
			Confidence:    0.9,
			EvidenceChain: []string{"E1"},
			LLMUsed:       true,
			Remediation:   []model.Command{{Category: model.CmdRemediation, Text: "kubectl -n prod set resources deployment web --containers=web --limits=memory=512Mi", Risk: model.RiskMedium}},
		}},
	}
}

func TestComputeHealthScore(t *testing.T) {
	findings := []model.Finding{
		{ID: "a", Rule: "R", Severity: model.SeverityCritical},
		{ID: "b", Rule: "R", Severity: model.SeverityHigh},
		{ID: "c", Rule: "R", Severity: model.SeverityHigh, Correlated: true},
		{ID: "d", Rule: "R", Severity: model.SeverityLow},
	}
	hs := ComputeHealthScore(findings)
	// 存在 HIGH 及以上（Critical/High）非 Correlated 根因：基础分由 100 降至 70，
	// 再叠加 Critical 30 + High 15 + Low 1。
	if hs.Score != 70-30-15-1 {
		t.Fatalf("score = %d, want %d", hs.Score, 70-30-15-1)
	}
	if hs.CorrelatedExcluded != 1 {
		t.Fatalf("CorrelatedExcluded = %d, want 1", hs.CorrelatedExcluded)
	}
	if len(hs.Penalties) != 3 {
		t.Fatalf("penalties = %d, want 3", len(hs.Penalties))
	}
	// 封顶 0
	all := make([]model.Finding, 6)
	for i := range all {
		all[i] = model.Finding{ID: string(rune('a' + i)), Severity: model.SeverityCritical}
	}
	if got := ComputeHealthScore(all).Score; got != 0 {
		t.Fatalf("score = %d, want 0", got)
	}
}

func TestMarkdownRender(t *testing.T) {
	out, err := MarkdownRenderer{}.Render(testResult())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"# Kubernetes 集群巡检报告", "## 2. 健康评分", "## 3. 异常摘要", "容器反复崩溃", "E1", "系统组件", "CoreDNS", "Root Cause", "修复方案", "风险：MEDIUM", "█"} {
		if !strings.Contains(s, want) {
			t.Errorf("markdown 缺少 %q", want)
		}
	}
}

// TestRenderRedaction 验证渲染边界二次脱敏且 JSON 仍合法。
func TestRenderRedaction(t *testing.T) {
	r := testResult()
	r.Findings[0].Evidence = append(r.Findings[0].Evidence,
		model.Evidence{ID: "E2", Kind: model.EvLog, Key: "logs/current", Value: "error token=abc-secret-123"})
	out, err := JSONRenderer{}.Render(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "abc-secret-123") {
		t.Fatalf("JSON 泄露敏感值: %s", out)
	}
	if !json.Valid(out) {
		t.Fatalf("脱敏后 JSON 不合法: %s", out)
	}
}

func TestWriterModes(t *testing.T) {
	dir := t.TempDir()
	// none：不落盘
	if paths, err := NewWriter(dir).Write(testResult(), model.ReportOptions{Mode: "none"}); err != nil || len(paths) != 0 {
		t.Fatalf("none 模式异常: %v %v", paths, err)
	}
	// latest：latest.md + latest.json
	paths, err := NewWriter(dir).Write(testResult(), model.ReportOptions{Mode: "latest", Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("latest 文件数 = %d, want 2", len(paths))
	}
	for _, f := range []string{"latest.md", "latest.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("缺少 %s: %v", f, err)
		}
	}
	// daily：追加时间戳文件
	paths, err = NewWriter(dir).Write(testResult(), model.ReportOptions{Mode: "daily", Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("daily 文件数 = %d, want 3", len(paths))
	}
}

// TestMarkdownRenderTargeted 验证目标扫描输出"命名空间巡检报告"且资源汇总不含集群级资源。
func TestMarkdownRenderTargeted(t *testing.T) {
	r := testResult()
	r.Meta.Namespace = "mysql-dev"
	out, err := MarkdownRenderer{}.Render(r)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"Kubernetes 命名空间巡检报告：mysql-dev", "Scan Scope: namespace mysql-dev", "集群级资源，不随 namespace 过滤"} {
		if !strings.Contains(s, want) {
			t.Errorf("目标扫描报告缺少 %q", want)
		}
	}
	if strings.Contains(s, "- Nodes: 19") {
		t.Error("目标扫描报告不应展示集群级 Nodes 计数")
	}
}

// TestMarkdownRenderPod 验证单 Pod 扫描输出 Pod 巡检报告。
func TestMarkdownRenderPod(t *testing.T) {
	r := testResult()
	r.Meta.Namespace = "mysql-dev"
	r.Meta.Pod = "db-0"
	out, err := MarkdownRenderer{}.Render(r)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"Kubernetes Pod 巡检报告：mysql-dev/db-0", "Scan Scope: pod mysql-dev/db-0", "- Pod: 1（mysql-dev/db-0）"} {
		if !strings.Contains(s, want) {
			t.Errorf("Pod 报告缺少 %q", want)
		}
	}
}

// TestRenderTerminal 验证终端一屏摘要。
func TestRenderTerminal(t *testing.T) {
	r := testResult()
	s := RenderTerminal(r)
	for _, want := range []string{"k8s-ai scan", "集群健康：", "█", "CRITICAL 1", "重点问题", "系统组件"} {
		if !strings.Contains(s, want) {
			t.Errorf("终端摘要缺少 %q", want)
		}
	}
	clean := testResult()
	clean.Findings = nil
	if !strings.Contains(RenderTerminal(clean), "未发现异常") {
		t.Fatal("无 Finding 时应显示未发现异常")
	}
}

// TestSummarizeLog 验证日志压缩：保留前 N 行并统计 ERROR。
func TestSummarizeLog(t *testing.T) {
	value := ""
	for i := 0; i < 30; i++ {
		value += "[ERROR] plugin/errors: timeout\n"
	}
	out := summarizeLog(value, 8)
	if strings.Count(out, "[ERROR]") != 8 {
		t.Fatalf("应保留 8 行: %q", out)
	}
	if !strings.Contains(out, "共 30 行") || !strings.Contains(out, "ERROR 30 行") {
		t.Fatalf("缺少统计: %q", out)
	}
}

// TestRenderDegradedDiagnosis 验证降级诊断渲染"初步判断（规则）"。
func TestRenderDegradedDiagnosis(t *testing.T) {
	r := testResult()
	r.Diagnoses = []model.Diagnosis{{
		FindingID: "f1",
		RootCause: "规则判定：容器反复崩溃（restartCount=17）。日志关键行：panic: boom",
		LLMUsed:   false,
		Error:     "LLM 调用超时",
	}}
	s, err := MarkdownRenderer{}.Render(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(s), "初步判断（规则）") || !strings.Contains(string(s), "panic: boom") {
		t.Fatalf("降级报告缺少初步判断: %s", s)
	}
	if !strings.Contains(string(s), "LLM 调用超时") {
		t.Fatalf("应标注 LLM 不可用原因")
	}
}

// TestLogHighlight 验证终端摘要会显示日志关键行/最后一行。
func TestLogHighlight(t *testing.T) {
	if got := logHighlight("INFO start\nnginx: [emerg] host not found in upstream\n"); !strings.Contains(got, "[emerg]") {
		t.Fatalf("未命中 emerg: %q", got)
	}
	if got := logHighlight("line1\nline2\n"); got != "line2" {
		t.Fatalf("无关键字时应取最后一行: %q", got)
	}
}

// TestJSONComponentFieldNames 回归：latest.json 组件字段必须是小写 camelCase（P0-1）。
func TestJSONComponentFieldNames(t *testing.T) {
	out, err := JSONRenderer{}.Render(testResult())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `"name": "CoreDNS"`) {
		t.Fatalf("组件字段应为小写 name: %s", s)
	}
	if strings.Contains(s, `"Name"`) {
		t.Fatal("JSON 中出现大写字段名 Name")
	}
}

// TestMarkdownHistorySection 验证历史对比段落渲染。
func TestMarkdownHistorySection(t *testing.T) {
	r := testResult()
	r.History = &model.HistoryDiff{
		PreviousScanAt: "2026-08-17T00:00:00Z",
		Added:          []model.FindingRef{{ID: "n", Rule: "R", Severity: model.SeverityHigh, Title: "新增问题", Resource: model.ResourceRef{Kind: "Pod", Namespace: "ns", Name: "x"}}},
		Continued:      []model.FindingRef{{ID: "c", Rule: "R", Severity: model.SeverityHigh, Title: "持续问题", Resource: model.ResourceRef{Kind: "Pod", Namespace: "ns", Name: "y"}}},
		Recovered:      []model.FindingRef{{ID: "g", Rule: "R", Severity: model.SeverityMedium, Title: "已恢复", Resource: model.ResourceRef{Kind: "Pod", Namespace: "ns", Name: "z"}}},
	}
	s, err := MarkdownRenderer{}.Render(r)
	if err != nil {
		t.Fatal(err)
	}
	out := string(s)
	for _, want := range []string{"历史对比", "新增：1　持续：1　恢复：1", "新增问题", "持续问题", "已恢复"} {
		if !strings.Contains(out, want) {
			t.Errorf("历史段落缺少 %q", want)
		}
	}
	if !strings.Contains(RenderTerminal(r), "历史对比：新增 1 / 持续 1 / 恢复 1") {
		t.Fatal("终端摘要缺少历史对比计数")
	}
}

// TestRenderIncidents 验证 Incident 聚合渲染：根因带诊断，派生成员折叠。
func TestRenderIncidents(t *testing.T) {
	rootID := "root-1"
	memberDepID := "dep-1"
	memberSvcID := "svc-1"
	r := &model.ScanResult{
		Meta: model.ScanMeta{ServerVersion: "v1.28.13"},
		Findings: []model.Finding{
			{ID: rootID, Rule: "CrashLoopBackOff", Severity: model.SeverityHigh, Title: "容器崩溃重启", Resource: model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-abc"}, Evidence: []model.Evidence{{ID: "E1", Key: "restartCount", Value: "9"}}},
			{ID: memberDepID, Rule: "DeploymentReplica", Severity: model.SeverityHigh, Title: "副本不足", Resource: model.ResourceRef{Kind: "Deployment", Namespace: "prod", Name: "web"}, Evidence: []model.Evidence{{ID: "E1", Key: "desiredReplicas", Value: "1"}}},
			{ID: memberSvcID, Rule: "ServiceNoEndpoint", Severity: model.SeverityMedium, Title: "无 Endpoint", Resource: model.ResourceRef{Kind: "Service", Namespace: "prod", Name: "web-svc"}},
		},
		Incidents: []model.Incident{{
			ID: rootID, Title: "容器崩溃重启", Severity: model.SeverityHigh,
			Root: model.FindingRef{ID: rootID, Rule: "CrashLoopBackOff", Severity: model.SeverityHigh, Title: "容器崩溃重启", Resource: model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-abc"}},
			Members: []model.FindingRef{
				{ID: memberDepID, Rule: "DeploymentReplica", Severity: model.SeverityHigh, Title: "副本不足", Resource: model.ResourceRef{Kind: "Deployment", Namespace: "prod", Name: "web"}},
				{ID: memberSvcID, Rule: "ServiceNoEndpoint", Severity: model.SeverityMedium, Title: "无 Endpoint", Resource: model.ResourceRef{Kind: "Service", Namespace: "prod", Name: "web-svc"}},
			},
		}},
		Diagnoses: []model.Diagnosis{{
			FindingID: rootID, Summary: "s", RootCause: "根因", Confidence: 0.9, ConfidenceLevel: "CONFIRMED", LLMUsed: true,
		}},
	}
	md, err := MarkdownRenderer{}.Render(r)
	if err != nil {
		t.Fatal(err)
	}
	s := string(md)
	for _, want := range []string{"关联问题（派生影响）", "副本不足", "无 Endpoint"} {
		if !strings.Contains(s, want) {
			t.Errorf("markdown 缺少 %q", want)
		}
	}
	// 派生告警不再作为独立 ### 标题显示，只并入根因的"关联问题（派生影响）"。
	for _, hidden := range []string{"### 🟠 prod/web：副本不足", "### 🟡 prod/web-svc：无 Endpoint"} {
		if strings.Contains(s, hidden) {
			t.Errorf("派生告警仍被独立显示：%q", hidden)
		}
	}
	term := RenderTerminal(r)
	if !strings.Contains(term, "关联（派生）：副本不足；无 Endpoint") {
		t.Fatalf("终端缺少派生折叠: %q", term)
	}
}

// TestRenderRemediationDirection 修复方向兜底为文字说明，在报告与终端渲染。
func TestRenderRemediationDirection(t *testing.T) {
	r := testResult()
	r.Diagnoses = []model.Diagnosis{{
		FindingID: "f1", RootCause: "根因", LLMUsed: true,
		RemediationDirection: "确认 PVC 是否应存在，人工确认后重建或修正引用。",
	}}
	s, err := MarkdownRenderer{}.Render(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(s), "修复方案") || !strings.Contains(string(s), "确认 PVC 是否应存在") {
		t.Fatalf("报告缺少修复方案文字: %s", s)
	}
	if !strings.Contains(RenderTerminal(r), "建议：确认 PVC 是否应存在") {
		t.Fatal("终端缺少修复建议文字")
	}
}

// TestRenderRemediationText 修复方案=文字说明+命令，Markdown 与终端都渲染。
func TestRenderRemediationText(t *testing.T) {
	r := testResult()
	r.Diagnoses = []model.Diagnosis{{
		FindingID: "f1", RootCause: "根因", LLMUsed: true,
		RemediationText: "先确认 PVC 是否存在，若缺失则创建或修正 Deployment 引用。",
		Remediation: []model.Command{
			{Category: model.CmdRemediation, Text: "kubectl -n prod get pvc data", Risk: model.RiskSafe},
		},
	}}
	s, err := MarkdownRenderer{}.Render(r)
	if err != nil {
		t.Fatal(err)
	}
	md := string(s)
	for _, want := range []string{"修复方案", "先确认 PVC 是否存在", "kubectl -n prod get pvc data"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown 缺少 %q", want)
		}
	}
	term := RenderTerminal(r)
	if !strings.Contains(term, "建议：先确认 PVC 是否存在") || !strings.Contains(term, "命令：kubectl -n prod get pvc data") {
		t.Fatalf("终端缺少文字+命令: %q", term)
	}
}
