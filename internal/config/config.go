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
	Agent      AgentConfig      `mapstructure:"agent"`    // 二期 Agent
	WebUI      WebUIConfig      `mapstructure:"webui"`    // 二期 Web UI
	MCP        MCPConfig        `mapstructure:"mcp"`      // 二期 MCP 预留
	Platform   PlatformConfig   `mapstructure:"platform"` // 四期平台静态启动基线（P6.12）
}

// KubernetesConfig controls cluster access (FR-003).
type KubernetesConfig struct {
	Kubeconfig  string        `mapstructure:"kubeconfig"` // empty = loading rules (KUBECONFIG or ~/.kube/config)
	Context     string        `mapstructure:"context"`
	Namespace   string        `mapstructure:"namespace"`
	KubectlPath string        `mapstructure:"kubectl_path"` // 二期：kubectl 二进制路径（空 = PATH 查找）
	Timeout     time.Duration `mapstructure:"timeout"`
	QPS         float32       `mapstructure:"qps"`
	Burst       int           `mapstructure:"burst"`
}

// LLMConfig controls the OpenAI-compatible endpoint (FR-015).
type LLMConfig struct {
	Enabled         bool              `mapstructure:"enabled"`
	Endpoint        string            `mapstructure:"endpoint"`
	APIKey          string            `mapstructure:"api_key"`
	Model           string            `mapstructure:"model"`
	Temperature     float64           `mapstructure:"temperature"`
	MaxTokens       int               `mapstructure:"max_tokens"`
	MaxInputTokens  int               `mapstructure:"max_input_tokens"`
	MaxTotalTokens  int               `mapstructure:"max_total_tokens"`
	MaxFindings     int               `mapstructure:"max_findings"`
	Timeout         time.Duration     `mapstructure:"timeout"`
	Stream          bool              `mapstructure:"stream"`           // chat 流式输出（true = SSE 逐 token 推送到终端/Web）
	Thinking        map[string]string `mapstructure:"thinking"`         // 标准思考参数，例如 {type: enabled}
	ReasoningFields []string          `mapstructure:"reasoning_fields"` // 可选：SSE delta 中的思考字段名
}

