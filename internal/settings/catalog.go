// Package settings 是四期配置中心应用服务（计划 P1.10，需求 v1.2 §15/§23）：
// 把三期平铺的 config.yaml 收敛成"分组 + 键"的 Store 化配置，向 API 提供
// GET/PATCH /settings/{group}。
//
// 安全边界（AGENTS.md + P3.9）：Sensitive 键（API Key、kubeconfig 路径、MCP env）
// 只在写接口接收，读接口一律不回明文，只回"是否已配置"；CONFIG_CHANGE 审计只
// 记录变更了哪些键，不记录值。P3.9 起敏感键在落库前由注入的 SecretCipher 静态
// 加密，密钥只从环境变量读取（K8S_AI_ENC_KEY），永不落库、不再存明文。
package settings

import (
	"fmt"
	"time"
)

// 配置分组（需求 v1.2 §15）。
const (
	GroupModel     = "model"
	GroupSkill     = "skill"
	GroupApproval  = "approval"
	GroupMemory    = "memory"
	GroupRetention = "retention"
	GroupMCP       = "mcp"
	GroupOps       = "ops"
)

// GroupMigration 是 migrate 命令的内部记账分组：不在 Groups() 中暴露，
// 也不允许经 API 读写。
const GroupMigration = "migration"

// Groups 返回对外可见的配置分组（稳定顺序）。
func Groups() []string {
	return []string{GroupModel, GroupSkill, GroupApproval, GroupMemory, GroupRetention, GroupMCP, GroupOps}
}

// Kind 是配置值类型，决定校验、存储编码与 API 回显形态。
type Kind string

const (
	KindString     Kind = "string"
	KindBool       Kind = "bool"
	KindInt        Kind = "int"
	KindFloat      Kind = "float"
	KindDuration   Kind = "duration"
	KindStringList Kind = "stringList"
	KindJSON       Kind = "json"
)

// Definition 描述一个配置键：类型、默认值、敏感标记、取值范围，以及它对应的
// 三期 config.yaml 路径（Source，空表示四期新增、无三期来源）。
type Definition struct {
	Key       string
	Kind      Kind
	Default   any
	Sensitive bool
	Enum      []string
	// Min/Max 仅对数值类型生效（含 duration 的纳秒下界）。
	Min float64
	Max float64
	// Source 是三期 config.yaml 的键路径，如 "llm.api_key"。
	Source string
	// Desc 是给前端配置中心展示的一句话说明。
	Desc string
}

