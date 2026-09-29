package skills

import (
	"embed"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

//go:embed skills/*.yaml
var builtinFS embed.FS

type registry struct {
	skills  []Skill
	byKind  map[string][]Skill
	byNames map[string]bool
}

// NewBuiltin loads the five built-in domain skills embedded in the binary.
// A schema error at startup aborts construction instead of silently dropping
// a safety rule.
func NewBuiltin() (Registry, error) {
	r := &registry{byKind: map[string][]Skill{}, byNames: map[string]bool{}}
	entries, err := builtinFS.ReadDir("skills")
	if err != nil {
		return nil, fmt.Errorf("skills: read embedded builtin skills: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		raw, err := builtinFS.ReadFile("skills/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("skills: read %s: %w", entry.Name(), err)
		}
		var s Skill
		if err := yaml.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("skills: decode %s: %w", entry.Name(), err)
		}
		if s.Source == "" {
			s.Source = SourceBuiltin
		}
		s.Path = "builtin:" + entry.Name()
		if err := r.Register(s); err != nil {
			return nil, fmt.Errorf("skills: register %s: %w", entry.Name(), err)
		}
	}
	if len(r.skills) == 0 {
		return nil, fmt.Errorf("skills: no builtin skill loaded")
	}
	return r, nil
}

// New returns an empty registry for tests and future user-skill loaders.
func New() Registry {
	return &registry{byKind: map[string][]Skill{}, byNames: map[string]bool{}}
}

func (r *registry) List() []Skill {
	out := make([]Skill, len(r.skills))
	copy(out, r.skills)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *registry) ForGVK(gvk schema.GroupVersionKind) []Skill {
	var out []Skill
	for _, s := range r.skills {
		for _, filter := range s.Kinds {
			if matchKindFilter(filter, gvk) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

func (r *registry) MatchIntent(intent string) []Skill {
	if strings.TrimSpace(intent) == "" {
		return nil
	}
	needle := strings.ToLower(strings.TrimSpace(intent))
	var out []Skill
	for _, s := range r.skills {
		if strings.Contains(strings.ToLower(s.Domain), needle) ||
			strings.Contains(strings.ToLower(s.Name), needle) {
			out = append(out, s)
		}
	}
	return out
}

func (r *registry) Register(s Skill) error {
	if err := validateSkill(s); err != nil {
		return err
	}
	if r.byNames[s.Name] {
		return fmt.Errorf("skill %q already registered", s.Name)
	}
	item := s
	if item.Source == "" {
		item.Source = SourceBuiltin
	}
	r.skills = append(r.skills, item)
	r.byNames[item.Name] = true
	for _, filter := range item.Kinds {
		if _, ok := r.byKind[filter]; !ok {
			r.byKind[filter] = []Skill{}
		}
		r.byKind[filter] = append(r.byKind[filter], item)
	}
	return nil
}

func (r *registry) Reload() error {
	return fmt.Errorf("skills: hot reload is reserved for Phase 4")
}

func (r *registry) Validate(gvk schema.GroupVersionKind, content map[string]any, scope Scope) []Finding {
	var out []Finding
	for _, s := range r.ForGVK(gvk) {
		for _, rule := range s.Validation {
			vals := walkPath(content, rule.Path)
			if matchWithScope(vals, rule.Condition, scope) {
				out = append(out, Finding{
					Skill: s.Name, Domain: s.Domain, Rule: rule.Name,
					Severity: rule.Severity, Path: rule.Path,
					Message: rule.Message, Fix: rule.Fix,
				})
			}
		}
		if s.Risk.Default == RiskHigh {
			out = append(out, Finding{
				Skill: s.Name, Domain: s.Domain, Rule: "skill_default_risk",
				Severity: SeveritySuggest, Message: fmt.Sprintf("Skill %s 默认风险为 %s，建议按高风险人工确认", s.Name, s.Risk.Default),
				Risk: s.Risk.Default,
			})
		}
		for _, clause := range s.Risk.ElevateWhen {
			vals := walkPath(content, clause.Path)
			if matchWithScope(vals, clause.Condition, scope) {
				out = append(out, Finding{
					Skill: s.Name, Domain: s.Domain, Rule: clause.Name,
					Severity: SeveritySuggest, Path: clause.Path,
					Message: clause.Message, Risk: clause.To,
				})
			}
		}
	}
	return out
}

func matchWithScope(vals []any, cond string, scope Scope) bool {
	if cond == "cluster_scoped" {
		return scope == ScopeCluster || scope == ScopeUnknown
	}
	return matchCondition(vals, cond)
}

func matchKindFilter(filter string, gvk schema.GroupVersionKind) bool {
	g, v, k, wildcard := parseKindFilter(filter)
	if !wildcard && (g != gvk.Group && g != "") {
		return false
	}
	if !wildcard && (v != gvk.Version && v != "") {
		return false
	}
	return strings.EqualFold(k, gvk.Kind)
}

func parseKindFilter(filter string) (group, version, kind string, wildcard bool) {
	parts := strings.Split(filter, "/")
	if len(parts) == 3 {
		group, version, kind = parts[0], parts[1], parts[2]
	} else if len(parts) == 2 {
		version, kind = parts[0], parts[1]
	} else if len(parts) == 1 {
		kind = parts[0]
	}
	for _, p := range []string{group, version, kind} {
		if p == "*" {
			wildcard = true
		}
	}
	if wildcard {
		group, version = "", ""
	}
	return group, version, kind, wildcard
}
