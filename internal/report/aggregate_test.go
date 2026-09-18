package report

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func mkFinding(category, kind string, suggestion bool, sev model.Severity) model.Finding {
	return model.Finding{Category: category, Kind: kind, Suggestion: suggestion, Severity: sev}
}

// TestAggregateGroupsByCategoryKind 按 category → kind 正确分组计数，并统计严重级。
func TestAggregateGroupsByCategoryKind(t *testing.T) {
	findings := []model.Finding{
		mkFinding("Workload", "Pod", false, model.SeverityHigh),
		mkFinding("Workload", "Pod", false, model.SeverityMedium),
		mkFinding("Workload", "Deployment", false, model.SeverityLow),
		mkFinding("Storage", "PersistentVolumeClaim", false, model.SeverityHigh),
	}
	agg := Aggregate(findings, AggregateOptions{IncludeSuggestions: true})
	if agg.Total != 4 || agg.DetectionCount != 4 || agg.SuggestionCount != 0 {
		t.Fatalf("计数异常: %+v", agg)
	}
	if agg.Severities[string(model.SeverityHigh)] != 2 || agg.Severities[string(model.SeverityMedium)] != 1 {
		t.Fatalf("严重级统计异常: %+v", agg.Severities)
	}
	if len(agg.Categories) != 2 {
		t.Fatalf("应有 2 个分类: %+v", agg.Categories)
	}
	// Workload 分类下应有 2 个 kind；总 count = 3。
	if agg.Categories[0].Category != "Storage" || agg.Categories[0].Count != 1 {
		t.Fatalf("Storage 分类异常: %+v", agg.Categories[0])
	}
	workload := agg.Categories[1]
	if workload.Category != "Workload" || workload.Count != 3 || len(workload.Kinds) != 2 {
		t.Fatalf("Workload 分类异常: %+v", workload)
	}
}

// TestAggregateSuggestionFilter IncludeSuggestions=false 时不把 suggestion 计入分类聚合。
func TestAggregateSuggestionFilter(t *testing.T) {
	findings := []model.Finding{
		mkFinding("Workload", "Pod", false, model.SeverityHigh),
		mkFinding("Network", "Namespace", true, model.SeverityLow), // suggestion
	}
	// 不包括 suggestion。
	filtered := Aggregate(findings, AggregateOptions{})
	if filtered.SuggestionCount != 1 || filtered.DetectionCount != 1 {
		t.Fatalf("suggestion/检测计数异常: %+v", filtered)
	}
	if len(filtered.Categories) != 1 || filtered.Categories[0].Category != "Workload" {
		t.Fatalf("应只聚合 detection 分类: %+v", filtered.Categories)
	}
	// 包括 suggestion。
	all := Aggregate(findings, AggregateOptions{IncludeSuggestions: true})
	if len(all.Categories) != 2 {
		t.Fatalf("应包括 suggestion 分类: %+v", all.Categories)
	}
}
