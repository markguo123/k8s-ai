package skills

import (
	"fmt"
	"strings"
)

var validationConditions = map[string]bool{
	"exists":          true,
	"present":         true,
	"missing":         true,
	"empty":           true,
	"true":            true,
	"latest_or_empty": true,
	"privileged":      true,
	"cluster_scoped":  true,
}

var checkKinds = map[string]bool{
	"namespace_exists":     true,
	"object_exists":        true,
	"readiness":            true,
	"storage_class_exists": true,
	"ingress_class_exists": true,
	"pvc_bound":            true,
	"service_ready":        true,
	"node_ready":           true,
}

func validateSkill(s Skill) error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("skill name is required")
	}
	if s.Source == "" {
		s.Source = SourceBuiltin
	}
	if s.Source != SourceBuiltin && s.Source != SourceUser {
		return fmt.Errorf("skill %q has invalid source %q", s.Name, s.Source)
	}
	if len(s.Kinds) == 0 {
		return fmt.Errorf("skill %q must declare at least one kind", s.Name)
	}
	for _, kind := range s.Kinds {
		if strings.TrimSpace(kind) == "" {
			return fmt.Errorf("skill %q has an empty kind filter", s.Name)
		}
	}
	if s.Source == SourceUser && s.Risk.Default != "" {
		return fmt.Errorf("user skill %q cannot set risk.default (only builtin skills set a baseline)", s.Name)
	}
	for i, rule := range s.Validation {
		if strings.TrimSpace(rule.Name) == "" {
			return fmt.Errorf("skill %q validation[%d] name is required", s.Name, i)
		}
		if !validationConditions[rule.Condition] {
			return fmt.Errorf("skill %q validation[%d] condition %q is not a builtin condition", s.Name, i, rule.Condition)
		}
		if rule.Severity != SeveritySuggest {
			return fmt.Errorf("skill %q validation[%d] severity must be %q", s.Name, i, SeveritySuggest)
		}
		if strings.TrimSpace(rule.Message) == "" {
			return fmt.Errorf("skill %q validation[%d] message is required", s.Name, i)
		}
	}
	for i, clause := range s.Risk.ElevateWhen {
		if strings.TrimSpace(clause.Name) == "" {
			return fmt.Errorf("skill %q risk.elevateWhen[%d] name is required", s.Name, i)
		}
		if !validationConditions[clause.Condition] {
			return fmt.Errorf("skill %q risk.elevateWhen[%d] condition %q is not a builtin condition", s.Name, i, clause.Condition)
		}
		if clause.To != RiskHigh && clause.To != RiskMedium {
			return fmt.Errorf("skill %q risk.elevateWhen[%d] to %q is not a supported risk level", s.Name, i, clause.To)
		}
		if strings.TrimSpace(clause.Message) == "" {
			return fmt.Errorf("skill %q risk.elevateWhen[%d] message is required", s.Name, i)
		}
		// Phase 3 baseline for known benign kinds is MEDIUM. A builtin skill may
		// keep MEDIUM in elevateWhen to document intent, but user skills must not
		// use elevateWhen to carry a MEDIUM default that looks like lowering.
		if s.Source == SourceUser && clause.To != RiskHigh {
			return fmt.Errorf("user skill %q risk.elevateWhen[%d] may only raise to HIGH", s.Name, i)
		}
	}
	for _, kind := range []struct {
		label string
		items []Check
	}{
		{"preChecks", s.PreChecks},
		{"postChecks", s.PostChecks},
	} {
		for i, c := range kind.items {
			if !checkKinds[c.Name] {
				return fmt.Errorf("skill %q %s[%d] name %q is not a builtin check", s.Name, kind.label, i, c.Name)
			}
		}
	}
	return nil
}
