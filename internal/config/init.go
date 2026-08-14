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
  disable_thinking: false   # true 时发送 enable_thinking=false（需网关支持，可显著提速）

scan:
  concurrency: 8
  phase2_concurrency: 4
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
