package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mitchellh/mapstructure"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// 配置服务错误（Handler 据此映射 HTTP 状态码）。
var (
	// ErrGroupNotFound 分组不存在（或属于内部分组）。
	ErrGroupNotFound = errors.New("settings: group not found")
	// ErrInvalid 键未登记或取值不合法。
	ErrInvalid = errors.New("settings: invalid setting")
	// ErrCipherRequired 写入包含敏感键但未装配静态加密器（P3.9）。
	ErrCipherRequired = errors.New("settings: encryption cipher is required for sensitive keys")
	// ErrModelNotConfigured 表示模型接入既无配置中心取值、也无环境变量覆盖，
	// 生效值只剩内置默认端点与默认模型名（P6.1）。Chat 直接报错，不再静默连本地默认地址。
	ErrModelNotConfigured = errors.New("settings: model is not configured")
)

// SecretCipher 是敏感配置值的静态加密接口（P3.9）：
// Encrypt 把明文归一化取值加密为带版本前缀的密文；Decrypt 做对称还原，
// 供"unchanged 比较"与迁移校验使用。默认实现为 cluster.Cipher（AES-256-GCM，
// v1: 前缀，密钥读取自 K8S_AI_ENC_KEY，永不落库）。
type SecretCipher interface {
	Encrypt(plain []byte) (string, error)
	Decrypt(encoded string) ([]byte, error)
}

// Option 是 Service 的装配选项。
type Option func(*Service)

// WithCipher 注入敏感配置值的静态加密器（P3.9）。未装配时：
// 读视图对敏感键照常只回 hasValue；包含敏感键的写入整批报 ErrCipherRequired。
func WithCipher(c SecretCipher) Option {
	return func(s *Service) { s.cipher = c }
}

// Actor 是一次配置变更的操作者（写入 CONFIG_CHANGE 审计）。
type Actor struct {
	UserID    string
	RequestID string
}

// Item 是单个配置键的对外视图。敏感键的 Value 恒为 null，只暴露 HasValue。
type Item struct {
	Key       string          `json:"key"`
	Kind      Kind            `json:"kind"`
	Value     json.RawMessage `json:"value"`
	HasValue  bool            `json:"hasValue"`
	Sensitive bool            `json:"sensitive,omitempty"`
	Default   json.RawMessage `json:"default"`
	Enum      []string        `json:"enum,omitempty"`
	Example   string          `json:"example,omitempty"`
	Min       *float64        `json:"min,omitempty"`
	Max       *float64        `json:"max,omitempty"`
	Source    string          `json:"source,omitempty"`
	Desc      string          `json:"desc,omitempty"`
	UpdatedAt *time.Time      `json:"updatedAt,omitempty"`
	// EnvOverride 非空表示该键当前由部署期环境变量/Secret 提供（D-29 v1.51 两档口径）：
	// 密钥态下页面写入只落 DB、运行值仍以部署方为准；种子态下保存一次即以页面为准。
	// 两种命中都要在前端标注来源，保存时给显式提示，不得静默成功。
	EnvOverride string `json:"envOverride,omitempty"`
	// EnvOverrideTier 是 EnvOverride 的生效档位："secret" 恒优先 / "seed" 首启种子值。
	// EnvOverride 为空时本字段为空。
	EnvOverrideTier string `json:"envOverrideTier,omitempty"`
}

// GroupView 是一个配置分组的读取结果。
type GroupView struct {
	Group               string `json:"group"`
	EncryptionAvailable bool   `json:"encryptionAvailable"`
	Items               []Item `json:"items"`
	// Warnings 只在 PATCH 返回里填充：本次写入的键里有被部署期变量覆盖的项，逐条说明
	// 生效结果（密钥态"已落库但运行值仍以 Secret 为准" / 种子态"已落库且此后以页面为准"）。
	Warnings []string `json:"warnings,omitempty"`
}

// Service 是配置中心应用服务：读写都经统一 Store，变更写 CONFIG_CHANGE 审计。
type Service struct {
	store  store.Store
	cipher SecretCipher
	// envLookup 是部署期环境变量覆盖的查找函数（D-29）；nil 时用 os.LookupEnv。
	envLookup func(string) (string, bool)
}

