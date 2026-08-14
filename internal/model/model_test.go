package model

import "testing"

func TestParseSeverity(t *testing.T) {
	for _, s := range []string{"INFO", "LOW", "MEDIUM", "HIGH", "CRITICAL"} {
		if _, err := ParseSeverity(s); err != nil {
			t.Errorf("ParseSeverity(%q) unexpected error: %v", s, err)
		}
	}
	if _, err := ParseSeverity("BOGUS"); err == nil {
		t.Fatal("ParseSeverity(BOGUS) should fail")
	}
}

func TestSeverityRank(t *testing.T) {
	if SeverityRank(SeverityCritical) <= SeverityRank(SeverityHigh) {
		t.Fatal("critical must rank above high")
	}
	if SeverityRank(SeverityHigh) <= SeverityRank(SeverityMedium) {
		t.Fatal("high must rank above medium")
	}
	if SeverityRank(Severity("")) != 0 {
		t.Fatal("unknown severity must rank 0")
	}
}
