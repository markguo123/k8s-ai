package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// EnvTier 是部署期环境变量覆盖的档位（D-29 v1.51 / ADR-040 v1.2 两档语义）。
type EnvTier string

const (
	// EnvTierSecret 密钥态：环境变量/Secret 恒优先于 DB。密钥只走 Secret、页面永不回显；
	// env 命中时页面写入的值暂不生效，但必须标注来源并在保存时显式提示。
	EnvTierSecret EnvTier = "secret"
	// EnvTierSeed 种子态（seed-once）：仅当 DB 该键无值时生效；页面保存过一次即以 DB 为准，
	// 环境变量不再覆盖。
	EnvTierSeed EnvTier = "seed"
)

// EnvVarOverride 把一个部署期环境变量绑定到一个动态配置键（D-29 / ADR-040 v1.2）。
//
// 口径：DB settings 是动态键的默认唯一权威源与唯一写入目标；环境变量只是"部署期注入"，
// 按两档生效——密钥态恒优先、种子态 seed-once；两种命中都不回写 DB、都不写审计。
// 页面读取被覆盖的键时标注来源与档位，保存此类键时给显式提示，避免"页面改了但看起来
// 没生效"的静默陷阱。
type EnvVarOverride struct {
	// Env 是环境变量名（不带 K8S_AI_ 前缀语义之外的含义，名字即字面量）。
	Env string
	// Group/Key 指向配置目录里的动态键，必须是 KindString 家族。
	Group string
	Key   string
	// Tier 是生效档位。空值按密钥态处理：宁可让页面值暂不生效，也不让密钥被页面旧值压掉。
	Tier EnvTier
	// Desc 说明这个覆盖的适用场景，供文档与告警复用。
	Desc string
}

// EnvOverrideWhitelist 是允许的环境变量覆盖白名单（v1.51 冻结口径：LLM 四键 + ops 四键）。
//
// 白名单之外一律不认：避免"任何 config 键都能用 env 塞进来"导致配置面重新分裂，也保证
// config validate 与页面标注能覆盖全部真实来源。temperature/max_tokens/timeout 刻意不纳入
// （KindFloat/KindInt/KindDuration 要多养三套类型转换分支，收益不划算，见 v1.51 ⑨）。
var EnvOverrideWhitelist = []EnvVarOverride{
	{Env: "K8S_AI_LLM_PROVIDER", Group: GroupModel, Key: "provider", Tier: EnvTierSeed, Desc: "模型提供方类型（首启种子值）"},
	{Env: "K8S_AI_LLM_ENDPOINT", Group: GroupModel, Key: "base_url", Tier: EnvTierSeed, Desc: "模型接口地址（首启种子值）"},
	{Env: "K8S_AI_LLM_API_KEY", Group: GroupModel, Key: "api_key", Tier: EnvTierSecret, Desc: "模型接口密钥（密钥态，恒优先）"},
	{Env: "K8S_AI_LLM_MODEL", Group: GroupModel, Key: "model", Tier: EnvTierSeed, Desc: "模型名（首启种子值）"},
	{Env: "K8S_AI_KUBECONFIG", Group: GroupOps, Key: "kubeconfig", Tier: EnvTierSeed, Desc: "kubeconfig 路径（首启种子值）"},
	{Env: "K8S_AI_KUBE_CONTEXT", Group: GroupOps, Key: "context", Tier: EnvTierSeed, Desc: "默认 kube context（首启种子值）"},
	{Env: "K8S_AI_NAMESPACE", Group: GroupOps, Key: "namespace", Tier: EnvTierSeed, Desc: "默认命名空间（首启种子值）"},
	{Env: "K8S_AI_KUBECTL_PATH", Group: GroupOps, Key: "kubectl_path", Tier: EnvTierSeed, Desc: "kubectl 路径（首启种子值）"},
}

