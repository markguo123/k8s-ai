package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// TemplateKind 选择 `k8s-ai config init` 输出的模板种类（D-21 / ADR-031 v1.12）。
// server 静态模板与 CLI 直用模板是两份完全独立的文件，互不覆盖。
type TemplateKind string

const (
	// TemplateServer 是平台进程静态模板（config.yaml）：platform: 启动段 + 落盘目录键。
	TemplateServer TemplateKind = "server"
	// TemplateCLI 是终端直用模板（~/.k8s-ai/cli.yaml）：承载全量引擎键与 CLI 缺省。
	TemplateCLI TemplateKind = "cli"
)

// ServerTemplate is written by `k8s-ai config init` (platform static config).
const ServerTemplate = `# k8s-ai configuration
# 仅承载“这台实例想跑成什么样”的静态启动参数（P6.12 / P6.2）。
# 内容分两部分：platform: 平台服务启动段，以及 report / agent 的落盘目录段
# （容器里根文件系统只读，报告和会话/审计必须显式指向可写卷）。deploy/base/configmap.yaml
# 就是本模板在容器部署下的实例，两者形状由测试锁定，不会各写各的。
#
# 动态业务配置（模型/Skill/审批/记忆/保留/MCP/集群运维）统一在 Web
# 配置中心（settings 表）维护并即时生效；本文件刻意不出现这些动态键，
# 避免“文件 vs 页面”双配置源漂移。k8s-ai chat / k8s-ai scan 等终端命令
# 读的是另一份文件 ~/.k8s-ai/cli.yaml（见 k8s-ai config init --cli），
# 与本平台配置文件互不覆盖。
#
# 敏感值约定：admin 口令、LLM API Key、存储 DSN、场景凭据只经环境变量
# / Kubernetes Secret 注入，config.yaml 永不写明文。Web 页面里模型 key、
# kubeconfig、MCP env 属动态敏感配置，落库前由服务端用 K8S_AI_ENC_KEY 加密。

# 平台服务监听（启动参数，修改需重启服务生效）
platform:
  server:
    addr: ":8080"             # 监听地址（e.g. :8080 / 0.0.0.0:8080 / 127.0.0.1:8080）
    static_enabled: true      # 形态 A：内嵌 SPA 静态托管；形态 B（独立前端）置 false
    tls:
      enabled: false          # true 时启用 HTTPS，需同时配置 cert_file/key_file
      cert_file: ""           # TLS 证书路径（可 ~/ 展开），仅 enabled=true 时要求
      key_file: ""            # TLS 私钥路径（可 ~/ 展开），仅 enabled=true 时要求
  store:
    driver: mysql             # 四期主存储 MySQL；sqlite 为本地/单机兜底
    sqlite_path: "~/.k8s-ai/k8s-ai.db"  # driver=sqlite 时的库文件（容器内须挂 PVC）
    dsn: ""                   # 只经 K8S_AI_PLATFORM_STORE_DSN / Secret 注入；YAML 留空
  auth:
    admin_username: "admin"   # 首次初始化内置管理员用户名
    token_ttl: 24h            # 登录会话 Token 有效期（> 0）
    session_store: memory     # 本期固定内存态；扩副本前需先支持持久化会话

# 落盘目录（静态键，非动态配置）：本机运行保持默认；容器部署换成可写卷挂载点。
report:
  directory: "./reports"      # 报告目录（容器内 e.g. /reports）
  format: markdown            # markdown | json | yaml
agent:
  session_dir: "~/.k8s-ai/sessions"  # 会话落盘（容器内 e.g. /data/sessions）
  audit_dir: "~/.k8s-ai/audit"       # 审计落盘（容器内 e.g. /data/audit）
`

