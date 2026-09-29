// Package skills holds the declarative resource-creation skill model and
// registry contract for Phase 3 (R5). The model intentionally contains no
// execution capability: all writes go through create_resource ->
// internal/executor.
package skills

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Source marks where a Skill came from. Builtin skills are embedded together
// with the binary; user skills will be discovered in Phase 4 through the same
// model and registry.
type Source string

const (
	SourceBuiltin Source = "builtin"
	SourceUser    Source = "user"
)

// Severity is the user-facing weight of a skill finding. Phase 3 treats all
// skill findings as suggestions and never as hard blocks.
type Severity string

const (
	SeveritySuggest Severity = "suggest"
)

// RiskLevel is the non-blocking risk escalation a skill may attach to a
// finding. Real final risk still comes from approval.CreateSubjectRisk.
type RiskLevel string

const (
	RiskMedium RiskLevel = "MEDIUM"
	RiskHigh   RiskLevel = "HIGH"
)

// Scope mirrors the manifest kind scope used by the skill evaluator.
type Scope string

const (
	ScopeNamespaced Scope = "namespaced"
	ScopeCluster    Scope = "cluster"
	ScopeUnknown    Scope = "unknown"
)

// Rule is a single declarative validation rule. Condition is matched against
// the values at Path (for example spec.template.spec.containers[].image).
type Rule struct {
	Name      string   `yaml:"name" json:"name"`
	Path      string   `yaml:"path,omitempty" json:"path,omitempty"`
	Condition string   `yaml:"condition" json:"condition"`
	Severity  Severity `yaml:"severity" json:"severity"`
	Message   string   `yaml:"message" json:"message"`
	Fix       string   `yaml:"fix,omitempty" json:"fix,omitempty"`
}

// RiskClause raises the risk level only when its condition matches. A user
// skill may not lower risk, so To must be higher than the applicable default.
type RiskClause struct {
	Name      string    `yaml:"name" json:"name"`
	Path      string    `yaml:"path,omitempty" json:"path,omitempty"`
	Condition string    `yaml:"condition" json:"condition"`
	To        RiskLevel `yaml:"to" json:"to"`
	Message   string    `yaml:"message" json:"message"`
}

// RiskRule is the risk metadata of a Skill. Default is only meaningful for
// builtin skills; user skills may only add elevateWhen clauses.
type RiskRule struct {
	Default     RiskLevel    `yaml:"default,omitempty" json:"default,omitempty"`
	ElevateWhen []RiskClause `yaml:"elevateWhen,omitempty" json:"elevateWhen,omitempty"`
}

// Check is a named pre/post check. The persisted name must hit the builtin
// check vocabulary so users cannot reference unknown checks silently.
type Check struct {
	Name   string            `yaml:"name" json:"name"`
	Params map[string]string `yaml:"params,omitempty" json:"params,omitempty"`
}

// Skill is the frozen data model shared by builtin and Phase 4 user skills.
type Skill struct {
	Name           string   `yaml:"name" json:"name"`
	Domain         string   `yaml:"domain" json:"domain"`
	Version        string   `yaml:"version" json:"version"`
	Kinds          []string `yaml:"kinds" json:"kinds"`
	RequiredInputs []string `yaml:"requiredInputs,omitempty" json:"requiredInputs,omitempty"`
	OptionalInputs []string `yaml:"optionalInputs,omitempty" json:"optionalInputs,omitempty"`
	Validation     []Rule   `yaml:"validation,omitempty" json:"validation,omitempty"`
	Risk           RiskRule `yaml:"risk" json:"risk"`
	PreChecks      []Check  `yaml:"preChecks,omitempty" json:"preChecks,omitempty"`
	PostChecks     []Check  `yaml:"postChecks,omitempty" json:"postChecks,omitempty"`
	Source         Source   `yaml:"source" json:"source"`
	Path           string   `yaml:"path,omitempty" json:"path,omitempty"`
}

// Finding is a single non-blocking skill recommendation attached to an
// object in a create proposal. Risk is empty for plain suggestions.
type Finding struct {
	Skill    string    `json:"skill"`
	Domain   string    `json:"domain,omitempty"`
	Rule     string    `json:"rule"`
	Severity Severity  `json:"severity"`
	Path     string    `json:"path,omitempty"`
	Message  string    `json:"message"`
	Fix      string    `json:"fix,omitempty"`
	Risk     RiskLevel `json:"risk,omitempty"`
}

// Registry is the read-only skill contract frozen in Phase 3. Phase 4 adds
// user-skill discovery and hot reload on top of this interface.
type Registry interface {
	List() []Skill
	ForGVK(gvk schema.GroupVersionKind) []Skill
	MatchIntent(intent string) []Skill
	Register(Skill) error
	Reload() error
}

// Validator evaluates a parsed manifest object against all matching skills.
// create_resource calls this during dry-run so findings can enter the
// approval proposal without changing any existing failure logic.
type Validator interface {
	Validate(gvk schema.GroupVersionKind, content map[string]any, scope Scope) []Finding
}