// New 创建配置服务。
func New(st store.Store, opts ...Option) *Service {
	s := &Service{store: st}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// GetGroup 读取分组：目录默认值与 Store 已存值合并；敏感键永不回明文。
func (s *Service) GetGroup(ctx context.Context, group string) (*GroupView, error) {
	if !groupExists(group) {
		return nil, fmt.Errorf("%w: %s", ErrGroupNotFound, group)
	}
	stored, err := s.store.Settings().ListByGroup(ctx, group)
	if err != nil {
		return nil, fmt.Errorf("list settings %s: %w", group, err)
	}
	byKey := make(map[string]*store.Settings, len(stored))
	for _, item := range stored {
		byKey[item.Key] = item
	}
	view := &GroupView{Group: group, EncryptionAvailable: s.cipher != nil}
	// 两档判定要看 DB 当前值：种子态 DB 有值即让位。本组键的 DB 取值直接复用上面
	// 已读到的 stored，避免为标注来源再读一次库。
	dbHas := make(map[string]bool, len(stored))
	for _, saved := range stored {
		if _, ok := envOverrideIndex[envKey(group, saved.Key)]; ok {
			dbHas[envKey(group, saved.Key)] = hasContent(saved.Value)
		}
	}
	overrides := s.activeEnvOverrides(dbHas)
	for _, def := range catalog[group] {
		item := renderItem(def, byKey[def.Key])
		if ov, ok := overrides[envKey(group, def.Key)]; ok {
			// 部署期覆盖：页面标注来源与档位并视为已配置，密钥值由部署方掌握，不回显。
			item.EnvOverride = ov.Env
			item.EnvOverrideTier = string(ov.Tier)
			item.HasValue = true
			if def.Sensitive {
				item.Value = json.RawMessage("null")
			}
		}
		view.Items = append(view.Items, item)
	}
	return view, nil
}

// PatchGroup 局部更新分组：未登记键与越界取值直接拒绝，成功后写 CONFIG_CHANGE 审计。
// 审计载荷只含分组与键名（含"哪些是敏感键"），不含任何值。
func (s *Service) PatchGroup(ctx context.Context, group string, patch map[string]json.RawMessage, actor Actor) (*GroupView, error) {
	if !groupExists(group) {
		return nil, fmt.Errorf("%w: %s", ErrGroupNotFound, group)
	}
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// 先整体校验再落库：任何一个键非法都整批拒绝，避免分组被写进一半。
	type pending struct {
		key   string
		def   Definition
		value string
	}
	items := make([]pending, 0, len(keys))
	for _, key := range keys {
		def, err := lookup(group, key)
		if err != nil {
			return nil, err
		}
		value, err := normalize(def, patch[key])
		if err != nil {
			return nil, fmt.Errorf("%w: %s.%s: %v", ErrInvalid, group, key, err)
		}
		items = append(items, pending{key: key, def: def, value: value})
	}

	// 保存前先看覆盖态（P6.12 ⑩/⑪）：判定依据必须是"用户这次保存时看到的来源"，
	// 写完再读就看不到了（种子态保存后 DB 已接管）。
	preOverrides, err := s.ActiveEnvOverrides(ctx)
	if err != nil {
		return nil, err
	}

	var changed, sensitive int
	for _, item := range items {
		current, err := s.store.Settings().Get(ctx, group, item.key)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("get setting %s.%s: %w", group, item.key, err)
		}

		// 敏感键写路径（P3.9）：密文带随机 nonce，unchanged 必须用解密后的
		// 明文比较，否则同一取值会被当成新值重复写库、重复审计。
		if item.def.Sensitive {
			if s.cipher == nil {
				return nil, fmt.Errorf("%w: %s.%s", ErrCipherRequired, group, item.key)
			}
			currentPlain := ""
			hasCurrent := false
			if current != nil && current.Value != "" {
				plain, decErr := s.cipher.Decrypt(current.Value)
				if decErr != nil {
					return nil, fmt.Errorf("decrypt %s.%s: %w", group, item.key, decErr)
				}
				currentPlain = string(plain)
				hasCurrent = true
			}
			if hasCurrent && currentPlain == item.value {
				continue
			}
			if !hasContent(item.value) {
				// 空值 = 清空敏感配置：删除记录，读视图回到 hasValue=false。
				if hasCurrent {
					if delErr := s.store.Settings().Delete(ctx, group, item.key); delErr != nil &&
						!errors.Is(delErr, store.ErrNotFound) {
						return nil, fmt.Errorf("delete setting %s.%s: %w", group, item.key, delErr)
					}
					changed++
					sensitive++
				}
				continue
			}
			enc, encErr := s.cipher.Encrypt([]byte(item.value))
			if encErr != nil {
				return nil, fmt.Errorf("encrypt %s.%s: %w", group, item.key, encErr)
			}
			if err := s.store.Settings().Set(ctx, &store.Settings{
				Group: group, Key: item.key, Value: enc, Sensitive: true,
			}); err != nil {
				return nil, fmt.Errorf("set setting %s.%s: %w", group, item.key, err)
			}
			changed++
			sensitive++
			continue
		}
		if current != nil && current.Value == item.value {
			continue
		}
		if err := s.store.Settings().Set(ctx, &store.Settings{
			Group: group, Key: item.key, Value: item.value, Sensitive: false,
		}); err != nil {
			return nil, fmt.Errorf("set setting %s.%s: %w", group, item.key, err)
		}
		changed++
	}
	if changed > 0 {
		s.recordChange(ctx, group, keys, changed, sensitive, actor)
	}
	view, err := s.GetGroup(ctx, group)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		ov, ok := preOverrides[envKey(group, key)]
		if !ok {
			continue
		}
		view.Warnings = append(view.Warnings, EnvOverrideWarning(ov, group, key))
	}
	return view, nil
}

