package tools

import "testing"

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
