package settings

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/config"
)

// staticEnv 用固定 map 冒充进程环境变量，避免测试污染真实进程环境。
func staticEnv(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := values[name]
		return v, ok
	}
}

// loadTestBaseline 取一份完整合法的启动基线（空 &config.Config{} 过不了 Validate）。
func loadTestBaseline(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	return cfg
}

// TestEnvOverrideWhitelistTiers 锁 D-29 v1.51 冻结口径：覆盖面严格等于 LLM 四键 + ops 四键，
// 档位是 api_key 密钥态、其余种子态。加键或改档必须同步 ADR-040 / 部署手册 §3.3 与 §4.9。
func TestEnvOverrideWhitelistTiers(t *testing.T) {
	want := map[string]EnvTier{
		envKey(GroupModel, "provider"):   EnvTierSeed,
		envKey(GroupModel, "base_url"):   EnvTierSeed,
		envKey(GroupModel, "api_key"):    EnvTierSecret,
		envKey(GroupModel, "model"):      EnvTierSeed,
		envKey(GroupOps, "kubeconfig"):   EnvTierSeed,
		envKey(GroupOps, "context"):      EnvTierSeed,
		envKey(GroupOps, "namespace"):    EnvTierSeed,
		envKey(GroupOps, "kubectl_path"): EnvTierSeed,
	}
	if len(EnvOverrideWhitelist) != len(want) {
		t.Fatalf("白名单键数变了: got %d want %d", len(EnvOverrideWhitelist), len(want))
	}
	for _, ov := range EnvOverrideWhitelist {
		key := envKey(ov.Group, ov.Key)
		tier, ok := want[key]
		if !ok {
			t.Fatalf("白名单多出未登记键 %s（env 覆盖面需先改 ADR-040）", key)
		}
		if ov.tier() != tier {
			t.Fatalf("%s 档位应为 %s, got %s", key, tier, ov.tier())
		}
	}
}

func TestActiveEnvOverrides(t *testing.T) {
	ctx := context.Background()
	svc := New(newTestStore(t), WithEnvLookup(staticEnv(map[string]string{
		"K8S_AI_LLM_MODEL":    "env-model",
		"K8S_AI_NAMESPACE":    "prod",
		"K8S_AI_KUBE_CONTEXT": "   ",
	})))
	active, err := svc.ActiveEnvOverrides(ctx)
	if err != nil {
		t.Fatalf("ActiveEnvOverrides: %v", err)
	}
	if got := active[envKey(GroupModel, "model")]; got.Env != "K8S_AI_LLM_MODEL" || got.Tier != EnvTierSeed {
		t.Fatalf("模型名种子覆盖未生效: %+v", active)
	}
	if got := active[envKey(GroupOps, "namespace")]; got.Env != "K8S_AI_NAMESPACE" || got.Tier != EnvTierSeed {
		t.Fatalf("命名空间种子覆盖未生效: %+v", active)
	}
	if _, ok := active[envKey(GroupOps, "context")]; ok {
		t.Fatal("空白环境变量应视为未设置")
	}
	if len(active) != 2 {
		t.Fatalf("只应命中白名单内非空变量, got %+v", active)
	}
}