// envOverrideIndex 是 "group.key" → 覆盖定义的索引（init 里与目录做一次一致性校验）。
var envOverrideIndex = func() map[string]EnvVarOverride {
	index := make(map[string]EnvVarOverride, len(EnvOverrideWhitelist))
	for _, ov := range EnvOverrideWhitelist {
		key := envKey(ov.Group, ov.Key)
		if _, dup := index[key]; dup {
			panic("settings: duplicate env override for " + key)
		}
		def, err := lookup(ov.Group, ov.Key)
		if err != nil {
			panic("settings: env override " + ov.Env + " points to unknown key " + key)
		}
		if def.Kind != KindString {
			panic("settings: env override " + ov.Env + " must target a string kind")
		}
		if ov.tier() != EnvTierSecret && ov.tier() != EnvTierSeed {
			panic("settings: env override " + ov.Env + " has unknown tier " + string(ov.Tier))
		}
		index[key] = ov
	}
	return index
}()

// tier 返回生效档位（空值按密钥态）。
func (o EnvVarOverride) tier() EnvTier {
	if o.Tier == "" {
		return EnvTierSecret
	}
	return o.Tier
}

// envKey 是覆盖索引用的稳定键。
func envKey(group, key string) string { return group + "." + key }

// overrideGroups 返回白名单涉及的配置分组（去重、稳定顺序）。
func overrideGroups() []string {
	seen := map[string]bool{}
	groups := make([]string, 0, 2)
	for _, ov := range EnvOverrideWhitelist {
		if !seen[ov.Group] {
			seen[ov.Group] = true
			groups = append(groups, ov.Group)
		}
	}
	return groups
}

// ActiveEnvOverride 是一条当前生效的覆盖来源（页面标注与保存提示共用）。
type ActiveEnvOverride struct {
	// Env 是命中的环境变量名。
	Env string `json:"env"`
	// Tier 是生效档位：secret 恒优先 / seed 首启种子值。
	Tier EnvTier `json:"tier"`
}

// lookupEnv 走可注入的查找函数（测试用 WithEnvLookup 替换）。
func (s *Service) lookupEnv(name string) (string, bool) {
	fn := s.envLookup
	if fn == nil {
		fn = os.LookupEnv
	}
	v, ok := fn(name)
	if !ok {
		return "", false
	}
	return v, true
}

// WithEnvLookup 注入环境变量查找函数（测试用；nil 恢复 os.LookupEnv）。
func WithEnvLookup(fn func(string) (string, bool)) Option {
	return func(s *Service) { s.envLookup = fn }
}

// envValue 返回环境变量的有效取值（未设置或只含空白时算"没配"，便于 K8s 里用空 env 占位）。
func (s *Service) envValue(ov EnvVarOverride) (string, bool) {
	raw, ok := s.lookupEnv(ov.Env)
	if !ok || strings.TrimSpace(raw) == "" {
		return "", false
	}
	return raw, true
}

// dbValuePresent 读取白名单涉及分组的 DB 取值状态："group.key" → 是否已有生效值。
//
// 种子态必须先看 DB 才能判定谁生效，因此这一步是两档语义的必读输入；空串/空容器按
// "未配置"处理（与 renderItem 的 hasValue 口径一致），页面清空过某键就重新接受种子值。
func (s *Service) dbValuePresent(ctx context.Context) (map[string]bool, error) {
	present := map[string]bool{}
	for _, group := range overrideGroups() {
		stored, err := s.store.Settings().ListByGroup(ctx, group)
		if err != nil {
			return nil, fmt.Errorf("list settings %s: %w", group, err)
		}
		for _, item := range stored {
			if _, ok := envOverrideIndex[envKey(group, item.Key)]; !ok {
				continue
			}
			present[envKey(group, item.Key)] = hasContent(item.Value)
		}
	}
	return present, nil
}

