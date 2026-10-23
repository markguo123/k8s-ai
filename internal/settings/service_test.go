package settings

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// testCipher 是 P3.9 的 fake 静态加密器：每次 Encrypt 都带随机 nonce，
// 保证同一明文两次加密结果不同，用来验证"unchanged 比较走解密后明文"。
type testCipher struct{}

func (testCipher) Encrypt(plain []byte) (string, error) {
	nonce := make([]byte, 4)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	buf := append(nonce, plain...)
	return "test:" + base64.StdEncoding.EncodeToString(buf), nil
}

func (testCipher) Decrypt(encoded string) ([]byte, error) {
	const prefix = "test:"
	if !strings.HasPrefix(encoded, prefix) {
		return nil, errors.New("test cipher: 未知前缀")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, prefix))
	if err != nil {
		return nil, err
	}
	if len(raw) < 4 {
		return nil, errors.New("test cipher: 载荷过短")
	}
	return raw[4:], nil
}

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.OpenSQLite("file:settings-test-" + time.Now().Format("150405.000000000") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func itemOf(t *testing.T, view *GroupView, key string) Item {
	t.Helper()
	for _, item := range view.Items {
		if item.Key == key {
			return item
		}
	}
	t.Fatalf("分组 %s 缺少键 %s", view.Group, key)
	return Item{}
}

func patch(t *testing.T, raw string) map[string]json.RawMessage {
	t.Helper()
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("解析补丁失败: %v", err)
	}
	return out
}

func TestGetGroupMergesDefaults(t *testing.T) {
	svc := New(newTestStore(t))
	view, err := svc.GetGroup(context.Background(), GroupApproval)
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if len(view.Items) == 0 {
		t.Fatal("分组应返回目录内全部键")
	}
	mode := itemOf(t, view, "approval_mode")
	if string(mode.Value) != `"strict"` || string(mode.Default) != `"strict"` {
		t.Fatalf("未写入时应回默认值, got value=%s default=%s", mode.Value, mode.Default)
	}
	if mode.HasValue {
		t.Fatal("未写入的键 hasValue 应为 false")
	}
	if view.EncryptionAvailable {
		t.Fatal("未装配 cipher 时 encryptionAvailable 应为 false")
	}
}

func TestGetGroupReportsEncryptionAndExamples(t *testing.T) {
	svc := New(newTestStore(t), WithCipher(testCipher{}))
	view := mustView(t, svc, GroupModel)
	if !view.EncryptionAvailable {
		t.Fatal("装配 cipher 后 encryptionAvailable 应为 true")
	}
	timeout := itemOf(t, view, "timeout")
	if timeout.Example != "120s" {
		t.Fatalf("duration 应返回示例 120s, got %q", timeout.Example)
	}
	if itemOf(t, view, "base_url").Example == "" {
		t.Fatal("字符串配置应返回示例")
	}
	temperature := itemOf(t, view, "temperature")
	if temperature.Min == nil || *temperature.Min != 0 {
		t.Fatal("数值配置应返回真实下限，包括 0")
	}
	if temperature.Max == nil || *temperature.Max != 2 {
		t.Fatal("数值配置应返回真实上限")
	}
	if itemOf(t, view, "max_tokens").Max != nil {
		t.Fatal("无上限的数值配置不应返回 max")
	}
}

func TestGetGroupUnknownAndInternal(t *testing.T) {
	svc := New(newTestStore(t))
	for _, group := range []string{"nope", GroupMigration} {
		if _, err := svc.GetGroup(context.Background(), group); !errors.Is(err, ErrGroupNotFound) {
			t.Fatalf("分组 %s 应报 ErrGroupNotFound, got %v", group, err)
		}
	}
	// 内部分组只用于 migrate 记账，不允许出现在对外分组里。
	for _, group := range Groups() {
		if group == GroupMigration {
			t.Fatal("migration 分组不应对外可见")
		}
	}
}

func TestPatchGroupValidatesWholeBatch(t *testing.T) {
	st := newTestStore(t)
	svc := New(st)
	ctx := context.Background()

	cases := []struct{ name, body string }{
		{"未登记键", `{"no_such_key":1}`},
		{"枚举越界", `{"secret_access":"always"}`},
		{"类型不符", `{"require_approval":"yes"}`},
	}
	for _, tc := range cases {
		_, err := svc.PatchGroup(ctx, GroupApproval, patch(t, `{"approval_mode":"smart",`+tc.body[1:]), Actor{})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: 应报 ErrInvalid, got %v", tc.name, err)
		}
		// 整批拒绝：同批里的合法键也不能残留。
		if _, err := st.Settings().Get(ctx, GroupApproval, "approval_mode"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s: 非法批次不得写入任何键, get err=%v", tc.name, err)
		}
	}
}

