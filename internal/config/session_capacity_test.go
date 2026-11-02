package config

import (
	"slices"
	"strings"
	"testing"
)

// TestSessionMaxBytesDefault 锁 P8.6 容量闸门的配置面口径：默认 12MiB、属平台静态引擎键
// （不进配置中心动态键）、未配置与显式 0 同义。
func TestSessionMaxBytesDefault(t *testing.T) {
	cfg, err := Load(LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.SessionMaxBytes != DefaultSessionMaxBytes {
		t.Fatalf("默认应为 %d 字节, got %d", DefaultSessionMaxBytes, cfg.Agent.SessionMaxBytes)
	}
	if got := cfg.Agent.EffectiveSessionMaxBytes(); got != DefaultSessionMaxBytes {
		t.Fatalf("EffectiveSessionMaxBytes 默认应取 %d, got %d", DefaultSessionMaxBytes, got)
	}
	// 数字默认值与文案默认值必须同值，否则提示语会指向一个不存在的上限。
	parsed, err := ParseByteSize(defaultSessionMaxBytesText)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != DefaultSessionMaxBytes {
		t.Fatalf("%s 解析为 %d，与 DefaultSessionMaxBytes=%d 不一致", defaultSessionMaxBytesText, parsed, DefaultSessionMaxBytes)
	}
	// 静态引擎键：改它要重启，不允许被在线配置改写。
	if !platformStaticEngineKeys["agent.session_max_bytes"] {
		t.Fatal("agent.session_max_bytes 必须登记为平台静态引擎键")
	}
	if !slices.Contains(KnownKeys(), "agent.session_max_bytes") {
		t.Fatal("agent.session_max_bytes 必须是可识别键")
	}
}

// TestSessionMaxBytesOverrides 锁显式配置与「未配置」的分界：0 回落到默认，负值直接拒。
func TestSessionMaxBytesOverrides(t *testing.T) {
	path := writeTempConfig(t, "agent:\n  session_max_bytes: 3145728\n")
	cfg, err := Load(LoadOptions{ConfigFile: path, NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Agent.EffectiveSessionMaxBytes(); got != 3<<20 {
		t.Fatalf("显式 3MiB 应生效, got %d", got)
	}

	zero := AgentConfig{}
	if got := zero.EffectiveSessionMaxBytes(); got != DefaultSessionMaxBytes {
		t.Fatalf("零值应回落默认, got %d", got)
	}

	base, err := Load(LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	base.Agent.SessionMaxBytes = -1
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "session_max_bytes") {
		t.Fatalf("负上限应被校验拒绝, got %v", err)
	}
	// 脏配置绕过校验直接构造时，闸门也必须回落到有限上限，不能退化成「不限」。
	if (AgentConfig{SessionMaxBytes: -1}).EffectiveSessionMaxBytes() != DefaultSessionMaxBytes {
		t.Fatal("EffectiveSessionMaxBytes 对负值必须回落默认")
	}
}

// TestSessionMemoryWarningPercent 锁预警阈值：必须严格低于拒写线，否则用户永远等不到
// 可读预警、只在写失败那一次才看见错误。
func TestSessionMemoryWarningPercent(t *testing.T) {
	if SessionMemoryWarningPercent <= 0 || SessionMemoryWarningPercent >= 100 {
		t.Fatalf("预警阈值应在 (0,100) 区间, got %d", SessionMemoryWarningPercent)
	}
}
