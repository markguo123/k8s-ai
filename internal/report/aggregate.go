package report

import (
	"sort"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// AggregateOptions 报告聚合选项（SPEC §9.5）。
type AggregateOptions struct {
	// IncludeSuggestions 为 true 时把 suggestion 类一并计入分类聚合；
	// false 时只聚合 detection（suggestion 仍计入 SuggestionCount）。
	IncludeSuggestions bool
}

// KindAggregate 按资源 Kind 聚合的一类问题。
type KindAggregate struct {
	Kind       string         `json:"kind"`
	Count      int            `json:"count"`
	Severities map[string]int `json:"severities"`
}

// CategoryAggregate 按问题域（category）聚合，内部再按 Kind 细分。
type CategoryAggregate struct {
	Category string          `json:"category"`
	Count    int             `json:"count"`
	Kinds    []KindAggregate `json:"kinds"`
}

// ReportAggregate 整份扫描结果的汇总模型（server/web/CLI 复用同一结构）。
type ReportAggregate struct {
	Total           int                 `json:"total"`
	DetectionCount  int                 `json:"detectionCount"`
	SuggestionCount int                 `json:"suggestionCount"`
	Severities      map[string]int      `json:"severities"`
	Categories      []CategoryAggregate `json:"categories"`
}

// Aggregate 将 findings 按 category → kind 聚合，并统计严重级与检测/建议计数。
// opts.IncludeSuggestions=false 时 suggestion 不计入分类聚合（仍计入 SuggestionCount）。
func Aggregate(findings []model.Finding, opts AggregateOptions) ReportAggregate {
	agg := ReportAggregate{Severities: map[string]int{}}
	for _, f := range findings {
		agg.Total++
		if f.Suggestion {
			agg.SuggestionCount++
		} else {
			agg.DetectionCount++
		}
		agg.Severities[string(f.Severity)]++
		if opts.IncludeSuggestions || !f.Suggestion {
			addCategory(&agg, f)
		}
	}
	// 确定性输出：category、kind 按名称排序。
	sort.Slice(agg.Categories, func(i, j int) bool { return agg.Categories[i].Category < agg.Categories[j].Category })
	for i := range agg.Categories {
		kinds := agg.Categories[i].Kinds
		sort.Slice(kinds, func(a, b int) bool { return kinds[a].Kind < kinds[b].Kind })
		agg.Categories[i].Kinds = kinds
	}
	return agg
}

func addCategory(agg *ReportAggregate, f model.Finding) {
	cat := f.Category
	if cat == "" {
		cat = "General"
	}
	var c *CategoryAggregate
	for i := range agg.Categories {
		if agg.Categories[i].Category == cat {
			c = &agg.Categories[i]
			break
		}
	}
	if c == nil {
		agg.Categories = append(agg.Categories, CategoryAggregate{Category: cat})
		c = &agg.Categories[len(agg.Categories)-1]
	}
	c.Count++
	kind := f.Kind
	if kind == "" {
		kind = "Unknown"
	}
	for j := range c.Kinds {
		if c.Kinds[j].Kind == kind {
			c.Kinds[j].Count++
			c.Kinds[j].Severities[string(f.Severity)]++
			return
		}
	}
	c.Kinds = append(c.Kinds, KindAggregate{Kind: kind, Count: 1, Severities: map[string]int{string(f.Severity): 1}})
}