func TestPatchGroupPersistsAndAudits(t *testing.T) {
	st := newTestStore(t)
	svc := New(st)
	ctx := context.Background()

	view, err := svc.PatchGroup(ctx, GroupModel, patch(t, `{"max_tokens":2048,"temperature":0.5}`), Actor{UserID: "u-1", RequestID: "r-1"})
	if err != nil {
		t.Fatalf("PatchGroup: %v", err)
	}
	if got := string(itemOf(t, view, "max_tokens").Value); got != "2048" {
		t.Fatalf("max_tokens 应更新为 2048, got %s", got)
	}
	if itemOf(t, view, "max_tokens").UpdatedAt == nil {
		t.Fatal("已写入的键应带 updatedAt")
	}

	events, err := st.Audit().List(ctx)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("应写入 1 条 CONFIG_CHANGE, got %d", len(events))
	}
	ev := events[0]
	if ev.EventType != store.AuditEventConfigChange || ev.Action != store.ActionConfigChange {
		t.Fatalf("事件类型/动作不符: %s / %s", ev.EventType, ev.Action)
	}
	if ev.UserID != "u-1" || ev.RequestID != "r-1" || ev.ResourceName != GroupModel {
		t.Fatalf("审计归属不符: %+v", ev)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(ev.ParameterSummary), &payload); err != nil {
		t.Fatalf("审计摘要应为 JSON: %v", err)
	}
	if payload["group"] != GroupModel || payload["changed"] != float64(2) {
		t.Fatalf("审计摘要内容不符: %v", payload)
	}
	if payload["value_in_payload"] != false {
		t.Fatal("审计摘要必须声明不含取值")
	}
	if strings.Contains(ev.ParameterSummary, "2048") || strings.Contains(ev.ParameterSummary, "0.5") {
		t.Fatalf("审计摘要不得包含配置取值: %s", ev.ParameterSummary)
	}
}

func TestPatchGroupUnchangedSkipsAudit(t *testing.T) {
	st := newTestStore(t)
	svc := New(st)
	ctx := context.Background()
	if _, err := svc.PatchGroup(ctx, GroupModel, patch(t, `{"max_tokens":2048}`), Actor{}); err != nil {
		t.Fatalf("首次写入: %v", err)
	}
	if _, err := svc.PatchGroup(ctx, GroupModel, patch(t, `{"max_tokens":2048}`), Actor{}); err != nil {
		t.Fatalf("重复写入: %v", err)
	}
	events, err := st.Audit().List(ctx)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("取值未变不应再写审计, got %d", len(events))
	}
}

// TestSensitiveKeysNeverReadBack 是红线用例：敏感键只写不读，
// 无论 API 视图还是 JSON 序列化结果都不得出现明文。
func TestSensitiveKeysNeverReadBack(t *testing.T) {
	svc := New(newTestStore(t), WithCipher(testCipher{}))
	ctx := context.Background()
	const secret = "sk-live-abcdef123456"

	if _, err := svc.PatchGroup(ctx, GroupModel, patch(t, `{"api_key":"`+secret+`"}`), Actor{}); err != nil {
		t.Fatalf("写入敏感键: %v", err)
	}
	if _, err := svc.PatchGroup(ctx, GroupOps, patch(t, `{"kubeconfig":"/root/.kube/admin.conf"}`), Actor{}); err != nil {
		t.Fatalf("写入 kubeconfig: %v", err)
	}
	for _, group := range []string{GroupModel, GroupOps} {
		view, err := svc.GetGroup(ctx, group)
		if err != nil {
			t.Fatalf("GetGroup %s: %v", group, err)
		}
		encoded, err := json.Marshal(view)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		for _, banned := range []string{secret, "/root/.kube/admin.conf"} {
			if strings.Contains(string(encoded), banned) {
				t.Fatalf("分组 %s 的读视图泄漏明文 %s", group, banned)
			}
		}
	}
	apiKey := itemOf(t, mustView(t, svc, GroupModel), "api_key")
	if string(apiKey.Value) != "null" || !apiKey.HasValue || !apiKey.Sensitive {
		t.Fatalf("敏感键应只回 hasValue, got value=%s has=%v sensitive=%v", apiKey.Value, apiKey.HasValue, apiKey.Sensitive)
	}
}

