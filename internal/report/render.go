package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/security"
)

// redactor 是渲染边界的二次脱敏器（ADR-006：报告会外发）。
var redactor = security.NewRedactor()

// Renderer 报告渲染器：纯函数，不写文件。
type Renderer interface {
	Format() string
	Render(result *model.ScanResult) ([]byte, error)
}

// RendererFor 按格式返回渲染器（markdown/json/yaml，默认 markdown）。
func RendererFor(format string) Renderer {
	switch format {
	case "json":
		return JSONRenderer{}
	case "yaml":
		return YAMLRenderer{}
	default:
		return MarkdownRenderer{}
	}
}

// MarkdownRenderer 输出规范 §28 结构的 Markdown 报告。
type MarkdownRenderer struct{}

func (MarkdownRenderer) Format() string { return "markdown" }

func (MarkdownRenderer) Render(r *model.ScanResult) ([]byte, error) {
	r = redactResult(r) // 渲染边界字段级脱敏（ADR-006）
	var b strings.Builder
	// 标题与范围：全集群是"集群体检"，指定 namespace/Pod 是"业务体检"——报告标题、报告
	// 列表、筛选器三处用同一套类别词，用户在页面上看到的分类名和下载下来的文件名对得上。
	title := "Kubernetes 集群体检报告"
	scope := "cluster"
	switch {
	case r.Meta.Pod != "":
		title = fmt.Sprintf("Kubernetes 业务体检报告：Pod %s/%s", r.Meta.Namespace, r.Meta.Pod)
		scope = "pod " + r.Meta.Namespace + "/" + r.Meta.Pod
	case r.Meta.Namespace != "":
		title = fmt.Sprintf("Kubernetes 业务体检报告：命名空间 %s", r.Meta.Namespace)
		scope = "namespace " + r.Meta.Namespace
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "## 1. 体检概览\n\n")
	fmt.Fprintf(&b, "- Scan Scope: %s\n", scope)
	fmt.Fprintf(&b, "- Cluster: %s\n", orDash(r.Meta.Cluster))
	fmt.Fprintf(&b, "- ServerVersion: %s\n", r.Meta.ServerVersion)
	fmt.Fprintf(&b, "- Scan Time: %s\n", r.Meta.ScanStartedAt)
	fmt.Fprintf(&b, "- Duration: %s\n\n", r.Meta.Duration)

	fmt.Fprintf(&b, "## 2. 健康评分\n\n**%d / %d**　`%s`\n\n", r.HealthScore.Score, r.HealthScore.Max, scoreBar(r.HealthScore.Score, r.HealthScore.Max))
	if len(r.HealthScore.Penalties) > 0 {
		b.WriteString("扣分明细：\n\n")
		for _, p := range r.HealthScore.Penalties {
			fmt.Fprintf(&b, "- [%s] %s：-%d 分\n", p.Severity, p.Reason, p.Points)
		}
		b.WriteString("\n")
	}
	if r.HealthScore.CorrelatedExcluded > 0 {
		fmt.Fprintf(&b, "已排除相关异常 %d 条（不重复扣分）。\n\n", r.HealthScore.CorrelatedExcluded)
	}
	if r.HealthScore.Capped {
		fmt.Fprintf(&b, "> 评分已封顶：%s（数据完整性 %s，覆盖率 %.1f%%）\n\n",
			orDash(r.HealthScore.CapReason), orDash(r.HealthScore.DataIntegrity), r.HealthScore.Coverage*100)
	}

	// v2 区块：结论 → 采集质量 → 诊断口径 → 异常摘要（v1 报告缺这些区块，整段跳过）。
	section := 3
	if r.Conclusion != nil {
		fmt.Fprintf(&b, "## %d. 体检结论\n\n", section)
		section++
		renderConclusion(&b, r.Conclusion)
	}
	if r.Collection != nil {
		fmt.Fprintf(&b, "## %d. 采集质量\n\n", section)
		section++
		renderCollection(&b, r.Collection)
	}
	if r.DiagnosisSummary != nil {
		fmt.Fprintf(&b, "## %d. 诊断信息\n\n", section)
		section++
		renderDiagnosisSummary(&b, r.DiagnosisSummary, r.LLMSummary.StatusCounts, r.LLMSummary.Degradations)
	}

	fmt.Fprintf(&b, "## %d. 异常摘要\n\n| Severity | Count |\n|---|---:|\n", section)
	section++
	for _, sev := range severityOrder {
		fmt.Fprintf(&b, "| %s | %d |\n", sev, countBySeverity(r.Findings, sev))
	}
	b.WriteString("\n")

	if r.History != nil {
		fmt.Fprintf(&b, "## %d. 历史对比\n\n", section)
		section++
		fmt.Fprintf(&b, "- 上一轮扫描：%s\n", orDash(r.History.PreviousScanAt))
		fmt.Fprintf(&b, "- 新增：%d　持续：%d　恢复：%d\n\n", len(r.History.Added), len(r.History.Continued), len(r.History.Recovered))
		renderHistoryRefs(&b, "新增", r.History.Added)
		renderHistoryRefs(&b, "持续", r.History.Continued)
		renderHistoryRefs(&b, "恢复", r.History.Recovered)
	}
	if len(r.Incidents) > 0 {
		fmt.Fprintf(&b, "## %d. 故障链与根因分析\n\n", section)
		section++
		renderIncidents(&b, r)
	}
	otherFindings := findingsOutsideIncidents(r)
	if len(otherFindings) > 0 {
		fmt.Fprintf(&b, "## %d. 其他异常\n\n", section)
		section++
		renderOtherFindings(&b, otherFindings)
	}
	if len(r.Findings) > 0 {
		fmt.Fprintf(&b, "## %d. 完整异常与证据\n\n", section)
		section++
		renderCompleteFindings(&b, r)
	}

	// v2 区块：资源健康 → 处置清单 → 执行阶段。
	if len(r.Statistics) > 0 {
		fmt.Fprintf(&b, "## %d. 资源健康\n\n", section)
		section++
		fmt.Fprintf(&b, "| 资源域 | 资源数 | 异常 | 高风险及以上 | 说明 |\n|---|---:|---:|---:|---|\n")
		for _, d := range r.Statistics {
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %s |\n", d.Domain, d.Total, d.Unhealthy, d.Critical, orDash(d.Detail))
		}
		b.WriteString("\n")
	}
	if len(r.Remediation) > 0 {
		fmt.Fprintf(&b, "## %d. 处置建议\n\n", section)
		section++
		for _, kind := range []string{"investigation", "remediation", "verification"} {
			var items []model.RemediationItem
			for _, it := range r.Remediation {
				if it.Kind == kind {
					items = append(items, it)
				}
			}
			if len(items) == 0 {
				continue
			}
			fmt.Fprintf(&b, "### %s\n\n", remediationKindTitle(kind))
			for _, it := range items {
				if it.Format == "text" {
					fmt.Fprintf(&b, "%s\n\n", it.Text)
				} else if isCommandText(it.Text) {
					fmt.Fprintf(&b, "- `%s`", it.Text)
				} else {
					fmt.Fprintf(&b, "- %s", it.Text)
				}
				if it.Target != "" {
					fmt.Fprintf(&b, "　_%s_", it.Target)
				}
				if it.Risk != "" {
					fmt.Fprintf(&b, "　风险 `%s`", it.Risk)
				}
				if it.Required {
					b.WriteString("　**必须处理**")
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
		b.WriteString("以上命令仅供人工确认后执行，程序不会代你执行（ADR-014）。\n\n")
	}
	if len(r.Stages) > 0 {
		fmt.Fprintf(&b, "## %d. 执行阶段\n\n| 阶段 | 状态 | 耗时 | 说明 |\n|---|---|---:|---|\n", section)
		section++
		for _, st := range r.Stages {
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", orDash(st.Title), st.Status, orDash(st.Elapsed), orDash(st.Detail))
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "## %d. 资源汇总\n\n", section)
	section++
	s := r.Summary
	switch {
	case r.Meta.Pod != "":
		// 单 Pod 报告：只聚焦该 Pod 及其直接关联资源。
		fmt.Fprintf(&b, "- Pod: 1（%s/%s）\n", r.Meta.Namespace, r.Meta.Pod)
		fmt.Fprintf(&b, "- Namespace: %s\n", r.Meta.Namespace)
		fmt.Fprintf(&b, "- 本报告仅含该 Pod 及其直接关联资源（Workload/Node/PVC/Service）的 Findings\n\n")
	case r.Meta.Namespace != "":
		// 目标扫描：只展示命名空间内资源；集群级资源明确标注不随 namespace 过滤。
		fmt.Fprintf(&b, "- Namespaces: 1（%s）\n", r.Meta.Namespace)
		fmt.Fprintf(&b, "- Pods: %d\n- Services: %d\n- EndpointSlices: %d\n", s.Pods, s.Services, s.EndpointSlices)
		fmt.Fprintf(&b, "- Workloads: %d\n- Ingresses: %d\n- NetworkPolicies: %d\n- Events: %d\n", s.Workloads, s.Ingresses, s.NetworkPolicies, s.Events)
		fmt.Fprintf(&b, "- Roles: %d\n- RoleBindings: %d\n", s.Roles, s.RoleBindings)
		fmt.Fprintf(&b, "- Nodes / Storage（PV/StorageClass/VolumeAttachment）/ ClusterRoles / ClusterRoleBindings：集群级资源，不随 namespace 过滤，未纳入本报告汇总\n\n")
	default:
		fmt.Fprintf(&b, "- Namespaces: %d\n- Pods: %d\n- Nodes: %d\n- Services: %d\n- EndpointSlices: %d\n", s.Namespaces, s.Pods, s.Nodes, s.Services, s.EndpointSlices)
		fmt.Fprintf(&b, "- Workloads: %d\n- Storage: %d\n- Ingresses: %d\n- NetworkPolicies: %d\n- Events: %d\n", s.Workloads, s.Storage, s.Ingresses, s.NetworkPolicies, s.Events)
		fmt.Fprintf(&b, "- Roles: %d\n- RoleBindings: %d\n- ClusterRoles: %d\n- ClusterRoleBindings: %d\n\n", s.Roles, s.RoleBindings, s.ClusterRoles, s.ClusterRoleBindings)
	}

	if len(r.Components) > 0 {
		fmt.Fprintf(&b, "## %d. 系统组件\n\n", section)
		section++
		for _, c := range r.Components {
			icon, state := "❌", "未部署"
			switch {
			case !c.Present:
				icon, state = "❌", "未部署"
			case !c.Ready:
				icon, state = "⚠️", "部署中"
			default:
				icon, state = "✅", "正常"
			}
			fmt.Fprintf(&b, "- %s %s：%s（%s）\n", icon, c.Name, state, c.Detail)
		}
		b.WriteString("\n")
	}

	if len(r.CollectionErrors) > 0 {
		fmt.Fprintf(&b, "## %d. 采集错误\n\n", section)
		section++
		for _, e := range r.CollectionErrors {
			fmt.Fprintf(&b, "- %s %s：%s\n", e.Operation, e.Resource.Name, e.Message)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "## %d. 说明\n\n- 本报告为规则 + LLM 诊断结果；kubectl 命令仅供人工确认后执行，程序绝不自动执行（一期只读）。\n", section)
	return []byte(b.String()), nil
}

var severityOrder = []model.Severity{model.SeverityCritical, model.SeverityHigh, model.SeverityMedium, model.SeverityLow, model.SeverityInfo}

func countBySeverity(findings []model.Finding, sev model.Severity) int {
	n := 0
	for _, f := range findings {
		if f.Severity == sev {
			n++
		}
	}
	return n
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// JSONRenderer 输出 ScanResult 的机器可读版本（latest.json 契约）。
type JSONRenderer struct{}

func (JSONRenderer) Format() string { return "json" }

func (JSONRenderer) Render(r *model.ScanResult) ([]byte, error) {
	return json.MarshalIndent(redactResult(r), "", "  ")
}

// YAMLRenderer 输出 YAML 机器可读版本。
type YAMLRenderer struct{}

func (YAMLRenderer) Format() string { return "yaml" }

func (YAMLRenderer) Render(r *model.ScanResult) ([]byte, error) {
	return yaml.Marshal(redactResult(r))
}

// redactResult 返回报告字段级脱敏后的深拷贝（渲染边界，ADR-006）。
// 逐字段处理避免对整段 JSON 正则脱敏破坏结构。
func redactResult(r *model.ScanResult) *model.ScanResult {
	out := *r
	out.Findings = make([]model.Finding, len(r.Findings))
	for i, src := range r.Findings {
		f := src
		f.Title = redactor.Redact(f.Title)
		f.Summary = redactor.Redact(f.Summary)
		f.Evidence = make([]model.Evidence, len(src.Evidence))
		for j, e := range src.Evidence {
			e.Value = redactor.Redact(e.Value)
			f.Evidence[j] = e
		}
		out.Findings[i] = f
	}
	out.Diagnoses = make([]model.Diagnosis, len(r.Diagnoses))
	for i, src := range r.Diagnoses {
		d := src
		d.Summary = redactor.Redact(d.Summary)
		d.RootCause = redactor.Redact(d.RootCause)
		d.Impact = redactor.Redact(d.Impact)
		d.RemediationText = redactor.Redact(d.RemediationText)
		d.RemediationDirection = redactor.Redact(d.RemediationDirection)
		d.PossibleCauses = make([]string, len(src.PossibleCauses))
		for j, c := range src.PossibleCauses {
			d.PossibleCauses[j] = redactor.Redact(c)
		}
		d.Investigation = copyCommands(src.Investigation)
		d.Remediation = copyCommands(src.Remediation)
		d.Verification = copyCommands(src.Verification)
		out.Diagnoses[i] = d
	}
	out.CollectionErrors = make([]model.CollectionError, len(r.CollectionErrors))
	for i, e := range r.CollectionErrors {
		e.Message = redactor.Redact(e.Message)
		out.CollectionErrors[i] = e
	}
	out.Components = make([]model.ComponentInfo, len(r.Components))
	for i, c := range r.Components {
		c.Detail = redactor.Redact(c.Detail)
		out.Components[i] = c
	}
	return &out
}

// copyCommands 深拷贝命令列表并脱敏 Text。
func copyCommands(cmds []model.Command) []model.Command {
	out := make([]model.Command, len(cmds))
	for i, c := range cmds {
		c.Text = redactor.Redact(c.Text)
		out[i] = c
	}
	return out
}

// diagnosesByID 建立 FindingID → Diagnosis 索引。
func diagnosesByID(r *model.ScanResult) map[string]model.Diagnosis {
	m := make(map[string]model.Diagnosis, len(r.Diagnoses))
	for _, d := range r.Diagnoses {
		m[d.FindingID] = d
	}
	return m
}

// renderDiagnosis 渲染单个 Finding 的 LLM 诊断段落。
func renderDiagnosis(b *strings.Builder, diag model.Diagnosis) {
	b.WriteString("\n#### Root Cause\n\n")
	if !diag.LLMUsed {
		if diag.RootCause != "" {
			fmt.Fprintf(b, "**初步判断（规则）**：%s\n\n（LLM 分析不可用：%s）\n", diag.RootCause, diag.Error)
		} else {
			fmt.Fprintf(b, "（LLM 分析不可用：%s）\n", diag.Error)
		}
	} else {
		fmt.Fprintf(b, "%s\n\n- 置信度：%.0f%%\n- 证据链：%s\n", diag.RootCause, diag.Confidence*100, strings.Join(diag.EvidenceChain, ", "))
		if diag.Impact != "" {
			fmt.Fprintf(b, "- 影响：%s\n", diag.Impact)
		}
		if len(diag.PossibleCauses) > 0 {
			fmt.Fprintf(b, "- 可能原因：%s\n", strings.Join(diag.PossibleCauses, "；"))
		}
	}
	if len(diag.Investigation) > 0 {
		b.WriteString("\n#### 排查命令\n\n")
		for _, c := range diag.Investigation {
			fmt.Fprintf(b, "```bash\n%s\n```\n", c.Text)
		}
	}
	if diag.LLMUsed && (diag.RemediationText != "" || len(diag.Remediation) > 0 || diag.RemediationDirection != "") {
		b.WriteString("\n#### 修复方案\n\n")
		text := diag.RemediationText
		if text == "" {
			text = diag.RemediationDirection
		}
		if text != "" {
			fmt.Fprintf(b, "%s\n\n", text)
		}
		for _, c := range diag.Remediation {
			fmt.Fprintf(b, "```bash\n%s\n```\n\n- 风险：%s\n\n", c.Text, c.Risk)
		}
		if diag.RemediationDirection != "" && diag.RemediationDirection != text {
			fmt.Fprintf(b, "**修复方向（需人工确认具体修改内容后执行）**：%s\n", diag.RemediationDirection)
		}
	} else if !diag.LLMUsed && diag.RemediationDirection != "" {
		b.WriteString("\n#### 修复方向（规则）\n\n")
		fmt.Fprintf(b, "%s\n\n（需人工确认具体修改内容后执行）\n", diag.RemediationDirection)
	}
	if diag.LLMUsed && len(diag.Verification) > 0 {
		b.WriteString("\n#### 验证命令\n\n")
		for _, c := range diag.Verification {
			fmt.Fprintf(b, "```bash\n%s\n```\n", c.Text)
		}
	}
}

// renderHistoryRefs 渲染历史对比中的 Finding 摘要列表。
func renderHistoryRefs(b *strings.Builder, label string, refs []model.FindingRef) {
	if len(refs) == 0 {
		return
	}
	fmt.Fprintf(b, "%s：\n\n", label)
	for _, r := range refs {
		fmt.Fprintf(b, "- %s %s/%s：%s（%s，rule=%s）\n", severityIcon(r.Severity), r.Resource.Namespace, r.Resource.Name, r.Title, r.Severity, r.Rule)
	}
	b.WriteString("\n")
}

// ---- v2 报告区块渲染（需求 §7 / 方案 18）----

func renderConclusion(b *strings.Builder, c *model.Conclusion) {
	label := map[string]string{"healthy": "正常", "attention": "需关注", "critical": "严重", "unknown": "无法判定"}[c.Level]
	fmt.Fprintf(b, "**结论：%s**　%s\n\n", orDash(label), c.Headline)
	if c.Summary != "" {
		fmt.Fprintf(b, "%s\n\n", c.Summary)
	}
	if len(c.TopRisks) > 0 {
		b.WriteString("主要风险：\n\n")
		for _, s := range c.TopRisks {
			fmt.Fprintf(b, "- %s\n", s)
		}
		b.WriteString("\n")
	}
	if len(c.Priorities) > 0 {
		b.WriteString("优先处置：\n\n")
		for i, s := range c.Priorities {
			fmt.Fprintf(b, "%d. %s\n", i+1, s)
		}
		b.WriteString("\n")
	}
	if len(c.DataCaveats) > 0 {
		b.WriteString("结论限制（数据缺口）：\n\n")
		for _, s := range c.DataCaveats {
			fmt.Fprintf(b, "- ⚠️ %s\n", s)
		}
		b.WriteString("\n")
	}
}

func renderCollection(b *strings.Builder, q *model.CollectionQuality) {
	label := map[string]string{
		model.IntegrityComplete: "完整", model.IntegrityPartial: "部分缺失", model.IntegrityInsufficient: "关键数据不足",
	}[q.Integrity]
	fmt.Fprintf(b, "- 完整性：**%s**（%s）\n", orDash(label), q.Integrity)
	fmt.Fprintf(b, "- 采集项：%d 计划 / %d 成功 / %d 失败 / %d 跳过\n", q.Planned, q.Collected, q.Failed, q.Skipped)
	fmt.Fprintf(b, "- 采集覆盖率：%.1f%%\n\n", q.Coverage*100)
	if len(q.FailedKinds) > 0 {
		fmt.Fprintf(b, "- 采集失败类型：%s\n\n", strings.Join(q.FailedKinds, "、"))
	}
	if len(q.SkippedKinds) > 0 {
		fmt.Fprintf(b, "- 本次跳过类型：%s（档位配置，不代表数据正常）\n\n", strings.Join(q.SkippedKinds, "、"))
	}
	if len(q.CriticalGaps) > 0 {
		b.WriteString("关键缺口：\n\n")
		for _, s := range q.CriticalGaps {
			fmt.Fprintf(b, "- ❌ %s\n", s)
		}
		b.WriteString("\n")
	}
	if len(q.MissingData) > 0 {
		b.WriteString("数据缺口：\n\n")
		for _, s := range q.MissingData {
			fmt.Fprintf(b, "- %s\n", s)
		}
		b.WriteString("\n")
	}
	if len(q.Errors) > 0 {
		b.WriteString("采集错误：\n\n")
		for _, s := range q.Errors {
			fmt.Fprintf(b, "- %s\n", s)
		}
		b.WriteString("\n")
	}
}

func renderDiagnosisSummary(b *strings.Builder, d *model.DiagnosisSummary, statusCounts map[string]int, degradations []model.LLMDegradation) {
	mode := map[string]string{
		"rule_only":    "仅确定性诊断（未调用 LLM）",
		"llm_enhanced": "确定性诊断 + LLM 增强",
		"llm_degraded": "LLM 未生效，结论均来自确定性诊断",
	}[d.Mode]
	fmt.Fprintf(b, "- 诊断口径：**%s**\n\n", orDash(mode))
	fmt.Fprintf(b, "- 规则 Finding：%d　确定性诊断：%d（确认 %d / 疑似 %d / 未定位 %d）\n",
		d.RuleFindings, d.Deterministic, d.Confirmed, d.Probable, d.Unknown)
	if d.LLMCalls > 0 || d.LLMSucceeded > 0 || d.LLMFailed > 0 {
		fmt.Fprintf(b, "- LLM 调用：%d　成功 %d　失败 %d　降级 %d　校验退回 %d　混合 %d\n\n",
			d.LLMCalls, d.LLMSucceeded, d.LLMFailed, d.LLMDegraded, d.LLMRejected, d.Hybrid)
	}
	if len(statusCounts) > 0 {
		keys := make([]string, 0, len(statusCounts))
		for k := range statusCounts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("- LLM 状态分布：\n\n")
		for _, k := range keys {
			fmt.Fprintf(b, "  - `%s`：%d\n", k, statusCounts[k])
		}
		b.WriteString("\n")
	}
	if len(degradations) > 0 {
		b.WriteString("- 降级明细：\n\n")
		for _, dg := range degradations {
			kept := ""
			if dg.KeptDeterministic {
				kept = "（已保留确定性结论）"
			}
			fmt.Fprintf(b, "  - %s `%s`：%s%s\n", orDash(dg.IncidentID), dg.Status, orDash(dg.Reason), kept)
		}
		b.WriteString("\n")
	}
}

// findingsOutsideIncidents 返回尚未归入任何故障链的 Finding。
// Incident 的 root 与 members 都算已归组，不能只在“其他异常”里再次平铺。
func findingsOutsideIncidents(r *model.ScanResult) []model.Finding {
	grouped := make(map[string]bool)
	for _, inc := range r.Incidents {
		grouped[inc.Root.ID] = true
		for _, member := range inc.Members {
			grouped[member.ID] = true
		}
	}
	var out []model.Finding
	for _, finding := range r.Findings {
		if !grouped[finding.ID] {
			out = append(out, finding)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := model.SeverityRank(out[i].Severity), model.SeverityRank(out[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if out[i].Resource.Namespace != out[j].Resource.Namespace {
			return out[i].Resource.Namespace < out[j].Resource.Namespace
		}
		if out[i].Resource.Name != out[j].Resource.Name {
			return out[i].Resource.Name < out[j].Resource.Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func renderOtherFindings(b *strings.Builder, findings []model.Finding) {
	b.WriteString("| 严重程度 | 资源 | 异常 | 规则 | 说明 |\n|---|---|---|---|---|\n")
	for _, finding := range findings {
		class := "独立异常"
		if finding.Correlated {
			class = "未形成独立因果链"
		}
		fmt.Fprintf(b, "| %s | `%s/%s` | %s | `%s` | %s |\n",
			finding.Severity,
			orDash(finding.Resource.Namespace),
			finding.Resource.Name,
			escapeTable(finding.Title),
			finding.Rule,
			class,
		)
	}
	b.WriteString("\n未归入故障链表示本次没有足够关系证据把它与某个根因连起来，不代表问题一定轻微。\n\n")
}

func renderCompleteFindings(b *strings.Builder, r *model.ScanResult) {
	fmt.Fprintf(b, "<details>\n<summary>展开完整 Finding 与 Evidence（%d 条，默认折叠）</summary>\n\n", len(r.Findings))
	diags := diagnosesByID(r)
	for _, finding := range r.Findings {
		renderFindingDetail(b, finding, diags[finding.ID])
	}
	b.WriteString("</details>\n\n")
}

func renderFindingDetail(b *strings.Builder, f model.Finding, diag model.Diagnosis) {
	fmt.Fprintf(b, "#### %s `%s/%s`：%s（%s）\n\n",
		severityIcon(f.Severity), orDash(f.Resource.Namespace), f.Resource.Name, f.Title, f.Severity)
	fmt.Fprintf(b, "- Rule: `%s`\n- Finding ID: `%s`", f.Rule, f.ID)
	if f.Role != "" {
		fmt.Fprintf(b, "\n- Role: `%s`", f.Role)
	}
	b.WriteString("\n")
	if f.Correlated {
		b.WriteString("- 状态：相关异常（不重复扣分）\n")
	}
	if len(f.Evidence) > 0 {
		b.WriteString("\n**Evidence**\n\n")
		for _, e := range f.Evidence {
			fmt.Fprintf(b, "- %s#%s `%s`: %s\n", f.ID, e.ID, e.Key, evidenceDisplayValue(e))
		}
	}
	if len(f.Related) > 0 {
		b.WriteString("\n**Related**\n\n")
		for _, ref := range f.Related {
			fmt.Fprintf(b, "- %s/%s（%s）\n", orDash(ref.Namespace), ref.Name, ref.Kind)
		}
	}
	if diag.FindingID != "" {
		renderDiagnosis(b, diag)
	}
	b.WriteString("\n")
}

func evidenceDisplayValue(e model.Evidence) string {
	value := e.Value
	if e.Kind == model.EvLog {
		return summarizeLog(value, 8)
	}
	if len(value) > 800 {
		return value[:800] + "…"
	}
	return value
}

func escapeTable(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

type incidentEvidenceItem struct {
	ref      string
	finding  string
	evidence model.Evidence
}

func incidentEvidenceItems(r *model.ScanResult, inc model.Incident) []incidentEvidenceItem {
	byID := make(map[string]model.Finding, len(r.Findings))
	for _, finding := range r.Findings {
		byID[finding.ID] = finding
	}
	refSet := make(map[string]bool, len(inc.EvidenceRefs))
	for _, ref := range inc.EvidenceRefs {
		refSet[ref] = true
	}
	memberIDs := append([]string{inc.Root.ID}, findingRefIDs(inc.Members)...)
	var out []incidentEvidenceItem
	seen := map[string]bool{}
	for _, findingID := range memberIDs {
		finding, ok := byID[findingID]
		if !ok {
			continue
		}
		for _, evidence := range finding.Evidence {
			ref := finding.ID + "#" + evidence.ID
			// 兼容早期 Incident.EvidenceRefs 只存 E-ID 的报告。
			if len(refSet) > 0 && !refSet[ref] && !refSet[evidence.ID] {
				continue
			}
			if seen[ref] {
				continue
			}
			seen[ref] = true
			out = append(out, incidentEvidenceItem{ref: ref, finding: finding.ID, evidence: evidence})
		}
	}
	return out
}

func findingRefIDs(refs []model.FindingRef) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.ID)
	}
	return out
}

func renderIncidents(b *strings.Builder, r *model.ScanResult) {
	diagByIncident := map[string]model.Diagnosis{}
	for _, d := range r.Diagnoses {
		if d.IncidentID != "" {
			diagByIncident[d.IncidentID] = d
		}
	}
	for i, inc := range r.Incidents {
		fmt.Fprintf(b, "### 故障链 %d：%s（%s）\n\n", i+1, orDash(inc.Title), inc.Severity)
		fmt.Fprintf(b, "- 根因状态：**%s**", orDash(inc.RootCauseStatus))
		if inc.RootCauseRule != "" {
			fmt.Fprintf(b, "　诊断器 `%s`", inc.RootCauseRule)
		}
		if inc.RootCauseMethod != "" {
			fmt.Fprintf(b, "　来源 `%s`", inc.RootCauseMethod)
		}
		if inc.Confidence > 0 {
			fmt.Fprintf(b, "　置信度 %.2f", inc.Confidence)
		}
		b.WriteString("\n")
		rootCause := inc.RootCause
		diag, hasDiag := diagByIncident[inc.ID]
		if hasDiag && strings.TrimSpace(diag.RootCause) != "" {
			rootCause = strings.TrimSpace(diag.RootCause)
		}
		if rootCause != "" {
			fmt.Fprintf(b, "- 根因：%s\n", rootCause)
		}
		if hasDiag && strings.TrimSpace(diag.Summary) != "" {
			fmt.Fprintf(b, "- 诊断摘要：%s\n", strings.TrimSpace(diag.Summary))
		}
		if hasDiag && strings.TrimSpace(diag.Uncertainty) != "" {
			fmt.Fprintf(b, "- 未确认边界：%s\n", strings.TrimSpace(diag.Uncertainty))
		}
		if inc.ConfidenceBasis != "" {
			fmt.Fprintf(b, "- 可信度依据：%s\n", inc.ConfidenceBasis)
		}
		fmt.Fprintf(b, "- 根 Finding：%s `%s/%s`（%s）\n", inc.Root.ID, inc.Root.Resource.Kind, inc.Root.Resource.Namespace+"/"+inc.Root.Resource.Name, inc.Root.Rule)
		if len(inc.ImpactChain) > 0 {
			fmt.Fprintf(b, "- 影响链：%s\n", strings.Join(inc.ImpactChain, " → "))
		}
		evidence := incidentEvidenceItems(r, inc)
		if len(evidence) > 0 {
			fmt.Fprintf(b, "\n<details>\n<summary>展开故障链证据（%d 条）</summary>\n\n", len(evidence))
			for _, item := range evidence {
				fmt.Fprintf(b, "- `%s` `%s`: %s\n", item.ref, item.evidence.Key, evidenceDisplayValue(item.evidence))
			}
			b.WriteString("\n</details>\n\n")
		} else if len(inc.EvidenceRefs) > 0 {
			fmt.Fprintf(b, "- 证据引用：%s\n", strings.Join(inc.EvidenceRefs, " "))
		}
		if len(inc.CausalEdges) > 0 {
			fmt.Fprintf(b, "\n<details>\n<summary>展开因果传播路径（%d 条，均有资源关系出处）</summary>\n\n", len(inc.CausalEdges))
			for _, e := range inc.CausalEdges {
				fmt.Fprintf(b, "- `%s` → `%s`　`%s`　%s（%s）\n",
					e.SourceFinding, e.TargetFinding, e.RelationType, e.Mechanism, e.Confidence)
				if len(e.EvidenceRefs) > 0 {
					fmt.Fprintf(b, "  - 依据证据：%s\n", strings.Join(e.EvidenceRefs, " "))
				}
			}
			b.WriteString("\n</details>\n\n")
		}
		if len(inc.Members) > 0 {
			b.WriteString("派生症状：\n\n")
			for _, m := range inc.Members {
				fmt.Fprintf(b, "- %s %s/%s：%s（%s）\n", severityIcon(m.Severity), m.Resource.Namespace, m.Resource.Name, m.Title, m.Rule)
			}
			b.WriteString("\n")
		}
		if len(inc.AlternativeCauses) > 0 {
			b.WriteString("待排除的其他可能：\n\n")
			for _, s := range inc.AlternativeCauses {
				fmt.Fprintf(b, "- %s\n", s)
			}
			b.WriteString("\n")
		}
		if len(inc.EvidenceGaps) > 0 {
			b.WriteString("证据缺口：\n\n")
			for _, s := range inc.EvidenceGaps {
				fmt.Fprintf(b, "- ⚠️ %s\n", s)
			}
			b.WriteString("\n")
		}
		if d, ok := diagByIncident[inc.ID]; ok {
			fmt.Fprintf(b, "- 诊断来源：`%s`　状态：`%s`\n\n", orDash(d.Source), orDash(d.Status))
			if d.Validation != nil && !d.Validation.Passed {
				b.WriteString("- LLM 结论未通过校验，已退回确定性结论：\n\n")
				for _, s := range d.Validation.Issues {
					fmt.Fprintf(b, "  - %s\n", s)
				}
				b.WriteString("\n")
			}
			if d.Validation != nil && len(d.Validation.CommandIssues) > 0 {
				b.WriteString("- 部分命令未收录（不会伪造替代写命令）：\n\n")
				for _, s := range d.Validation.CommandIssues {
					fmt.Fprintf(b, "  - %s\n", s)
				}
				b.WriteString("\n")
			}
			renderDiagnosis(b, d)
		}
	}
}

func remediationKindTitle(kind string) string {
	switch kind {
	case "remediation":
		return "修复动作（需人工确认）"
	case "verification":
		return "验证方式"
	default:
		return "排查步骤"
	}
}

func isCommandText(s string) bool {
	for _, p := range []string{"kubectl", "helm", "crictl", "journalctl", "systemctl", "curl ", "openssl", "df ", "ls "} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