// TestSeedOverrideYieldsToDBValue 锁两档语义的前两态：种子态在 DB 无值时生效，
// 页面保存一次即以 DB 为准（seed-once），清空后重新接受种子值。
func TestSeedOverrideYieldsToDBValue(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	const envEndpoint = "http://env.example:8000/v1"
	const dbEndpoint = "http://db.example:9000/v1"
	svc := New(st, WithEnvLookup(staticEnv(map[string]string{"K8S_AI_LLM_ENDPOINT": envEndpoint})))

	// 态一：空库 —— 种子值进运行时，页面标注来源与档位。
	cfg, err := svc.ChatRuntimeConfig(ctx, loadTestBaseline(t))
	if err != nil {
		t.Fatalf("ChatRuntimeConfig: %v", err)
	}
	if cfg.LLM.Endpoint != envEndpoint {
		t.Fatalf("DB 无值时种子值应生效, got %q", cfg.LLM.Endpoint)
	}
	view, err := svc.GetGroup(ctx, GroupModel)
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	item := itemOf(t, view, "base_url")
	if item.EnvOverride != "K8S_AI_LLM_ENDPOINT" || item.EnvOverrideTier != string(EnvTierSeed) || !item.HasValue {
		t.Fatalf("应标注种子态覆盖来源, got envOverride=%q tier=%q hasValue=%v", item.EnvOverride, item.EnvOverrideTier, item.HasValue)
	}

	// 态二：页面保存一次 —— 必须返回显式提示，运行值切到 DB，覆盖标注消失。
	saved, err := svc.PatchGroup(ctx, GroupModel, patch(t, `{"base_url":"`+dbEndpoint+`"}`), Actor{UserID: "u-1"})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if len(saved.Warnings) != 1 || !strings.Contains(saved.Warnings[0], "K8S_AI_LLM_ENDPOINT") ||
		!strings.Contains(saved.Warnings[0], "以页面配置为准") {
		t.Fatalf("种子态保存必须提示接管来源, got %+v", saved.Warnings)
	}
	if item := itemOf(t, saved, "base_url"); item.EnvOverride != "" || item.EnvOverrideTier != "" {
		t.Fatalf("DB 接管后不应再标注覆盖, got envOverride=%q tier=%q", item.EnvOverride, item.EnvOverrideTier)
	}
	cfg, err = svc.ChatRuntimeConfig(ctx, loadTestBaseline(t))
	if err != nil {
		t.Fatalf("ChatRuntimeConfig: %v", err)
	}
	if cfg.LLM.Endpoint != dbEndpoint {
		t.Fatalf("保存后应以页面值为准, got %q", cfg.LLM.Endpoint)
	}

	// 清空后重新接受种子值：判定口径是"DB 有没有值"，不是"存没存过记录"。
	if _, err := svc.PatchGroup(ctx, GroupModel, patch(t, `{"base_url":""}`), Actor{UserID: "u-1"}); err != nil {
		t.Fatalf("清空: %v", err)
	}
	cfg, err = svc.ChatRuntimeConfig(ctx, loadTestBaseline(t))
	if err != nil {
		t.Fatalf("ChatRuntimeConfig: %v", err)
	}
	if cfg.LLM.Endpoint != envEndpoint {
		t.Fatalf("DB 清空后种子值应重新生效, got %q", cfg.LLM.Endpoint)
	}
}

// TestSecretOverrideAlwaysPrecedesDB 锁第三态：密钥态 env 恒优先，DB 有值也不让位，
// 页面只知"已配置"、不回显，且覆盖绝不回写 DB。
func TestSecretOverrideAlwaysPrecedesDB(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	svc := New(st, WithCipher(testCipher{}), WithEnvLookup(staticEnv(map[string]string{
		"K8S_AI_LLM_API_KEY": "sk-from-env",
	})))
	if _, err := svc.PatchGroup(ctx, GroupModel, patch(t, `{"base_url":"http://db.example:9000/v1","api_key":"sk-from-db"}`), Actor{UserID: "u-1"}); err != nil {
		t.Fatalf("patch: %v", err)
	}

	cfg, err := svc.LoadRuntimeConfig(ctx, loadTestBaseline(t))
	if err != nil {
		t.Fatalf("LoadRuntimeConfig: %v", err)
	}
	if cfg.LLM.APIKey != "sk-from-env" {
		t.Fatalf("密钥态覆盖必须压过 DB 值, got %q", cfg.LLM.APIKey)
	}

	view, err := svc.GetGroup(ctx, GroupModel)
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	item := itemOf(t, view, "api_key")
	if item.EnvOverride != "K8S_AI_LLM_API_KEY" || item.EnvOverrideTier != string(EnvTierSecret) || !item.HasValue {
		t.Fatalf("敏感键覆盖应标注密钥态来源, got %+v", item)
	}
	if string(item.Value) != "null" {
		t.Fatalf("敏感键永不回明文, got %s", item.Value)
	}

	stored, err := st.Settings().Get(ctx, GroupModel, "api_key")
	if err != nil {
		t.Fatalf("get setting: %v", err)
	}
	plain, err := testCipher{}.Decrypt(stored.Value)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	var storedKey string
	if err := json.Unmarshal(plain, &storedKey); err != nil {
		t.Fatalf("stored api_key 应为 JSON 字符串: %v", err)
	}
	if storedKey != "sk-from-db" {
		t.Fatalf("环境变量覆盖不得写回 DB, got %q", plain)
	}
}

// TestSaveUnderSecretOverrideIsNotSilent 锁验收"保存被覆盖的键不得静默成功"：
// 提示只在 PATCH 返回里出现，且没被覆盖的键不得产生噪音。
func TestSaveUnderSecretOverrideIsNotSilent(t *testing.T) {
	ctx := context.Background()
	svc := New(newTestStore(t), WithCipher(testCipher{}), WithEnvLookup(staticEnv(map[string]string{
		"K8S_AI_LLM_API_KEY": "sk-from-env",
	})))
	view, err := svc.PatchGroup(ctx, GroupModel, patch(t, `{"api_key":"sk-from-page"}`), Actor{UserID: "u-1"})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if len(view.Warnings) != 1 || !strings.Contains(view.Warnings[0], "K8S_AI_LLM_API_KEY") ||
		!strings.Contains(view.Warnings[0], "Secret") {
		t.Fatalf("密钥态保存必须提示运行值仍以 Secret 为准, got %+v", view.Warnings)
	}

	got, err := svc.GetGroup(ctx, GroupModel)
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if len(got.Warnings) != 0 {
		t.Fatalf("读取不应带保存提示, warnings=%+v", got.Warnings)
	}
	other, err := svc.PatchGroup(ctx, GroupModel, patch(t, `{"temperature":0.3}`), Actor{UserID: "u-1"})
	if err != nil {
		t.Fatalf("patch temperature: %v", err)
	}
	if len(other.Warnings) != 0 {
		t.Fatalf("未被覆盖的键不应产生提示, got %+v", other.Warnings)
	}
}

