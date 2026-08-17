package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func TestCompare(t *testing.T) {
	prev := &model.ScanResult{
		Meta: model.ScanMeta{ScanEndedAt: "2026-08-17T00:00:00Z"},
		Findings: []model.Finding{
			{ID: "same", Rule: "R", Severity: model.SeverityHigh, Resource: model.ResourceRef{Kind: "Pod", Namespace: "ns", Name: "a"}},
			{ID: "gone", Rule: "R", Severity: model.SeverityMedium, Resource: model.ResourceRef{Kind: "Pod", Namespace: "ns", Name: "b"}},
		},
	}
	cur := []model.Finding{
		{ID: "same", Rule: "R", Severity: model.SeverityHigh, Title: "t", Resource: model.ResourceRef{Kind: "Pod", Namespace: "ns", Name: "a"}},
		{ID: "new", Rule: "R", Severity: model.SeverityLow, Resource: model.ResourceRef{Kind: "Pod", Namespace: "ns", Name: "c"}},
	}
	d := Compare(prev, cur)
	if len(d.Continued) != 1 || d.Continued[0].ID != "same" {
		t.Fatalf("continued = %+v", d.Continued)
	}
	if len(d.Added) != 1 || d.Added[0].ID != "new" {
		t.Fatalf("added = %+v", d.Added)
	}
	if len(d.Recovered) != 1 || d.Recovered[0].ID != "gone" {
		t.Fatalf("recovered = %+v", d.Recovered)
	}
	if d.PreviousScanAt != "2026-08-17T00:00:00Z" {
		t.Fatalf("previousScanAt = %q", d.PreviousScanAt)
	}
}

func TestCompareNoPrevious(t *testing.T) {
	d := Compare(nil, []model.Finding{{ID: "a", Rule: "R"}})
	if len(d.Added) != 1 || len(d.Continued) != 0 || len(d.Recovered) != 0 {
		t.Fatalf("diff = %+v", d)
	}
}

func TestLoadPrevious(t *testing.T) {
	dir := t.TempDir()
	if got, err := LoadPrevious(dir); err != nil || got != nil {
		t.Fatalf("空目录应返回 nil: %v %v", got, err)
	}
	prev := &model.ScanResult{Meta: model.ScanMeta{ServerVersion: "v1.28.13"}}
	raw, _ := json.Marshal(prev)
	if err := os.WriteFile(filepath.Join(dir, "latest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPrevious(dir)
	if err != nil || got == nil || got.Meta.ServerVersion != "v1.28.13" {
		t.Fatalf("load = %+v, %v", got, err)
	}
	// 损坏文件返回错误
	if err := os.WriteFile(filepath.Join(dir, "latest.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrevious(dir); err == nil {
		t.Fatal("损坏 JSON 应返回错误")
	}
}