// recordChange 写 CONFIG_CHANGE 审计。审计失败只告警：配置已生效，不因观察面回滚业务。
func (s *Service) recordChange(ctx context.Context, group string, keys []string, changed, sensitive int, actor Actor) {
	payload, err := json.Marshal(map[string]any{
		"group":            group,
		"keys":             keys,
		"changed":          changed,
		"unchanged":        len(keys) - changed,
		"sensitive_keys":   sensitive,
		"value_in_payload": false,
	})
	if err != nil {
		slog.Warn("settings: 审计载荷编码失败", "group", group, "error", err)
		return
	}
	err = s.store.Audit().Write(ctx, &store.AuditEvent{
		Timestamp:        time.Now().UTC(),
		RequestID:        actor.RequestID,
		UserID:           actor.UserID,
		Action:           store.ActionConfigChange,
		EventType:        store.AuditEventConfigChange,
		ResourceKind:     "settings",
		ResourceName:     group,
		Operation:        "PATCH",
		ParameterSummary: string(payload),
		Status:           "success",
	})
	if err != nil {
		slog.Warn("settings: CONFIG_CHANGE 审计写入失败", "group", group, "error", err)
	}
}

// ImportSummary 是一次旧配置导入的结果。
type ImportSummary struct {
	// Imported 是写入 Store 的键数（与三期取值不同或三期显式设置过）。
	Imported int
	// Defaults 是取值等同默认、无需入库的键数（不丢数据：读取时按默认值合并）。
	Defaults int
	// Missing 是三期配置里不存在来源的键数（四期新增项）。
	Missing int
}

