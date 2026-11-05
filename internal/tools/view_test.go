package tools

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/executor"
)

func TestDisplayType(t *testing.T) {
	cases := []struct {
		name string
		data any
		want string
	}{
		{"nil → empty", nil, "empty"},
		{"error map → text", map[string]any{"error": "boom"}, "text"},
		{"items map → table", map[string]any{"items": []any{map[string]any{"a": 1}}}, "table"},
		{"events map → events", map[string]any{"events": []any{map[string]any{"a": 1}}}, "events"},
		{"conditions map → events", map[string]any{"conditions": []any{map[string]any{"a": 1}}}, "events"},
		{"scalar map → kv", map[string]any{"name": "nginx", "status": "Running"}, "kv"},
		{"complex map → yaml", map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "x"}}}, "yaml"},
		{"array → table", []any{map[string]any{"name": "nginx"}}, "table"},
		{"empty array → empty", []any{}, "empty"},
		{"yaml text", "apiVersion: v1\nkind: Pod\n", "yaml"},
		{"log text", "2026-10-15 12:00:00 ERROR something", "logs"},
		{"plain text", "hello world", "text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisplayType(tc.data); got != tc.want {
				t.Fatalf("DisplayType() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestToolSummary(t *testing.T) {
	if got := ToolSummary("12 pods found\nwith details"); got != "12 pods found" {
		t.Fatalf("ToolSummary() = %q, want first line", got)
	}
	if got := ToolSummary("  \n  "); got != "" {
		t.Fatalf("ToolSummary() = %q, want empty", got)
	}
}

func TestDisplayTypeExecutorResultIsTerminal(t *testing.T) {
	data := executor.Result{
		Command:  "kubectl -n test-complex get pods",
		Stdout:   "NAME READY\napp 0/2",
		Stderr:   "Forbidden: User \"system:serviceaccount:k8s-ai-test:k8s-ai\" cannot patch resource \"deployments\"",
		ExitCode: 1,
	}
	if got := DisplayType(data); got != "terminal" {
		t.Fatalf("DisplayType(executor.Result) = %q, want terminal", got)
	}
}

func TestPresentExecutorForbiddenIsDeniedFailure(t *testing.T) {
	p := Present(ToolResult{Data: executor.Result{
		Command:  "kubectl -n test-complex patch deployment/app-backend --type=json",
		Stderr:   `Forbidden: User "system:serviceaccount:k8s-ai-test:k8s-ai" cannot patch resource "deployments"`,
		ExitCode: 1,
	}})
	if p.Ok {
		t.Fatal("non-zero kubectl exit must not be successful")
	}
	if p.ErrorCode != ErrorRBACDenied {
		t.Fatalf("ErrorCode = %q, want %q", p.ErrorCode, ErrorRBACDenied)
	}
}
