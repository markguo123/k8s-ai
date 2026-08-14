package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// 严重级图标（终端与 Markdown 通用，支持邮件/飞书等场景）。
var severityIcons = map[model.Severity]string{
	model.SeverityCritical: "🔴",
	model.SeverityHigh:     "🟠",
	model.SeverityMedium:   "🟡",
	model.SeverityLow:      "🔵",
	model.SeverityInfo:     "⚪",
}

func severityIcon(s model.Severity) string {
	if ic, ok := severityIcons[s]; ok {
		return ic
	}
	return "⚪"
}

// scoreBar 文本健康条：按分数占满 10 格（score/100 或 max）。
func scoreBar(score, max int) string {
	if max <= 0 {
		max = 100
	}
	filled := score * 10 / max
	if filled < 0 {
		filled = 0
	}
	if filled > 10 {
		filled = 10
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
}

// summarizeLog 压缩日志证据：只保留前 maxLines 行，附加行数与 ERROR/WARN 统计。
func summarizeLog(value string, maxLines int) string {
	if maxLines <= 0 {
		maxLines = 8
	}
	lines := strings.Split(strings.TrimRight(value, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return ""
	}
	errCnt, warnCnt := 0, 0
	for _, l := range lines {
		u := strings.ToUpper(l)
		if strings.Contains(u, "ERROR") || strings.Contains(u, "FATAL") {
			errCnt++
		}
		if strings.Contains(u, "WARN") {
			warnCnt++
		}
	}
	shown := lines
	tail := ""
	if len(lines) > maxLines {
		shown = lines[len(lines)-maxLines:] // 保留尾部：panic/错误通常在最后
		tail = fmt.Sprintf("\n…（共 %d 行，前略 %d 行，ERROR %d 行，WARN %d 行）", len(lines), len(lines)-maxLines, errCnt, warnCnt)
	} else if errCnt > 0 || warnCnt > 0 {
		tail = fmt.Sprintf("（共 %d 行，ERROR %d 行，WARN %d 行）", len(lines), errCnt, warnCnt)
	}
	return strings.Join(shown, "\n") + tail
}

// logStat 只返回日志行数统计（终端摘要用）。
func logStat(value string) string {
	n := strings.Count(strings.TrimRight(value, "\n"), "\n") + 1
	if value == "" {
		n = 0
	}
	return fmt.Sprintf("（%d 行）", n)
}

// RenderTerminal 终端一屏摘要：健康条、严重级计数、Top 重点问题、系统组件、采集错误。
func RenderTerminal(r *model.ScanResult) string {
	var b strings.Builder
	scope := "cluster"
	switch {
	case r.Meta.Pod != "":
		scope = "pod " + r.Meta.Namespace + "/" + r.Meta.Pod
	case r.Meta.Namespace != "":
		scope = "namespace " + r.Meta.Namespace
	}
	fmt.Fprintf(&b, "k8s-ai scan　Scope: %s\n", scope)
	fmt.Fprintf(&b, "集群健康：%d/%d  %s\n", r.HealthScore.Score, r.HealthScore.Max, scoreBar(r.HealthScore.Score, r.HealthScore.Max))
	fmt.Fprintf(&b, "%s\n", severityCountsLine(r.Findings))

	top := topFindings(r.Findings, 10)
	if len(top) > 0 {
		b.WriteString("\n重点问题：\n")
		for _, f := range top {
			renderTerminalFinding(&b, f, diagnosisFor(r, f.ID))
		}
		if len(r.Findings) > len(top) {
			fmt.Fprintf(&b, "… 还有 %d 个问题，详见报告或 --verbose\n", len(r.Findings)-len(top))
		}
	} else {
		b.WriteString("\n未发现异常。\n")
	}

	if len(r.Components) > 0 {
		var parts []string
		for _, c := range r.Components {
			icon := "❌"
			switch {
			case !c.Present:
				icon = "❌"
			case !c.Ready:
				icon = "⚠️"
			default:
				icon = "✅"
			}
			parts = append(parts, icon+" "+c.Name)
		}
		fmt.Fprintf(&b, "\n系统组件：%s\n", strings.Join(parts, "  "))
	}
	if len(r.CollectionErrors) > 0 {
		fmt.Fprintf(&b, "\n采集错误：%d 条\n", len(r.CollectionErrors))
	}
	return b.String()
}

// severityCountsLine 形如 "CRITICAL 1 | HIGH 3 | ..."。
func severityCountsLine(findings []model.Finding) string {
	counts := map[model.Severity]int{}
	for _, f := range findings {
		counts[f.Severity]++
	}
	order := []model.Severity{model.SeverityCritical, model.SeverityHigh, model.SeverityMedium, model.SeverityLow, model.SeverityInfo}
	parts := make([]string, 0, len(order))
	for _, s := range order {
		parts = append(parts, fmt.Sprintf("%s %d", s, counts[s]))
	}
	return strings.Join(parts, " | ")
}

// topFindings 按严重级降序取前 N 条（同级按指纹稳定）。
func topFindings(findings []model.Finding, n int) []model.Finding {
	sorted := make([]model.Finding, len(findings))
	copy(sorted, findings)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, rj := model.SeverityRank(sorted[i].Severity), model.SeverityRank(sorted[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return sorted[i].ID < sorted[j].ID
	})
	if len(sorted) > n {
		sorted = sorted[:n]
	}
	return sorted
}

// renderTerminalFinding 单条重点问题：图标 + 现象 + 根因 + 首条修复建议。
func renderTerminalFinding(b *strings.Builder, f model.Finding, diag *model.Diagnosis) {
	fmt.Fprintf(b, "%s %s %s/%s：%s\n", severityIcon(f.Severity), f.Severity, f.Resource.Namespace, f.Resource.Name, f.Title)
	var facts []string
	logNote := ""
	logValue := ""
	for _, e := range f.Evidence {
		if e.Kind == model.EvLog {
			if logNote == "" {
				logNote = "日志" + logStat(e.Value)
				logValue = e.Value
			}
			continue
		}
		if len(facts) < 3 {
			facts = append(facts, e.Key+"="+e.Value)
		}
	}
	if len(facts) > 0 {
		fmt.Fprintf(b, "　现象：%s\n", strings.Join(facts, "；"))
	}
	if logNote != "" {
		if hl := logHighlight(logValue); hl != "" {
			fmt.Fprintf(b, "　%s：%s\n", logNote, hl)
		} else {
			fmt.Fprintf(b, "　%s\n", logNote)
		}
	}
	if diag != nil {
		if diag.LLMUsed {
			fmt.Fprintf(b, "　根因：%s\n", diag.RootCause)
			if len(diag.Remediation) > 0 {
				fmt.Fprintf(b, "　建议：%s（风险 %s）\n", diag.Remediation[0].Text, diag.Remediation[0].Risk)
			}
		} else if diag.RootCause != "" {
			fmt.Fprintf(b, "　初步判断（规则）：%s\n", diag.RootCause)
		}
	}
}

// diagnosisFor 按 FindingID 查找诊断。
func diagnosisFor(r *model.ScanResult, findingID string) *model.Diagnosis {
	for i := range r.Diagnoses {
		if r.Diagnoses[i].FindingID == findingID {
			return &r.Diagnoses[i]
		}
	}
	return nil
}

// logHighlight 取日志中最有用的一行：优先命中关键字（panic/error/emerg 等），
// 否则取最后一行（错误通常在末尾）；限长 160 供终端摘要显示。
func logHighlight(value string) string {
	lines := strings.Split(strings.TrimRight(value, "\n"), "\n")
	for _, l := range lines {
		u := strings.ToUpper(l)
		for _, k := range []string{"PANIC", "FATAL", "ERROR", "EMERG", "EXCEPTION", "FAILED", "INVALID"} {
			if strings.Contains(u, k) {
				return capLine(l)
			}
		}
	}
	if len(lines) > 0 {
		return capLine(lines[len(lines)-1])
	}
	return ""
}

func capLine(l string) string {
	l = strings.TrimSpace(l)
	if len(l) > 160 {
		l = l[:160] + "…"
	}
	return l
}
