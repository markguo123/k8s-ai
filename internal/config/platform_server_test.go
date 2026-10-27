package config

import "testing"

// TestPlatformStaticEnabledFromEnv 锁 P6.2 形态 B 的部署开关契约：
// deploy/separated/deployment.yaml 用环境变量把内嵌静态托管关掉
// （后端只提供 /api/v1 与探针），因此这个键必须能被环境变量覆盖，否则形态 B
// 会静默地继续跑成形态 A。
func TestPlatformStaticEnabledFromEnv(t *testing.T) {
	t.Run("默认开启形态A", func(t *testing.T) {
		t.Setenv("K8S_AI_PLATFORM_SERVER_STATIC_ENABLED", "")
		cfg, err := Load(LoadOptions{})
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if !cfg.Platform.Server.StaticEnabled {
			t.Fatal("默认应开启内嵌静态托管（形态 A）")
		}
	})

	t.Run("环境变量关闭为纯API", func(t *testing.T) {
		t.Setenv("K8S_AI_PLATFORM_SERVER_STATIC_ENABLED", "false")
		cfg, err := Load(LoadOptions{})
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.Platform.Server.StaticEnabled {
			t.Fatal("K8S_AI_PLATFORM_SERVER_STATIC_ENABLED=false 应关闭静态托管（形态 B）")
		}
	})
}
