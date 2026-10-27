package settings

import (
	"context"
	"errors"
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

func TestActiveEnvOverrides(t *testing.T) {
	svc := New(newTestStore(t), WithEnvLookup(staticEnv(map[string]string{
		"K8S_AI_LLM_MODEL":    "env-model",
		"K8S_AI_NAMESPACE":    "prod",
		"K8S_AI_KUBE_CONTEXT": "   ",
	})))
	active := svc.ActiveEnvOverrides()
	if active[envKey(GroupModel, "model")] != "K8S_AI_LLM_MODEL" {
		t.Fatalf("模型名覆盖未生效: %+v", active)
	}
	if active[envKey(GroupOps, "namespace")] != "K8S_AI_NAMESPACE" {
		t.Fatalf("命名空间覆盖未生效: %+v", active)
	}
	if _, ok := active[envKey(GroupOps, "context")]; ok {
		t.Fatal("空白环境变量应视为未设置")
	}
	if len(active) != 2 {
		t.Fatalf("只应命中白名单内非空变量, got %+v", active)
	}
}

// TestEnvOverridePrecedesDB 锁 D-29 优先级：env > DB > 内置默认，且覆盖不回写 DB。
func TestEnvOverridePrecedesDB(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	svc := New(st, WithEnvLookup(staticEnv(map[string]string{
		"K8S_AI_LLM_ENDPOINT": "http://env.example:8000/v1",
	})))
	if _, err := svc.PatchGroup(ctx, GroupModel, patch(t, `{"base_url":"http://db.example:9000/v1"}`), Actor{UserID: "u-1"}); err != nil {
		t.Fatalf("patch: %v", err)
	}

	cfg, err := svc.LoadRuntimeConfig(ctx, loadTestBaseline(t))
	if err != nil {
		t.Fatalf("LoadRuntimeConfig: %v", err)
	}
	if cfg.LLM.Endpoint != "http://env.example:8000/v1" {
		t.Fatalf("环境变量应压过配置中心取值, got %q", cfg.LLM.Endpoint)
	}

	// 页面读取：标注覆盖来源，但 Value 仍是 DB 值（不回显部署方配置）。
	view, err := svc.GetGroup(ctx, GroupModel)
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	item := itemOf(t, view, "base_url")
	if item.EnvOverride != "K8S_AI_LLM_ENDPOINT" || !item.HasValue {
		t.Fatalf("应标注环境变量覆盖来源, got envOverride=%q hasValue=%v", item.EnvOverride, item.HasValue)
	}
	if string(item.Value) != `"http://db.example:9000/v1"` {
		t.Fatalf("覆盖态下 Value 仍应是 DB 值, got %s", item.Value)
	}

	// 覆盖绝不回写 DB：Store 里仍是页面写入的值。
	saved, err := st.Settings().Get(ctx, GroupModel, "base_url")
	if err != nil {
		t.Fatalf("get setting: %v", err)
	}
	if saved == nil || saved.Value != `"http://db.example:9000/v1"` {
		t.Fatalf("环境变量覆盖不得写回 DB, got %+v", saved)
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

// TestSensitiveEnvOverrideNotEchoed 锁敏感键：env 覆盖 api_key 时页面只知"已配置"，不回值。
func TestSensitiveEnvOverrideNotEchoed(t *testing.T) {
	svc := New(newTestStore(t), WithEnvLookup(staticEnv(map[string]string{"K8S_AI_LLM_API_KEY": "sk-from-env"})))
	view, err := svc.GetGroup(context.Background(), GroupModel)
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	item := itemOf(t, view, "api_key")
	if item.EnvOverride != "K8S_AI_LLM_API_KEY" || !item.HasValue {
		t.Fatalf("敏感键覆盖应标注来源并视为已配置, got %+v", item)
	}
	if string(item.Value) != "null" {
		t.Fatalf("敏感键永不回明文, got %s", item.Value)
	}
}
