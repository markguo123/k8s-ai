// Package skills holds the declarative resource-creation skill model and
// registry contract for Phase 3 (R5), extended in Phase 4 (P3.8) with the
// procedural (standard agent skill) track. The model intentionally contains
// no execution capability: all writes go through create_resource ->
// internal/executor, and procedural Skill 正文只作文本上下文注入，从不执行。
package skills

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// SkillType 是 Skill 的两维之一（P3.8）：表达方法论形态，与 Source 正交。
// structured = 资源创建确定性规则（既有三期模型）；procedural = 标准 Agent
// 技能（SKILL.md，正文作为不可信方法论参考注入聊天系统提示词）。
type SkillType string

const (
	// TypeStructured 是资源创建/校验的声明式确定性规则（缺省类型，兼容既有 YAML）。
	TypeStructured SkillType = "structured"
	// TypeProcedural 是标准 Agent 技能：SKILL.md 前端源数据（name/description）
	// + 正文 Body；正文视为不可信文本，只注入不执行。
	TypeProcedural SkillType = "procedural"
)

// ValidType 返回 t 是否为受支持的 Skill 类型（structured/procedural）。
func ValidType(t SkillType) bool {
	switch t {
	case TypeStructured, TypeProcedural:
		return true
	default:
		return false
	}
}

// Source marks where a Skill came from. Builtin skills are embedded together
// with the binary; user skills will be discovered in Phase 4 through the same
// model and registry.
type Source string

// Source 枚举 Skill 的平台来源（四期 P3.5，见 PRD §13.2）。
const (
	// SourceBuiltin 是与二进制一起嵌入的内置方法论 Skill。
	SourceBuiltin Source = "builtin"
	// SourceCustom 是用户通过 YAML 导入的自定义 Skill。
	SourceCustom Source = "custom"
	// SourceThirdParty 是第三方 Skill：只实现 Loader/Validation Contract，
	// 不做在线市场。值与来源绑定，禁止伪造（validator 强制校验）。
	SourceThirdParty Source = "third-party"

	// SourceUser 是 SourceCustom 的三期向后兼容别名，保留以兼容既有调用与迁移。
	SourceUser = SourceCustom
)

// ValidSource 返回 s 是否为受支持的平台来源（builtin/custom/third-party）。
func ValidSource(s Source) bool {
	switch s {
	case SourceBuiltin, SourceCustom, SourceThirdParty:
		return true
	default:
		return false
	}
}

// IsRestrictedSource 返回该来源是否禁止设置风险基线（只有内置 Skill 可设 Default）。
// custom 与 third-party 都只能通过 elevateWhen 升档到 HIGH，永不降档。
func IsRestrictedSource(s Source) bool {
	return s == SourceCustom || s == SourceThirdParty
}

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

// Example 是一个 Skill 的可复用示例（PRD §13.1 examples）：展示输入要求与
// 产出形态，仅作文档用途，不携带任何可执行内容。
type Example struct {
	Name    string `yaml:"name,omitempty" json:"name,omitempty"`
	Purpose string `yaml:"purpose,omitempty" json:"purpose,omitempty"`
	Content string `yaml:"content,omitempty" json:"content,omitempty"`
}

// SkillAsset 是一个 procedural Skill 技能包内资产的平台元数据（P3.8.3）。
// 只记录 name/path/kind/size/SHA-256/inert，绝不携带任何可执行内容字段；
// 包内 scripts/ 一律 inert=true，永不进入 Agent 工具路径。
type SkillAsset struct {
	Name   string `yaml:"name" json:"name"`
	Path   string `yaml:"path" json:"path"`
	Kind   string `yaml:"kind,omitempty" json:"kind,omitempty"` // text | script
	Size   int64  `yaml:"size,omitempty" json:"size,omitempty"`
	SHA256 string `yaml:"sha256,omitempty" json:"sha256,omitempty"`
	Inert  bool   `yaml:"inert,omitempty" json:"inert,omitempty"`
}

// Skill is the frozen data model shared by builtin and Phase 4 user skills.
type Skill struct {
	// Type 是 Skill 类型（structured/procedural，P3.8）：缺省视为 structured。
	Type SkillType `yaml:"type,omitempty" json:"type,omitempty"`
	// Author 是 Skill 的作者/维护方（PRD §13.1 author）。
	Author string `yaml:"author,omitempty" json:"author,omitempty"`
	// Description 是 Skill 的方法论说明（PRD §13.1 description）。
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Name        string   `yaml:"name" json:"name"`
	Domain      string   `yaml:"domain" json:"domain"`
	Version     string   `yaml:"version" json:"version"`
	Kinds       []string `yaml:"kinds" json:"kinds"`
	// Body 是 procedural Skill 的 SKILL.md 正文（P3.8.2），视为不可信文本，
	// 只在意图命中后注入 system prompt 供方法论参考，任何内容都不执行。
	Body string `yaml:"body,omitempty" json:"body,omitempty"`
	// Assets 是 procedural Skill 技能包的资产元数据清单（P3.8.3）。
	Assets []SkillAsset `yaml:"assets,omitempty" json:"assets,omitempty"`
	// RequiredInputs/OptionalInputs 是声明式输入要求（PRD §13.1）。
	RequiredInputs []string `yaml:"requiredInputs,omitempty" json:"requiredInputs,omitempty"`
	OptionalInputs []string `yaml:"optionalInputs,omitempty" json:"optionalInputs,omitempty"`
	// Validation 是声明式校验规则 rules；Risk 是 risk_policy。
	Validation []Rule   `yaml:"validation,omitempty" json:"validation,omitempty"`
	Risk       RiskRule `yaml:"risk" json:"risk"`
	PreChecks  []Check  `yaml:"preChecks,omitempty" json:"preChecks,omitempty"`
	PostChecks []Check  `yaml:"postChecks,omitempty" json:"postChecks,omitempty"`
	// Examples 是文档用途的示例清单（PRD §13.1 examples）。
	Examples []Example `yaml:"examples,omitempty" json:"examples,omitempty"`
	Source   Source    `yaml:"source" json:"source"`
	Path     string    `yaml:"path,omitempty" json:"path,omitempty"`
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
