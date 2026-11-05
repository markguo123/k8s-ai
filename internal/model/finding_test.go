package model

import "testing"

func TestEvidenceReferencesAreFindingQualified(t *testing.T) {
	if got := GlobalEvidenceRef("finding-1", "E2"); got != "finding-1#E2" {
		t.Fatalf("GlobalEvidenceRef = %q", got)
	}
	if got := NormalizeEvidenceRef("finding-1", "E2"); got != "finding-1#E2" {
		t.Fatalf("NormalizeEvidenceRef(E2) = %q", got)
	}
	if got := NormalizeEvidenceRef("finding-1", "member-2#E3"); got != "member-2#E3" {
		t.Fatalf("NormalizeEvidenceRef(qualified) = %q", got)
	}
	if got := NormalizeEvidenceRef("finding-1", ""); got != "" {
		t.Fatalf("empty ref normalized to %q", got)
	}
}
