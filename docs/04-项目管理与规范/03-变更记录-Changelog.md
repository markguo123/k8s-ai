# Changelog

本项目遵循 docs/VERSIONING.md 的里程碑阶梯约定。

## [v3.0.0] - 2026-10-08

三期交付封版（v3.0.0，2026-10-08）：资源创建能力完成（R0–R8）：自然语言创建资源进入统一 `create_resource` 通道，支持 dry-run 提案、人工审批、Server-Side Apply、创建后 verify，并为四期 Skill 引擎冻结数据模型与读接口。

### Added

- **`create_resource` Tool**：`internal/tools/create.go` 提供 `manifest` / `mode` / `verify` / `timeout_seconds` 参数；dry-run 产出会话级提案，apply 需通过 `proposal_id` 或同指纹 manifest。
- **Executor 创建能力**：`internal/executor/apply.go` 基于 client-go dynamic + Server-Side Apply（fieldManager=`k8s-ai`），支持 server-side dry-run、存在性检查、创建后 verify；写入路径仍只在 Executor。
- **创建安全门禁**：`internal/approval/codeguard.go` / `create_risk.go` 提供 kind 黑白名单、cluster 授权闸门、命名空间存在性、未 dry-run 拒绝、未知 Kind/危险字段风险升档。
- **Secret 创建通道**：`agent.secret.access=create_only`；只允许 `stringData` 使用 `$GENERATE` / `$ASK`，明文值走审批带外传入，审计只记录键名/长度/SHA-256/来源。
- **内置 Skill 元数据**：`internal/skills/` 冻结 Skill/Rule/RiskClause/RiskRule/Check/Source/Registry 契约，go:embed 内置 workload/config/storage/network/rbac 五域 YAML，Skill 模型无执行动作字段。
- **Agent 创建编排**：`internal/agent/prompt.go` 新增创建/部署/新建意图路由与 `createProtocol` 约束，必须信息收集后 dry-run → 提案 → 用户确认 → apply。
- **CLI/Web 渲染**：CLI 与 Web 展示创建提案卡、五步执行状态流、创建结果与 Secret 脱敏清单；提案/manifest/指纹/verify 落库并支持刷新恢复。
- **集成测试**：`internal/creationtest/` 覆盖 dry_run → 审批 → SSA 创建 → verify、Secret `$ASK` 带外取值与审计无明文、绕过 dry-run 拒绝等场景。

### Changed

- `k8s-ai-test.yaml`、`docs/INSTALLATION.md` 增加 `agent.create.*` 与 `agent.secret.access` 配置样例。
- `docs/E2E_SMOKE.md` 增加资源创建、Secret 审计与失败路径冒烟场景。
- `docs/RESOURCE_CREATE_DESIGN.md` 实现状态标注为三期已落地。
- `internal/webui/server.go` 新增 `POST /api/sessions/{id}/cancel` 取消当前 Chat 运行（按 session 记录 `context.CancelFunc`，删除会话时同步取消）；发送区新增停止按钮，运行状态改由停止按钮标识，移除原旋转 spinner；新增 `TestCancelActiveRun` 覆盖取消接口与无活动运行冲突提示。

### Tests

- `go test ./...` 通过（含新增 `internal/creationtest`）
- `go vet ./...` 通过
- `tests/arch` 通过

## [v2.6.0] - 2026-09-20

Web 工具卡片结构化渲染与会话持久化补全（Data.kind 驱动工具结果表格/键值展示；kubectl 工具卡仅展示原始命令；arguments/data/ok 落库并随 SSE 回传）。

### Added

- **工具结果结构化渲染**：`internal/webui/index.html` 新增 `renderToolStructured`，由 `Data.kind` 驱动；List 类型（PodList/EventList/NodeUsageList 等）用表头映射 + 表格渲染，单一对象（如 Pod）用键值表 + containers/conditions/events 子表展示。
- **kubectl 命令特例**：kubectl 工具卡片只展示原始命令文本，避免把 kubectl 原命令再包装成 `[kubectl] JSON`；结构化工具显示 `[工具名] 参数`。
- **会话持久化补全**：`internal/session/session.go` Message 增加 `arguments`/`data`/`ok`；`internal/agent/loop.go` 落库并输出 `summary`/`data`/`ok`；刷新后命令、参数与结果表格不丢失。

### Changed

- **SSE 工具结果字段**：`internal/service/chat.go` 的 `ChatToolCallView` 增加 `risk`/`summary`/`ok`/`data`，`ok` 作为工具成功与否的权威标记。

### Tests

- `go test ./...` 通过
- `go vet ./...` 通过

## [v2.5.1] - 2026-09-20

scan 终端报告渲染分层优化（`internal/report/summary.go` 的 `RenderTerminal`）。

### Changed

- **终端摘要分层**：`RenderTerminal` 输出从单一巨大无序列表改为块级标题 + 表格 + 代码块 + 修复建议列表，scan 终端一屏摘要更易扫读。

### Tests

- `go test ./internal/report/` 通过

## [v2.5.0] - 2026-09-18

P7 扫描能力补全与规则池扩展（借鉴 k8sgpt 规则池思路：A/B/C 12 条规则 + 规则元数据体系 + DocProvider + 报告聚合）。

### Added

- **规则元数据体系**：`RuleMeta` 与 `Registry.RegisterWithMeta`，现有全部规则补齐元数据（rule_id / category / kind / severity_base / needs_logs / doc_ref），支持按 category / kind 过滤（SPEC §28.1）
- **A 级规则（4 条）**：`CronJobSuspended` 挂起、`NetworkPolicyNoDefaultDeny` 缺默认拒绝、`IngressHostConflict` 主机冲突、`DeploymentOverReplicas` 超副本
- **B 级规则（3 条）**：`VolumeAttachmentStuck` 卷挂载卡住、`StorageClassNoProvisioner` 无 Provisioner、`ContainerHealthTimeout` 容器健康超时
- **C 级采集与规则（5 条）**：新增 `ListHPA`（autoscaling/v2）/ `ListPDB`（policy/v1）/ `ListClusterRoles`（rbac/v1）Reader；`HpaReplicasMismatch` / `HpaScaleTargetMissing` / `PdbBlocking` / `RbacOverPrivileged` / `ConfigRefNotFound`
- **DocProvider**：按需 lazy 加载 + 内存缓存 + 静态 JSON 兜底，`--with-doc` 开关可用且默认不增加 API 请求
- **报告聚合**：按 category / kind 聚合，suggestion 可单独过滤，server / web / CLI 复用同一模型

