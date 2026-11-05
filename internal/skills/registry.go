package skills

import (
	"embed"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

//go:embed skills/*.yaml
var builtinFS embed.FS

// intentMaxHits 是单轮意图检索最多同时注入的 procedural Skill 数（P3.8.4 优化）：
// 防止整句子串命中把多篇长正文一次灌入 system prompt 撑爆上下文。显式 @name 不受
// 此限制（单篇、最高优先级）。
const intentMaxHits = 2

// intentTermRe 提取检索用关键词：ASCII 字母数字串 + 中文 1-2 字（贪婪、不重叠）。
// 中文按 2 字切分（如 “故障排查” → 故障/排查），既能匹配名词又能容忍停用词噪音。
var intentTermRe = regexp.MustCompile(`[a-z0-9]+|[\p{Han}]{1,2}`)

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

// FindByName 按名称精确查找（大小写不敏感），返回是否命中。显式 @name 指定与
// get_skill 工具都用它解析，保证只命中已启用（registry 内）的 Skill。
func FindByName(reg Registry, name string) (Skill, bool) {
	needle := strings.ToLower(strings.TrimSpace(name))
	if needle == "" {
		return Skill{}, false
	}
	for _, s := range reg.List() {
		if strings.ToLower(s.Name) == needle {
			return s, true
		}
	}
	return Skill{}, false
}

func (r *registry) ForGVK(gvk schema.GroupVersionKind) []Skill {
	var out []Skill
	for _, s := range r.skills {
		// P3.8：只有 structured（声明式规则）Skill 参与资源创建 GVK 命中；
		// procedural Skill 走聊天意图检索（MatchIntent）注入，不在这里匹配。
		if s.Type != TypeStructured {
			continue
		}
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
	query := strings.TrimSpace(intent)
	if query == "" {
		return nil
	}
	needle := strings.ToLower(query)
	// 快路径 1：整句与某个启用的 procedural Skill 名称完全一致（大小写不敏感）。
	// 显式名称调用不依赖正文/描述子串，避免名称歧义被检索漏掉。
	for _, s := range r.skills {
		if s.Type != TypeProcedural {
			continue
		}
		if strings.ToLower(s.Name) == needle {
			return []Skill{s}
		}
	}
	// 快路径 2：整句作为子串命中某个 Skill 的 名称/描述/domain（短而明确的问题，
	// 例如 “mysql” 或 “database 故障排查”）。按名称排序保证结果稳定。
	var sub []Skill
	for _, s := range r.skills {
		if s.Type != TypeProcedural {
			continue
		}
		if strings.Contains(strings.ToLower(s.Name), needle) ||
			strings.Contains(strings.ToLower(s.Description), needle) ||
			strings.Contains(strings.ToLower(s.Domain), needle) {
			sub = append(sub, s)
		}
	}
	sort.SliceStable(sub, func(i, j int) bool { return sub[i].Name < sub[j].Name })
	if len(sub) > 0 {
		if len(sub) > intentMaxHits {
			sub = sub[:intentMaxHits]
		}
		return sub
	}
	// 打分路径：长自然语言句按关键词打分（名称 > domain > 描述加权），
	// 覆盖“我这边有个 mysql 服务的 pod 一直在重启”这类不整句命中描述的提问。
	terms := intentTerms(query)
	if len(terms) == 0 {
		return nil
	}
	type scored struct {
		skill Skill
		score int
	}
	var scoredList []scored
	for _, s := range r.skills {
		if s.Type != TypeProcedural {
			continue
		}
		score := intentScore(s, terms)
		if score > 0 {
			scoredList = append(scoredList, scored{skill: s, score: score})
		}
	}
	sort.SliceStable(scoredList, func(i, j int) bool {
		if scoredList[i].score != scoredList[j].score {
			return scoredList[i].score > scoredList[j].score
		}
		return scoredList[i].skill.Name < scoredList[j].skill.Name
	})
	max := len(scoredList)
	if max > intentMaxHits {
		max = intentMaxHits
	}
	out := make([]Skill, 0, max)
	for _, it := range scoredList[:max] {
		out = append(out, it.skill)
	}
	return out
}

// intentTerms 把用户意图切成检索关键词：空格/标点/驼峰/连字符天然拆分 ASCII 词，
// 中文按 2 字切分。返回去重、有序的 term 列表。
func intentTerms(intent string) []string {
	matches := intentTermRe.FindAllString(strings.ToLower(intent), -1)
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		// 过滤单字符 ASCII 噪音（中文字符单独成词时保留，如 “图”）。
		if !containsHan(m) && len(m) < 2 {
			continue
		}
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// intentScore 计算单个 Skill 与关键词的重合度：名称命中 3 分、domain 命中 2 分、
// 描述命中 1 分，每个关键词只计一次最高权重。
func intentScore(s Skill, terms []string) int {
	name := strings.ToLower(s.Name)
	domain := strings.ToLower(s.Domain)
	desc := strings.ToLower(s.Description)
	score := 0
	for _, t := range terms {
		switch {
		case strings.Contains(name, t):
			score += 3
		case strings.Contains(domain, t):
			score += 2
		case strings.Contains(desc, t):
			score++
		}
	}
	return score
}

// containsHan 判断字符串是否包含中文字符。
func containsHan(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
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
	if item.Type == "" {
		item.Type = TypeStructured
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