func mustView(t *testing.T, svc *Service, group string) *GroupView {
	t.Helper()
	view, err := svc.GetGroup(context.Background(), group)
	if err != nil {
		t.Fatalf("GetGroup %s: %v", group, err)
	}
	return view
}

func TestImportConfigSkipsDefaults(t *testing.T) {
	st := newTestStore(t)
	svc := New(st, WithCipher(testCipher{}))
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.LLM.Model = "custom-model"
	cfg.LLM.APIKey = "sk-import-secret"
	cfg.LLM.MaxTokens = 4096 // 与目录默认值相同，不应入库
	cfg.Agent.MaxIterations = 40

	summary, err := svc.ImportConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("ImportConfig: %v", err)
	}
	if summary.Imported < 3 {
		t.Fatalf("非默认值应入库, got %+v", summary)
	}
	if summary.Defaults == 0 {
		t.Fatalf("等同默认值的键应计入 Defaults 而不入库, got %+v", summary)
	}
	if got, err := st.Settings().Get(ctx, GroupModel, "model"); err != nil || got.Value != `"custom-model"` {
		t.Fatalf("model 应导入为 custom-model, got %+v err=%v", got, err)
	}
	if _, err := st.Settings().Get(ctx, GroupModel, "max_tokens"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("默认值 max_tokens 不应入库, err=%v", err)
	}
	// 敏感键以密文入库（P3.9），但标记 Sensitive 且读接口不回显。
	stored, err := st.Settings().Get(ctx, GroupModel, "api_key")
	if err != nil || !stored.Sensitive {
		t.Fatalf("api_key 应作为敏感键入库, got %+v err=%v", stored, err)
	}
	if stored.Value == `"sk-import-secret"` || strings.Contains(stored.Value, "sk-import-secret") {
		t.Fatalf("api_key 不得以明文落库, got %q", stored.Value)
	}
	if encoded, err := json.Marshal(mustView(t, svc, GroupModel)); err == nil && strings.Contains(string(encoded), "sk-import-secret") {
		t.Fatal("导入后的读视图不得泄漏 api_key 明文")
	}
}

// TestPatchGroupSensitiveEncryptsAtRest 验证 P3.9：敏感键以密文落库，
// Store 原始值不含明文，且密文可由注入的 cipher 还原。
func TestPatchGroupSensitiveEncryptsAtRest(t *testing.T) {
	st := newTestStore(t)
	svc := New(st, WithCipher(testCipher{}))
	ctx := context.Background()
	const secret = "sk-at-rest-secret"

	if _, err := svc.PatchGroup(ctx, GroupModel, patch(t, `{"api_key":"`+secret+`"}`), Actor{}); err != nil {
		t.Fatalf("写入敏感键: %v", err)
	}
	stored, err := st.Settings().Get(ctx, GroupModel, "api_key")
	if err != nil {
		t.Fatalf("读取落库值: %v", err)
	}
	if stored.Value == `"`+secret+`"` || strings.Contains(stored.Value, secret) {
		t.Fatalf("敏感键不得以明文落库, got %q", stored.Value)
	}
	if !stored.Sensitive {
		t.Fatal("落库记录应标记 Sensitive")
	}
	// 落库保存的是归一化后的 JSON 编码（含引号），cipher 应无损还原该值。
	encoded, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := testCipher{}.Decrypt(stored.Value)
	if err != nil || string(plain) != string(encoded) {
		t.Fatalf("密文应还原为入库前的归一化取值, got %q want %s err=%v", plain, encoded, err)
	}
}

// TestPatchGroupSensitiveUnchangedSkipsAudit 验证随机 nonce 下重复写同一
// 敏感取值不会重复写库/重复审计（unchanged 比较按解密后的明文进行）。
func TestPatchGroupSensitiveUnchangedSkipsAudit(t *testing.T) {
	st := newTestStore(t)
	svc := New(st, WithCipher(testCipher{}))
	ctx := context.Background()
	if _, err := svc.PatchGroup(ctx, GroupModel, patch(t, `{"api_key":"sk-repeat"}`), Actor{}); err != nil {
		t.Fatalf("首次写入: %v", err)
	}
	if _, err := svc.PatchGroup(ctx, GroupModel, patch(t, `{"api_key":"sk-repeat"}`), Actor{}); err != nil {
		t.Fatalf("重复写入: %v", err)
	}
	events, err := st.Audit().List(ctx)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("敏感键取值未变不应再写审计, got %d", len(events))
	}
}

