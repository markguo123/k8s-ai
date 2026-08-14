// Package config loads and validates k8s-ai configuration.
// Precedence: CLI overrides > ENV > YAML file > defaults (FR-002).
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mitchellh/mapstructure"
	"github.com/spf13/viper"
)

// Config is the root configuration document.
type Config struct {
	Kubernetes KubernetesConfig `mapstructure:"kubernetes"`
	LLM        LLMConfig        `mapstructure:"llm"`
	Scan       ScanConfig       `mapstructure:"scan"`
	Report     ReportConfig     `mapstructure:"report"`
	Rules      RulesConfig      `mapstructure:"rules"`
}

// KubernetesConfig controls cluster access (FR-003).
type KubernetesConfig struct {
	Kubeconfig string        `mapstructure:"kubeconfig"` // empty = loading rules (KUBECONFIG or ~/.kube/config)
	Context    string        `mapstructure:"context"`
	Namespace  string        `mapstructure:"namespace"`
	Timeout    time.Duration `mapstructure:"timeout"`
	QPS        float32       `mapstructure:"qps"`
	Burst      int           `mapstructure:"burst"`
}

// LLMConfig controls the OpenAI-compatible endpoint (FR-015).
type LLMConfig struct {
	Enabled         bool          `mapstructure:"enabled"`
	Endpoint        string        `mapstructure:"endpoint"`
	APIKey          string        `mapstructure:"api_key"`
	Model           string        `mapstructure:"model"`
	Temperature     float64       `mapstructure:"temperature"`
	MaxTokens       int           `mapstructure:"max_tokens"`
	MaxInputTokens  int           `mapstructure:"max_input_tokens"`
	MaxTotalTokens  int           `mapstructure:"max_total_tokens"`
	MaxFindings     int           `mapstructure:"max_findings"`
	Timeout         time.Duration `mapstructure:"timeout"`
	DisableThinking bool          `mapstructure:"disable_thinking"` // 关闭思考模式（Qwen/vLLM chat_template_kwargs），网关不支持则忽略或报错
}

// ScanConfig controls collection behaviour (FR-004, FR-022, FR-024).
type ScanConfig struct {
	Concurrency         int           `mapstructure:"concurrency"`
	Phase2Concurrency   int           `mapstructure:"phase2_concurrency"`
	CollectLogs         bool          `mapstructure:"collect_logs"`
	CollectPreviousLogs bool          `mapstructure:"collect_previous_logs"`
	CollectEvents       bool          `mapstructure:"collect_events"`
	MaxLogLines         int           `mapstructure:"max_log_lines"`
	MaxLogBytes         int           `mapstructure:"max_log_bytes"`
	MaxLogLineBytes     int           `mapstructure:"max_log_line_bytes"`
	PodLogsTimeout      time.Duration `mapstructure:"pod_logs_timeout"`
	Timeout             time.Duration `mapstructure:"timeout"`
}

// ReportConfig controls report output (FR-019).
type ReportConfig struct {
	Directory string `mapstructure:"directory"`
	Format    string `mapstructure:"format"` // markdown | json | yaml
}

// RulesConfig enables or disables rules by name (FR-013).
type RulesConfig struct {
	Enabled  []string `mapstructure:"enabled"`
	Disabled []string `mapstructure:"disabled"`
}

// LoadOptions controls a single Load call.
type LoadOptions struct {
	ConfigFile string         // explicit --config path
	Overrides  map[string]any // CLI values, highest precedence
	NoEnv      bool           // skip environment merging (test isolation)
}

// DefaultConfigFile returns the default config path (~/.k8s-ai/config.yaml).
func DefaultConfigFile() (string, error) {
	return expandHome("~/.k8s-ai/config.yaml")
}