// ImportConfig 把三期 config.yaml 导入配置中心（P1.12 复用）。
//
// 口径：按目录逐项映射，值等同默认值时不入库（读取侧仍会得到同一值）；
// 敏感键（api_key / kubeconfig / mcp.servers）非默认值必须由注入的
// SecretCipher 加密后入库，未装配加密器时整批报错，绝不静默丢弃、也
// 不回落明文（P3.9）；读接口对敏感键永不回显。
func (s *Service) ImportConfig(ctx context.Context, cfg *config.Config) (ImportSummary, error) {
	var summary ImportSummary
	values, err := configValues(cfg)
	if err != nil {
		return summary, err
	}
	for _, group := range Groups() {
		for _, def := range catalog[group] {
			if def.Source == "" {
				summary.Missing++
				continue
			}
			raw, ok := lookupPath(values, def.Source)
			if !ok {
				summary.Missing++
				continue
			}
			encoded, err := json.Marshal(raw)
			if err != nil {
				return summary, fmt.Errorf("encode %s: %w", def.Source, err)
			}
			value, err := normalize(def, encoded)
			if err != nil {
				// 旧配置里的非法残值不能阻断导入：跳过并在返回值里计数为缺失。
				slog.Warn("settings: 旧配置项不合法，跳过导入", "key", def.Source, "error", err)
				summary.Missing++
				continue
			}
			defValue, err := normalize(def, mustJSON(def.Default))
			if err == nil && defValue == value {
				summary.Defaults++
				continue
			}
			saveValue := value
			if def.Sensitive {
				if s.cipher == nil {
					return summary, fmt.Errorf("%w: %s.%s", ErrCipherRequired, group, def.Key)
				}
				enc, encErr := s.cipher.Encrypt([]byte(value))
				if encErr != nil {
					return summary, fmt.Errorf("encrypt %s.%s: %w", group, def.Key, encErr)
				}
				saveValue = enc
			}
			if err := s.store.Settings().Set(ctx, &store.Settings{
				Group: group, Key: def.Key, Value: saveValue, Sensitive: def.Sensitive,
			}); err != nil {
				return summary, fmt.Errorf("import %s.%s: %w", group, def.Key, err)
			}
			summary.Imported++
		}
	}
	return summary, nil
}

// LoadRuntimeConfig 把动态配置叠加到启动基线 config 上（P6.12 / P6.1）。
//
// 口径：config.yaml 只供静态与平台参数作为启动基线；运行时组件（chat/llm/
// agent/scan）统一从这里取动态配置。叠加顺序由低到高是：文件与内置默认 <
// DB settings < 环境变量白名单覆盖（D-29）。DB 是动态键的默认唯一权威源与唯一
// 写入目标，环境变量只是部署期只读覆盖、不回写 DB。敏感键（api_key/kubeconfig/
// mcp servers）在叠加前解密；已配置敏感值但未装配 SecretCipher 时整批返回
// ErrCipherRequired，绝不以明文或半解密状态进入运行时。
func (s *Service) LoadRuntimeConfig(ctx context.Context, base *config.Config) (*config.Config, error) {
	cfg, _, err := s.loadRuntime(ctx, base)
	return cfg, err
}

// ChatRuntimeConfig 是给 Chat/LLM 使用的运行时生效配置（P6.1）：在
// LoadRuntimeConfig 之上再加一道"模型是否已配置"的门禁。
//
// 门禁口径：配置中心已存值或环境变量覆盖算"已配置"。文件里的 llm.endpoint 与
// 内置默认在 Load 之后无法区分，因此把"生效端点与模型名同时等于目录默认值"判定为
// 未配置并返回 ErrModelNotConfigured——容器里首次启动没配模型时必须明确报错，
// 不允许静默去连本地默认地址。
func (s *Service) ChatRuntimeConfig(ctx context.Context, base *config.Config) (*config.Config, error) {
	cfg, configured, err := s.loadRuntime(ctx, base)
	if err != nil {
		return nil, err
	}
	if s.modelExplicitlyConfigured(configured, cfg) {
		return cfg, nil
	}
	return nil, fmt.Errorf("%w: 请在配置中心「模型」分组设置接口地址与模型名，或部署时注入 K8S_AI_LLM_ENDPOINT / K8S_AI_LLM_MODEL", ErrModelNotConfigured)
}