// catalog 是分组 → 键定义的唯一事实来源。默认值与 config.setDefaults 对齐，
// 目的是让 P1.12 的旧配置导入做到"非默认值全部入库、默认值不落库"。
var catalog = map[string][]Definition{
	GroupModel: {
		{Key: "enabled", Kind: KindBool, Default: true, Source: "llm.enabled", Desc: "是否启用模型调用"},
		{Key: "provider", Kind: KindString, Default: "openai-compatible", Desc: "模型提供方类型"},
		{Key: "base_url", Kind: KindString, Default: "http://localhost:8000/v1", Source: "llm.endpoint", Desc: "OpenAI 兼容接口地址"},
		{Key: "api_key", Kind: KindString, Default: "", Sensitive: true, Source: "llm.api_key", Desc: "接口密钥（不回显）"},
		{Key: "model", Kind: KindString, Default: "qwen-plus", Source: "llm.model", Desc: "模型名"},
		{Key: "temperature", Kind: KindFloat, Default: 0.1, Min: 0, Max: 2, Source: "llm.temperature", Desc: "采样温度"},
		{Key: "max_tokens", Kind: KindInt, Default: 4096, Min: 1, Source: "llm.max_tokens", Desc: "单次回复上限"},
		{Key: "max_input_tokens", Kind: KindInt, Default: 8192, Min: 1, Source: "llm.max_input_tokens", Desc: "单条输入上限"},
		{Key: "max_total_tokens", Kind: KindInt, Default: 32768, Min: 1, Source: "llm.max_total_tokens", Desc: "会话累计上限"},
		{Key: "max_findings", Kind: KindInt, Default: 30, Min: 1, Source: "llm.max_findings", Desc: "诊断结论条数上限"},
		{Key: "timeout", Kind: KindDuration, Default: "120s", Source: "llm.timeout", Desc: "模型请求超时"},
		{Key: "stream", Kind: KindBool, Default: true, Source: "llm.stream", Desc: "流式输出"},
		{Key: "thinking", Kind: KindJSON, Default: nil, Source: "llm.thinking", Desc: "思考参数"},
		{Key: "reasoning_fields", Kind: KindStringList, Default: []string{}, Source: "llm.reasoning_fields", Desc: "思考字段名白名单"},
	},
	GroupSkill: {
		{Key: "builtin_enabled", Kind: KindBool, Default: true, Desc: "启用内置 Skill 校验"},
		{Key: "create_enabled", Kind: KindBool, Default: true, Source: "agent.create.enabled", Desc: "启用结构化创建通道"},
		{Key: "create_deny_kinds", Kind: KindStringList, Default: []string{}, Source: "agent.create.deny_kinds", Desc: "禁止创建的Kind"},
		{Key: "create_allow_kinds", Kind: KindStringList, Default: []string{}, Source: "agent.create.allow_kinds", Desc: "允许创建的Kind白名单"},
		{Key: "create_cluster_kinds", Kind: KindStringList, Default: []string{}, Source: "agent.create.cluster_kinds", Desc: "放行的集群级Kind"},
		{Key: "create_max_objects_per_call", Kind: KindInt, Default: 3, Min: 1, Max: 10, Source: "agent.create.max_objects_per_call", Desc: "单次调用对象数上限"},
		{Key: "create_max_manifest_bytes", Kind: KindInt, Default: 8192, Min: 1024, Max: 1048576, Source: "agent.create.max_manifest_bytes", Desc: "单次清单字节上限"},
	},
	GroupApproval: {
		{Key: "approval_mode", Kind: KindString, Default: "strict", Enum: []string{"strict", "smart"}, Source: "agent.approval_mode", Desc: "审批模式"},
		{Key: "require_approval", Kind: KindBool, Default: true, Source: "agent.require_approval", Desc: "写操作需要人工确认"},
		{Key: "skip_permissions", Kind: KindBool, Default: false, Source: "agent.skip_permissions", Desc: "非交互自动化放行写操作"},
		{Key: "deny_ops", Kind: KindStringList, Default: []string{}, Source: "agent.deny_ops", Desc: "禁止的操作名单"},
		{Key: "cmd_max_bytes", Kind: KindInt, Default: 65536, Min: 1, Source: "agent.cmd_max_bytes", Desc: "命令长度上限"},
		{Key: "secret_access", Kind: KindString, Default: "create_only", Enum: []string{"off", "create_only"}, Source: "agent.secret.access", Desc: "Secret 创建通道"},
	},
	GroupMemory: {
		{Key: "enabled", Kind: KindBool, Default: false, Desc: "启用长期记忆（P5.2 消费）"},
		{Key: "scope_user", Kind: KindBool, Default: true, Desc: "允许用户级记忆"},
		{Key: "scope_namespace", Kind: KindBool, Default: true, Desc: "允许命名空间级记忆"},
		{Key: "scope_cluster", Kind: KindBool, Default: true, Desc: "允许集群级记忆"},
		{Key: "max_items", Kind: KindInt, Default: 200, Min: 1, Desc: "单作用域条目上限"},
	},
	GroupRetention: {
		{Key: "enabled", Kind: KindBool, Default: false, Desc: "启用自动清理（P5.4 消费）"},
		{Key: "default_retention_days", Kind: KindInt, Default: 90, Min: 0, Desc: "默认保留天数"},
		{Key: "default_cleanup_mode", Kind: KindString, Default: "delete", Enum: []string{"delete", "archive"}, Desc: "默认清理方式"},
	},
	GroupMCP: {
		{Key: "enabled", Kind: KindBool, Default: false, Source: "mcp.enabled", Desc: "启用 MCP 外部工具"},
		// servers 内含各服务的 env（可能带令牌），整体按敏感字段处理：只写不读。
		{Key: "servers", Kind: KindJSON, Default: []any{}, Sensitive: true, Source: "mcp.servers", Desc: "MCP 服务器列表（不回显）"},
	},
	GroupOps: {
		{Key: "kubeconfig", Kind: KindString, Default: "", Sensitive: true, Source: "kubernetes.kubeconfig", Desc: "kubeconfig 路径（凭据定位信息，不回显）"},
		{Key: "context", Kind: KindString, Default: "", Source: "kubernetes.context", Desc: "默认上下文"},
		{Key: "namespace", Kind: KindString, Default: "", Source: "kubernetes.namespace", Desc: "默认命名空间"},
		{Key: "kubectl_path", Kind: KindString, Default: "", Source: "kubernetes.kubectl_path", Desc: "kubectl 路径"},
		{Key: "kube_timeout", Kind: KindDuration, Default: "30s", Source: "kubernetes.timeout", Desc: "Kubernetes 请求超时"},
		{Key: "qps", Kind: KindFloat, Default: 20, Min: 0.1, Source: "kubernetes.qps", Desc: "API Server QPS"},
		{Key: "burst", Kind: KindInt, Default: 40, Min: 1, Source: "kubernetes.burst", Desc: "API Server 突发上限"},
		{Key: "scan_concurrency", Kind: KindInt, Default: 8, Min: 1, Source: "scan.concurrency", Desc: "扫描并发"},
		{Key: "scan_phase2_concurrency", Kind: KindInt, Default: 4, Min: 1, Source: "scan.phase2_concurrency", Desc: "阶段二并发"},
		{Key: "scan_collect_logs", Kind: KindBool, Default: true, Source: "scan.collect_logs", Desc: "采集容器日志"},
		{Key: "scan_collect_previous_logs", Kind: KindBool, Default: true, Source: "scan.collect_previous_logs", Desc: "采集上一次日志"},
		{Key: "scan_collect_events", Kind: KindBool, Default: true, Source: "scan.collect_events", Desc: "采集事件"},
		{Key: "scan_max_log_lines", Kind: KindInt, Default: 500, Min: 1, Source: "scan.max_log_lines", Desc: "日志行数上限"},
		{Key: "scan_max_log_bytes", Kind: KindInt, Default: 65536, Min: 1, Source: "scan.max_log_bytes", Desc: "日志字节上限"},
		{Key: "scan_max_log_line_bytes", Kind: KindInt, Default: 1024, Min: 1, Source: "scan.max_log_line_bytes", Desc: "单行日志字节上限"},
		{Key: "scan_pod_logs_timeout", Kind: KindDuration, Default: "30s", Source: "scan.pod_logs_timeout", Desc: "日志采集超时"},
		{Key: "scan_timeout", Kind: KindDuration, Default: "5m", Source: "scan.timeout", Desc: "扫描总超时"},
		{Key: "scan_thinking", Kind: KindJSON, Default: nil, Source: "scan.thinking", Desc: "扫描阶段思考参数"},
		{Key: "rules_enabled", Kind: KindStringList, Default: []string{}, Source: "rules.enabled", Desc: "启用规则白名单"},
		{Key: "rules_disabled", Kind: KindStringList, Default: []string{}, Source: "rules.disabled", Desc: "禁用规则名单"},
		{Key: "report_directory", Kind: KindString, Default: "./reports", Source: "report.directory", Desc: "报告目录"},
		{Key: "report_format", Kind: KindString, Default: "markdown", Enum: []string{"markdown", "json", "yaml"}, Source: "report.format", Desc: "报告格式"},
		{Key: "agent_max_iterations", Kind: KindInt, Default: 15, Min: 1, Source: "agent.max_iterations", Desc: "Agent 最大轮次"},
		{Key: "agent_max_tool_calls", Kind: KindInt, Default: 20, Min: 1, Source: "agent.max_tool_calls", Desc: "单轮最大工具调用"},
		{Key: "agent_timeout", Kind: KindDuration, Default: "5m", Source: "agent.timeout", Desc: "单轮超时"},
		{Key: "agent_tool_calling", Kind: KindString, Default: "native", Enum: []string{"native", "shim", "auto"}, Source: "agent.tool_calling", Desc: "工具调用方式"},
		{Key: "agent_result_max_bytes", Kind: KindInt, Default: 8192, Min: 1, Source: "agent.result_max_bytes", Desc: "工具结果字节上限"},
		{Key: "agent_turn_max_bytes", Kind: KindInt, Default: 32768, Min: 1, Source: "agent.turn_max_bytes", Desc: "单轮上下文字节上限"},
		{Key: "agent_history_turns", Kind: KindInt, Default: 20, Min: 1, Source: "agent.history_turns", Desc: "携带历史轮数"},
		{Key: "agent_session_dir", Kind: KindString, Default: "~/.k8s-ai/sessions", Source: "agent.session_dir", Desc: "会话目录（旧数据兼容）"},
		{Key: "agent_audit_dir", Kind: KindString, Default: "~/.k8s-ai/audit", Source: "agent.audit_dir", Desc: "审计文件目录"},
		{Key: "agent_disabled_tools", Kind: KindStringList, Default: []string{}, Source: "agent.disabled_tools", Desc: "停用的工具"},
		{Key: "webui_addr", Kind: KindString, Default: "127.0.0.1:8081", Source: "webui.addr", Desc: "Web 终端监听地址"},
	},
}

// durations 是各分组内合法的 duration 默认值校验（避免拼错默认值直到运行期才暴露）。
func init() {
	for group, defs := range catalog {
		seen := map[string]bool{}
		for _, def := range defs {
			if seen[def.Key] {
				panic("settings: duplicate key " + group + "." + def.Key)
			}
			seen[def.Key] = true
			if def.Kind == KindDuration {
				if s, ok := def.Default.(string); ok {
					if _, err := time.ParseDuration(s); err != nil {
						panic("settings: bad duration default for " + group + "." + def.Key)
					}
				}
			}
		}
	}
}

// groupExists 判断分组是否对外可见。
func groupExists(group string) bool {
	_, ok := catalog[group]
	return ok
}

// lookup 按键名取定义（跳过内部分组）。
func lookup(group, key string) (Definition, error) {
	for _, def := range catalog[group] {
		if def.Key == key {
			return def, nil
		}
	}
	return Definition{}, fmt.Errorf("%w: unknown key %s.%s", ErrInvalid, group, key)
}