// ScanConfig controls collection behaviour (FR-004, FR-022, FR-024).
type ScanConfig struct {
	Concurrency       int               `mapstructure:"concurrency"`
	Phase2Concurrency int               `mapstructure:"phase2_concurrency"`
	Thinking          map[string]string `mapstructure:"thinking"` // 一期 Scan 的思考参数，例如 {type: enabled}

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

// AgentConfig 控制二期 Agent（需求规格 §24）。
type AgentConfig struct {
	MaxIterations   int           `mapstructure:"max_iterations"`
	MaxToolCalls    int           `mapstructure:"max_tool_calls"`
	Timeout         time.Duration `mapstructure:"timeout"`
	ToolCalling     string        `mapstructure:"tool_calling"`  // native | shim | auto
	ApprovalMode    string        `mapstructure:"approval_mode"` // strict | smart（Phase C 才启用 smart）
	RequireApproval bool          `mapstructure:"require_approval"`
	SkipPermissions bool          `mapstructure:"skip_permissions"`
	DenyOps         []string      `mapstructure:"deny_ops"`
	ResultMaxBytes  int           `mapstructure:"result_max_bytes"`
	TurnMaxBytes    int           `mapstructure:"turn_max_bytes"`
	CmdMaxBytes     int           `mapstructure:"cmd_max_bytes"`
	HistoryTurns    int           `mapstructure:"history_turns"`
	SessionDir      string        `mapstructure:"session_dir"`
	AuditDir        string        `mapstructure:"audit_dir"`
	// AuditFileCopy keeps the per-session audit JSONL file in addition to the
	// platform audit table (P8.0). The platform DB is the source of truth; the
	// file is an optional copy. Default true keeps existing deployments intact,
	// and CLI mode (no platform store) always writes it.
	AuditFileCopy bool         `mapstructure:"audit_file_copy"`
	DisabledTools []string     `mapstructure:"disabled_tools"`
	Create        CreateConfig `mapstructure:"create"`
	Secret        SecretConfig `mapstructure:"secret"`
}

// CreateConfig controls the Phase 3 resource-creation capability
// (docs/RESOURCE_CREATE_DESIGN.md, docs/DEVELOPMENT_PLAN_三期.md R0.2).
type CreateConfig struct {
	// Enabled turns the create_resource tool on/off. Disabled tools are removed
	// from the LLM tool registry at assembly time.
	Enabled bool `mapstructure:"enabled"`
	// DenyKinds is a hard blacklist of kinds that can never be created
	// (D-11: the only kind-level restriction; empty = no extra blacklist).
	DenyKinds []string `mapstructure:"deny_kinds"`
	// AllowKinds optionally narrows creation to a whitelist. Empty means
	// "no whitelist": every kind not in DenyKinds is accepted.
	AllowKinds []string `mapstructure:"allow_kinds"`
	// ClusterKinds lists cluster-scoped kinds the operator authorizes for
	// creation. Empty means cluster-scoped creation is refused (D-11 gate).
	ClusterKinds []string `mapstructure:"cluster_kinds"`
	// MaxObjectsPerCall caps how many objects a single call may carry (D-10).
	MaxObjectsPerCall int `mapstructure:"max_objects_per_call"`
	// MaxManifestBytes caps the manifest size of a single call (D-10).
	MaxManifestBytes int `mapstructure:"max_manifest_bytes"`
}

// SecretConfig controls the Secret create channel (D-06/D-12). Reading Secret
// content values stays a hard red line in every mode.
type SecretConfig struct {
	// Access is "off" (no Secret creation), "create_only" (default: create with
	// values that never enter the LLM context) or "read_write" (reserved,
	// rejected by Validate until a later phase).
	Access string `mapstructure:"access"`
}

// WebUIConfig 控制二期 Web UI 监听地址。
type WebUIConfig struct {
	Addr string `mapstructure:"addr"`
}

// MCPConfig 是 MCP 客户端预留配置（v1 只解析，不连接）。
type MCPConfig struct {
	Enabled bool              `mapstructure:"enabled"`
	Servers []MCPServerConfig `mapstructure:"servers"`
}

// MCPServerConfig 描述一个 MCP 服务器（stdio 或 http）。
type MCPServerConfig struct {
	Name    string            `mapstructure:"name"`
	Command string            `mapstructure:"command"`
	Args    []string          `mapstructure:"args"`
	Env     map[string]string `mapstructure:"env"`
	URL     string            `mapstructure:"url"`
}

// PlatformConfig 是四期平台运行时的静态启动基线（P6.12）。
// 口径：动态业务配置（model/skill/approval/memory/retention/mcp/ops）的
// 唯一事实来源是 DB settings 表；本段只承载"这台实例想跑成什么样"的启动参数
// （监听地址/存储/管理员账号），修改需改配置文件并重启服务生效。
type PlatformConfig struct {
	Server PlatformServerConfig `mapstructure:"server"`
	Store  PlatformStoreConfig  `mapstructure:"store"`
	// Blobs 是二进制对象后端（附件/报告产物/可选会话正文外置，P8.1 / D-32）。
	Blobs PlatformBlobsConfig `mapstructure:"blobs"`
	Auth  PlatformAuthConfig  `mapstructure:"auth"`
	Exec  PlatformExecConfig  `mapstructure:"exec"`
}

// PlatformExecConfig 控制命令执行通道（P6.3 / D-33 / ADR-044 v1.1）。
// 它是静态启动参数：改它要改配置文件并重启，不进配置中心页面（7 组动态键不含它）。
type PlatformExecConfig struct {
	// ShellEnabled 决定含 shell 构造（管道、串联、重定向、命令替换、heredoc）的
	// kubectl 命令能否进入执行。默认 true，与本地 CLI 的执行能力对齐：安全不靠
	// "镜像里没有 shell"，而靠 shell 命令必须经真人审批、且不被 --skip-permissions
	// 免除（AGENTS.md）。
	// 部署在无 shell 镜像变体（Dockerfile.distroless）时置 false：executor 在构造
	// exec.Cmd **之前**拒绝并返回可指导文案，argv 直调与 client-go 写通道不受影响，
	// 也不会再把 "bash: executable file not found" 这类环境错误抛给模型。
	ShellEnabled bool `mapstructure:"shell_enabled"`
}

// PlatformServerConfig 控制平台 HTTP 服务监听参数。
type PlatformServerConfig struct {
	Addr string            `mapstructure:"addr"`
	TLS  PlatformTLSConfig `mapstructure:"tls"`
	// StaticEnabled 控制是否由本进程托管内嵌前端产物（P6.1/P6.11，D-26）：
	// true = 形态 A 一体化（单端口同时提供 SPA 与 API）；false = 形态 B 纯 API
	// （静态资源交给独立前端镜像/Nginx）。
	StaticEnabled bool `mapstructure:"static_enabled"`
}

// PlatformTLSConfig 控制平台服务 HTTPS 监听。
type PlatformTLSConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	CertFile string `mapstructure:"cert_file"`
	KeyFile  string `mapstructure:"key_file"`
}

