package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// fakeRunner 记录调用参数并返回固定结果。
type fakeRunner struct {
	opts   model.ScanOptions
	result *model.ScanResult
	err    error
}

func (f *fakeRunner) Run(_ context.Context, opts model.ScanOptions) (*model.ScanResult, error) {
	f.opts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func cannedScanResult() *model.ScanResult {
	return &model.ScanResult{
		Meta:        model.ScanMeta{Cluster: "prod", Namespace: "production"},
		Summary:     model.ClusterSummary{Pods: 10, Nodes: 3},
		HealthScore: model.HealthScore{Score: 80, Max: 100},
		Findings: []model.Finding{
			{ID: "f1", Rule: "crash-loop", Severity: model.SeverityHigh, Title: "CrashLoopBackOff",
				Resource: model.ResourceRef{Kind: "Deployment", Namespace: "production", Name: "payment-api"}},
			{ID: "f2", Rule: "node-not-ready", Severity: model.SeverityCritical, Title: "NodeNotReady",
				Resource: model.ResourceRef{Kind: "Node", Name: "node-03"}},
			{ID: "f3", Rule: "pod-pending", Severity: model.SeverityMedium, Title: "PendingPod",
				Resource: model.ResourceRef{Kind: "Pod", Namespace: "production", Name: "web-0"},
				Related:  []model.ResourceRef{{Kind: "Deployment", Namespace: "production", Name: "web"}}},
		},
		Diagnoses: []model.Diagnosis{
			{FindingID: "f1", RootCause: "OOMKilled", Confidence: 0.9, EvidenceChain: []string{"E1"}},
		},
		History: &model.HistoryDiff{
			Added:     []model.FindingRef{{ID: "f1"}},
			Continued: []model.FindingRef{{ID: "f2"}},
		},
	}
}

func TestScanClusterTool(t *testing.T) {
	r := &fakeRunner{result: cannedScanResult()}
	tr, err := NewDiagnosisTools(r, model.ScanOptions{})[0].Execute(context.Background(), json.RawMessage(`{"namespace":"production"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.opts.Namespace != "production" || r.opts.LLM.Enabled || r.opts.ReportMode != "none" || r.opts.PodTarget != "" {
		t.Fatalf("opts = %+v", r.opts)
	}
	m := tr.Data.(map[string]any)
	if m["kind"] != "ScanResult" || len(m["findings"].([]map[string]any)) != 3 {
		t.Fatalf("summary = %v", m)
	}
	if m["healthScore"] != 80 {
		t.Fatalf("healthScore = %v", m["healthScore"])
	}
	hist := m["history"].(map[string]any)
	if hist["added"] != 1 || hist["continued"] != 1 {
		t.Fatalf("history = %v", hist)
	}
}

func TestScanPodTool(t *testing.T) {
	r := &fakeRunner{result: cannedScanResult()}
	tr, err := NewDiagnosisTools(r, model.ScanOptions{})[1].Execute(context.Background(), json.RawMessage(`{"namespace":"production","name":"web-0"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.opts.Namespace != "production" || r.opts.PodTarget != "web-0" || r.opts.LLM.Enabled {
		t.Fatalf("opts = %+v", r.opts)
	}
	if tr.Data.(map[string]any)["kind"] != "ScanResult" {
		t.Fatalf("data = %v", tr.Data)
	}
}

func TestDiagnosePodTool(t *testing.T) {
	r := &fakeRunner{result: cannedScanResult()}
	if _, err := NewDiagnosisTools(r, model.ScanOptions{})[2].Execute(context.Background(), json.RawMessage(`{"namespace":"production","name":"web-0"}`)); err != nil {
		t.Fatal(err)
	}
	if r.opts.LLM.Enabled {
		t.Fatalf("diagnose_pod 不应开启内部 LLM 诊断（Agent 是唯一大脑）: %+v", r.opts.LLM)
	}
	if r.opts.PodTarget != "web-0" || r.opts.Namespace != "production" {
		t.Fatalf("diagnose_pod 参数 = %+v", r.opts)
	}
}

func TestDiagnoseNodeTool(t *testing.T) {
	r := &fakeRunner{result: cannedScanResult()}
	tr, err := NewDiagnosisTools(r, model.ScanOptions{})[3].Execute(context.Background(), json.RawMessage(`{"name":"node-03"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.opts.LLM.Enabled {
		t.Fatal("diagnose_node 不应开启内部 LLM 诊断（Agent 是唯一大脑）")
	}
	m := tr.Data.(map[string]any)
	findings := m["findings"].([]map[string]any)
	if len(findings) != 1 || findings[0]["id"] != "f2" {
		t.Fatalf("node 过滤结果 = %v", findings)
	}
}

func TestDiagnoseWorkloadTool(t *testing.T) {
	r := &fakeRunner{result: cannedScanResult()}
	tr, err := NewDiagnosisTools(r, model.ScanOptions{})[4].Execute(context.Background(), json.RawMessage(`{"kind":"deployment","namespace":"production","name":"web"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.opts.LLM.Enabled {
		t.Fatal("diagnose_workload 不应开启内部 LLM 诊断（Agent 是唯一大脑）")
	}
	m := tr.Data.(map[string]any)
	findings := m["findings"].([]map[string]any)
	if len(findings) != 1 || findings[0]["id"] != "f3" {
		t.Fatalf("workload 过滤结果 = %v", findings)
	}
	// 不支持的 kind。
	tr, err = NewDiagnosisTools(r, model.ScanOptions{})[4].Execute(context.Background(), json.RawMessage(`{"kind":"job","namespace":"x","name":"y"}`))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Data.(map[string]any)["error"] == nil {
		t.Fatal("不支持的 workload kind 应返回错误")
	}
}

func TestDiagnosisToolErrorIsolation(t *testing.T) {
	r := &fakeRunner{err: context.DeadlineExceeded}
	tr, err := NewDiagnosisTools(r, model.ScanOptions{})[0].Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Data.(map[string]any)["error"] == nil {
		t.Fatal("scan 失败应返回错误结果而非中断")
	}
}

func TestNewDiagnosisToolsCount(t *testing.T) {
	ts := NewDiagnosisTools(&fakeRunner{}, model.ScanOptions{})
	if len(ts) != 5 {
		t.Fatalf("工具数 = %d, want 5", len(ts))
	}
	names := map[string]bool{}
	for _, tl := range ts {
		names[tl.Name()] = true
	}
	for _, want := range []string{"scan_cluster", "scan_pod", "diagnose_pod", "diagnose_node", "diagnose_workload"} {
		if !names[want] {
			t.Fatalf("缺少 %s", want)
		}
	}
}