### Notes

- 规则结论与健康评分口径不降级；新增 `ListSecretMeta`（internal/kubernetes 只读门面）后，`ConfigRefNotFound` 可校验 Secret/ConfigMap 引用存在性（仅元数据/键级，内容值零进入）；安全红线收紧为“Secret 内容值永不进入 evidence / LLM / 会话 / 审计”

### Tests

- `go test ./...` 通过
- `go vet ./...` 通过

## [v2.4.0] - 2026-09-17

Web 纯前端产品化升级（暗色主题 + 顶部状态条 + 横向阶段步进器 + 过程时间线 + 诊断报告视觉升级，承接 v2.3.2「后续版本继续优化视觉」）。

### Added

- **暗色主题**：Web UI 统一暗色配色（`color-scheme: dark`），整体更接近桌面产品的视觉观感
- **顶部状态条**：会话顶部新增运行状态与耗时计数（`sessionElapsed`），会话进度一目了然
- **横向阶段步进器**：每轮对话顶部新增 Reasoning → Tools → Result 横向步进器，随阶段推进高亮/标记完成，最终报告输出后全部置为已完成
- **报告视觉升级**：诊断报告卡片增加“诊断报告”标识（CSS `::before` Pill），主次结构更清晰

### Changed

- **思考框全程动态展开**：推理期间“分析过程”面板全程展开并实时滚动展示模型隐藏思考，直到最终报告打印完成后再自动折叠，避免中途频繁折叠打断阅读
- **工具卡自动 peek 优化**：移除工具结果固定的 1.4s 延时 peek，工具卡拿到结果立即展开并保持；最终报告输出完成后统一折叠非错误工具卡（错误卡保持展开便于排查）
- **思考展示边界说明**：带工具调用轮次的可见正文只保留简短进度说明，完整推理由系统从隐藏 reasoning 通道单独采集并交由 Web 分析面板展示，不再依赖模型写入可见输出
- **阶段标签**：Web 时间线每条记录保留明确的阶段标签（思考 / 工具执行 / 工具执行中 / 中间分析 / 诊断结论），配合色点与高亮描边更醒目地呈现当前进度

### Tests

- `go test ./...` 通过
- `go vet ./...` 通过

## [v2.3.2] - 2026-09-16

标准思考参数与思考过程展示修复。

### Changed

- **OpenAI 标准思考参数**：移除 `llm.disable_thinking`，请求体改为标准 `thinking` 字段，例如 `{type: enabled}`；`llm.thinking` 控制 chat，`scan.thinking` 控制一期 Scan 诊断
- **流式思考字段兼容**：流式解析支持 `reasoning_content` / `reasoning` / `thinking`，可通过 `llm.reasoning_fields` 调整识别顺序
- **Web 思考展示**：新增 `reasoning-delta` 事件，Web 端在“分析过程”面板实时展示思考文本，并增加状态、滚动、等宽字体与流式光标反馈
- **Web 流程时间线**：同一轮对话中的“思考 / 工具执行 / 最终结论”纳入左侧竖向时间线，增强过程可观测性（后续版本继续优化视觉）
- **终端思考展示**：终端逐 token 输出隐藏式思考会干扰阅读，改为一次性显示“分析中...”状态，最终答案仍按 Markdown 渲染

### Fixed

- 修复 `scan.thinking` 配置字段缺失导致的编译失败
- 修复 `config init` 模板中文注释乱码
- 修复思考消息持久化位置，确保插入到当前轮次最终回答之前

### Tests

- `go test ./...` 通过
- `go vet ./...` 通过

## [v2.3.1] - 2026-09-14

Web UI 会话体验优化。

### Changed

- **会话记忆**：使用 sessionStorage 记住上次活跃会话，刷新页面不再跳回第一个会话
- **欢迎页标语**：从"从现象走向结论"改为"开口说话"，更简洁有力
- **首屏体验**：`showWelcome()` 提前到 `loadSessions()` 之前执行，避免页面加载时的空白闪烁

## [v2.3.0] - 2026-09-11

Web UI 运维诊断流程可视化重构。

### Changed

- **Web UI 设计系统重构**：`internal/webui/index.html` 全面重构，围绕"运维诊断流程可视化"设计：
  - **字体与排版**：引入 Inter + JetBrains Mono 字体组合，诊断报告字号 15.5px、行高 1.8，提升可读性
  - **诊断报告卡片**：最终 answer 以 `.msg.report` 样式呈现（左侧强调条 + 阴影 + 更大内边距），与中间分析过程视觉分离
  - **分析过程时间线**：`.phase-progress` 面板增加步骤计数、完成勾选标记（✓）、当前步骤高亮动画（●）
  - **工具卡片摘要栏**：新增 `.tool-summary-bar` 显示"工具调用 N · X 已完成 · Y 执行中"，多工具场景一目了然
  - **等待状态细分**：typing indicator 区分"AI 正在分析…"与"等待审批…"（后者橙色高亮），审批卡片按钮文案中文化
  - **错误卡片可折叠**：长错误信息截断显示 + "查看详情"折叠面板
  - **中文本地化**：展开按钮"Show output"→"展开"/"收起"，who 标签"You"→"你"、"AI Assistant"→"AI 助手"
  - **响应式适配**：960px 断点 + 移动端侧栏抽屉（汉堡菜单 + 遮罩），工具命令窄屏自动换行
  - **Reduced Motion**：`prefers-reduced-motion: reduce` 时禁用所有动画

### Fixed

- **流式增量串扰 bug**：`appendDelta()` 现在只找 `data-streaming="true"` 的消息，避免追加到已降级的进度消息
- **工具结果错配**：`fillToolOutput()` 按 `toolId` 匹配，同名工具多卡片时不再填错
- **工具卡片构建统一**：`buildToolCard()` 工厂函数统一实时与历史卡片，历史卡片补齐复制按钮

### Tests

- 现有全部测试通过（28 包），前端为纯 HTML/JS 无独立测试。

## [v2.2.1] - 2026-09-10

诊断报告完整性与流式对话稳定性修复：保留真实诊断内容，避免工具调用阶段和证据不足时过早收敛到修复方案。