// ActiveEnvOverrides 返回当前进程真正生效的覆盖（键为 "group.key"）。
//
// 两档判定（D-29 v1.51）：密钥态只要环境变量非空即生效；种子态还要看 DB —— 该键在
// settings 表里已有值就让位给 DB（seed-once），无值才用环境变量兜底。DB 读不到时直接
// 报错：判定不了谁生效，就不能假装"生效来源"是清楚的。
func (s *Service) ActiveEnvOverrides(ctx context.Context) (map[string]ActiveEnvOverride, error) {
	dbHas, err := s.dbValuePresent(ctx)
	if err != nil {
		return nil, err
	}
	return s.activeEnvOverrides(dbHas), nil
}

// activeEnvOverrides 用已取到的 DB 取值状态判定生效覆盖（同一次请求内复用，避免重复读库）。
func (s *Service) activeEnvOverrides(dbHas map[string]bool) map[string]ActiveEnvOverride {
	active := map[string]ActiveEnvOverride{}
	for _, ov := range EnvOverrideWhitelist {
		if _, ok := s.envValue(ov); !ok {
			continue
		}
		key := envKey(ov.Group, ov.Key)
		if ov.tier() == EnvTierSeed && dbHas[key] {
			continue // 种子态：页面配过就以页面为准
		}
		active[key] = ActiveEnvOverride{Env: ov.Env, Tier: ov.tier()}
	}
	return active
}

// envOverrideValues 把生效覆盖解析成可写入运行时 config 的取值：
// 返回 config 键路径（Definition.Source）到值的映射，以及 "group.key" 命中集合。
//
// dbHas 是 DB 取值状态（由调用方在既有读取里顺带算出；nil 时这里自行读库）。
func (s *Service) envOverrideValues(ctx context.Context, dbHas map[string]bool) (map[string]any, map[string]bool, error) {
	if dbHas == nil {
		var err error
		if dbHas, err = s.dbValuePresent(ctx); err != nil {
			return nil, nil, err
		}
	}
	byPath := map[string]any{}
	configured := map[string]bool{}
	for _, ov := range EnvOverrideWhitelist {
		raw, ok := s.envValue(ov)
		if !ok {
			continue
		}
		key := envKey(ov.Group, ov.Key)
		if ov.tier() == EnvTierSeed && dbHas[key] {
			continue // 种子态已被 DB 接管：环境变量不再进入运行时
		}
		def, err := lookup(ov.Group, ov.Key)
		if err != nil {
			continue // 白名单与目录漂移时宁可不生效
		}
		if def.Source == "" {
			// 该键目前没有 config 承载方（如 model.provider：只做"已配置"判定与页面标注），
			// 不报错也不静默丢弃成错误——运行时没有它可读。
			configured[key] = true
			continue
		}
		stored, err := normalize(def, mustJSON(raw))
		if err != nil {
			return nil, nil, fmt.Errorf("env override %s: %w", ov.Env, err)
		}
		var value any
		if err := json.Unmarshal([]byte(stored), &value); err != nil {
			return nil, nil, fmt.Errorf("env override %s: parse value: %w", ov.Env, err)
		}
		byPath[def.Source] = value
		configured[key] = true
	}
	return byPath, configured, nil
}

// EnvOverrideWarning 生成"保存了被部署变量覆盖的键"的提示文案（P6.12 ⑩）。
//
// 两档话术必须分清：密钥态是"写库成功但运行时仍以 Secret 为准"，种子态是"写库成功且
// 从现在起以页面为准"。两者都不能静默成功。
func EnvOverrideWarning(ov ActiveEnvOverride, group, key string) string {
	label := group + "." + key
	if ov.Tier == EnvTierSecret {
		return fmt.Sprintf("%s 已写入配置库，但当前运行值仍由部署密钥 %s 提供；要让页面值生效，请由部署方移除该环境变量/Secret。", label, ov.Env)
	}
	return fmt.Sprintf("%s 已写入配置库并立即生效：该键此前由部署变量 %s 提供首启种子值，保存后以页面配置为准。", label, ov.Env)
}
