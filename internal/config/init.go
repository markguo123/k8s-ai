package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultTemplate is written by `k8s-ai config init`.
const DefaultTemplate = `# k8s-ai configuration
kubernetes:
  # 留空则使用 KUBECONFIG 环境变量或默认 ~/.kube/config
  kubeconfig: ""
  context: ""
  namespace: ""          # 空 = 全集群；非空 = 仅该 namespace（系统组件探测除外）
  timeout: 30s
  qps: 20
  burst: 40

llm:
  enabled: true
  endpoint: "http://localhost:8000/v1"
  api_key: ""
  model: "qwen-plus"      # 建议：巡检诊断用更快的非思考型模型（如 qwen-turbo/flash）；思考型大模型（如 Qwen3.5-397B）较慢，可留二期 chat
  temperature: 0.1
  max_tokens: 4096
  max_input_tokens: 8192
  max_total_tokens: 32768
  max_findings: 30
  timeout: 120s
  stream: true              # Web chat 需要通过 SSE 接收 reasoning_content 并实时展示在“分析过程”面板
  thinking:
    type: enabled           # 标准思考参数：enabled | disabled

scan:
  concurrency: 8
  phase2_concurrency: 4
  thinking:
    type: enabled           # 一期 Scan 思考控制：enabled | disabled
  collect_logs: true
  collect_previous_logs: true
  collect_events: true
  max_log_lines: 500
  max_log_bytes: 65536
  max_log_line_bytes: 1024
  pod_logs_timeout: 30s
  timeout: 5m

report:
  directory: "./reports"
  format: markdown

# 二期 Agent 配置（chat / web 模式）
agent:
  max_iterations: 15          # LLM 往返上限
  max_tool_calls: 20          # 单轮工具调用上限
  timeout: 5m                 # Agent 单次会话超时
  tool_calling: native        # native | shim | auto（auto 先 native，不支持时降级 shim）
  require_approval: true      # 写操作必须人工确认（false 仅用于 --skip-permissions 场景）
  skip_permissions: false     # true 时跳过审批（非交互自动化，慎用）
  result_max_bytes: 8192      # 单个工具结果截断上限
  turn_max_bytes: 32768       # 单轮上下文截断上限
  cmd_max_bytes: 65536        # kubectl 命令输出截断上限
  history_turns: 20           # 保留的历史对话轮数
  session_dir: "~/.k8s-ai/sessions"
  audit_dir: "~/.k8s-ai/audit"
  deny_ops: []                # 硬拒绝动词列表（如 ["delete","edit"]），默认空
  disabled_tools: []          # 禁用的工具名列表

# Web UI 配置（k8s-ai web）
webui:
  addr: "127.0.0.1:8081"      # 监听地址（非回环地址会输出安全警告）

# MCP 预留（二期 v1 不连接外部服务器）
mcp:
  enabled: false
  servers: []

rules:
  enabled: []
  disabled: []
`

// Init writes the default config template to path. force overwrites an
// existing file.
func Init(path string, force bool) (string, error) {
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
	if err := os.WriteFile(p, []byte(DefaultTemplate), 0o600); err != nil {
		return "", fmt.Errorf("write config: %w", err)
	}
	return p, nil
}