### Added

- **语义化 LLM 流事件**：区分 `content-delta` 与 `reasoning-delta`，保留真实 reasoning 增量并避免用固定占位文本替代。
- **阶段化 Chat 事件**：新增 `progress` 与 `answer-delta`，配合 `tool-call`、`tool-response`、`answer` 区分进度、工具活动和最终报告。
- **Web 分析过程折叠**：分析摘要进入可展开/收起面板，工具执行或最终报告出现后自动收起。

### Changed

- **工具轮次输出闸门**：带 `tool_calls` 的 LLM 轮次先缓存正文；短说明转为 `progress`，完整报告型正文不提前展示；工具结果之后才允许最终回答增量进入 Web/CLI。
- **诊断报告协议**：最终回答明确按现象、根因、关键证据、修复方案、验证结果组织，避免排查请求直接收敛为方案列表。
- **证据不足安全边界**：证据不足时优先继续只读取证；完成必要排查仍无法确认时，明确报告证据缺口，不提前生成写入命令。
- **流式工具参数归一化**：兼容部分 OpenAI 兼容网关对 `function.arguments` 的二次编码和尾部引号噪声；无法安全恢复时保留参数错误，不盲目执行。
- **失败工具调用收敛**：同一只读工具与同一参数执行失败后不再原样执行，要求模型更换参数或工具，避免反复失败耗尽工具预算。
- **工具前文本过滤**：移除对“根因/修复/建议/结论”等关键词的过度过滤，避免误删真实诊断摘要。
- **终端渲染**：CLI 将回答增量缓存至 `answer`，统一使用 Markdown 渲染器输出，保持与 kubectl-ai 类似的终端报告效果。

### Fixed

- 修复流式聊天中完整报告可能早于工具调用展示的问题。
- 修复最终回答通过增量与完整 `answer` 双重展示的问题。
- 修复工具失败后模型重复调用同一工具、最终超过 `max_tool_calls`/`max_iterations` 的问题。
- 修复流式 reasoning 内容被固定为“正在分析证据”的问题。

## [v2.2.0] - 2026-09-09

LLM 响应流式输出（chat 流式逐 token 推送 + SSE 降级回退 + Web UI 增量渲染）。

### Added

- **LLM 流式输出支持**：新增 `llm.StreamClient` 接口（`ChatStream`），`internal/llm/client.go` 实现 OpenAI Compatible SSE 流式协议，`internal/llm/stream.go` 解析流式 chunk（content/tool_calls/usage 累积 + content 增量回调）。
- **`ChatEventDelta` 事件类型**：`internal/service/chat.go` 新增 `ChatEventDelta`，Agent 消息总线中的 `SourceModel` 增量实时转为 `delta` 事件，与 `ChatEventText`（用户输入回显）和 `ChatEventAnswer`（最终回答）区分。
- **Web UI 增量渲染**：`internal/webui/index.html` 新增 `case "delta"` 处理分支 + `appendDelta()` 函数，流式增量实时追加到最后一条 agent 消息的 content 中，保持 Markdown 实时渲染。
- **配置项 `llm.stream`**：`internal/config/config.go` 新增 `Stream bool` 字段（默认 `false`），控制 chat 流式开关。

### Changed

- **Agent 循环 `chatOnce` 方法**（`internal/agent/loop.go`）：`native` 模式且 LLM 客户端支持 `StreamClient` 时走 `ChatStream`，content 增量实时 Emit 到消息总线；`shim` 模式保持非流式（避免中间 JSON 泄漏）。
- **流式失败自动降级**：`ChatStream` 失败且未发出任何增量 → 退回到非流式 `Chat`（复用其 429/5xx 重试），`streamed` 标记据实返回，审计日志准确记录实际路径。

### Fixed

- **`chatOnce` 回退路径 `streamed` 标志**：流式失败降级到非流式 `Chat` 后不再误报 `stream: true`。

### Borrowed from kubectl-ai

- **Web UI delta 增量渲染**：`appendDelta()` 函数设计借鉴 kubectl-ai 的流式消息追加模式（data-raw 属性累积纯文本 → 全量重渲染 → 保持 Markdown 一致性）。

### Tests

- 现有全部测试通过（28 包），`fakeLLM` 未实现 `StreamClient` → `chatOnce` 在测试中自动走非流式，无需修改测试固件。

## [v2.1.0] - 2026-09-08

修复高危正确性与安全边界，补齐配置项生效，并同步提示词。

### Fixed

- **提示词移除已删除的结构化写工具**：`prompt.go` 不再引导 LLM 调用 `patch_deployment / scale_deployment / delete_pod / patch_configmap / restart_rollout / label`，写操作统一指向 kubectl 工具。
- **kubectl 动词解析支持全局 flag 独立值**：`kubectl -n default get pods`、`kubectl --context prod get pods` 等命令不再把 flag 值误判为 verb；风险分级与只读判断同步修正。
- **shell 模式交互式命令硬拒绝**：`kubectl edit ... && echo`、`kubectl exec -it ... && echo` 等不再因动词位于命令开头而绕过交互命令拦截。
- **Secret / --raw 红线加固**：`IsForbiddenCommand` 能识别 `sec"ret"`、`$(echo secret)`、`'--raw'` 等引用/替换混淆。
- **delete 风险分级补 Service**：`kubectl delete service ...` 现在归类为 CRITICAL，与 AGENTS.md 红线一致。
- **配置项生效**：`agent.skip_permissions` 现在会跳过写操作人工确认；`agent.disabled_tools` 会在 Chat 装配工具时移除指定工具。

### Borrowed from kubectl-ai

- **命令安全分类改用 shell 解析器**：`IsForbiddenCommand` 增加 `mvdan.cc/sh/v3/syntax` 解析路径，先按 AST 识别每个 kubectl 调用中的 Secret/`--raw`，解析失败再回退到轻量词法扫描；比纯字符串匹配更能覆盖引号拼接与命令替换。
- **kubectl 子进程注入 KUBECONFIG**：executor 把 `kubernetes.kubeconfig` 通过 `KUBECONFIG` 环境变量传给 kubectl，避免写命令与 client-go 使用不同集群配置。

### Tests

- 新增解析层、审批层、registry、service 层测试，覆盖全局 flag 前置、shell 交互命令拦截、Secret 混淆、disabled_tools 与 skip_permissions。

