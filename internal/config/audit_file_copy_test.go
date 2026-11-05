package config

import "testing"

// TestAuditFileCopyDefaults 锁住 P8.0 的审计副本默认口径：默认 true（既有部署行为
// 不变，现场排障仍能在容器卷上找到会话 jsonl），关掉后审计事实源只剩平台库。
func TestAuditFileCopyDefaults(t *testing.T) {
	cfg, err := Load(LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Agent.AuditFileCopy {
		t.Fatal("agent.audit_file_copy 默认必须为 true")
	}
	// 平台静态引擎键：平台模式下只能通过重启生效，不得被在线配置改写。
	if !platformStaticEngineKeys["agent.audit_file_copy"] {
		t.Fatal("agent.audit_file_copy 必须登记为平台静态引擎键")
	}
}
