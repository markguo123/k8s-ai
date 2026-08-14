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
