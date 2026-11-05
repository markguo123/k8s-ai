package config

import (
	"strings"
	"testing"
)

// TestPlatformShellEnabledDefault 锁 D-33（ADR-044 v1.1）默认口径：shell 执行通道
// 默认开启。翻这条测试必须先改方案口径，不能悄悄改成"默认关闭"再让现场踩坑。
func TestPlatformShellEnabledDefault(t *testing.T) {
	t.Setenv("K8S_AI_PLATFORM_EXEC_SHELL_ENABLED", "")
	cfg, err := Load(LoadOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Platform.Exec.ShellEnabled {
		t.Fatal("platform.exec.shell_enabled 默认应为 true（无 shell 变体才显式关闭）")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("默认配置应通过校验: %v", err)
	}
}

// TestPlatformShellEnabledFromEnv 锁部署侧关闭通道的方式：无 shell 镜像变体
// （Dockerfile.distroless）通过环境变量/ConfigMap 关掉，不需要改代码。
func TestPlatformShellEnabledFromEnv(t *testing.T) {
	t.Setenv("K8S_AI_PLATFORM_EXEC_SHELL_ENABLED", "false")
	cfg, err := Load(LoadOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Platform.Exec.ShellEnabled {
		t.Fatal("K8S_AI_PLATFORM_EXEC_SHELL_ENABLED=false 应关闭 shell 通道")
	}
}

// TestPlatformShellEnabledRejectsGarbageValue 锁"配错不静默"：写 `maybe` 这类
// 取值必须报错，不能悄悄按 false（关掉排障能力）或 true（无 shell 镜像里空跑）处理。
func TestPlatformShellEnabledRejectsGarbageValue(t *testing.T) {
	t.Setenv("K8S_AI_PLATFORM_EXEC_SHELL_ENABLED", "maybe")
	cfg, err := Load(LoadOptions{})
	if err == nil {
		t.Fatalf("非法取值应报错，实际加载成功且 shell_enabled=%v", cfg.Platform.Exec.ShellEnabled)
	}
	if !strings.Contains(err.Error(), "shell_enabled") {
		t.Fatalf("报错应点出出问题的键，实际: %v", err)
	}
}