// modelExplicitlyConfigured 判定模型接入是否已被显式配置。
func (s *Service) modelExplicitlyConfigured(configured map[string]bool, cfg *config.Config) bool {
	if configured[envKey(GroupModel, "base_url")] || configured[envKey(GroupModel, "model")] {
		return true
	}
	defURL, err1 := lookup(GroupModel, "base_url")
	defModel, err2 := lookup(GroupModel, "model")
	if err1 != nil || err2 != nil {
		return true // 目录缺失属异常配置，不在这里拦截对话
	}
	defEndpoint, _ := defURL.Default.(string)
	defModelName, _ := defModel.Default.(string)
	return cfg.LLM.Endpoint != defEndpoint || cfg.LLM.Model != defModelName
}

// loadRuntime 执行分层叠加，并回报"哪些动态键被显式配置过"（DB 存值或 env 覆盖）。
func (s *Service) loadRuntime(ctx context.Context, base *config.Config) (*config.Config, map[string]bool, error) {
	raw, err := configValues(base)
	if err != nil {
		return nil, nil, err
	}
	configured := map[string]bool{}
	// 顺带记录白名单键的 DB 取值状态：种子态环境变量只在 DB 无值时生效。
	dbHas := map[string]bool{}
	for _, group := range Groups() {
		stored, err := s.store.Settings().ListByGroup(ctx, group)
		if err != nil {
			return nil, nil, fmt.Errorf("list settings %s: %w", group, err)
		}
		for _, item := range stored {
			if _, ok := envOverrideIndex[envKey(group, item.Key)]; ok {
				dbHas[envKey(group, item.Key)] = hasContent(item.Value)
			}
			def, lerr := lookup(group, item.Key)
			if lerr != nil {
				continue // 未知/内部分组键不参与运行时叠加
			}
			if def.Source == "" {
				continue // 纯 DB 配置（如 memory/retention），本阶段不注入 config.Config
			}
			if !hasContent(item.Value) {
				continue
			}
			decoded, derr := decodeStored(def, item.Value, s.cipher)
			if derr != nil {
				return nil, nil, fmt.Errorf("decode %s.%s: %w", group, item.Key, derr)
			}
			applyPath(raw, def.Source, decoded)
			configured[envKey(group, item.Key)] = true
		}
	}
	// 环境变量白名单最后叠加：密钥态恒优先，种子态只在 DB 无值时兜底（D-29 v1.51）。
	envByPath, envConfigured, err := s.envOverrideValues(ctx, dbHas)
	if err != nil {
		return nil, nil, err
	}
	for path, value := range envByPath {
		applyPath(raw, path, value)
	}
	for key := range envConfigured {
		configured[key] = true
	}
	var out config.Config
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		DecodeHook: mapstructure.StringToTimeDurationHookFunc(),
		Result:     &out,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("build runtime config decoder: %w", err)
	}
	if err := dec.Decode(raw); err != nil {
		return nil, nil, fmt.Errorf("decode runtime config: %w", err)
	}
	if err := out.Validate(); err != nil {
		return nil, nil, fmt.Errorf("validate runtime config: %w", err)
	}
	return &out, configured, nil
}

// decodeStored 把 Store 里的一条配置值解码成可回写 config 的 Go 值。
// 敏感键先解密（密文是“规范化明文 JSON”的加密结果），再按 JSON 解析。
func decodeStored(def Definition, storedValue string, cipher SecretCipher) (any, error) {
	encoded := storedValue
	if def.Sensitive {
		if cipher == nil {
			return nil, ErrCipherRequired
		}
		plain, err := cipher.Decrypt(storedValue)
		if err != nil {
			return nil, fmt.Errorf("decrypt sensitive value: %w", err)
		}
		encoded = string(plain)
	}
	var value any
	if err := json.Unmarshal([]byte(encoded), &value); err != nil {
		return nil, fmt.Errorf("parse stored value: %w", err)
	}
	return value, nil
}

// applyPath 按 "a.b.c" 点分路径把值写入嵌套 map（用于与 viper 同源的分层叠加）。
func applyPath(root map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	cur := root
	for _, p := range parts[:len(parts)-1] {
		child, ok := cur[p].(map[string]any)
		if !ok {
			child = map[string]any{}
			cur[p] = child
		}
		cur = child
	}
	cur[parts[len(parts)-1]] = value
}