## [v2.0.9] - 2026-09-08

删除 6 个结构化写工具，写操作统一走 kubectlTool + 执行器 bug 修复 + Web UI 第一批交互优化。

### Added

- **Web UI 第一批交互优化（index.html）**：
  - 输入框 disabled + spinner：AI 处理中输入框变灰、placeholder 切为"AI 正在处理..."、发送按钮 disabled、右侧旋转 spinner；
  - 空会话欢迎页：渐变 logo + 产品名 + 3 张可点击能力卡片（巡检集群/故障诊断/执行变更），点击自动填充提问；
  - 删除会话确认：删除前 `confirm()` 防误删；
  - 会话列表时间戳：session item 下方显示"刚刚 / X 分钟前 / X 小时前 / X 天前"；
  - AI 思考 typing indicator：三点跳动动画 + "AI 正在分析..."，执行完成自动隐藏。
- **kubectlTool.Execute 补 Text 字段**：之前不填 Text → `MarshalForLLM()` 只能 `json.Marshal(Data)` → 用户看到带引号的 JSON 字符串；现在 `extractKubectlSummary()` 成功取 Stdout 前 200 字符，失败取 exit+stderr，空输出显示 "ok"。

### Changed

- **写工具从 7 个精简为 1 个**：删除 `scale_deployment` / `patch_deployment` / `patch_configmap` / `restart_rollout` / `label` / `delete_pod` 共 6 个结构化写工具（~250 行 Go 代码），全部走 `kubectlTool` → `executor.RunKubectl` 一条路径。WriteRunner 接口和 executor.Runner 的 typed client 实现保留，留作未来内部场景。
- **NewWriteTools 只注册 kubectlTool**：之前 7 个 → 现在 1 个。LLM 写操作统一生成完整 kubectl 命令（含 shell 构造），执行器自动判断 argv 直调或 `bash -c`。

### Why

6 个结构化工具是 v2.0.8 放开 shell 之前的过渡产物（shell 被 parse.go 硬拒时，LLM 只能用结构化工具调 typed client）。v2.0.8 放开 shell + 新增批量审批后，kubectlTool 已能覆盖全部写操作，结构化工具成为多余维护负担：

- 覆盖不全（写不完 patch_pvc / patch_ingress / annotate / cordon 等无限场景）；
- LLM 在 JSON patch 参数上反复试错（日志出现 `--access-mode` 单数、`apiVersion: v11` 笔误）；
- prompt 说"结构化优先"但 LLM 发现覆盖不了又退回 kubectl，体验不统一。

删除后写操作**一条清晰路径**：LLM 生成 kubectl 命令 → `RunKubectl` → 风险分级 → 审批 → 执行。

## [v2.0.8] - 2026-09-07

chat 修复体验对齐 kubectl-ai：放开 shell 构造 + 批量审批 + 拒绝闸门归一化（优化 1/2/3/4/6 全部落地）。

### Added

- **放开 shell 构造（AGENTS.md + executor + approval）**：
  - AGENTS.md 修改 Kubernetes Safety 条款：shell 构造（pipe/heredoc/redirect/`&&`/`||`/command substitution）允许在 chat 模式使用，但含写操作的 shell 命令必须走人工审批，shell 命令永不自动执行（不被 `--skip-permissions` 豁免）；
  - `parse.go` 引号外 shell 元字符不再拒绝，改为标记 `isShell=true` 透传 executor；shell 模式跳过 `kubectl` 前缀检查 + 全命令扫描交互式动词；非 shell 模式保持原校验不变；
  - `kubectl.go` `isShell=true` 时走 `exec.CommandContext(ctx, "bash", "-c", command)`，非 shell 走 argv 直调；两条路径共享 deny_ops + Secret 拦截 + 脱敏 + 截断 + 流式超时；
  - `approval/risk.go` 新增 `HasShellSyntax()`：`IsReadOnlyCommand()` 对含 shell 语法的命令永远返回 `false`（必须审批）。
- **批量审批（loop.go `batchApprove`）**：native 模式下 LLM 一轮返回 2+ 写工具调用时，收集所有写操作 → 合并为一个审批面板（逐条展示命令/变更/风险等级）→ 用户选一次 → 顺序执行 → 分别回传结果。借鉴 kubectl-ai `conversation.go L757-794`。审批次数从 9 次降到 3-4 次。
- **拒绝闸门命令归一化（loop.go `declineKey` + `normalizeKubectlCommand`）**：kubectl 工具的拒绝计数 key 做命令归一化（提取 verb+resource+name+namespace，忽略 `--force`/`--grace-period`/`--now`/`--wait`/`--timeout`/`--cascade`/`--dry-run` 等修饰性 flag 和 flag 顺序），防止 LLM 用"加个 --force"绕过拒绝闸门。结构化工具不归一化（原始 JSON 作为 key）。
- **Prompt 决策树扩展工具选择引导（prompt.go）**：交叉规则改为"结构化写工具优先，kubectl+shell 作为 fallback"；新增"shell 命令永不自动执行"规则；healthCheckProtocol 去掉 shell 禁止语。
- **kubectlTool Description 更新（write.go）**：声明支持 shell 构造 + 写命令走审批 + 结构化工具优先 + 复杂场景可直接用 kubectl+shell。

### Changed

- `executeTool` 签名新增 `batchApproved bool` 参数（批量审批预授权时跳过个体审批）。
- 架构守护测试 `readonly_test.go` 反转：`!inExecutor` 时禁止 shell（executor 内允许 policy-gated shell 路径）；`relPkg` 改用绝对路径修复 WalkDir 场景。

### Tests

- `parse_test.go`：shell 命令检测 isShell=true（7 类 shell 构造）+ 非 shell 不误判（4 类普通命令）。
- `kubectl_test.go`：交互式命令仍拒绝 + `HasShellSyntax` 正负例。
- `approval_test.go`：shell 命令永不归类为只读（7 类 shell + 引号内不误判）。
- `loop_test.go`：批量审批（Yes/No/Mixed 3 组）+ 命令归一化（6 类 kubectl 变体 + 结构化工具不归一化）。

## [v2.0.7] - 2026-09-03

chat 全场景架构优化 + 规则补齐与误报治理 + 安全与诊断解析鲁棒性闭环（一次把近期 5 项优化集中收口：规则覆盖到 23 条、安全洞堵上、双重 LLM 砍掉、工具选择分层、confidence/risk 解析归一化）。

