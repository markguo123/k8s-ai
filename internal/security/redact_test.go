package security

import (
	"strings"
	"testing"
)

// TestRedact 覆盖每种高置信脱敏模式。
func TestRedact(t *testing.T) {
	r := NewRedactor()
	cases := []struct {
		name    string
		in      string
		absent  string
		present string
	}{
		{"api key", "api_key=sk-1234567890abcdef1234567890", "sk-1234567890abcdef1234567890", "[REDACTED]"},
		{"password", "password: hunter2", "hunter2", "[REDACTED]"},
		{"token", "token=abc-def-123", "abc-def-123", "[REDACTED]"},
		{"bearer", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig", "eyJhbGciOiJIUzI1NiJ9.payload.sig", "[REDACTED]"},
		{"jwt", "jwt=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc123", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc123", "[REDACTED]"},
		{"private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----", "BEGIN RSA PRIVATE KEY", "[REDACTED]"},
		{"conn string", "postgres://app:secretpw@db:5432/db", "secretpw@", "[REDACTED]"},
		{"plain text", "pod restarted 3 times", "", "pod restarted 3 times"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Redact(tc.in)
			if tc.absent != "" && strings.Contains(got, tc.absent) {
				t.Errorf("Redact(%q) 仍包含敏感内容 %q: %q", tc.in, tc.absent, got)
			}
			if tc.present != "" && !strings.Contains(got, tc.present) {
				t.Errorf("Redact(%q) 未包含 %q: %q", tc.in, tc.present, got)
			}
		})
	}
}

// TestRedactValueByKey 验证敏感键整值替换策略。
func TestRedactValueByKey(t *testing.T) {
	r := NewRedactor()
	if got := r.RedactValueByKey("MYSQL_PASSWORD", "pw-123"); got != "[REDACTED]" {
		t.Fatalf("敏感键值应整体替换，got %q", got)
	}
	if got := r.RedactValueByKey("description", "pw-123"); got != "pw-123" {
		t.Fatalf("普通键值不应被替换，got %q", got)
	}
}

// TestRedactIdempotent 验证脱敏幂等（二次脱敏不再变化）。
func TestRedactIdempotent(t *testing.T) {
	r := NewRedactor()
	in := "password: hunter2 and api_key=sk-abcdefghijklmnopqrstuvwxyz"
	once := r.Redact(in)
	if once != r.Redact(once) {
		t.Fatalf("脱敏非幂等: %q vs %q", once, r.Redact(once))
	}
}