// PlatformStoreConfig 控制平台持久化存储（P6.1 / D-31）。
// 主路径是 mysql（DSN 经 Secret 注入）；sqlite 作为单机/离线兜底，
// 兜底时 sqlite_path 必须落在持久卷上，禁止使用容器可写层。
type PlatformStoreConfig struct {
	Driver     string `mapstructure:"driver"`
	SQLitePath string `mapstructure:"sqlite_path"`
	// DSN 是 mysql 驱动的 go-sql-driver 连接串，必须带 parseTime=true
	// （TIMESTAMP 需要扫描为 time.Time）。口令等敏感值只经环境变量/Secret 注入，
	// 不写入配置文件与日志。
	DSN string `mapstructure:"dsn"`
}

// 存储驱动取值（platform.store.driver）。
const (
	StoreDriverSQLite = "sqlite"
	StoreDriverMySQL  = "mysql"
)

// IsMySQL 报告当前配置是否使用 MySQL 驱动（空值按兜底驱动 sqlite 处理）。
func (p PlatformStoreConfig) IsMySQL() bool {
	return strings.EqualFold(strings.TrimSpace(p.Driver), StoreDriverMySQL)
}

// PlatformAuthConfig 控制平台内建账号与登录会话参数。
type PlatformAuthConfig struct {
	AdminUsername string        `mapstructure:"admin_username"`
	TokenTTL      time.Duration `mapstructure:"token_ttl"`
	SessionStore  string        `mapstructure:"session_store"` // memory（本期）
}

// LoadOptions controls a single Load call.
type LoadOptions struct {
	ConfigFile string         // explicit --config path
	Overrides  map[string]any // CLI values, highest precedence
	NoEnv      bool           // skip environment merging (test isolation)
	CLI        bool           // 终端直用面（chat/scan）：默认读 cli.yaml，缺失回退 config.yaml
}

// DefaultConfigFile returns the default config path (~/.k8s-ai/config.yaml).
func DefaultConfigFile() (string, error) {
	return expandHome("~/.k8s-ai/config.yaml")
}

// DefaultCLIConfigFile 返回终端直用配置的默认路径（~/.k8s-ai/cli.yaml，D-21）。
// 它与平台静态 config.yaml 是两份独立文件：由 `config init --cli` 生成，只有
// k8s-ai chat / scan 读取，server 既不读取也不回写。
func DefaultCLIConfigFile() (string, error) {
	return expandHome("~/.k8s-ai/cli.yaml")
}