### Added

- **规则引擎补 4 条**，规则总数达到 **23 条**：
  - `EvictedPod`（Pod 因节点压力被驱逐，Medium；根因指向节点，避免误报普通 Failed）；
  - `HighRestartCount`（容器仍在 Running 但 restartCount≥5 隐性崩溃抖动，Low；取日志）；
  - `DaemonSetReplica`（补齐 Workload 三件套对称性，High，desired>ready<desired 即报）；
  - `DeploymentProgressDeadline`（发布超时时 ProgressDeadlineExceeded 单独报 Medium，比副本不足更精确）。
- **模型层：chat 按意图分层的工具选择决策树**（internal/agent/prompt.go `toolSelectionProtocol`）：
  - 第一层 诊断意图 → 首选 `kubectl get <资源> -o yaml`（完整视图不漏字段）+ previous logs + events；
  - 第二层 浏览意图 → 结构化 get\_\* 摘要工具（省 token）；
  - 第三层 命名空间级批量健康 → `scan_cluster namespace=<ns>`（23 规则秒级确定性覆盖）；
  - 第四层 集群全景 → 健康检查协议 + scan_cluster() 全量；
  - 硬性交叉规则：结构化工具=摘要视图（不够时必须升级 YAML）、写操作永不走 kubectl 工具、CRD 走 YAML 兜底。
- **诊断输出解析鲁棒性**（internal/diagnosis/schema.go）：
  - `normalizeConfidenceLevel`：兼容 `HIGH_CONF0.9 / HIGH_CONF / Confidence High / POSSIBLE / uncertain` 等 10+ 类常见变体；空值或无法识别时按 confidence 数值分档兜底（≥0.85 CONFIRMED / ≥0.6 HIGH_CONFIDENCE / ≥0.3 POSSIBLE / <0.3 UNKNOWN），消除因 LLM 枚举轻微偏差走降级；
  - `normalizeRisk`：兼容 `SAFE / HIGH_RISK / SEVERE / FATAL` 别名，未知值默认 MEDIUM（保守方向）。
- **安全红线：kubectl 链路拒读 Secret + 输出统一脱敏**（internal/approval/policy.go `IsForbiddenCommand` + internal/executor/kubectl.go）：
  - 任何形式的 `get/describe/delete secret(s)` 以及 `secret/name`（大小写不敏感、`--raw / --raw=`）无论审批与否一律拒绝；
  - kubectl stdout/stderr 全路径（含流式超时路径）统一走 `security.Redactor`，明文 env、token、密码等替换为 `[REDACTED]`。
- **砍 diagnose\_\* 工具内部双重 LLM**（internal/tools/diagnosis.go `LLM.Enabled=false`）：diagnose_pod / diagnose_node / diagnose_workload 退化为**证据采集器**（秒级），分析由 Agent LLM 一次完成，典型 chat 诊断从 40s+ 降到秒级。
- **模型 & 采集补齐**：PodInfo 补 `Reason`（EvictedPod 判定依据）、`scanner.normalizePod` 采集 `Status.Reason`。
- **测试**：`TestIsForbiddenCommand`（16 用例：8 拦截 + 8 放行）、`TestRunKubectlForbidsSecretAccess / TestRunKubectlRedactsSensitiveOutput`、`TestSchemaNewFields` 扩充 10 类 confidence 变体 + 6 类 risk 变体断言。

### Changed

- efficiencyProtocol 排查顺序修正：诊断意图先 kubectl -o yaml 再 previous logs + events + configmap，而不是先结构化摘要；
- agent prompt 重构为 `basePrompt()`，native/shim 两种 tool calling 模式统一装载决策树协议，避免遗漏；
- kubectl 工具 Description 明确标注禁读 Secret 与 --raw，以及写命令需审批的边界提示。

### Fixed

- confidenceLevel 写为 `HIGH_CONF0.9`、risk 写为 `HIGH_RISK` 等常见 LLM 输出变体被合法枚举拒判 → 走规则初步判断降级的问题，现全部归一化正常进入诊断流程；
- `kubectl get secret -o yaml` 被 risk.go 误判为只读 SAFE → Secret 明文可能进入 LLM 的安全洞，现于 executor 入口 + deny_ops 双层硬拦截并统一输出脱敏；
- chat 单点诊断 `diagnose_pod` 因内部 LLM（40s+）+ 摘要视图漏字段导致绕路问题，通过砍内部 LLM + 决策树引导 YAML 取证两条并行路径解决。

## [v2.0.6] - 2026-08-30

规则引擎补齐 4 项重要盲区与误报治理（为后续 chat 优化打基础）。

### Added

- `PVFailed` 规则（storage_rules.go）：PV `Failed=High`、`Released=Low`（Retain 策略下属正常生命周期，低级别提示待清理），PVC 正常但 PV 异常不再静默；
- `PodTerminating` 规则（pod_rules.go）：`DeletionTimestamp` 存在且年龄 >5min 报 Medium，证据包含 finalizers（finalizer 卡住/卷卸载失败常见）；
- `NodePIDPressure` 规则（node_rules.go）：High，并纳入 `markCorrelated` 的节点根因集合（PID 压力引发的 Pod 异常标记 Correlated，避免健康评分重复扣分）；
- IngressBackend 增强：Service 存在但端口不匹配或 Endpoint=0 也报（之前只查 Service 是否存在），使用 `ServiceNoEndpoint` 已有索引；
- ServiceNoEndpoint 滚动宽限：匹配 Pod 全是 <2min 新建时跳过（滚动发布瞬间 0 端点误报）。
- `PendingPod` 60s 时间门槛：新建 <60s Pending 属滚动发布暂态跳过；
- Job `NeedsLogs=true`：`LogTargets` 通过关联索引反查 Job 下属 Pod 进行 Phase2 日志采集；
- init 容器盲区补齐：CrashLoopBackOff / ImagePullBackOff / ContainerCreateError 均识别 init 容器并在标题注明，配置缺失仍升 HIGH。

### Changed

- `LogTargets(reg, findings, index)` 接收关联索引：非 Pod 但 NeedsLogs 的资源（如 Job）反查下属 Pod 作为日志目标；
- `markCorrelated` 根因规则表新增 PIDPressure。