// configValues 按 mapstructure 标签把 Config 摊平成 group.key 可查的嵌套映射。
func configValues(cfg *config.Config) (map[string]any, error) {
	var out map[string]any
	if err := mapstructure.Decode(cfg, &out); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	return out, nil
}

// lookupPath 按 "llm.api_key" 形式逐段取值。
func lookupPath(values map[string]any, path string) (any, bool) {
	var current any = values
	for _, part := range strings.Split(path, ".") {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// renderItem 合并默认值与已存值，并对敏感键脱敏。
func renderItem(def Definition, stored *store.Settings) Item {
	item := Item{
		Key:       def.Key,
		Kind:      def.Kind,
		Sensitive: def.Sensitive,
		Enum:      def.Enum,
		Example:   def.Example,
		Min:       numericBound(def, def.Min),
		Max:       numericBound(def, def.Max),
		Source:    def.Source,
		Desc:      def.Desc,
		Default:   mustJSON(def.Default),
	}
	if stored == nil || stored.Value == "" {
		if def.Sensitive {
			item.Value = json.RawMessage("null")
			return item
		}
		item.Value = def.defaultJSON()
		return item
	}
	item.UpdatedAt = &stored.UpdatedAt
	has := hasContent(stored.Value)
	if def.Sensitive {
		item.HasValue = has
		item.Value = json.RawMessage("null")
		return item
	}
	item.HasValue = has
	item.Value = json.RawMessage(stored.Value)
	return item
}

// numericBound 只向数值控件暴露真实边界；duration 的 Min/Max 以纳秒表示，
// 与前端秒/分钟输入不是同一单位，不能混用。Max==0 表示无上限。
func numericBound(def Definition, value float64) *float64 {
	if def.Kind != KindInt && def.Kind != KindFloat {
		return nil
	}
	if value == 0 && def.Kind == KindInt {
		return nil
	}
	bound := value
	return &bound
}

// defaultJSON 返回默认值的 JSON 编码（编码失败退化为 null）。
func (d Definition) defaultJSON() json.RawMessage {
	raw := mustJSON(d.Default)
	if _, err := normalize(d, raw); err != nil {
		return json.RawMessage("null")
	}
	return raw
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return raw
}

// hasContent 判断已存值是否算"已配置"（空串/空容器/null 视为未配置）。
func hasContent(raw string) bool {
	switch strings.TrimSpace(raw) {
	case "", "null", `""`, "[]", "{}":
		return false
	default:
		return true
	}
}

// normalize 校验取值并返回入库用的 JSON 编码。
func normalize(def Definition, raw json.RawMessage) (string, error) {
	switch def.Kind {
	case KindString:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", fmt.Errorf("需要字符串")
		}
		if len(def.Enum) > 0 && !contains(def.Enum, s) {
			return "", fmt.Errorf("取值必须是 %s", strings.Join(def.Enum, "|"))
		}
		return string(mustJSON(s)), nil
	case KindBool:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return "", fmt.Errorf("需要布尔值")
		}
		return strconv.FormatBool(b), nil
	case KindInt:
		n, err := jsonNumber(raw)
		if err != nil {
			return "", err
		}
		if n != float64(int64(n)) {
			return "", fmt.Errorf("需要整数")
		}
		if err := checkRange(def, n); err != nil {
			return "", err
		}
		return strconv.FormatInt(int64(n), 10), nil
	case KindFloat:
		n, err := jsonNumber(raw)
		if err != nil {
			return "", err
		}
		if err := checkRange(def, n); err != nil {
			return "", err
		}
		return strconv.FormatFloat(n, 'g', -1, 64), nil
	case KindDuration:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			// 兼容三期 config 里的 duration 原始值（纳秒整数）。
			n, numErr := jsonNumber(raw)
			if numErr != nil {
				return "", fmt.Errorf("需要时长字符串（如 30s）")
			}
			s = time.Duration(int64(n)).String()
		}
		d, err := time.ParseDuration(s)
		if err != nil || d < 0 {
			return "", fmt.Errorf("需要合法时长（如 30s/5m）")
		}
		if def.Max > 0 && float64(d) > def.Max {
			return "", fmt.Errorf("时长超出上限")
		}
		return string(mustJSON(d.String())), nil
	case KindStringList:
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			return "", fmt.Errorf("需要字符串数组")
		}
		cleaned := make([]string, 0, len(list))
		for _, item := range list {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				cleaned = append(cleaned, trimmed)
			}
		}
		return string(mustJSON(cleaned)), nil
	case KindJSON:
		trimmed := strings.TrimSpace(string(raw))
		if !json.Valid([]byte(trimmed)) {
			return "", fmt.Errorf("需要合法 JSON")
		}
		switch def.Key {
		case "thinking", "scan_thinking":
			return normalizeThinking(trimmed)
		case "servers":
			if def.Source == "mcp.servers" {
				if err := validateMCPServers(trimmed); err != nil {
					return "", err
				}
			}
		}
		return trimmed, nil
	default:
		return "", fmt.Errorf("未知类型 %s", def.Kind)
	}
}