// ResolveConfigFile 解析实际生效的配置文件路径（显式 --config 优先）。终端直用面
// 先取 cli.yaml，缺失时回退旧 config.yaml 以兼容存量部署；都没有时返回空串，
// 表示只用默认值与环境变量。
func ResolveConfigFile(explicit string, cli bool) (string, error) {
	if explicit != "" {
		return expandHome(explicit)
	}
	if cli {
		cliPath, err := DefaultCLIConfigFile()
		if err != nil {
			return "", err
		}
		if fileExists(cliPath) {
			return cliPath, nil
		}
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

	path, err := ResolveConfigFile(opts.ConfigFile, opts.CLI)
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
	v.SetDefault("llm.stream", true)

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

	v.SetDefault("agent.max_iterations", 15)
	v.SetDefault("agent.max_tool_calls", 20)
	v.SetDefault("agent.timeout", 5*time.Minute)
	v.SetDefault("agent.tool_calling", "native")
	v.SetDefault("agent.approval_mode", "strict")
	v.SetDefault("agent.require_approval", true)
	v.SetDefault("agent.skip_permissions", false)
	v.SetDefault("agent.result_max_bytes", 8*1024)
	v.SetDefault("agent.turn_max_bytes", 32*1024)
	v.SetDefault("agent.cmd_max_bytes", 64*1024)
	v.SetDefault("agent.history_turns", 20)
	v.SetDefault("agent.session_dir", "~/.k8s-ai/sessions")
	v.SetDefault("agent.audit_dir", "~/.k8s-ai/audit")
	v.SetDefault("agent.audit_file_copy", true)

	// Phase 3 resource creation (RESOURCE_CREATE_DESIGN.md / DEVELOPMENT_PLAN_三期.md).
	v.SetDefault("agent.create.enabled", true)
	v.SetDefault("agent.create.deny_kinds", []string{})
	v.SetDefault("agent.create.allow_kinds", []string{})
	v.SetDefault("agent.create.cluster_kinds", []string{})
	v.SetDefault("agent.create.max_objects_per_call", 3)
	v.SetDefault("agent.create.max_manifest_bytes", 8*1024)
	v.SetDefault("agent.secret.access", "create_only")

	v.SetDefault("webui.addr", "127.0.0.1:8081")

	v.SetDefault("mcp.enabled", false)

	// 四期平台静态启动基线（P6.12）。
	v.SetDefault("platform.server.addr", ":8080")
	v.SetDefault("platform.server.static_enabled", true)
	v.SetDefault("platform.server.tls.enabled", false)
	v.SetDefault("platform.server.tls.cert_file", "")
	v.SetDefault("platform.server.tls.key_file", "")
	v.SetDefault("platform.store.driver", StoreDriverSQLite)
	v.SetDefault("platform.store.sqlite_path", "~/.k8s-ai/k8s-ai.db")
	v.SetDefault("platform.store.dsn", "")
	// 二进制对象后端（P8.1 / D-32）：默认 db，未配置的环境升级即无感。每个键都必须
	// 有默认值，viper 才能在 Unmarshal 时合并只来自环境变量（Secret）注入的取值。
	v.SetDefault("platform.blobs.driver", BlobDriverDB)
	v.SetDefault("platform.blobs.prefix", "k8sai")
	v.SetDefault("platform.blobs.dir", "")
	// tmp_dir 留空时由 objectstore 取 os.TempDir()（方案 15 §14）：装配层决定回落值，
	// 配置面不猜，避免 validate 在有无临时卷的不同环境下结论不一致。
	v.SetDefault("platform.blobs.tmp_dir", "")
	v.SetDefault("platform.blobs.endpoint", "")
	v.SetDefault("platform.blobs.region", "us-east-1")
	v.SetDefault("platform.blobs.use_tls", false)
	v.SetDefault("platform.blobs.path_style", true)
	v.SetDefault("platform.blobs.bucket", "k8s-ai")
	v.SetDefault("platform.blobs.access_key", "")
	v.SetDefault("platform.blobs.secret_key", "")
	v.SetDefault("platform.blobs.create_bucket", true)
	v.SetDefault("platform.blobs.presign_ttl", 15*time.Minute)
	v.SetDefault("platform.blobs.max_object_size", "32MiB")
	v.SetDefault("platform.blobs.session_data", false)
	v.SetDefault("platform.blobs.gc_grace_period", 168*time.Hour)
	v.SetDefault("platform.auth.admin_username", "admin")
	v.SetDefault("platform.auth.token_ttl", 24*time.Hour)
	v.SetDefault("platform.auth.session_store", "memory")
	// 执行通道默认开启（D-33）：关闭是给无 shell 镜像变体的现场用的。
	v.SetDefault("platform.exec.shell_enabled", true)
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
	sessDir, err := expandHome(cfg.Agent.SessionDir)
	if err != nil {
		return err
	}
	cfg.Agent.SessionDir = sessDir
	auditDir, err := expandHome(cfg.Agent.AuditDir)
	if err != nil {
		return err
	}
	cfg.Agent.AuditDir = auditDir
	sqlitePath, err := expandHome(cfg.Platform.Store.SQLitePath)
	if err != nil {
		return err
	}
	cfg.Platform.Store.SQLitePath = sqlitePath
	blobsDir, err := expandHome(cfg.Platform.Blobs.Dir)
	if err != nil {
		return err
	}
	cfg.Platform.Blobs.Dir = blobsDir
	certFile, err := expandHome(cfg.Platform.Server.TLS.CertFile)
	if err != nil {
		return err
	}
	cfg.Platform.Server.TLS.CertFile = certFile
	keyFile, err := expandHome(cfg.Platform.Server.TLS.KeyFile)
	if err != nil {
		return err
	}
	cfg.Platform.Server.TLS.KeyFile = keyFile
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
	if c.Agent.MaxIterations <= 0 {
		return fmt.Errorf("agent.max_iterations must be > 0")
	}
	if c.Agent.MaxToolCalls <= 0 {
		return fmt.Errorf("agent.max_tool_calls must be > 0")
	}
	if c.Agent.Timeout <= 0 {
		return fmt.Errorf("agent.timeout must be > 0")
	}
	switch c.Agent.ToolCalling {
	case "native", "shim", "auto":
	default:
		return fmt.Errorf("agent.tool_calling must be one of native, shim, auto")
	}
	switch c.Agent.ApprovalMode {
	case "strict", "smart":
	default:
		return fmt.Errorf("agent.approval_mode must be one of strict, smart")
	}
	if c.Agent.ResultMaxBytes <= 0 || c.Agent.TurnMaxBytes <= 0 || c.Agent.CmdMaxBytes <= 0 {
		return fmt.Errorf("agent result/turn/cmd max bytes must be > 0")
	}
	if c.Agent.HistoryTurns <= 0 {
		return fmt.Errorf("agent.history_turns must be > 0")
	}
	if c.Agent.SessionDir == "" || c.Agent.AuditDir == "" {
		return fmt.Errorf("agent.session_dir and agent.audit_dir must not be empty")
	}
	// Phase 3: creation limits (D-10 object/byte caps are configurable but bounded).
	if c.Agent.Create.MaxObjectsPerCall <= 0 || c.Agent.Create.MaxObjectsPerCall > 10 {
		return fmt.Errorf("agent.create.max_objects_per_call must be between 1 and 10")
	}
	if c.Agent.Create.MaxManifestBytes < 1024 || c.Agent.Create.MaxManifestBytes > 1024*1024 {
		return fmt.Errorf("agent.create.max_manifest_bytes must be between 1KiB and 1MiB")
	}
	if err := validateSecretAccess(c.Agent.Secret.Access); err != nil {
		return err
	}
	if c.WebUI.Addr == "" {
		return fmt.Errorf("webui.addr must not be empty")
	}
	if err := c.Platform.Validate(); err != nil {
		return err
	}
	return nil
}

// Validate 校验平台静态启动基线（P6.12/P6.1）：监听地址、TLS 成对、
// 存储驱动与连接参数、管理员账号、会话 Token 时长与存储。
func (p PlatformConfig) Validate() error {
	if strings.TrimSpace(p.Server.Addr) == "" {
		return fmt.Errorf("platform.server.addr must not be empty")
	}
	if p.Server.TLS.Enabled {
		if strings.TrimSpace(p.Server.TLS.CertFile) == "" || strings.TrimSpace(p.Server.TLS.KeyFile) == "" {
			return fmt.Errorf("platform.server.tls requires both cert_file and key_file")
		}
	}
	switch p.Store.Driver {
	case "", StoreDriverSQLite:
		if strings.TrimSpace(p.Store.SQLitePath) == "" {
			return fmt.Errorf("platform.store.sqlite_path must not be empty")
		}
	case StoreDriverMySQL:
		if err := validateMySQLDSN(p.Store.DSN); err != nil {
			return err
		}
	default:
		return fmt.Errorf("platform.store.driver must be %s or %s", StoreDriverSQLite, StoreDriverMySQL)
	}
	if strings.TrimSpace(p.Auth.AdminUsername) == "" {
		return fmt.Errorf("platform.auth.admin_username must not be empty")
	}
	if p.Auth.TokenTTL <= 0 {
		return fmt.Errorf("platform.auth.token_ttl must be > 0")
	}
	switch p.Auth.SessionStore {
	case "", "memory":
	default:
		return fmt.Errorf("platform.auth.session_store must be memory (persistent store not supported this phase)")
	}
	// blob 后端放在最后校验：存储驱动与密钥的错误更靠近「服务起不来」的根因，
	// 先报它们能让现场少绕一圈。
	if err := p.Blobs.Validate(); err != nil {
		return err
	}
	return nil
}

// validateMySQLDSN 校验 MySQL 连接串：非空且参数段带 parseTime=true。
// go-sql-driver 的 DSN 不是 URL，不能整体交给 url.Parse；这里只按驱动
// 约定切出 "/" 之后的参数段做键值校验，避免把口令当路径误解析。
func validateMySQLDSN(dsn string) error {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return fmt.Errorf("platform.store.dsn must not be empty when platform.store.driver is %s", StoreDriverMySQL)
	}
	idx := strings.LastIndex(dsn, "/")
	if idx < 0 {
		return fmt.Errorf("platform.store.dsn must include a database name (user:pass@tcp(host:port)/dbname)")
	}
	dbName, params, hasParams := strings.Cut(dsn[idx+1:], "?")
	if strings.TrimSpace(dbName) == "" {
		return fmt.Errorf("platform.store.dsn must include a database name (user:pass@tcp(host:port)/dbname)")
	}
	if !hasParams {
		return fmt.Errorf("platform.store.dsn must set parseTime=true (got no query parameters)")
	}
	for _, pair := range strings.Split(params, "&") {
		k, v, _ := strings.Cut(pair, "=")
		if !strings.EqualFold(strings.TrimSpace(k), "parseTime") {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1":
			return nil
		}
	}
	return fmt.Errorf("platform.store.dsn must set parseTime=true")
}

// validateSecretAccess enforces the Phase 3 Secret channel gate (D-12/R4.7):
// only "off" and "create_only" are available; reading Secret content values
// ("read_write") stays out of scope and is rejected instead of silently ignored.
func validateSecretAccess(access string) error {
	switch access {
	case "", "off", "create_only":
		return nil
	case "read_write":
		return fmt.Errorf("agent.secret.access=read_write is not supported: reading Secret content values stays a red line")
	default:
		return fmt.Errorf("agent.secret.access must be one of off, create_only")
	}
}
