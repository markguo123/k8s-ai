package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	cfg, err := Load(LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Kubernetes.Timeout != 30*time.Second {
		t.Errorf("kubernetes.timeout = %v, want 30s", cfg.Kubernetes.Timeout)
	}
	if cfg.Kubernetes.QPS != 20 || cfg.Kubernetes.Burst != 40 {
		t.Errorf("qps/burst = %v/%v, want 20/40", cfg.Kubernetes.QPS, cfg.Kubernetes.Burst)
	}
	if !cfg.LLM.Enabled || cfg.LLM.Model != "qwen-plus" {
		t.Errorf("unexpected llm defaults: %+v", cfg.LLM)
	}
	if cfg.Scan.Concurrency != 8 || cfg.Scan.Timeout != 5*time.Minute {
		t.Errorf("unexpected scan defaults: %+v", cfg.Scan)
	}
	if cfg.Report.Format != "markdown" {
		t.Errorf("report.format = %q, want markdown", cfg.Report.Format)
	}
	if cfg.Agent.MaxIterations != 15 || cfg.Agent.MaxToolCalls != 20 || cfg.Agent.Timeout != 5*time.Minute {
		t.Errorf("unexpected agent defaults: %+v", cfg.Agent)
	}
	if cfg.Agent.ToolCalling != "native" || !cfg.Agent.RequireApproval || cfg.Agent.SkipPermissions {
		t.Errorf("unexpected agent approval defaults: %+v", cfg.Agent)
	}
	if cfg.Agent.ResultMaxBytes != 8*1024 || cfg.Agent.TurnMaxBytes != 32*1024 || cfg.Agent.CmdMaxBytes != 64*1024 {
		t.Errorf("unexpected agent bytes defaults: %+v", cfg.Agent)
	}
	if cfg.WebUI.Addr != "127.0.0.1:8081" {
		t.Errorf("webui.addr = %q", cfg.WebUI.Addr)
	}
	if cfg.MCP.Enabled || len(cfg.MCP.Servers) != 0 {
		t.Errorf("unexpected mcp defaults: %+v", cfg.MCP)
	}
}

func TestPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "kubernetes:\n  qps: 5\n  burst: 10\nllm:\n  model: file-model\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("K8S_AI_LLM_MODEL", "env-model")
	t.Setenv("K8S_AI_KUBERNETES_QPS", "7")

	cfg, err := Load(LoadOptions{
		ConfigFile: path,
		Overrides:  map[string]any{"llm.model": "cli-model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Kubernetes.Burst != 10 {
		t.Errorf("file value burst = %d, want 10", cfg.Kubernetes.Burst)
	}
	if cfg.Kubernetes.QPS != 7 {
		t.Errorf("env value qps = %v, want 7", cfg.Kubernetes.QPS)
	}
	if cfg.LLM.Model != "cli-model" {
		t.Errorf("override value model = %q, want cli-model", cfg.LLM.Model)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{
		NoEnv:     true,
		Overrides: map[string]any{"kubernetes.kubeconfig": "~/kube/config"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "kube", "config")
	if cfg.Kubernetes.Kubeconfig != want {
		t.Errorf("kubeconfig = %q, want %q", cfg.Kubernetes.Kubeconfig, want)
	}
}

func TestAgentDirsExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	wantSessions := filepath.Join(home, ".k8s-ai", "sessions")
	wantAudit := filepath.Join(home, ".k8s-ai", "audit")
	if cfg.Agent.SessionDir != wantSessions {
		t.Errorf("session_dir = %q, want %q", cfg.Agent.SessionDir, wantSessions)
	}
	if cfg.Agent.AuditDir != wantAudit {
		t.Errorf("audit_dir = %q, want %q", cfg.Agent.AuditDir, wantAudit)
	}
}

func TestAgentConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "agent:\n  max_iterations: 5\n  tool_calling: shim\n  require_approval: false\n  deny_ops:\n    - \"delete namespace\"\nwebui:\n  addr: 0.0.0.0:9090\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{ConfigFile: path, NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.MaxIterations != 5 || cfg.Agent.ToolCalling != "shim" || cfg.Agent.RequireApproval {
		t.Errorf("unexpected agent config: %+v", cfg.Agent)
	}
	if len(cfg.Agent.DenyOps) != 1 || cfg.Agent.DenyOps[0] != "delete namespace" {
		t.Errorf("deny_ops = %v", cfg.Agent.DenyOps)
	}
	if cfg.WebUI.Addr != "0.0.0.0:9090" {
		t.Errorf("webui.addr = %q", cfg.WebUI.Addr)
	}
}

func TestAgentValidateErrors(t *testing.T) {
	base, err := Load(LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	bad := []struct {
		name string
		mut  func(*Config)
	}{
		{"max_iterations", func(c *Config) { c.Agent.MaxIterations = 0 }},
		{"max_tool_calls", func(c *Config) { c.Agent.MaxToolCalls = 0 }},
		{"timeout", func(c *Config) { c.Agent.Timeout = 0 }},
		{"tool_calling", func(c *Config) { c.Agent.ToolCalling = "json" }},
		{"result_max_bytes", func(c *Config) { c.Agent.ResultMaxBytes = 0 }},
		{"session_dir", func(c *Config) { c.Agent.SessionDir = "" }},
		{"webui_addr", func(c *Config) { c.WebUI.Addr = "" }},
	}
	for _, b := range bad {
		c := *base
		b.mut(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: 应校验失败", b.name)
		}
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"timeout", func(c *Config) { c.Kubernetes.Timeout = 0 }},
		{"qps", func(c *Config) { c.Kubernetes.QPS = 0 }},
		{"concurrency", func(c *Config) { c.Scan.Concurrency = 0 }},
		{"llm endpoint", func(c *Config) { c.LLM.Endpoint = "not-a-url" }},
		{"format", func(c *Config) { c.Report.Format = "html" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(LoadOptions{NoEnv: true})
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestInit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	written, err := Init(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if written != path {
		t.Fatalf("written = %q, want %q", written, path)
	}
	if !fileExists(path) {
		t.Fatal("config file not created")
	}
	if _, err := Init(path, false); err == nil {
		t.Fatal("second init without force should fail")
	}
	if _, err := Init(path, true); err != nil {
		t.Fatalf("init with force: %v", err)
	}
}