// normalizeThinking 把思考开关收敛为标准 OpenAI thinking 对象。配置中心只
// 暴露 enabled/disabled 两个状态，避免管理员手填任意 JSON；空值/null 仍可
// 用于清除旧配置。
func normalizeThinking(raw string) (string, error) {
	if raw == "null" {
		return raw, nil
	}
	var value map[string]string
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", fmt.Errorf("思考配置必须是 JSON 对象，例如 {\"type\":\"enabled\"}")
	}
	if len(value) != 1 {
		return "", fmt.Errorf("思考配置只支持 type 字段")
	}
	mode, ok := value["type"]
	if !ok || (mode != "enabled" && mode != "disabled") {
		return "", fmt.Errorf("思考配置 type 必须是 enabled 或 disabled")
	}
	return string(mustJSON(map[string]string{"type": mode})), nil
}

// validateMCPServers 校验 MCP 服务器列表的基本形状。完整连接参数仍由
// config.Validate 在装配阶段校验，这里先拦住明显的非数组/非对象输入。
func validateMCPServers(raw string) error {
	if raw == "null" {
		return nil
	}
	var servers []map[string]any
	if err := json.Unmarshal([]byte(raw), &servers); err != nil {
		return fmt.Errorf("MCP 服务器必须是 JSON 数组")
	}
	for i, server := range servers {
		name, _ := server["name"].(string)
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("MCP 服务器第 %d 项缺少 name", i+1)
		}
		command, _ := server["command"].(string)
		endpoint, _ := server["url"].(string)
		if strings.TrimSpace(command) == "" && strings.TrimSpace(endpoint) == "" {
			return fmt.Errorf("MCP 服务器 %s 必须配置 command 或 url", name)
		}
		if value, exists := server["args"]; exists {
			args, ok := value.([]any)
			if !ok {
				return fmt.Errorf("MCP 服务器 %s 的 args 必须是字符串数组", name)
			}
			for _, arg := range args {
				if _, ok := arg.(string); !ok {
					return fmt.Errorf("MCP 服务器 %s 的 args 必须是字符串数组", name)
				}
			}
		}
		if value, exists := server["env"]; exists {
			env, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("MCP 服务器 %s 的 env 必须是字符串键值对象", name)
			}
			for key, item := range env {
				if _, ok := item.(string); !ok {
					return fmt.Errorf("MCP 服务器 %s 的 env.%s 必须是字符串", name, key)
				}
			}
		}
	}
	return nil
}

func jsonNumber(raw json.RawMessage) (float64, error) {
	var n float64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, fmt.Errorf("需要数字")
	}
	return n, nil
}

// checkRange 校验数值取值：目录内的数值项都是非负阈值/计数，因此未声明下限
// （Min==0）即视为下界 0；Max==0 表示不设上限。
func checkRange(def Definition, n float64) error {
	if n < def.Min {
		return fmt.Errorf("取值不得小于 %v", def.Min)
	}
	if def.Max > 0 && n > def.Max {
		return fmt.Errorf("取值不得大于 %v", def.Max)
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
