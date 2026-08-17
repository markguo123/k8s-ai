package report

import (
	"encoding/json"
	"fmt"
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
	// 标题与范围：目标扫描输出"命名空间巡检报告"，全集群输出"集群巡检报告"。
	title := "Kubernetes 集群巡检报告"
	scope := "cluster"
	switch {
	case r.Meta.Pod != "":
		title = fmt.Sprintf("Kubernetes Pod 巡检报告：%s/%s", r.Meta.Namespace, r.Meta.Pod)
		scope = "pod " + r.Meta.Namespace + "/" + r.Meta.Pod
	case r.Meta.Namespace != "":
		title = fmt.Sprintf("Kubernetes 命名空间巡检报告：%s", r.Meta.Namespace)
		scope = "namespace " + r.Meta.Namespace
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "## 1. 巡检概览\n\n")
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

	fmt.Fprintf(&b, "## 3. 异常摘要\n\n| Severity | Count |\n|---|---:|\n")
	for _, sev := range severityOrder {
		fmt.Fprintf(&b, "| %s | %d |\n", sev, countBySeverity(r.Findings, sev))
	}
	b.WriteString("\n")

	section := 4
	if r.History != nil {
		fmt.Fprintf(&b, "## %d. 历史对比\n\n", section)
		section++
		fmt.Fprintf(&b, "- 上一轮扫描：%s\n", orDash(r.History.PreviousScanAt))
		fmt.Fprintf(&b, "- 新增：%d　持续：%d　恢复：%d\n\n", len(r.History.Added), len(r.History.Continued), len(r.History.Recovered))
		renderHistoryRefs(&b, "新增", r.History.Added)
		renderHistoryRefs(&b, "持续", r.History.Continued)
		renderHistoryRefs(&b, "恢复", r.History.Recovered)
	}
	for _, sev := range severityOrder {
		issues := findBySeverity(r.Findings, sev)
		if len(issues) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %d. %s Issues\n\n", section, sev)
		section++
		for _, f := range issues {
			fmt.Fprintf(&b, "### %s %s/%s：%s（%s）\n\n", severityIcon(f.Severity), f.Resource.Namespace, f.Resource.Name, f.Title, f.Severity)
			fmt.Fprintf(&b, "- Severity: %s\n- Rule: %s\n- Finding ID: %s\n", f.Severity, f.Rule, f.ID)
			if f.Correlated {
				b.WriteString("- 状态：相关异常（不重复扣分）\n")
			}
			b.WriteString("\n#### Evidence\n\n")
			for _, e := range f.Evidence {
				val := e.Value
				if e.Kind == model.EvLog {
					val = summarizeLog(e.Value, 8) // 日志只留前 8 行 + 统计，防止倾倒
				} else if len(val) > 200 {
					val = val[:200] + "…"
				}
				fmt.Fprintf(&b, "- %s `%s`: %s\n", e.ID, e.Key, val)
			}
			if len(f.Related) > 0 {
				b.WriteString("\n#### Related\n\n")
				for _, ref := range f.Related {
					fmt.Fprintf(&b, "- %s/%s (%s)\n", ref.Namespace, ref.Name, ref.Kind)
				}
			}
			if diag, ok := diagnosesByID(r)[f.ID]; ok {
				renderDiagnosis(&b, diag)
			}
			b.WriteString("\n")
		}
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
		fmt.Fprintf(&b, "- Nodes / Storage（PV/StorageClass/VolumeAttachment）：集群级资源，不随 namespace 过滤，未纳入本报告汇总\n\n")
	default:
		fmt.Fprintf(&b, "- Namespaces: %d\n- Pods: %d\n- Nodes: %d\n- Services: %d\n- EndpointSlices: %d\n", s.Namespaces, s.Pods, s.Nodes, s.Services, s.EndpointSlices)
		fmt.Fprintf(&b, "- Workloads: %d\n- Storage: %d\n- Ingresses: %d\n- NetworkPolicies: %d\n- Events: %d\n\n", s.Workloads, s.Storage, s.Ingresses, s.NetworkPolicies, s.Events)
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

func findBySeverity(findings []model.Finding, sev model.Severity) []model.Finding {
	var out []model.Finding
	for _, f := range findings {
		if f.Severity == sev {
			out = append(out, f)
		}
	}
	return out
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
	if diag.LLMUsed && len(diag.Remediation) > 0 {
		b.WriteString("\n#### 修复命令\n\n")
		for _, c := range diag.Remediation {
			fmt.Fprintf(b, "```bash\n%s\n```\n\n- 风险：%s\n", c.Text, c.Risk)
		}
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