// CLITemplate is written by `k8s-ai config init --cli`（终端直用全量引擎键）。
const CLITemplate = `# k8s-ai CLI 直用配置
# 本文件只供终端命令读取：k8s-ai chat / k8s-ai scan 直接消费下列引擎键，
# 不依赖平台数据库。k8s-ai server（企业平台进程）不读取也不回写本文件，
# 平台侧模型/Skill/审批/记忆/保留/MCP/集群运维配置唯一落 DB settings 表，
# 由 Web 配置中心维护；平台静态启动参数在 config.yaml 的 platform: 段（见
# k8s-ai config init）。
#
# 优先级：CLI flag > 环境变量 > 本文件 > 内置默认。
# 敏感值（LLM api_key）建议只经 K8S_AI_LLM_API_KEY 注入，本文件不留明文。

kubernetes:
  kubeconfig: ""              # 留空 = 使用 KUBECONFIG 环境变量或默认 ~/.kube/config
  context: ""                 # kubeconfig 上下文；留空用当前
  namespace: ""               # 留空 = 全集群；非空 = 默认只扫该命名空间
  kubectl_path: ""            # kubectl 二进制路径（留空 = PATH 查找；无 kubectl 只走 client-go）
  timeout: 30s                # 单次 API 请求超时
  qps: 20                     # API Server 访问 QPS 上限
  burst: 40                   # 突发上限

llm:
  enabled: true               # 关闭则只出规则结论
  endpoint: "http://localhost:8000/v1"  # OpenAI Compatible 基础地址（自动拼 /chat/completions）
  api_key: ""                 # 也可用环境变量 K8S_AI_LLM_API_KEY 注入
  model: "qwen-plus"          # 建议：快速非思考型模型；思考型大模型较慢
  temperature: 0.1
  max_tokens: 4096
  max_input_tokens: 8192      # 单 Finding 上下文上限
  max_total_tokens: 32768     # 诊断阶段总预算
  max_findings: 30            # 最多送诊 Finding 数
  timeout: 120s               # 单次 LLM 调用超时
  stream: true                # chat 流式输出（true = SSE 逐 token 推送到终端）
  thinking:
    type: enabled             # 标准思考参数：enabled | disabled
  reasoning_fields: []        # 可选：SSE delta 中的思考字段名

scan:
  concurrency: 8              # Phase1 并发
  phase2_concurrency: 4       # Phase2 日志采集并发
  thinking:
    type: enabled             # 一期 Scan 思考控制：enabled | disabled
  collect_logs: true
  collect_previous_logs: true
  collect_events: true
  max_log_lines: 500          # 每容器日志行数上限
  max_log_bytes: 65536        # 单容器日志字节上限
  max_log_line_bytes: 1024    # 单行截断
  pod_logs_timeout: 30s       # 单 Pod 日志超时
  timeout: 10m                # 整次扫描超时（LLM 场景建议 10m）

report:
  directory: "./reports"      # 报告目录（集群内 CronJob 用 /reports）
  format: markdown            # markdown | json | yaml

agent:
  max_iterations: 15          # Agent 往返上限
  max_tool_calls: 20          # 单轮工具调用上限
  timeout: 5m                 # Agent 单次会话超时
  tool_calling: native        # native | shim | auto（auto 先 native，不支持时降级 shim）
  approval_mode: strict       # strict | smart（smart 需显式开启）
  require_approval: true      # 写操作必须人工确认（false 仅用于 --skip-permissions 场景）
  skip_permissions: false     # true 时跳过审批（非交互自动化，慎用）
  session_dir: "~/.k8s-ai/sessions"
  audit_dir: "~/.k8s-ai/audit"
  create:
    enabled: true             # create_resource 开关
    deny_kinds: []            # 黑名单，默认空
    allow_kinds: []           # 白名单，默认空
    cluster_kinds: []         # cluster-scoped 授权白名单，默认空即禁止集群级资源
    max_objects_per_call: 3   # 单次最多对象数（1-10）
    max_manifest_bytes: 8192  # 单次 manifest 最大字节数（1KiB-1MiB）
  secret:
    access: create_only       # off | create_only（read_write 未开放：读取 Secret 内容仍是红线）

rules:
  enabled: []                 # 非空则只启用这些规则
  disabled: []                # 禁用指定规则
`

// DefaultTemplate 保留为 server 静态模板的别名（三期调用方兼容）。
const DefaultTemplate = ServerTemplate

// TemplateFor 返回 kind 对应的模板内容。
func TemplateFor(kind TemplateKind) (string, error) {
	switch kind {
	case "", TemplateServer:
		return ServerTemplate, nil
	case TemplateCLI:
		return CLITemplate, nil
	default:
		return "", fmt.Errorf("unknown config template %q (server|cli)", string(kind))
	}
}

// Init writes the template for kind to path. force overwrites an existing
// file.
func Init(path string, kind TemplateKind, force bool) (string, error) {
	tmpl, err := TemplateFor(kind)
	if err != nil {
		return "", err
	}
	p, err := expandHome(path)
	if err != nil {
		return "", err
	}
	if fileExists(p) && !force {
		return "", fmt.Errorf("config file already exists: %s (use --force to overwrite)", p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(p, []byte(tmpl), 0o600); err != nil {
		return "", fmt.Errorf("write config: %w", err)
	}
	return p, nil
}
