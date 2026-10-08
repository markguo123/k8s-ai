package config

import (
	"strings"
	"testing"
)

// TestValidateSecretAccess 锁住三期 R4.7 的配置闸门（D-12）：
// agent.secret.access 只有 off 与 create_only 两档；read_write 是保留位，
// 必须被明确拒绝，不能静默当成 create_only 放行「读 Secret 内容」这条红线。
func TestValidateSecretAccess(t *testing.T) {
	base, err := Load(LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("默认配置应通过校验: %v", err)
	}

	for _, access := range []string{"", "off", "create_only"} {
		cfg := *base
		cfg.Agent.Secret.Access = access
		if err := cfg.Validate(); err != nil {
			t.Errorf("access=%q 应通过校验: %v", access, err)
		}
	}

	for _, tc := range []struct{ access, want string }{
		{"read_write", "not supported"},
		{"read-only", "must be one of"},
	} {
		cfg := *base
		cfg.Agent.Secret.Access = tc.access
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("access=%q 应被拒绝并说明原因（含 %q），实际: %v", tc.access, tc.want, err)
		}
	}
}