### Fixed

- PodInfo 缺 `DeletionTimestamp` 与 `Finalizers` 字段 → Pod 长时间 Terminating 无法判定的盲区；
- normalize 阶段未采集 Job/CronJob 失败相关的状态字段（导致 JobFailed / CronJobFailed 无法精确命中的问题已通过索引补足）。

## [v2.0.5] - 2026-08-29

Web UI 会话重命名功能 + chat 终端横幅美化与工具调用缩进输出。

### Added

- Web 会话侧栏：会话删除按钮前新增重命名按钮（✎），点击后 prompt 输入新名称；空名或未变更时不发请求；失败在聊天区提示错误，成功后刷新会话列表。
  - 后端接口：复用 `POST /api/sessions/{id}/rename`（server.go + chatService.RenameSession 既有），零后端改动。
- chat 终端欢迎横幅（internal/cli/banner.go）：带边框的 ASCII 风格横幅，显示「k8s-ai」产品名 + 版本 + 集群信息；下方展示 3 条精简能力介绍和 exit/clear/Ctrl+C 退出提示；非 TTY（管道/测试）自动降级为无框纯文本。
- 工具调用行格式优化（chat.go emit）：带缩进箭头 `  ↳ [Tool] 工具名 (参数)`，最终回答前插入空行分隔。

## [v2.0.4] - 2026-08-28

Windows + Linux 双平台二进制重新打包（含会话重命名 + 横幅优化功能）。

## [v2.0.3] - 2026-08-28

Web UI 会话重命名（✎ 按钮）+ Makefile 默认版本升级到 v2.0.3，双平台打包。

## [v2.0.2] - 2026-08-27

Web UI 支持集群下拉切换本机 ~/.kube/config context；会话模型新增 `Session.Cluster` 绑定会话集群，运行中会话不允许热切。

## [v2.0.1] - 2026-08-24

终端交互体验与执行层安全/正确性优化（对标 kubectl-ai，见 docs/CREDITS.md）。

### Added

- **终端 Markdown 渲染**（internal/cli/chat.go）：`k8s-ai chat` 交互模式最终回答经 glamour 渲染（表格/列表/代码块）；`WithAutoStyle` 在管道/非 TTY 自动去样式，`--format json` 不受影响。
- **readline 交互输入**（internal/cli/chat.go）：终端输入走 chzyer/readline（历史记录文件 `k8s-ai-history`、Ctrl+C/Ctrl+D 优雅退出）；非 TTY（管道/测试）自动回退 bufio，行为与原先一致。
- **流式命令超时采样**（internal/executor）：`logs -f` / `get -w` 不再在解析层拒绝，改由 executor 套固定超时（默认 7s，`Options.StreamTimeout` 可配）捕获部分输出并标记 `Result.StreamTimeout`，超时不视为错误，不阻塞 Agent Loop。交互式流（attach/exec -it）仍被拒绝。
- **kubectl 动词分类表扩充**（internal/approval/risk.go + internal/executor/parse.go）：
  - `rollout history` / `rollout status` 归为只读（SAFE，自动执行）；
  - 写动词带 `--dry-run` 降级为只读（不实际变更）；
  - 新增写动词 `autoscale` / `expose` / `taint` / `reconcile` / `certificate` 纳入审批流。
- 测试：`TestIsStreaming` / `TestRunKubectlStreamingTimeout` / `TestBufInput` / `TestRenderMarkdown` / 风险分类新增 rollout 子命令与 dry-run 用例。

### Changed

- 流式命令（logs -f / get -w）从「解析层拒绝」改为「执行层超时采样」，行为更接近 kubectl-ai 且不再阻塞。

### Fixed

- `kubectl delete ... --dry-run` 此前仍被分类为 HIGH/CRITICAL（delete 走特殊分支、未纳入 dry-run 只读判定）；现统一降级为只读（SAFE），与 apply/scale 等 dry-run 行为一致。

## [v2.0.0] - 2026-08-24

二期交付：自然语言 Kubernetes Agent（chat/web）+ 读写操作 + 人工确认闭环。

### Added

- **`k8s-ai chat`**：交互 / 单发 / stdin / `--format json`；会话管理（--session/--new-session/--list-sessions/--delete-session）；`clear`/`exit` 元命令。
- **`k8s-ai web`**：浏览器 chat（REST + SSE 流式 + 会话侧栏 + Markdown 渲染 + 审批卡片 + 工具执行过程 Show output）。
- **Agent 核心**（internal/agent）：消息总线/状态机、native + shim + auto 双模式 Tool Calling、max_iterations/max_tool_calls/timeout、单轮上下文预算、ToolResult 截断/脱敏/不可信定界。
- **工具集**（internal/tools + tools/k8s）：19 个只读工具、5 个诊断工具（复用一期 ScanService.Run）、7 个写工具（kubectl/scale/patch_deployment/patch_configmap/restart_rollout/label/delete_pod）；`CommandDescriber` 审批可读化。
- **审批与执行**（internal/approval + internal/executor）：SAFE..CRITICAL 风险分级、deny_ops、require_approval、--skip-permissions；kubectl argv 直调（无 shell）+ client-go 结构化写；执行后验证（Deployment 状态核对）。
- **修复行为门禁**：诊断类提问只给结论+方案并询问"需要我修复吗？"，用户同意后才调用写工具。
- **审计**（internal/audit 接入主流程）：用户输入 / LLM / 工具调用 / 审批 / 执行事件落盘（脱敏）。
- **MCP 预留**（internal/mcp）：Client 接口 + 工具注册表适配器骨架。
- **RBAC 写权限示例**（deploy/rbac-write.example.yaml，默认清单保持只读）。

### Changed

- `agent.max_iterations` 默认 10 → 15（对标 kubectl-ai，降低弱模型触发上限概率）。
- LLM tools 请求/回传 wire 对齐 OpenAI 标准（`{"type":"function","function":{...}}`），兼容 vLLM/Qwen 网关。
- 注册 client-go 认证插件（k8s.io/client-go/plugin/pkg/client/auth），支持 OIDC 等 kubeconfig 认证。
- 一期兼容改动：kubernetes 包增加节点 metrics 读取（k8s.io/metrics）、kubernetes.Client NewClientset、scanner.Reader Get\*/ConfigMap 扩展。

### Fixed

