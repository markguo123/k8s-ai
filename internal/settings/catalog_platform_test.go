package settings

import (
	"strings"
	"testing"
)

// TestCatalogExcludesPlatformKeys 锁存储/启动形态参数的归属边界（P8.1 / 方案 15 §8）：
// platform.* 属静态启动参数，改它必须重启，七组动态键与配置中心页面一个都不能出现，
// 否则「页面改了不生效」与「文件页面双权威」的老问题会重新长回来。
func TestCatalogExcludesPlatformKeys(t *testing.T) {
	for group, defs := range catalog {
		for _, def := range defs {
			if strings.HasPrefix(def.Source, "platform.") {
				t.Errorf("%s.%s 指向静态键 %s（platform.* 不得进动态键目录）", group, def.Key, def.Source)
			}
		}
	}
}

// TestEnvWhitelistExcludesBlobs 锁环境变量覆盖白名单：blob 后端与桶凭据只走部署期
// 环境变量（K8S_AI_PLATFORM_BLOBS_*），不进 settings 的 env 覆盖面（ADR-040 v1.2 冻结
// 为 LLM 四键 + ops 四键）。
func TestEnvWhitelistExcludesBlobs(t *testing.T) {
	for _, override := range EnvOverrideWhitelist {
		if strings.Contains(strings.ToUpper(override.Env), "BLOBS") {
			t.Errorf("env 覆盖白名单混入 blob 键 %s", override.Env)
		}
		if override.Group == "" {
			t.Errorf("白名单项 %s 缺少分组", override.Env)
		}
	}
}
