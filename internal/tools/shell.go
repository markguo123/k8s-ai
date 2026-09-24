package tools

import "encoding/json"

// ShellSyntaxAware is an optional interface for tools that can express
// shell constructs (currently kubectl). The Agent loop uses it as the
// approval shell gate: shell syntax never auto-executes under
// --skip-permissions or require_approval=false.
type ShellSyntaxAware interface {
	HasShellSyntax(input json.RawMessage) bool
}
