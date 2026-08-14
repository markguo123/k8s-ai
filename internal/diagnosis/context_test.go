package diagnosis

import (
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func TestBuildContextsBudget(t *testing.T) {
	findings := []model.Finding{
		{ID: "f1", Rule: "R", Severity: model.SeverityCritical, Title: "critical", Evidence: []model.Evidence{{ID: "E1", Key: "k", Value: "v"}}},
		{ID: "f2", Rule: "R", Severity: model.SeverityHigh, Title: "high", Evidence: []model.Evidence{{ID: "E1", Key: "k", Value: "v"}}},
		{ID: "f3", Rule: "R", Severity: model.SeverityLow, Title: "low", Evidence: []model.Evidence{{ID: "E1", Key: "k", Value: "v"}}},
	}
	ctxs := buildContexts(findings, model.LLMOptions{MaxFindings: 2, MaxInputTokens: 8192, MaxTotalTokens: 32768})
	if len(ctxs) != 2 {
		t.Fatalf("top-N = %d, want 2", len(ctxs))
	}
	if ctxs[0].finding.ID != "f1" || ctxs[1].finding.ID != "f2" {
		t.Fatalf("应按严重级降序: %s, %s", ctxs[0].finding.ID, ctxs[1].finding.ID)
	}
}

func TestBuildContextsTrimsPerFinding(t *testing.T) {
	big := strings.Repeat("x", 100000)
	f := model.Finding{ID: "f1", Rule: "R", Severity: model.SeverityHigh, Title: "t",
		Evidence: []model.Evidence{{ID: "E1", Key: "logs", Value: big}}}
	ctxs := buildContexts([]model.Finding{f}, model.LLMOptions{MaxInputTokens: 512, MaxTotalTokens: 100000, MaxFindings: 10})
	if len(ctxs) != 1 {
		t.Fatal("应保留该 finding")
	}
	if ctxs[0].tokens > 512 {
		t.Fatalf("单 finding 预算超限: %d > 512", ctxs[0].tokens)
	}
}

func TestBuildContextTextRedactsAndCaps(t *testing.T) {
	f := model.Finding{
		ID: "f1", Rule: "R", Severity: model.SeverityHigh, Title: "t",
		Resource: model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-0"},
		Evidence: []model.Evidence{{ID: "E1", Key: "logs", Value: strings.Repeat("x", 5000)}},
	}
	text := buildContextText(f)
	if len(text) > 2000 {
		t.Fatalf("上下文未截断: %d", len(text))
	}
	if !strings.Contains(text, "Finding:") || !strings.Contains(text, "- E1") {
		t.Fatalf("上下文结构异常: %q", text)
	}
}

// TestBuildContextTextKeepsLogTail 验证日志证据保留尾部（panic 通常在末尾）。
func TestBuildContextTextKeepsLogTail(t *testing.T) {
	f := model.Finding{
		ID: "f1", Rule: "CrashLoopBackOff", Severity: model.SeverityHigh, Title: "t",
		Resource: model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "app-0"},
		Evidence: []model.Evidence{{
			ID: "E1", Kind: model.EvLog,
			Value: strings.Repeat("INFO line\n", 200) + "panic: StartConsumer fail, topic not found\n",
		}},
	}
	text := buildContextText(f)
	if !strings.Contains(text, "panic: StartConsumer fail") {
		t.Fatalf("日志尾部（panic）未保留: %q", text)
	}
}