// Load merges defaults, file, environment and overrides in that order.
func Load(opts LoadOptions) (*Config, error) {
	v := viper.New()
	v.SetConfigType("yaml")
	setDefaults(v)

	if !opts.NoEnv {
		v.SetEnvPrefix("K8S_AI")
		v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
		v.AutomaticEnv()
	}

	path, err := resolveConfigPath(opts.ConfigFile)
	if err != nil {
		return nil, err
	}
	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
	}

	for key, val := range opts.Overrides {
		v.Set(key, val)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg, viper.DecodeHook(mapstructure.StringToTimeDurationHookFunc())); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if err := expandHomePaths(&cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("kubernetes.timeout", 30*time.Second)
	v.SetDefault("kubernetes.qps", float32(20))
	v.SetDefault("kubernetes.burst", 40)

	v.SetDefault("llm.enabled", true)
	v.SetDefault("llm.endpoint", "http://localhost:8000/v1")
	v.SetDefault("llm.api_key", "")
	v.SetDefault("llm.model", "qwen-plus")
	v.SetDefault("llm.temperature", 0.1)
	v.SetDefault("llm.max_tokens", 4096)
	v.SetDefault("llm.max_input_tokens", 8192)
	v.SetDefault("llm.max_total_tokens", 32768)
	v.SetDefault("llm.max_findings", 30)
	v.SetDefault("llm.timeout", 120*time.Second)

	v.SetDefault("scan.concurrency", 8)
	v.SetDefault("scan.phase2_concurrency", 4)
	v.SetDefault("scan.collect_logs", true)
	v.SetDefault("scan.collect_previous_logs", true)
	v.SetDefault("scan.collect_events", true)
	v.SetDefault("scan.max_log_lines", 500)
	v.SetDefault("scan.max_log_bytes", 64*1024)
	v.SetDefault("scan.max_log_line_bytes", 1024)
	v.SetDefault("scan.pod_logs_timeout", 30*time.Second)
	v.SetDefault("scan.timeout", 5*time.Minute)

	v.SetDefault("report.directory", "./reports")
	v.SetDefault("report.format", "markdown")
}

func resolveConfigPath(explicit string) (string, error) {
	if explicit != "" {
		return expandHome(explicit)
	}
	def, err := DefaultConfigFile()
	if err != nil {
		return "", err
	}
	if fileExists(def) {
		return def, nil
	}
	return "", nil
}

func expandHome(path string) (string, error) {
	if path == "" || path == "~" {
		return path, nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func expandHomePaths(cfg *Config) error {
	kc, err := expandHome(cfg.Kubernetes.Kubeconfig)
	if err != nil {
		return err
	}
	cfg.Kubernetes.Kubeconfig = kc
	dir, err := expandHome(cfg.Report.Directory)
	if err != nil {
		return err
	}
	cfg.Report.Directory = dir
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// Validate checks configuration invariants.
func (c *Config) Validate() error {
	if c.Kubernetes.Timeout <= 0 {
		return fmt.Errorf("kubernetes.timeout must be > 0")
	}
	if c.Kubernetes.QPS <= 0 {
		return fmt.Errorf("kubernetes.qps must be > 0")
	}
	if c.Kubernetes.Burst <= 0 {
		return fmt.Errorf("kubernetes.burst must be > 0")
	}
	if c.Scan.Timeout <= 0 {
		return fmt.Errorf("scan.timeout must be > 0")
	}
	if c.Scan.Concurrency <= 0 {
		return fmt.Errorf("scan.concurrency must be > 0")
	}
	if c.Scan.Phase2Concurrency <= 0 {
		return fmt.Errorf("scan.phase2_concurrency must be > 0")
	}
	if c.Scan.MaxLogLines <= 0 {
		return fmt.Errorf("scan.max_log_lines must be > 0")
	}
	if c.Scan.MaxLogBytes <= 0 {
		return fmt.Errorf("scan.max_log_bytes must be > 0")
	}
	if c.Scan.MaxLogLineBytes <= 0 {
		return fmt.Errorf("scan.max_log_line_bytes must be > 0")
	}
	if c.Scan.PodLogsTimeout <= 0 {
		return fmt.Errorf("scan.pod_logs_timeout must be > 0")
	}
	if c.LLM.Enabled {
		u, err := url.Parse(c.LLM.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("llm.endpoint must be a valid http(s) URL")
		}
		if c.LLM.Model == "" {
			return fmt.Errorf("llm.model must not be empty")
		}
		if c.LLM.Temperature < 0 || c.LLM.Temperature > 2 {
			return fmt.Errorf("llm.temperature must be in [0,2]")
		}
		if c.LLM.MaxTokens <= 0 || c.LLM.MaxInputTokens <= 0 || c.LLM.MaxTotalTokens <= 0 {
			return fmt.Errorf("llm token limits must be > 0")
		}
		if c.LLM.MaxFindings <= 0 {
			return fmt.Errorf("llm.max_findings must be > 0")
		}
		if c.LLM.Timeout <= 0 {
			return fmt.Errorf("llm.timeout must be > 0")
		}
	}
	if c.Report.Directory == "" {
		return fmt.Errorf("report.directory must not be empty")
	}
	switch c.Report.Format {
	case "markdown", "json", "yaml":
	default:
		return fmt.Errorf("report.format must be one of markdown, json, yaml")
	}
	return nil
}
