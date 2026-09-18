package rule

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

type fakeRule struct {
	name      string
	needsLogs bool
}

func (f fakeRule) Name() string              { return f.name }
func (f fakeRule) NeedsLogs() bool           { return f.needsLogs }
func (f fakeRule) Evaluate(*RuleContext) []*model.Finding { return nil }

func TestRegisterWithMetaAndDefaults(t *testing.T) {
	reg := NewRegistry()
	r := fakeRule{name: "FakeRule", needsLogs: true}
	if err := reg.RegisterWithMeta(r, RuleMeta{RuleID: "FAKE_RULE", Category: "Workload", Kind: "Deployment", SeverityBase: model.SeverityHigh}); err != nil {
		t.Fatalf("RegisterWithMeta: %v", err)
	}
	if len(reg.All()) != 1 {
		t.Fatalf("rules = %d, want 1", len(reg.All()))
	}
	m, ok := reg.Meta("FakeRule")
	if !ok {
		t.Fatal("meta not found")
	}
	if m.RuleID != "FAKE_RULE" || m.Category != "Workload" || m.Kind != "Deployment" || m.SeverityBase != model.SeverityHigh {
		t.Fatalf("meta mismatch: %+v", m)
	}
	if !m.NeedsLogs || m.RuleType != RuleTypeDetection {
		t.Fatalf("default field mismatch: %+v", m)
	}

	// duplicate registration updates meta, does not duplicate list
	if err := reg.RegisterWithMeta(r, RuleMeta{RuleID: "FAKE_RULE", Category: "Storage"}); err != nil {
		t.Fatalf("reregister: %v", err)
	}
	if len(reg.All()) != 1 {
		t.Fatalf("rules after dup = %d, want 1", len(reg.All()))
	}
	m2, _ := reg.Meta("FakeRule")
	if m2.Category != "Storage" || m2.Kind != "" {
		t.Fatalf("duplicate update mismatch: %+v", m2)
	}

	// Register() compat: default meta from rule
	reg2 := NewRegistry()
	reg2.Register(fakeRule{name: "PlainRule", needsLogs: true})
	pm, ok := reg2.Meta("PlainRule")
	if !ok {
		t.Fatal("default meta missing")
	}
	if pm.RuleID != "PlainRule" || pm.Category != "General" || pm.RuleType != RuleTypeDetection || !pm.NeedsLogs || pm.SeverityBase != model.SeverityMedium {
		t.Fatalf("default meta mismatch: %+v", pm)
	}
}

func TestFilterWithMeta(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterWithMeta(fakeRule{name: "A", needsLogs: true}, RuleMeta{RuleID: "R_A", Category: "Workload", Kind: "Deployment", SeverityBase: model.SeverityHigh})
	reg.RegisterWithMeta(fakeRule{name: "B", needsLogs: false}, RuleMeta{RuleID: "R_B", Category: "Storage", Kind: "StorageClass", SeverityBase: model.SeverityMedium})
	reg.RegisterWithMeta(fakeRule{name: "C", needsLogs: false}, RuleMeta{RuleID: "R_C", Category: "Config", RuleType: RuleTypeSuggestion, SeverityBase: model.SeverityLow})

	byCat := reg.FilterWithMeta(RuleMetaFilter{Category: "Workload"})
	if len(byCat) != 1 || byCat[0].Meta.RuleID != "R_A" {
		t.Fatalf("category filter = %d, want 1 R_A", len(byCat))
	}
	byType := reg.FilterWithMeta(RuleMetaFilter{RuleType: RuleTypeSuggestion})
	if len(byType) != 1 || byType[0].Meta.RuleID != "R_C" {
		t.Fatalf("type filter = %d", len(byType))
	}
	bySev := reg.FilterWithMeta(RuleMetaFilter{Severity: model.SeverityMedium})
	if len(bySev) != 1 || bySev[0].Meta.RuleID != "R_B" {
		t.Fatalf("severity filter = %d", len(bySev))
	}
	all := reg.FilterWithMeta(RuleMetaFilter{})
	if len(all) != 3 {
		t.Fatalf("all = %d", len(all))
	}
}

func TestRegisterWithMetaErrors(t *testing.T) {
	reg := NewRegistry()
	if err := reg.RegisterWithMeta(fakeRule{name: "", needsLogs: false}, RuleMeta{}); err == nil {
		t.Fatal("expected error for empty rule name")
	}
	if err := reg.RegisterWithMeta(fakeRule{name: "X", needsLogs: false}, RuleMeta{RuleID: "", Category: "C", RuleType: RuleTypeDetection, SeverityBase: model.SeverityHigh}); err != nil {
		t.Fatalf("empty rule_id should auto-fill to rule name: %v", err)
	}
	if m, ok := reg.Meta("X"); !ok || m.RuleID != "X" {
		t.Fatalf("rule_id not backfilled to rule name, got %q", m.RuleID)
	}
	if err := reg.RegisterWithMeta(fakeRule{name: "X", needsLogs: false}, RuleMeta{RuleID: "X", Category: "C", RuleType: "bogus", SeverityBase: model.SeverityHigh}); err == nil {
		t.Fatal("expected error for invalid rule type")
	}
	if err := reg.RegisterWithMeta(fakeRule{name: "X", needsLogs: false}, RuleMeta{RuleID: "X", Category: "C", RuleType: RuleTypeDetection, SeverityBase: "NOPE"}); err == nil {
		t.Fatal("expected error for invalid severity")
	}
}
