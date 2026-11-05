package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestKubectlApprovalPreviewExpandsJSONPatch(t *testing.T) {
	tool := NewWriteTools(&fakeWriteRunner{})[0].(*kubectlTool)
	preview := tool.PreviewApproval(json.RawMessage(`{"command":"kubectl -n test-complex patch deployment app-backend --type=json -p='[{\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/livenessProbe/httpGet/path\",\"value\":\"/\"},{\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/readinessProbe/httpGet/path\",\"value\":\"/\"}]'"}`))

	if preview.Target.Namespace != "test-complex" || preview.Target.Kind != "deployment" || preview.Target.Name != "app-backend" {
		t.Fatalf("target = %+v", preview.Target)
	}
	if len(preview.Changes) != 2 {
		t.Fatalf("changes = %+v, want 2 JSON patch entries", preview.Changes)
	}
	if !strings.Contains(preview.Changes[0].Path, "livenessProbe.httpGet.path") {
		t.Fatalf("first path = %q", preview.Changes[0].Path)
	}
	if preview.Changes[0].After != "/" {
		t.Fatalf("first value = %#v", preview.Changes[0].After)
	}
	if len(preview.Verification) == 0 {
		t.Fatal("deployment preview should include verification guidance")
	}
}