- 工具结果投喂 LLM 被 Text 摘要覆盖（get_pods 只见 "pods=20"）→ Text+Data 一起投喂。
- `get_pod` 支持省略 namespace 全集群查找 + 前缀匹配（Deployment 名查 Pod），避免模型猜命名空间导致 max_iterations。
- markdown 表格重复渲染（解析器每行重复建表）。
- 审批卡片展示原始转义 JSON → 人类可读命令 + 变更说明。

## [v1.0.3] - 2026-08-19

一期体验迭代：修复命令非交互化（edit→patch/apply）+ scan 终端阶段进度 UI（Spinne + LLM 长任务抚慰）。

### Added

- **scan 终端阶段进度 UI**（internal/cli/progress.go + model.ScanProgress）：
  - 6 个阶段统一显示 `⠋ 1. 标题 ➜ 当前子项` 单行 Spinner（TTY 下 80ms/帧，行内覆盖刷新）；
  - 阶段结束时 `✓ 1. 标题 完成（耗时 Xs，细节 Y）` 换行落盘，不刷屏；
  - JSON/YAML/`--verbose` 模式自动退化为逐行日志（无 `\r`），不污染机器可读输出；
  - 进度接口仅用 stdlib 类型放在 domain 包，Server/Cron 不设置则静默，严格符合「业务不依赖 CLI」；
  - 二期 Agent 的 Thinking / Tool Calling 状态指示器可直接复用同一接口扩展实现。
- **LLM 长任务抚慰提示**（internal/diagnosis/diagnostics.go DiagnoseIncidents）：
  - 每个 Incident 独立 ticker，超过 10s 未完成时触发 `LongTask`，文本显示「仍在诊断中（已 15s，常见耗时 10-30s，请稍候）」，每 10s 再提醒一次；
  - 避免用户在 LLM 诊断 20s+ 时误以为卡死而 Ctrl+C（经验 1073789）。
- **Diagnoser / service 层进度打点**：connect、phase1、rules、phase2、enrich、llm、report 共 7 阶段统一 PhaseStart/PhaseDone；LLM 阶段用 `scanProgressLLMBridge` 把 Incident 粒度事件（Started/Done）桥接到 PhaseSub，保证"诊断 i/N incident xxx"在终端可见。

### Changed

- **修复命令非交互强制化**（system.md §三十五 规则 4 + internal/diagnosis/commands.go `remediationAllowsNonInteractive`）：
  - 提示词禁止 `kubectl edit` / `exec -it` / `run -i` / `attach` / `port-forward` 等任何 TTY/编辑器交互；
  - 给出 4 种非交互替代并按推荐度排序：① `patch --type=merge`、② `get -o yaml > file && 改 && apply`、③ `create ... --dry-run=client \| apply`、④ `set image/resources/env/selector`；
  - 命令校验层同步丢弃交互式修复命令，避免模型「不听话」时进入报告（edit/attach/port-forward 直接失败；exec/run 带 `-i -t -it -ti --stdin --tty` 失败）。
- 补全 `exec`/`run`/`port-forward` 子命令缺资源名解析（按 firstPositional 取 Pod 名，与 `logs` 同一模式），保证后续命名空间补全/校验覆盖这些子命令。

### Fixed

- 上一版本 v1.0.2 中，修复命令推荐为 `kubectl edit configmap`（交互式、不可复制执行）的问题；现在会被命令校验过滤并要求 LLM 给出 patch/apply 非交互版本。
- **诊断修复退化为纯只读**：当 LLM 只给出 `get cm -o yaml` 等只读命令（被 §三十五 要求丢弃 edit/exec -it 后，常见"只给确认步骤"）时，remediation 列表最终全是只读命令，未满足 §三十五「至少一条写入操作命令」的硬性要求。现由 `ensureRemediation` + `fallbackWriteCommands` 规则级兜底，按 Finding.Rule / Kind 自动补齐 create/patch/apply/set/scale/rollout restart 等写入类命令（带 `<待确认>` 占位），保证每条诊断都能"下一步真正动起来"。同样兜底也应用于 `degraded` 降级路径。
- **Spinner 刷屏（非 TTY）**：`useSpinner` 此前只检查报告格式（markdown 且非 verbose），未检测 stdout 是否真正是交互式终端；当输出被管道/重定向时 `\r` 覆盖退化为逐行打印，80ms/帧 × 数十秒产生数百行垃圾。现由 `isTerminal(os.Stdout)` 用 `os.ModeCharDevice` 零依赖检测，管道/重定向自动退化为单行日志模式。
- **健康度标签写死"集群健康"**：`k8s-ai scan -n xxx` 扫描的是命名空间，`k8s-ai scan pod xxx` 扫描的是 Pod，但摘要头始终打印"集群健康"。现按 scope 动态显示：cluster →"集群健康"、namespace →"命名空间健康"、pod →"Pod 健康度"。

## [v1.0.2] - 2026-08-19

一期迭代：故障等级/修复方案/健康评分/派生展示四项优化 + kubectl 命令命名空间校验生效。

### Changed

- **故障等级强化（FailedMount / CreateContainerConfigError）**：容器启动、存储挂载失败的根因，若证据消息含 ConfigMap/Secret "not found"，等级强制提升为 HIGH（服务完全不可用）；`CreateContainerError` 规则已覆盖 `CreateContainerConfigError` 的等级映射（internal/rule）。
- **修复方案必须含写入命令**：system.md §三十五 新增硬性要求——每套修复方案必须至少包含一条 create/patch/apply/delete/set/edit/scale 等写入命令，不允许只有 get/describe/logs 等只读命令；只读确认命令仅作前置步骤；字段不确定时用 `<待确认的值>` 标注而非退回只读；多步操作按顺序列出。
- **健康评分基础分调整**：存在 HIGH 及以上等级的非关联（非 Correlated）根因时，基础分由 100 直接降至 70 再叠加其他罚分，保证"服务完全不可用"场景评分 ≤ 70（internal/report/health.go）。
- **派生告警不独立显示**：Deployment 副本不足、Service 无 Endpoint 等派生告警不再作为独立问题渲染，只并入根因的"关联问题（派生影响）"影响范围字段（internal/report/render.go）。

