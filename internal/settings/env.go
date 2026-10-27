package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// EnvVarOverride 把一个部署期环境变量绑定到一个动态配置键（D-29 / ADR-040）。
//
// 口径：DB settings 是动态键的默认唯一权威源与唯一写入目标；环境变量只是
// "部署期只读覆盖"，优先级最高、不回写 DB、不写审计。页面读取被覆盖的键时
// 会标注来源，避免"页面改了但看起来没生效"的误判。
type EnvVarOverride struct {
	// Env 是环境变量名（不带 K8S_AI_ 前缀语义之外的含义，名字即字面量）。
	Env string
	// Group/Key 指向配置目录里的动态键，必须是 KindString 家族。
	Group string
	Key   string
	// Desc 说明这个覆盖的适用场景，供文档与告警复用。
	Desc string
}

// EnvOverrideWhitelist 是允许的环境变量覆盖白名单（v1.49 冻结口径）。
//
// 白名单之外一律不认：避免"任何 config 键都能用 env 塞进来"导致配置面重新
// 分裂，也保证 config validate 与页面标注能覆盖全部真实来源。
var EnvOverrideWhitelist = []EnvVarOverride{
	{Env: "K8S_AI_LLM_PROVIDER", Group: GroupModel, Key: "provider", Desc: "模型提供方类型"},
	{Env: "K8S_AI_LLM_ENDPOINT", Group: GroupModel, Key: "base_url", Desc: "模型接口地址"},
	{Env: "K8S_AI_LLM_API_KEY", Group: GroupModel, Key: "api_key", Desc: "模型接口密钥"},
	{Env: "K8S_AI_LLM_MODEL", Group: GroupModel, Key: "model", Desc: "模型名"},
	{Env: "K8S_AI_KUBECONFIG", Group: GroupOps, Key: "kubeconfig", Desc: "kubeconfig 路径"},
	{Env: "K8S_AI_KUBE_CONTEXT", Group: GroupOps, Key: "context", Desc: "默认 kube context"},
	{Env: "K8S_AI_NAMESPACE", Group: GroupOps, Key: "namespace", Desc: "默认命名空间"},
	{Env: "K8S_AI_KUBECTL_PATH", Group: GroupOps, Key: "kubectl_path", Desc: "kubectl 路径"},
}

// envKey 是覆盖索引用的稳定键。
func envKey(group, key string) string { return group + "." + key }

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

// ActiveEnvOverrides 返回当前进程真正生效的覆盖：键为 "group.key"，值为环境变量名。
// 空值（含只空白）视为未设置，便于 K8s 里用空 env 占位。
func (s *Service) ActiveEnvOverrides() map[string]string {
	active := map[string]string{}
	for _, ov := range EnvOverrideWhitelist {
		v, ok := s.lookupEnv(ov.Env)
		if !ok || strings.TrimSpace(v) == "" {
			continue
		}
		if _, err := lookup(ov.Group, ov.Key); err != nil {
			continue // 白名单与目录漂移时宁可不生效
		}
		active[envKey(ov.Group, ov.Key)] = ov.Env
	}
	return active
}

// envOverrideValues 把生效覆盖解析成可写入运行时 config 的取值：
// 返回 config 键路径（Definition.Source）到值的映射，以及 "group.key" 命中集合。
func (s *Service) envOverrideValues() (map[string]any, map[string]bool, error) {
	byPath := map[string]any{}
	configured := map[string]bool{}
	for _, ov := range EnvOverrideWhitelist {
		raw, ok := s.lookupEnv(ov.Env)
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		def, err := lookup(ov.Group, ov.Key)
		if err != nil {
			continue
		}
		if def.Kind != KindString {
			return nil, nil, fmt.Errorf("env override %s: key %s.%s must be a string kind", ov.Env, ov.Group, ov.Key)
		}
		if def.Source == "" {
			return nil, nil, fmt.Errorf("env override %s: key %s.%s has no config source", ov.Env, ov.Group, ov.Key)
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
		configured[envKey(ov.Group, ov.Key)] = true
	}
	return byPath, configured, nil
}
