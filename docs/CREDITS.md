# k8s-ai 借鉴来源记录

> 更新规则：每次直接复用或借鉴第三方代码/设计后，在本文件登记。

## kubectl-ai（Apache-2.0）

来源：<https://github.com/GoogleCloudPlatform/kubectl-ai>（本地参考：`D:\08_code\k8s\kubectl-ai-main`）

许可：Apache License 2.0。直接复用片段保留原文件头部 Copyright + License 声明。

### 直接复用

| 文件 | 说明 | 状态 |
|---|---|---|
| （暂无） | 当前二期代码均为按 k8s-ai 架构重新实现；后续如直接复用 HTML/终端 UI、SSE、会话、审计等文件，在此登记并保留许可头 | — |

### 设计借鉴（重新实现）

| 设计点 | 借鉴来源 | k8s-ai 落地位置 |
|---|---|---|
| 消息模型 / MessageType / AgentState | pkg/agent、pkg/api/models.go | internal/agent/message.go |
| Agent Loop 结构（native + shim 双模式） | pkg/agent/conversation.go | internal/agent/loop.go |
| 会话 Store/Manager 模式（memory/file） | pkg/sessions | internal/session |
| 事件式审计 Recorder | pkg/journal | internal/audit |
| SSE Broadcaster + 会话侧栏 Web chat 交互 | pkg/ui/html | internal/webui |
| 审批交互 y / a / n | pkg/agent 审批流 | internal/agent + internal/webui 审批卡片 |
| kubectl 动词分类思路 | pkg/tools/kubectl_filter.go | internal/approval/risk.go（改为风险分级） |
| kubectl 执行工具（无 shell 改良） | pkg/tools/kubectl_tool.go | internal/executor + internal/tools/write.go |
| 终端 Markdown 渲染 + readline 输入 | pkg/ui/terminal.go（glamour + chzyer/readline） | internal/cli/chat.go |
| 流式命令超时采样（logs -f / get -w） | pkg/tools/streaming.go DetectKubectlStreaming | internal/executor（kubectl.go IsStreaming + RunKubectl 超时） |
| rollout 子命令只读/写细分 + --dry-run 降级只读 | pkg/tools/kubectl_filter.go readOnlyOps/writeOps | internal/approval/risk.go classifyCommand |

## 其他

- 无其他第三方代码复用。
