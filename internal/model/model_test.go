package model

import (
	"strings"
	"testing"
)

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

// TestTruncateLogsBasic 验证按行截断：短日志完整保留，超长按行边界切断。
func TestTruncateLogsBasic(t *testing.T) {
	lines := []string{"line1", "line2", "line3", "line4"}
	raw := []byte(strings.Join(lines, "\n") + "\n")
	out := TruncateLogs(raw, 14, 0)
	got := string(out)
	if !strings.HasPrefix(got, "line1\nline2") {
		t.Fatalf("应保留前两行，实际: %q", got)
	}
	if strings.Contains(got, "line3") {
		t.Fatalf("不应包含第3行及以后，实际: %q", got)
	}
}

// TestTruncateLogsDefault 验证 maxBytes<=0 时使用 64KB 默认值。
func TestTruncateLogsDefault(t *testing.T) {
	raw := []byte(strings.Repeat("a\n", 1000))
	out := TruncateLogs(raw, 0, 0)
	if len(out) != len(raw) {
		t.Fatalf("默认值应保留短日志，got=%d want=%d", len(out), len(raw))
	}
}

// TestTruncateLogsFirstLine 验证首条超长行（≤ HardLogLineCap）可独占配额。
func TestTruncateLogsFirstLine(t *testing.T) {
	first := strings.Repeat("x", 100)
	raw := []byte(first + "\nsecond\n")
	out := TruncateLogs(raw, 50, 0)
	got := string(out)
	if !strings.HasPrefix(got, first) {
		t.Fatalf("首超长行应被保留，got=%q", got)
	}
	if strings.Contains(got, "second") {
		t.Fatalf("超出配额的后续行应被截断，got=%q", got)
	}
}

// TestTruncateLogsNoTrailingNL 验证末尾无换行符也能正确处理。
func TestTruncateLogsNoTrailingNL(t *testing.T) {
	raw := []byte("line1\nline2\nline3")
	out := TruncateLogs(raw, 100, 0)
	if string(out) != "line1\nline2\nline3" {
		t.Fatalf("末尾无换行应完整保留，got=%q", string(out))
	}
}

// TestTruncateLogsHardLineCap 验证超长单行（> HardLogLineCap）被截断到 1MiB。
func TestTruncateLogsHardLineCap(t *testing.T) {
	hugeLine := strings.Repeat("z", HardLogLineCap+100)
	raw := []byte(hugeLine + "\n")
	out := TruncateLogs(raw, HardLogLineCap+200, 0)
	want := HardLogLineCap + 1
	if len(out) != want {
		t.Fatalf("超长单行应截断到 HardLogLineCap，got=%d want=%d", len(out), want)
	}
}
