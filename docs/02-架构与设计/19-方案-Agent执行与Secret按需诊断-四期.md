# Agent 非交互执行与 Secret 按需诊断方案

- 版本：v1.0
- 日期：2026-11-06
- 状态：已落地
- 关联：ADR-048

## 1. 目标

在不放开交互式/阻塞式终端能力的前提下，让 Agent 能够：

1. 经真人审批执行非交互 `kubectl exec`；
2. 在证据表明故障与 Secret 配置有关时，请求读取指定 Secret 的具体 key；
3. 使用真实 Secret 值完成当前轮诊断，同时保证平台审计、会话、数据库、SSE、
   最终回答和后续轮次不保存或回显明文。

## 2. 不可放宽的边界

- `attach`、`port-forward`、`proxy`、`edit`、`run`、`cp`、`debug` 继续硬拒绝。
- `exec -i/-t/--stdin/--tty` 继续硬拒绝。
- 非交互 `exec` 必须使用 `kubectl exec <pod> -n <ns> -- <command>` 形式，
  `--` 后必须有明确命令。
- 非交互 `exec` 与 Secret 读取都必须真人审批，`--skip-permissions` 和
  `require_approval=false` 不得绕过。
- kubectl 直接访问 Secret 与 `--raw` 继续硬拒绝。
- Secret 读取仅允许 `get_secret_value` 结构化工具，单次最多 5 个 key；
  单 key 上限 16 KiB，单次总上限 32 KiB。
- 巡检、Evidence、报告、长期记忆永远不使用 Secret 内容值。

## 3. Secret 访问模式

`agent.secret.access` 支持：

| 模式 | 创建 Secret | 按需读取 |
|---|---:|---:|
| `off` | 否 | 否 |
| `create_only` | 是 | 否 |
| `read_on_demand` | 是 | 是，逐 key + 逐次审批 |

`read_write` 名称过宽，配置校验继续拒绝。默认值为 `read_on_demand`，
但工具每次调用仍必须经过审批卡。

## 4. Secret 诊断流程

```text
Agent 发现认证/配置异常
  -> get_secret_value(namespace, name, keys, reason)
  -> 平台展示审批卡（目标、key、原因、风险）
  -> 用户批准
  -> internal/kubernetes 仅读取指定 key
  -> 当前轮模型收到模型专用数据
  -> UI/SSE/会话/数据库/审计只收到脱敏事实
  -> 最终回答与后续工具调用执行精确值脱敏
```

用户拒绝后，Agent 不得重复同一读取或改用 kubectl Secret 访问。

## 5. 数据分流

`ToolResult` 将模型数据与非模型数据分开：

- `ModelOnly`：只进入当前一轮 LLM 请求；
- `SensitiveValues`：只用于当前轮精确值脱敏，不序列化；
- `Text/Data`：进入 UI、SSE、会话、数据库和审计的安全结果，只含
  `[REDACTED]`、key、长度、编码与 SHA-256。

Agent 在任何 Secret 读取后进入敏感值保护状态：

- 停止输出可能包含值的模型流式分片；
- 最终回答、reasoning、工具参数、工具输出、审批文案、命令和 tool data 均做
  精确值替换；
- 后续调用不允许形成 `yes_and_dont_ask_again` 授权；
- 当前轮结束后清空敏感值集合，下一轮必须重新读取并审批。

## 6. RBAC

默认只读角色已包含 `secrets get`，因为 Secret 元数据采集与按需 key 读取共用同一
ServiceAccount。应用层仍保证巡检只剥取元数据，内容值只能经 `get_secret_value`。

写权限示例新增：

```yaml
- apiGroups: [""]
  resources: ["pods/exec"]
  verbs: ["create"]
```

不授权 `pods/portforward` 与 `pods/attach`。

## 7. 外部模型边界

审批后的 Secret 明文会发送给当前配置的模型提供方，否则模型无法基于真实值诊断。
部署方必须在启用 `read_on_demand` 前确认模型服务的数据处理与留存政策、环境数据
出网要求，以及是否应改用本地模型或私有模型网关。

平台通过“不落盘、不回显、当前轮有效、逐次审批”控制本地副本与界面暴露，不能承诺
模型提供方从未看到该值。

## 8. 验证

- parser：`exec` 必须有 `-- <command>`，全部 stdin/tty 变体被拒；
- RBAC：写角色仅新增 `pods/exec create`，attach/port-forward 不授权；
- Secret：只读取请求 key，超量与超限拒绝；
- 审批：`--skip-permissions` 不能绕过 exec 与 Secret 读取；
- 隔离：审计、会话、SSE、最终回答、后续工具参数与工具输出无明文；
- 模型：当前轮 LLM 请求可见审批后的值。