// TestPatchGroupSensitiveRequiresCipher 验证未装配加密器时，包含敏感键的
// 整批写入按红线拒绝（ErrCipherRequired），批内非敏感键也不得残留。
func TestPatchGroupSensitiveRequiresCipher(t *testing.T) {
	st := newTestStore(t)
	svc := New(st)
	ctx := context.Background()
	_, err := svc.PatchGroup(ctx, GroupModel, patch(t, `{"api_key":"sk-nocipher","max_tokens":1024}`), Actor{})
	if !errors.Is(err, ErrCipherRequired) {
		t.Fatalf("未装配 cipher 写敏感键应报 ErrCipherRequired, got %v", err)
	}
	if _, err := st.Settings().Get(ctx, GroupModel, "max_tokens"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("被拒绝批次不得写入任何键, err=%v", err)
	}
}

// TestImportConfigSensitiveRequiresCipher 验证无 cipher 导入含非默认敏感键
// 的 config 整批报错，绝不静默丢弃、也不回落明文。
func TestImportConfigSensitiveRequiresCipher(t *testing.T) {
	st := newTestStore(t)
	svc := New(st)
	ctx := context.Background()
	cfg := &config.Config{}
	cfg.LLM.APIKey = "sk-import-nocipher"
	if _, err := svc.ImportConfig(ctx, cfg); !errors.Is(err, ErrCipherRequired) {
		t.Fatalf("无 cipher 导入敏感键应报 ErrCipherRequired, got %v", err)
	}
	if _, err := st.Settings().Get(ctx, GroupModel, "api_key"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("报错后不得落任何敏感明文, err=%v", err)
	}
}

func TestNormalizeCoversKinds(t *testing.T) {
	stringDef := Definition{Key: "k", Kind: KindString, Enum: []string{"a", "b"}}
	if _, err := normalize(stringDef, json.RawMessage(`"c"`)); err == nil {
		t.Fatal("枚举外取值应报错")
	}
	durationDef := Definition{Key: "k", Kind: KindDuration, Default: "30s"}
	// 三期配置里的 duration 原始值是纳秒整数，导入时必须能吃下。
	value, err := normalize(durationDef, json.RawMessage(`30000000000`))
	if err != nil || value != `"30s"` {
		t.Fatalf("纳秒时长应归一化为 30s, got %s err=%v", value, err)
	}
	if _, err := normalize(durationDef, json.RawMessage(`"-5s"`)); err == nil {
		t.Fatal("负时长应报错")
	}
	listDef := Definition{Key: "k", Kind: KindStringList}
	value, err = normalize(listDef, json.RawMessage(`[" a ","","b"]`))
	if err != nil || value != `["a","b"]` {
		t.Fatalf("字符串数组应去空白与空项, got %s err=%v", value, err)
	}
	intDef := Definition{Key: "k", Kind: KindInt, Min: 1, Max: 10}
	if _, err := normalize(intDef, json.RawMessage(`11`)); err == nil {
		t.Fatal("超出上限应报错")
	}
	if _, err := normalize(intDef, json.RawMessage(`1.5`)); err == nil {
		t.Fatal("非整数应报错")
	}
}

func TestNormalizeThinkingSwitchAndMCPServers(t *testing.T) {
	thinking := Definition{Key: "thinking", Kind: KindJSON, Source: "llm.thinking"}
	value, err := normalize(thinking, json.RawMessage(`{"type":"enabled"}`))
	if err != nil || value != `{"type":"enabled"}` {
		t.Fatalf("合法思考配置应归一化, got %s err=%v", value, err)
	}
	for _, raw := range []string{
		`{"type":"on"}`,
		`{"type":"enabled","extra":1}`,
		`"enabled"`,
	} {
		if _, err := normalize(thinking, json.RawMessage(raw)); err == nil {
			t.Fatalf("非法思考配置应报错: %s", raw)
		}
	}

	servers := Definition{Key: "servers", Kind: KindJSON, Source: "mcp.servers"}
	valid := `[{"name":"demo","command":"npx","args":["-y","pkg"],"env":{"TOKEN":"x"}}]`
	if value, err := normalize(servers, json.RawMessage(valid)); err != nil || value != valid {
		t.Fatalf("合法 MCP 列表应保留, got %s err=%v", value, err)
	}
	for _, raw := range []string{
		`{"name":"demo"}`,
		`[{"command":"npx"}]`,
		`[{"name":"demo"}]`,
		`[{"name":"demo","command":"npx","args":"-y"}]`,
		`[{"name":"demo","command":"npx","env":{"TOKEN":1}}]`,
	} {
		if _, err := normalize(servers, json.RawMessage(raw)); err == nil {
			t.Fatalf("非法 MCP 列表应报错: %s", raw)
		}
	}
}
