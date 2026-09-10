package llm

import (
	"encoding/json"
	"testing"
)

func TestNormalizeToolArguments(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty object", raw: `{}`, want: `{}`},
		{name: "quoted object", raw: `"{\"name\":\"node-1\"}"`, want: `{"name":"node-1"}`},
		{name: "trailing quote noise", raw: `{}""`, want: `{}`},
		{name: "invalid content", raw: `{"name":}`, want: `{"name":}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(normalizeToolArguments(json.RawMessage(tt.raw)))
			if got != tt.want {
				t.Fatalf("normalizeToolArguments() = %q, want %q", got, tt.want)
			}
		})
	}
}