- **`-n` 命令校验真正生效**：此前"命令含 namespace 或 Finding 有 namespace 即通过"的兜底校验恒真，导致 LLM 生成的命令缺 `-n`、甚至写错命名空间也能进报告。现在命名空间级资源（Pod/Deployment/PVC/Service/ConfigMap/Secret 等）的命令必须携带 `-n/--namespace`（含 `-n=`/`--namespace=` 形式，动词前/后均可）：
  - 命令自带 namespace 但与 Finding 命名空间不一致 → 视为 LLM 编造/笔误，整条命令丢弃（ADR-005 防编造）；
  - 命令缺省 namespace → 自动补全 Finding 命名空间（如 `kubectl get pod web-0` → `kubectl -n prod get pod web-0`），保证报告命令复制即用（system.md §三十五）；
  - 显式 `-A`/`--all-namespaces` 的跨命名空间查询不补全、不校验。
- 动词前 `-n` 解析修复：此前 `kubectl -n prod get pod ...` 中动词前的 `-n prod` 会被解析器静默吞掉（namespace 丢失），现由 `extractVerb` 正确提取并参与校验/补全。
- `kubectl logs <pod>` 支持：`logs` 动词的位置参数是 Pod 名（无资源类型），此前会被整条丢弃；现按 Pod 处理，`-n` 补全对其同样生效（system.md §八 常用排查命令）。

### Fixed

- 修复命令/排查命令缺失 `-n` 或引用错误命名空间时仍被采纳的问题（校验形同虚设）。

## [v1.0.1] - 2026-08-18

一期迭代：Incident 聚合诊断落地 + 诊断体验优化（修复方案文字+命令化、日志完整保留、通用信号规则）。

### Added

- Incident 聚合诊断正式入库：Finding → Correlation → Incident → LLM，同一故障链只调用一次 LLM（之前是每个 Finding 一次）；派生症状（Deployment 副本不足/Service 无 Endpoint）折叠为影响范围，不单独分析/重复扣分；健康评分只统计根因
- `Incident` 模型与 `internal/incident` 聚合包（并查集按关联资源合并 + 根因选择）；Incident 诊断/报告渲染（Markdown 关联问题段、终端派生折叠）
- LLM 输出新增 `remediationText`（修复文字说明：做什么/为什么/预期结果，文字是主体）及 `confidenceLevel`（CONFIRMED/HIGH_CONFIDENCE/POSSIBLE/UNKNOWN）、`causalChain`、`uncertainty`、`remediationDirection` 字段，对齐 system.md 诊断协议
- 通用信号规则：`ContainerCreateError`（Secret/ConfigMap 缺失等启动失败）、`Unhealthy`（探针失败）、`IngressBackend`（backend 指向不存在 Service）

### Changed

- 修复方案"文字+命令化"（system.md §三十五 重写）：remediationText 必填文字说明，remediation 配套可执行 kubectl 命令（含 -n/资源名/完整参数）；报告与终端渲染"建议=文字说明+命令（含风险）"，禁止"只有命令没有文字"或"只有文字没有命令"
- **修复建议永不缺失**：LLM 漏写文字/空命令时，工具按根因规则兜底生成"修复方向 + 前置确认"（remediationDirection），报告/终端始终有修复指导
- 日志按行完整保留（TailLines 500、单行不截断仅 >1MiB 防 OOM、总字节按行边界）；Incident 上下文只送根因，派生症状合并到 impact
- 诊断上下文补充 Pod 引用的 ConfigMap/Secret **名称**（仅名称不读数据），配置/凭据类根因优先给出针对性命令

### Fixed

- 修复方案回归：LLM 只输出命令、丢失文字说明 → 新增 remediationText 字段并同步 latest.json 契约，LLM 漏写时工具用修复方向兜底
- 终端一屏摘要最多展示 2 条修复命令，其余提示 `--verbose` 完整输出

### Security

- 一期严格只读不变：LLM 生成的 kubectl 命令仅展示不执行；Secret 数据永不读取/发送

## [v1.0.0] - 2026-08-17

一期 1.1 + 1.2 交付（当前版本基线）。

### Added

- 只读 Kubernetes 巡检：全集群 / 命名空间 / 单 Pod 三种扫描形态
- 13 条内置规则（CrashLoopBackOff、OOMKilled、ImagePullBackOff、PendingPod、Node 压力、PVC Pending、FailedMount、Service 无 Endpoint、副本不匹配、Job Failed 等）
- 两阶段采集（Phase1 全量 list 无 N+1 / Phase2 仅异常对象取日志）+ 关联索引（owner/PVC/Service）
- Evidence 证据链：对象字段 / Events / 日志（保留尾部 + 关键行提取）/ 注解，采集边界脱敏
- LLM 诊断（OpenAI Compatible）：预算裁剪、JSON Schema / Evidence ID / kubectl 命令校验、失败降级为规则初步判断
- 健康评分 + Markdown/JSON/YAML 报告 + 终端一屏摘要（--verbose 完整输出）
- 历史对比（v1.0.0 1.2）：按 Finding 指纹输出新增/持续/恢复，写入 latest.json history 字段
- HTTP Server（v1.0.0 1.2）：healthz/readyz/version + 异步扫描 API
- 部署：Dockerfile（distroless 非 root）、deploy/ RBAC/ConfigMap/Secret 示例/PVC/CronJob/Deployment/Service
- 文档：README、INSTALLATION.md（安装部署手册）、TECHNICAL_GUIDE.md、docs/design/ 系列、docs/VERSIONING.md

### Changed

- 诊断上下文补充 Pod 引用的 ConfigMap **名称**（仅名称不读数据）与关联 Pod 的 ConfigMaps 信息；系统提示词引导"配置类根因优先给出 `kubectl edit configmap`"——修复建议不再只会给 rollout restart（实测输出 `kubectl edit configmap nginx-config -n yanshou-nginx`）

- 报告可读性优化：日志证据压缩（体积 -91%）、严重级图标、健康条、组件状态
- JSON 契约字段小写化；证据链 E-ID 与报告一致性；Duration 含 LLM 耗时（P10 前体检修复）

### Fixed

- P10 前体检 3 个 P0：ComponentInfo JSON 字段名、enrich 顺序导致 E-ID 不一致、Duration 不含 LLM
- Makefile lint 目录参数失效（改为文件列表校验）

### Security

- 四层只读保证 + AST 架构测试（含 Secrets 禁访）；-race 需 CGO（本机无 gcc，CI/Linux 可跑）
