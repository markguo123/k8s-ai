package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultTemplate is written by `k8s-ai config init`.
const DefaultTemplate = `# k8s-ai configuration
# 仅承载“这台实例想跑成什么样”的静态启动参数（P6.12）。
#
# 动态业务配置（模型/Skill/审批/记忆/保留/MCP/集群运维）统一在 Web
# 配置中心（settings 表）维护并即时生效；本文件刻意不出现这些动态键，
# 避免“文件 vs 页面”双配置源漂移。k8s-ai chat / k8s-ai scan 等终端命令
# 仍读取本文件与内置默认值，不依赖数据库。
#
# 敏感值约定：admin 口令、LLM API Key、存储 DSN、场景凭据只经环境变量
# / Kubernetes Secret 注入，config.yaml 永不写明文。Web 页面里模型 key、
# kubeconfig、MCP env 属动态敏感配置，落库前由服务端用 K8S_AI_ENC_KEY 加密。

# 平台服务监听（启动参数，修改需重启服务生效）
platform:
  server:
    addr: ":8080"             # 监听地址（e.g. :8080 / 0.0.0.0:8080 / 127.0.0.1:8080）
    tls:
      enabled: false          # true 时启用 HTTPS，需同时配置 cert_file/key_file
      cert_file: ""           # TLS 证书路径（可 ~/ 展开），仅 enabled=true 时要求
      key_file: ""            # TLS 私钥路径（可 ~/ 展开），仅 enabled=true 时要求
  store:
    driver: sqlite            # 本期仅 sqlite；mysql 预留后续阶段
    sqlite_path: "~/.k8s-ai/k8s-ai.db"
    dsn: ""                   # 预留：切换非 sqlite 驱动时的连接串
  auth:
    admin_username: "admin"   # 首次初始化内置管理员用户名
    token_ttl: 24h            # 登录会话 Token 有效期（> 0）
    session_store: memory     # 本期仅内存会话；持久化会话预留后续阶段
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
