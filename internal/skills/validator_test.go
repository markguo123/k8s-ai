package skills

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

func TestValidateSkillThirdPartyContract(t *testing.T) {
	// 合法 third-party：带显式版本，可升档到 HIGH，可设 Pre/Post Check。
	ok := baseBuiltinSkill()
	ok.Name = "vendor-scan"
	ok.Author = "vendor-cloud"
	ok.Description = "第三方供应商提供的工作负载扫描方法论"
	ok.Version = "2.1.0"
	ok.Source = SourceThirdParty
	ok.Risk.Default = ""
	ok.Risk.ElevateWhen = []RiskClause{{
		Name:      "force_high",
		Path:      "",
		Condition: "exists",
		To:        RiskHigh,
		Message:   "vendor rule raises to high",
	}}
	ok.PreChecks = []Check{{Name: "object_exists"}}
	ok.PostChecks = []Check{{Name: "pvc_bound"}}
	if err := validateSkill(ok); err != nil {
		t.Fatalf("合法 third-party skill 应通过: %v", err)
	}
}

func TestValidateSkillThirdPartyRejectsMissingAuthor(t *testing.T) {
	s := baseBuiltinSkill()
	s.Source = SourceThirdParty
	s.Version = "1.0.0"
	s.Risk.Default = ""
	if err := validateSkill(s); err == nil {
		t.Fatal("third-party skill 缺 author 应报错")
	}
}

func TestValidateSkillThirdPartyRejectsMissingDescription(t *testing.T) {
	s := baseBuiltinSkill()
	s.Source = SourceThirdParty
	s.Author = "vendor-cloud"
	s.Version = "1.0.0"
	s.Risk.Default = ""
	if err := validateSkill(s); err == nil {
		t.Fatal("third-party skill 缺 description 应报错")
	}
}

func TestValidateSkillThirdPartyRejectsMissingVersion(t *testing.T) {
	s := baseBuiltinSkill()
	s.Source = SourceThirdParty
	s.Author = "vendor-cloud"
	s.Description = "供应商方法论"
	s.Version = ""
	s.Risk.Default = ""
	if err := validateSkill(s); err == nil {
		t.Fatal("third-party skill 缺 version 应报错")
	}
}

func TestValidateSkillThirdPartyRejectsDefaultRisk(t *testing.T) {
	s := baseBuiltinSkill()
	s.Source = SourceThirdParty
	s.Author = "vendor-cloud"
	s.Description = "供应商方法论"
	s.Version = "1.0.0"
	// 保留 baseBuiltinSkill 的 Default=MEDIUM，直接落入受限来源禁止基线分支。
	if err := validateSkill(s); err == nil {
		t.Fatal("third-party skill 设 default risk 应报错")
	}
}

func TestValidateSkillThirdPartyRejectsMediumElevate(t *testing.T) {
	s := baseBuiltinSkill()
	s.Source = SourceThirdParty
	s.Author = "vendor-cloud"
	s.Description = "供应商方法论"
	s.Version = "1.0.0"
	s.Risk = RiskRule{ElevateWhen: []RiskClause{{
		Name:      "keep_medium",
		Path:      "",
		Condition: "exists",
		To:        RiskMedium,
		Message:   "not a raise",
	}}}
	if err := validateSkill(s); err == nil {
		t.Fatal("third-party skill 升档到 MEDIUM（疑似降档）应报错")
	}
}

// TestSkillMetadataRoundTrip 证明 PRD §13.1 的 author/description/examples
// 元数据字段能被 YAML 完整往返，不会被序列化链路丢弃。
func TestSkillMetadataRoundTrip(t *testing.T) {
	s := baseBuiltinSkill()
	s.Name = "with-metadata"
	s.Author = "platform-team"
	s.Description = "带全量 §13 元数据的自定义 Skill"
	s.Examples = []Example{{
		Name:    "minimal-config",
		Purpose: "展示最小编制形态",
		Content: "data:\n  key: value",
	}}
	raw, err := yaml.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Skill
	if err := yaml.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Author != s.Author || got.Description != s.Description {
		t.Fatalf("元数据未往返: got author=%q desc=%q", got.Author, got.Description)
	}
	if !reflect.DeepEqual(got.Examples, s.Examples) {
		t.Fatalf("examples 未往返: %+v", got.Examples)
	}
}

func TestIsRestrictedSource(t *testing.T) {
	cases := []struct {
		source Source
		want   bool
	}{
		{SourceBuiltin, false},
		{SourceCustom, true},
		{SourceUser, true},
		{SourceThirdParty, true},
		{Source("builtin"), false},
	}
	for _, tc := range cases {
		if got := IsRestrictedSource(tc.source); got != tc.want {
			t.Errorf("IsRestrictedSource(%q) = %v, want %v", tc.source, got, tc.want)
		}
	}
}

func TestValidSource(t *testing.T) {
	for _, s := range []Source{SourceBuiltin, SourceCustom, SourceUser, SourceThirdParty} {
		if !ValidSource(s) {
			t.Errorf("ValidSource(%q) = false, want true", s)
		}
	}
	if ValidSource(Source("hacked")) {
		t.Fatal("ValidSource(hacked) 应为 false")
	}
}