// TestProviderEnvOverrideHasNoConfigCarrier 锁 model.provider：该键没有 config 承载方，
// 只做"已配置"判定与页面标注，不得让运行时加载失败（历史缺陷：无 Source 直接返回错误）。
func TestProviderEnvOverrideHasNoConfigCarrier(t *testing.T) {
	ctx := context.Background()
	svc := New(newTestStore(t), WithEnvLookup(staticEnv(map[string]string{
		"K8S_AI_LLM_PROVIDER": "openai",
		"K8S_AI_LLM_ENDPOINT": "http://gw.internal/v1",
	})))
	if _, err := svc.LoadRuntimeConfig(ctx, loadTestBaseline(t)); err != nil {
		t.Fatalf("无 config 承载方的覆盖不应让运行时加载失败: %v", err)
	}
	view, err := svc.GetGroup(ctx, GroupModel)
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if item := itemOf(t, view, "provider"); item.EnvOverride != "K8S_AI_LLM_PROVIDER" || !item.HasValue {
		t.Fatalf("provider 应标注来源, got %+v", item)
	}
}

// TestDBValuePrecedesBuiltinDefault 锁另一侧优先级：没有 env 时 DB 生效值压过内置默认。
func TestDBValuePrecedesBuiltinDefault(t *testing.T) {
	ctx := context.Background()
	svc := New(newTestStore(t), WithEnvLookup(staticEnv(nil)))
	if _, err := svc.PatchGroup(ctx, GroupModel, patch(t, `{"base_url":"http://db.example:9000/v1","model":"db-model"}`), Actor{UserID: "u-1"}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	cfg, err := svc.ChatRuntimeConfig(ctx, loadTestBaseline(t))
	if err != nil {
		t.Fatalf("ChatRuntimeConfig: %v", err)
	}
	if cfg.LLM.Endpoint != "http://db.example:9000/v1" || cfg.LLM.Model != "db-model" {
		t.Fatalf("配置中心取值应生效, got endpoint=%q model=%q", cfg.LLM.Endpoint, cfg.LLM.Model)
	}
}

// TestChatRuntimeConfigRequiresExplicitModel 锁 P6.1 验收：既无 DB 取值又无 env 覆盖时
// 必须明确报"模型未配置"，不允许静默连内置默认地址。
func TestChatRuntimeConfigRequiresExplicitModel(t *testing.T) {
	ctx := context.Background()
	base := loadTestBaseline(t)

	// 空库 + 无 env：内置默认只是兜底，判定为未配置。
	svc := New(newTestStore(t), WithEnvLookup(staticEnv(nil)))
	if _, err := svc.ChatRuntimeConfig(ctx, base); !errors.Is(err, ErrModelNotConfigured) {
		t.Fatalf("未配置模型时应返回 ErrModelNotConfigured, got %v", err)
	}

	// 只有空白 env 值：仍算未配置。
	blank := New(newTestStore(t), WithEnvLookup(staticEnv(map[string]string{
		"K8S_AI_LLM_MODEL":    "  ",
		"K8S_AI_LLM_ENDPOINT": "",
	})))
	if _, err := blank.ChatRuntimeConfig(ctx, base); !errors.Is(err, ErrModelNotConfigured) {
		t.Fatalf("空白环境变量不应算已配置, got %v", err)
	}

	// 只配 env 端点（模型名留默认）：算已配置，不再拦对话。
	envOnly := New(newTestStore(t), WithEnvLookup(staticEnv(map[string]string{"K8S_AI_LLM_ENDPOINT": "http://gw.internal/v1"})))
	cfg, err := envOnly.ChatRuntimeConfig(ctx, base)
	if err != nil {
		t.Fatalf("env 覆盖端点应算已配置: %v", err)
	}
	if cfg.LLM.Endpoint != "http://gw.internal/v1" {
		t.Fatalf("env 端点未落到运行时配置, got %q", cfg.LLM.Endpoint)
	}
}
