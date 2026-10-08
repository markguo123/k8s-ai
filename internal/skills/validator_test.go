package skills

import (
	"strings"
	"testing"
)

func baseBuiltinSkill() Skill {
	return Skill{
		Name:    "base",
		Source:  SourceBuiltin,
		Version: "1.0.0",
		Kinds:   []string{"v1/ConfigMap"},
		Risk:    RiskRule{Default: RiskMedium},
		Validation: []Rule{{
			Name:      "missing_data",
			Condition: "missing",
			Severity:  SeveritySuggest,
			Message:   "data is required",
		}},
	}
}

func TestValidateSkillRejectsInvalid(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Skill)
		want   string
	}{
		{
			name:   "empty name",
			mutate: func(s *Skill) { s.Name = "  " },
			want:   "name",
		},
		{
			name:   "invalid source",
			mutate: func(s *Skill) { s.Source = "system" },
			want:   "source",
		},
		{
			name:   "missing kinds",
			mutate: func(s *Skill) { s.Kinds = nil },
			want:   "kind",
		},
		{
			name:   "empty kind",
			mutate: func(s *Skill) { s.Kinds = []string{" "} },
			want:   "kind",
		},
		{
			name:   "unknown condition",
			mutate: func(s *Skill) { s.Validation[0].Condition = "not_builtin" },
			want:   "condition",
		},
		{
			name:   "missing severity",
			mutate: func(s *Skill) { s.Validation[0].Severity = "" },
			want:   "severity",
		},
		{
			name:   "missing message",
			mutate: func(s *Skill) { s.Validation[0].Message = "" },
			want:   "message",
		},
		{
			name: "user default risk",
			mutate: func(s *Skill) {
				s.Source = SourceUser
				s.Risk.Default = RiskMedium
			},
			want: "risk.default",
		},
		{
			name: "user lower risk",
			mutate: func(s *Skill) {
				s.Source = SourceUser
				s.Risk = RiskRule{ElevateWhen: []RiskClause{{
					Name:      "keep_medium",
					Path:      "",
					Condition: "exists",
					To:        RiskMedium,
					Message:   "not a raise",
				}}}
			},
			want: "HIGH",
		},
		{
			name: "unknown check",
			mutate: func(s *Skill) {
				s.PostChecks = []Check{{Name: "unknown_check"}}
			},
			want: "check",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := baseBuiltinSkill()
			tc.mutate(&s)
			err := validateSkill(s)
			if err == nil {
				t.Fatal("validateSkill 应报错")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want contains %q", err, tc.want)
			}
		})
	}
}

func TestValidateSkillAcceptsBuiltinShape(t *testing.T) {
	s := baseBuiltinSkill()
	s.PostChecks = []Check{{Name: "object_exists"}}
	s.PreChecks = []Check{{Name: "namespace_exists"}}
	if err := validateSkill(s); err != nil {
		t.Fatalf("builtin 形态不应报错: %v", err)
	}
}

func TestValidateSkillAcceptsUserHighOnly(t *testing.T) {
	s := baseBuiltinSkill()
	s.Source = SourceUser
	s.Risk.Default = ""
	s.Risk.ElevateWhen = []RiskClause{{
		Name:      "force_high",
		Path:      "",
		Condition: "exists",
		To:        RiskHigh,
		Message:   "user rule raises to high",
	}}
	if err := validateSkill(s); err != nil {
		t.Fatalf("用户 skill 只升到 HIGH 应通过: %v", err)
	}
}
