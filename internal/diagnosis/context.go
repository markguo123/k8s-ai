package diagnosis

import (
	"fmt"
	"sort"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// findingContext 是送诊前构造好的单个 Finding 上下文（ADR-017 预算裁剪后）。
type findingContext struct {
	finding model.Finding
	text    string
	tokens  int
}

// buildContexts 按严重级排序，在预算内构造 DiagnosisContext：
// 单 Finding 上限 + 诊断阶段总预算 + top-N（FR-016/ADR-017）。
func buildContexts(findings []model.Finding, opts model.LLMOptions) []findingContext {
	perFinding := opts.MaxInputTokens
	if perFinding <= 0 {
		perFinding = 8192
	}
	total := opts.MaxTotalTokens
	if total <= 0 {
		total = 32768
	}
	maxFindings := opts.MaxFindings
	if maxFindings <= 0 {
		maxFindings = 30
	}

	sorted := make([]model.Finding, len(findings))
	copy(sorted, findings)
	// 严重级降序（同级按指纹稳定排序），保证最严重的问题优先送诊。
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, rj := model.SeverityRank(sorted[i].Severity), model.SeverityRank(sorted[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return sorted[i].ID < sorted[j].ID
	})

	var out []findingContext
	used := 0
	for _, f := range sorted {
		if len(out) >= maxFindings {
			break
		}
		text := buildContextText(f)
		est := estimateTokens(text)
		// 超出单 Finding 预算：丢弃最低优先级证据直到满足（只保留摘要兜底）。
		for est > perFinding && strings.Count(text, "\n- E") > 1 {
			text = dropLastEvidence(text)
			est = estimateTokens(text)
		}
		if used+est > total && len(out) > 0 {
			break // 总预算不足，剩余 Finding 保留规则结论（不送诊）
		}
		used += est
		out = append(out, findingContext{finding: f, text: text, tokens: est})
	}
	return out
}

// buildContextText 构造单个 Finding 的诊断上下文（已脱敏证据，日志值截断）。
func buildContextText(f model.Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Finding: %s（%s）\n", f.Title, f.Severity)
	fmt.Fprintf(&b, "Resource: %s/%s (%s)\n", f.Resource.Namespace, f.Resource.Name, f.Resource.Kind)
	fmt.Fprintf(&b, "Rule: %s\n", f.Rule)
	b.WriteString("Evidence:\n")
	for _, e := range f.Evidence {
		v := truncateText(e.Value, 1024)
		if e.Kind == model.EvLog {
			v = tailText(e.Value, 1500) // 日志保留尾部：panic/错误通常在最后
		}
		fmt.Fprintf(&b, "- %s %s: %s\n", e.ID, e.Key, v)
	}
	if len(f.Related) > 0 {
		b.WriteString("Related:\n")
		for _, r := range f.Related {
			fmt.Fprintf(&b, "- %s/%s (%s)\n", r.Namespace, r.Name, r.Kind)
		}
	}
	return b.String()
}

// dropLastEvidence 移除文本中最后一条证据行（预算裁剪用）。
func dropLastEvidence(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "- E") {
			return strings.Join(append(lines[:i], lines[i+1:]...), "\n")
		}
	}
	return text
}

// estimateTokens 保守估算 token 数（约 4 字节/词元）。
func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}

func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// tailText 保留文本末尾 max 字符（对齐换行），日志类证据用尾部。
func tailText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := len(s) - max
	if i := strings.IndexByte(s[cut:], '\n'); i >= 0 {
		cut += i + 1
	}
	return "…（前略）\n" + s[cut:]
}
