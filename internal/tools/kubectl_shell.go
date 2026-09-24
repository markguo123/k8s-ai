package tools

import (
	"encoding/json"

	"github.com/k8s-ai/k8s-ai/internal/approval"
)

// HasShellSyntax reports whether the kubectl command in the input uses
// shell constructs. It is consumed by the Agent loop's approval decision
// so shell commands always require a human approver.
func (t *kubectlTool) HasShellSyntax(input json.RawMessage) bool {
	args, err := ParseArgs(input, "command")
	if err != nil {
		return false
	}
	return approval.HasShellSyntax(args["command"])
}
