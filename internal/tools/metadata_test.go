package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// metadataStub 实现 MetadataDescriber，用于验证 Schema 描述前缀注入。
type metadataStub struct {
	meta string
}

func (s *metadataStub) Name() string        { return "meta_tool" }
func (s *metadataStub) Description() string { return "stub description" }
func (s *metadataStub) InputSchema() any    { return map[string]any{"type": "object"} }
func (s *metadataStub) Execute(context.Context, json.RawMessage) (ToolResult, error) {
	return ToolResult{Data: map[string]any{"ok": true}}, nil
}
func (s *metadataStub) IsReadOnly(json.RawMessage) bool { return true }
func (s *metadataStub) Risk(json.RawMessage) model.RiskLevel {
	return model.RiskSafe
}
func (s *metadataStub) SchemaMetadata() string { return s.meta }

func TestRegistrySchemasPrefixMetadata(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&metadataStub{meta: "[READ ONLY][RISK: SAFE][APPROVAL REQUIRED: NO]"}); err != nil {
		t.Fatal(err)
	}
	specs := r.Schemas()
	if len(specs) != 1 {
		t.Fatalf("schemas = %d", len(specs))
	}
	for _, want := range []string{"[READ ONLY]", "[RISK: SAFE]", "[APPROVAL REQUIRED: NO]", "stub description"} {
		if !strings.Contains(specs[0].Description, want) {
			t.Fatalf("description 缺少 %q: %q", want, specs[0].Description)
		}
	}
}

func TestKubectlSchemaMetadata(t *testing.T) {
	r := NewRegistry()
	for _, tool := range NewWriteTools(&fakeWriteRunner{}) {
		if err := r.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	for _, spec := range r.Schemas() {
		if spec.Name != "kubectl" {
			continue
		}
		for _, want := range []string{"[READ/WRITE]", "[RISK: DEPENDS ON COMMAND]", "[APPROVAL REQUIRED FOR WRITE OR SHELL]"} {
			if !strings.Contains(spec.Description, want) {
				t.Fatalf("kubectl description 缺少 %q: %q", want, spec.Description)
			}
		}
	}
}
