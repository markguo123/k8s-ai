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
)

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
	Source    string          `json:"source,omitempty"`
	Desc      string          `json:"desc,omitempty"`
	UpdatedAt *time.Time      `json:"updatedAt,omitempty"`
}

// GroupView 是一个配置分组的读取结果。
type GroupView struct {
	Group string `json:"group"`
	Items []Item `json:"items"`
}

// Service 是配置中心应用服务：读写都经统一 Store，变更写 CONFIG_CHANGE 审计。
type Service struct {
	store store.Store
}

// New 创建配置服务。
func New(st store.Store) *Service {
	return &Service{store: st}
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
	view := &GroupView{Group: group}
	for _, def := range catalog[group] {
		view.Items = append(view.Items, renderItem(def, byKey[def.Key]))
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

	var changed, sensitive int
	for _, item := range items {
		current, err := s.store.Settings().Get(ctx, group, item.key)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("get setting %s.%s: %w", group, item.key, err)
		}
		if current != nil && current.Value == item.value {
			continue
		}
		if err := s.store.Settings().Set(ctx, &store.Settings{
			Group: group, Key: item.key, Value: item.value, Sensitive: item.def.Sensitive,
		}); err != nil {
			return nil, fmt.Errorf("set setting %s.%s: %w", group, item.key, err)
		}
		changed++
		if item.def.Sensitive {
			sensitive++
		}
	}
	if changed > 0 {
		s.recordChange(ctx, group, keys, changed, sensitive, actor)
	}
	view, err := s.GetGroup(ctx, group)
	if err != nil {
		return nil, err
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
		ResourceKind:   "settings",
		ResourceName:   group,
		Operation:      "PATCH",
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
// 口径：按目录逐项映射，值等同默认值时不入库（读取侧仍会得到同一值），
// 敏感键（api_key / kubeconfig / mcp.servers）照原样入库但标记 Sensitive，
// 读接口永不回显。
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
			if err := s.store.Settings().Set(ctx, &store.Settings{
				Group: group, Key: def.Key, Value: value, Sensitive: def.Sensitive,
			}); err != nil {
				return summary, fmt.Errorf("import %s.%s: %w", group, def.Key, err)
			}
			summary.Imported++
		}
	}
	return summary, nil
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
		if !json.Valid(raw) {
			return "", fmt.Errorf("需要合法 JSON")
		}
		return string(raw), nil
	default:
		return "", fmt.Errorf("未知类型 %s", def.Kind)
	}
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
