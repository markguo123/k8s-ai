# Agent 总体架构（二期/三期已落地）

- 版本：v3.0.0
- 日期：2026-10-08
- 状态：已封版（二期 Agent / 三期创建闭环，v3.0.0 基线）

> 范围说明：本文档定义 Agent 的稳定职责边界——Agent、Tools、Scanner/Rule、
> Approval/Executor、Agent Loop 如何协作。对应能力已在二期/三期实现并随 v3.0.0 封版；
> 正文中保留的 CURRENT / TARGET / MIGRATION 标注仅用于记录设计论证过程，不代表当前仍未实现。

---

## 一、现状分析

### 1.1 组件现状

**Prompt**

- `internal/agent/prompt.go` 通过 `basePrompt()` 拼接了身份与诚实约束、响应协议、健康检查协议、工具选择协议、效率协议、修复门约束。
- `internal/llm/prompt.go` 返回 `prompts/system.md`，承载 Evidence 唯一、故障链聚合、根因/直接症状/派生症状、确认根因与推断的区别、修复方案结构等要求。
- 结论：当前存在两套系统提示词，`agent` 负责 Agentic 行为，`llm/system.md` 负责诊断结果质量。两者部分重复，但没有到必须合并的程度。主要问题是安全与执行约束大量出现在 prompt 中，而真正安全边界在代码层已经存在。

**Tool Schema**

- `internal/tools/tool.go` 定义 `Tool` 接口：`Name / Description / InputSchema / Execute / IsReadOnly / Risk`。
- `internal/tools/registry.go` 的 `Schemas()` 只把 `name / description / parameters` 传给 LLM；`IsReadOnly / Risk` 不进入模型可读的 Tool Schema，只用于审批展示、审计和 Agent Loop 的只读判断。
- `kubectl` 是唯一写工具。`internal/tools/k8s` 提供 22 个只读 Kubernetes 工具（`get_pod / get_deployment / get_node / get_events / get_logs ...`）。
- CURRENT 中“工具风险”的半结构化信息存在于两部分：代码里的 `Tool.IsReadOnly/Risk`，以及 prompt/kubectl Description 里的安全措辞。两者不一致时需要人工维护两份。

**Code Guard**

- `internal/approval/risk.go` 统一做命令分类：`IsReadOnlyCommand`、`ClassifyCommand`、`HasShellSyntax`、`IsForbiddenCommand`。
- `internal/executor/parse.go` 做命令解析：`deny_ops`、Secret/`--raw` 红线、交互式命令拒绝、shell 元字符识别、非 shell 命令动词白名单、`exec -i/-t` 拦截、流式命令识别。
- `internal/executor/kubectl.go` 在 shell 模式下用 `bash -c` 执行并做脱敏/截断/超时采样。
- `internal/agent/loop.go` 在 Tool 执行前判断 `Tool.IsReadOnly`，写操作走 `Approver.Confirm`；同签名拒绝达 2 次后硬拦截。
- 结论：安全边界已经落在代码层，且比 prompt 更可靠。prompt 中的大量“必须/禁止”更多是行为引导，不是安全边界本身。

**Scanner / Rule Engine**

- `internal/scanner` 负责两阶段只读采集：Phase1 轻量 list，Phase2 对异常 Pod 取日志，单个采集失败进入 `CollectionErrors` 不中断扫描。
- `internal/rule` 基于采集快照产出 `model.Finding`，包含 `Severity / Category / Kind / Doc / Correlated` 等字段。
- `internal/incident` 基于 `CorrelationIndex` 将因果相关 Finding 聚合成 `Incident`，根因选择由 `pickRoot` 的规则优先级完成。
- `internal/service` 编排 Scan → Rule → Phase2 → Incident → LLM Diagnosis；同一条故障链只送诊一次。
- 结论：Scanner/Rule/Incident/Diagnosis 已有清晰分层，属于一期核心能力；二期 Agent 主要消费这些结果，而不是替代它们。

### 1.2 现状判断

#### 1. Prompt 当前承担哪些职责？

1. 身份、诚实、证据来源约束。
2. 输出协议：工具调用期间可见输出、最终报告结构。
3. 健康检查调查维度。
4. 工具选择决策树：意图路由、结构化工具 vs `kubectl`、`-o yaml` 升级、`scan_cluster` 优先。
5. 调查效率与收敛：同一资源不重复调用、预算 6 次、回答结构。
6. 修复门：诊断类问题不主动写、修复四步、写操作审批、拒绝语义、delete/PVC 偏好。
7. 部分安全红线：Secret、`--raw`、shell 语法永不自动执行、交互式命令禁止。

#### 2. 哪些职责应该继续留在 Prompt？

- 身份和诚实性。
- 证据唯一、禁止编造。
- 意图路由和工具选择原则。
- 回答结构与收敛。
- 修复前解释与用户确认的对话流程。
- 区分确认、推断、未知、症状、影响的行为要求。

这些是模型行为规范，适合 Prompt 承载。

#### 3. 哪些规则应该移动到 Tool Schema？

- 每个工具何时使用、何时不要用。
- 工具是否只读。
- 工具/单次调用的风险等级。
- 是否需要审批、是否有硬拒绝红线。
- `kubectl` 工具的能力边界、命令格式、红线摘要。

当前 `Tool.IsReadOnly / Risk` 已经在代码接口里，建议下一步把这些元数据注入模型可见的 Tool Schema 或作为系统级工具说明，而不是只写在 prompt 和 Description 里。

#### 4. 哪些规则应该由 Code Guard 强制保证？

- `deny_ops` 硬拦截。
- Secret 内容读取与 `--raw` 红线。
- 写操作审批（Agent Loop + Approver）。
- shell 命令永不自动执行。
- 交互式/阻塞性命令拒绝。
- 同签名写操作被拒后禁止原样重试。
- 命令输出脱敏与截断。
- 流式命令超时采样。

这些能力当前已经在 `internal/approval`、`internal/executor`、`internal/agent/loop.go` 实现。后续应保持“不依赖 Prompt 也能拦”的原则。

#### 5. 哪些规则应该由 Rule Engine 负责？

- 确定性 Kubernetes 异常检测。
- 生成 Finding 及严重度、类别、文档链接。
- 关联相关资源。
- 将因果相关 Finding 聚合为 Incident。
- 为 LLM 诊断提供规则证据。

#### 6. Scanner、Rule Engine、Agent、Tools 之间是否存在职责重叠？

整体边界清晰，重叠主要在小地方：

- `prompts/system.md`、`agentPrompt`、`kubectlTool.Description` 三处都写了“禁止编造证据/写操作要确认/Secret 红线”，语义重复。
- `remediationGate` 和 `kubectlTool.Description` 都写了 delete 优先 patch、PVC patch 优先 delete 的产品规则，存在双份维护。
- `toolSelectionProtocol` 把一部分本可以由 Tool 元数据表达的信息写进 prompt。例如 `kubectl` 能否 describe、哪些是结构化摘要工具，其实可以进 Tool Description。
- Scanner/Rule/Incident 与 Agent 不重叠：Agent 消费 Finding/Incident，不重新实现扫描或规则判断，这是合理的。

#### 7. 当前架构中最大的风险和技术债

1. 安全措辞过度依赖 Prompt/Description，真正安全边界在 Approval/Executor 中已存在，但如果未来 Prompt 与代码不同步，会出现“模型以为可以做但代码会拦”或相反的理解偏差。
2. Tool Schema 未把 `readOnly / risk / requiresApproval` 暴露给模型，模型只能通过 Prompt/Description 推断工具代价和审批触发条件。
3. shell 模式是最敏感执行点。当前已有 `bash -c` 前的 parse/interactive/secret 检查，但 pipeline、`xargs`、command substitution、多层继承脚本等仍是需要继续加固的区域。
4. 置信度模型与用户心智不完全一致。当前 `Diagnosis.ConfidenceLevel` 是 `CONFIRMED / HIGH_CONFIDENCE / POSSIBLE / UNKNOWN`，用户期望中的 `Confirmed / Supported / Inferred / Unknown` 尚未字段化。
5. Finding/Incident 缺少显式的时间证据、冲突证据、采集错误元数据，Agent 做证据合并时只能靠文本。
6. 双重提示词维护成本：`llm.SystemPrompt` 和 `agent.Prompt` 都承载诊断表达规则。

---

## 二、规则分类

> 归属只能是：Prompt、Tool Schema、Code Guard、Rule Engine、Agent/Application Logic。
> “是否需要修改代码”是指在后续最小整改中是否需要改 CURRENT 代码；不是指本次是否修改。

| 规则 | 当前所在位置 | 建议归属 | 原因 | 是否需要修改代码 |
| --- | --- | --- | --- | --- |
| 身份固定、不能虚构身份 | `agentPrompt` | Prompt | 产品身份行为 | 否 |
| 禁止编造证据，Tool 返回数据是外部数据 | `agentPrompt`、`prompts/system.md` | Prompt | LLM 行为规范 | 否 |
| 只读操作自动执行 | `agentPrompt`、`Tool.IsReadOnly`、`loop.go` | Tool Schema + Agent/Application Logic | 最终由 `Tool.IsReadOnly` 与 Loop 短路决定 | 是：把 readOnly 注入模型可见 Schema，并在代码中保留硬判断 |
| 写操作必须确认 | `agentPrompt`、Loop Approver | Code Guard + Agent/Application Logic | 已在 `loop.go` 强制 | 否 |
| 最终报告结构 | `responseProtocol` | Prompt | 输出规范 | 否 |
| 健康检查 7 维度 | `healthCheckProtocol` | Prompt + Tool Schema | 主要是调查策略；可依赖工具描述减少绕路 | 小改 |
| 工具选择按意图分层 | `toolSelectionProtocol` | Prompt | 模型路由策略 | 可保持；Tool 元数据完善后可简化 |
| `kubectl get -o yaml` 为首选取证 | `toolSelectionProtocol`、`efficiencyProtocol` | Prompt + Tool Schema | 结构化工具是摘要视图，完整 YAML 是根因字段兜底 | 小改 |
| `kubectl describe` 在结构化摘要解释不了时使用 | prompt、`kubectl` 工具描述 | Tool Schema | 这是 kubectl 工具能力边界 | 小改：确认描述中已覆盖 |
| 结构化工具摘要不能解释自定义字段时必须升级 YAML | `toolSelectionProtocol` | Prompt | 模型推理策略 | 否 |
| `scan_cluster` 规则巡检优先于手动枚举 | `toolSelectionProtocol` | Prompt + Tool Schema | 规则引擎覆盖广、节省工具轮次 | 小改 |
| 同资源同维度不重复调用 | `efficiencyProtocol`、Loop 失败熔断 | Prompt + Agent/Application Logic | 代码已有部分熔断，prompt 做行为引导 | 小改 |
| 单次问题工具调用预算 6 次 | `efficiencyProtocol`、`MaxToolCalls` | Prompt + Agent 配置 | 代码有硬上限，prompt 做预算引导 | 小改 |
| 诊断类问题不得直接调用写工具 | `remediationGate`、`Tool.IsReadOnly/Risk` | Prompt + Code Guard | 真正拦截在 Loop/Approver | 可保持 |
| 修复四步流程 | `remediationGate` | Prompt | 对话流程规范 | 否 |
| delete 永远最后选择，patch 优先 | `remediationGate`、`kubectlTool.Description` | Tool Schema + Code Guard（可叠加） | 这是产品安全偏好，应作为工具的强约束；减少两份文本维护 | 是 |
| PVC 修复优先 patch storageClassName，而不是直接 delete pvc | `remediationGate`、`kubectlTool.Description` | Tool Schema + Prompt（产品决策） | 属于产品修复策略；代码层可校验但不应完全替代模型选择 | 是，待确认 |
| 同签名写操作拒绝 2 次后硬拦截 | `remediationGate`、`loop.go maxDeclinesBeforeBlock` | Agent/Application Logic | 代码已实现 | 小改：从 prompt 删除重复细节 |
| `USER_EXPLICITLY_DECLINED` 不是工具错误 | `remediationGate`、`formatDeclinedMsg` | Tool Protocol + Agent/Application Logic | 代码通过结果文本表达 | 小改 |
| shell 构造永不自动执行 | prompt、`approval.IsReadOnlyCommand`、`executor.ParseCommandDetail`、`kubectl` 描述 | Code Guard | 代码层已强制 | 是：精简 prompt 重复 |
| Secret 读取与 `--raw` 红线 | `approval.IsForbiddenCommand`、executor、`kubectl` 描述 | Code Guard | 必须由代码拦截 | 是：prompt 保留语义提示，代码保留硬拦截 |
| 交互式命令拒绝 | `kubectl` 描述、`executor.rejectedInteractiveVerbs` | Code Guard | 代码层强制 | 否 |
| 流式命令超时采样 | `kubectl` 描述、`executor.IsStreaming` | Code Guard | 代码层实现 | 否 |
| 输出脱敏与截断 | `tools.ToolResult`、executor 输出处理 | Code Guard + Application Logic | 代码已有 | 否 |
| Evidence 唯一、禁止臆造 | `prompts/system.md`、`agentPrompt` | Prompt | 模型行为规范 | 保留，但避免重复 |
| Finding 聚合成 Incident | `prompts/system.md`、`internal/incident` | Rule Engine + Agent/Application Logic | 代码层已聚合，prompt 描述语义即可 | 是：可在 Finding/Incident 补充角色字段 |
| 根因/直接症状/派生症状区分 | `prompts/system.md`、Diagnosis schema | Prompt + Model/Diagnosis Schema | 需要字段化表达 | 是 |
| 置信度枚举 | Diagnosis schema | Model/Schema + Prompt | 用户心智更接近 Confirmed/Supported/Inferred/Unknown | 是，待确认是否改枚举 |
| 证据时间顺序、冲突证据、采集错误 | 目前散落在 Finding/Diagnosis/文本 | Model/Schema + Agent/Application Logic | 需要显式元数据供 Agent 合并 | 是 |

---

## 三、最小改动目标架构

### 3.1 总体原则

- 不加层、不重构 Scanner/Rule。
- 让 Tool 成为能力与约束的半结构化来源。
- 让 Code Guard 继续作为唯一真正的执行安全边界。
- 让 Prompt 从“执行安全条例”降为“行为引导、意图路由、回答结构”。

### 3.2 Target 数据流

```text
┌──────────────────────────────────────────────────────┐
│ Prompt                                              │
│ 身份 / 意图路由 / 取证策略 / 输出结构 / 对话流程        │
└──────────────────────┬───────────────────────────────┘
                       │
                       v
┌──────────────────────────────────────────────────────┐
│ Tool Schema（模型可见）                              │
│ name / description / parameters                     │
│ + metadata: readonly / risk / requiresApproval       │
└──────────────────────┬───────────────────────────────┘
                       │
                       v
┌──────────────────────────────────────────────────────┐
│ Agent Loop                                          │
│ reason → tool → observe → reason                    │
└──────────────────────┬───────────────────────────────┘
                       │
                       v
┌──────────────────────────────────────────────────────┐
│ Code Guard：Approval + Executor                     │
│ deny_ops / Secret / --raw / shell / interactive      │
│ write approval / output redaction / truncation       │
└──────────────────────┬───────────────────────────────┘
                       │
                       v
┌──────────────────────────────────────────────────────┐
│ Executor / kubectl / client-go                      │
└──────────────────────────────────────────────────────┘

Scanner → Rule → Finding → Incident → Diagnosis
             （确定性证据）   （故障链）   （LLM 结论）
```

### 3.3 Agent 如何调用 Tool

- 保持 CURRENT：`agent.Chat` 从 `a.Tools.Schemas()` 拿模型可见工具定义。
- TARGET：`Schemas()` 在保持 OpenAI 兼容的前提下，把 `readonly / risk / requiresApproval` 作为 `description` 或扩展 `metadata` 注入；模型可以据此选择工具，但模型选择不对安全负责。
- 执行前仍以 `tool.IsReadOnly(input)` 和 `tool.Risk(input)` 为准，写操作必须在 Agent Loop 走 Approver。

### 3.4 Tool Schema 如何表达 read-only / risk / approval

- CURRENT：`Tool` 接口已有 `IsReadOnly / Risk`，但 `Registry.Schemas()` 未暴露。
- TARGET：给 `ToolSpec`/Schema 增加只读输出字段，例如在 description 前缀生成 `[只读，风险: SAFE]` 或提供可选 `metadata`。对 `kubectl` 工具，把“写命令需要审批 / shell 永不自动执行 / 交互式禁止 / Secret/--raw 禁止”压缩到几行稳定文本。
- MIGRATION：先给 `kubectl` 和结构化只读工具补 metadata，再考虑通用 `ToolSpec` 字段；不需要把所有工具全部拆成多个写工具。

### 3.5 Code Guard 在哪里介入

Code Guard 的介入点和 CURRENT 基本一致：

1. `internal/executor` 收到工具执行请求。
2. `internal/approval` 做命令分类：只读、写、风险、禁止。
3. `internal/agent` 对写命令调用 Approver。
4. 执行时再次通过 `executor` 的 parse/line checks。
5. 输出完成后统一脱敏/截断。

TARGET 只是减少 prompt 上的重复安全措辞，不改变 Code Guard 位置。

### 3.6 Scanner / Rule Engine 如何向 Agent 提供 Finding

- 保持 CURRENT：Scanner 提供 ClusterSnapshot，Rule 引擎产出 `Finding`，`internal/incident` 聚合出 `Incident`，`internal/diagnosis` 将 Incident 根因送 LLM。
- TARGET：在 `Finding`/`Diagnosis` 上增加显式字段表达证据角色和时间信息，让 Agent 更容易把“多个 Finding”合成为根因、派生症状、影响。

### 3.7 Agent 如何从多个 Finding 形成 Incident

- CURRENT：`internal/incident.Build` 用 CorrelationIndex 做故障链聚合，`pickRoot` 用规则优先级选根因。
- TARGET：保留该确定性聚合，但在 Feed 给 LLM 前附加：
  - Root Finding 证据链。
  - 派生成员摘要（作为影响范围）。
  - 聚合时保留 `CollectionErrors`、时间信息、冲突证据。
- Agent 不重写聚合逻辑，只消费聚合结果做诊断解释。

### 3.8 Prompt 如何与这些组件协作

- Prompt 负责：意图分类、工具选择原则、何时升级 YAML、何时收敛、最终回答结构、修复前对话。
- Tool Schema 负责：能力、参数、模型可见约束摘要。
- Code Guard 负责：所有硬性执行边界。
- Agent/Application Logic 负责：审批、拒绝闸门、失败熔断、上下文预算。

---

## 四、Prompt v1.1 建议

> 本文不重写 prompt，只列改动方向。

### 4.1 必须修改的部分

1. 去掉/弱化与代码重复的“安全条例”细节：
   - Secret、`--raw` 红线的完整列举。
   - shell 构造可以做什么以及“永不自动执行”的重复描述。
   - 同签名拒绝 2 次的硬拦截说明。
   这些信息保留在 Tool Description 和 Code Guard，Prompt 只保留一句话语义。
2. 将 delete/PVC 修复偏好的表述与 `kubectlTool.Description` 对齐，避免“Prompt 一套、Tool 一套”。
3. 明确 Evidence 置信度表达：优先区分 `Confirmed / Supported / Inferred / Unknown`（需在 Schema 对齐后）。
4. 将“Root Cause / Contributing Factor / Symptom / Impact”作为最终回答输出结构，与 Diagnosis Schema 对应。

### 4.2 建议删除或下沉的部分

- `remediationGate` 中关于“PVC 绑定失败先 patch storageClassName”的完整步骤，可下沉到 kubectl Tool 描述/审批文案。
- `toolSelectionProtocol` 中过多可枚举工具路径细节，可浓缩为：
  - 诊断意图优先完整 YAML/describe/kubectl。
  - 浏览意图用结构化 `get_*`。
  - 局部/全量健康检查优先 `scan_cluster`。
- `healthCheckProtocol` 的具体 7 维度可以保留，但不要在 prompt 里重复每个工具名。
- `efficiencyProtocol` 保留预算和收敛原则，删除“具体工具二选一”这类 Tool 级建议。

### 4.3 建议新增的部分

- 证据可信度分级（Confirmed/Supported/Inferred/Unknown）与输出约束。
- 时间证据使用方式：FirstSeen/LastSeen、事件时间序、日志时间区间。
- 采集错误处理：发现 `CollectionErrors` 时不要臆造缺失数据。
- 冲突证据处理：两个结论冲突时优先确定性 Rule/Evidence，再向用户说明不确定性。
- 证据链要求：结论必须引用 `Finding/Evidence` 编号。

### 4.4 可以暂时保持不动的部分

- 身份、诚实、禁止编造证据。
- 最终报告结构（现象→根因→证据→修复→验证）。
- 修复前征得用户同意的基本语义。
- 工具调用预算和收敛原则。
- 回答中文、简洁、分点。

### 4.5 重点检查项结论

| 检查项 | 结论 |
| --- | --- |
| Intent Routing | 保留 Prompt；可依赖 Tool 元数据简化枚举。 |
| Evidence Collection | Keep Prompt 原则；把具体工具优先级下沉到 Tool Schema。 |
| Root Cause / Symptom / Impact | 需要字段化表达；Prompt 与 Diagnosis Schema 对齐。 |
| Evidence Confidence | 当前字段不是用户期望枚举；需要确认是否扩展。 |
| Temporal Evidence | 当前模型缺少显式字段；建议扩展。 |
| Collection Error | 当前在 ScanResult 有 `CollectionErrors`；Agent 使用时要显式传递。 |
| Conflicting Evidence | 当前靠 prompt 和 LLM 文本；建议 Schema/Evidence 支持冲突标记。 |
| Tool Call Budget | 代码有 `MaxToolCalls/TurnMaxBytes`；Prompt 只做行为约束。 |
| Remediation Gate | 保留对话流程；硬性审批保持 Code Guard。 |
| Approval | 已在 Loop/Approver 实现；不要依赖 prompt。 |
| Read-only / Write operation | 已在 Tool.IsReadOnly + approval 实现；建议注入模型可见元数据。 |

---

## 五、实施计划

> 每阶段可独立测试、可 Git commit；都不要求大面积重构。

### Phase A：架构整理（文档先行，零代码）

- 产出本文档。
- 整理 prompt、Tool、Code Guard、Scanner/Rule 职责边界。
- 不改变 Agent Loop。
- 验证：文档评审、现有 `go test ./...` 通过。

### Phase B：Tool Schema / metadata

- 为 `llm.ToolSpec` 或 `tools.Registry.Schemas()` 增加只读/风险/审批提示。
- 首选最小实现：在 `Description()` 前自动生成稳定元数据前缀，不改变 OpenAI tools 结构。
- 先用 `kubectl` 和 2-3 个代表性只读工具接入，再做全局。
- 验证：`internal/tools`、`internal/llm` 单测断言 Schema 包含 readonly/risk。

### Phase C：Code Guard

- 补强 shell 命令解析：
  - 扫描 pipeline、`xargs`、command substitution、`eval`、`sh -c`、base64 混淆等常见绕过。
  - 保证任意 shell 构造仍需人工审批。
  - 保持当前 `bash -c` 路径不变或改为更严格的白名单解析路径。
- 补充 `internal/executor`、`internal/approval` 测试用例。
- 验证：安全测试通过，Agent Loop 行为不变。

### Phase D：Agent / Prompt 调整

- 精简 Prompt 中重复安全条例。
- 把可下沉内容移入 Tool Description / metadata。
- 通过 `prompt_test.go` 断言关键语义仍存在，不会因为精简丢失“写操作需确认、禁止编造、输出结构”等核心行为。
- 验证：`go test ./...`、现有 prompt 契约测试通过。

### Phase E：Finding / Incident 统一

- 在 `model.Finding`/诊断输入中增加显式字段：
  - 证据角色（Root Cause / Contributing Factor / Symptom / Impact）。
  - 时间信息（FirstSeen/LastSeen 已有，可补充采集时间/事件时间）。
  - 冲突证据或采集错误标记。
- 保留 `Incident` 聚合算法，不重构。
- 验证：`internal/model`、`internal/incident`、`internal/diagnosis` 测试覆盖。

### Phase F：测试

- 增加 Prompt 契约测试。
- 增加 Tool Schema metadata 测试。
- 增加 Code Guard 绕过用例。
- 增加 Incident→Diagnosis 聚合后的置信度/证据链测试。
- 运行 `go test ./...` 和 `go vet ./...`。

---

## 六、CURRENT / TARGET / MIGRATION 摘要

**CURRENT**

- Prompt 承担大量行为规范和安全措辞。
- Tool 已有 `IsReadOnly / Risk`，但未注入模型可见 Schema。
- Approval/Executor 是安全边界，依赖 Agent Loop 调用。
- Scanner/Rule/Incident 已成型，Agent 消费 Finding/Incident，边界合理。

**TARGET**

- Prompt 精简为“意图、取证策略、输出结构、对话流程”。
- Tool Schema 成为模型可见的“能力 + 约束摘要”。
- Code Guard 是唯一硬性安全边界。
- Scanner/Rule 保持确定性异常检测与 Incident 聚合。
- Diagnosis 增加显式的置信度、时间、冲突、采集错误元数据。

**MIGRATION**

- 按 Phase A-F 逐步执行。
- 每步保持 `go test ./...` 可运行，不破坏现有 Agent Loop、Scanner、Rule Engine。
- 最小改动，不新建脚本、不重构 Tool 体系、不把 Prompt 重写为全新版本。

---

## 八、四期多 Agent 编排扩展（2026-10-13）

> 对应四期 P4.10 与 `13-方案-企业级身份权限与审批中心.md` §6。本期起平台演进为
> 多 Agent 编排架构：主 Agent 负责任务编排与会话上下文，子 Agent 各自持专精能力，
> 最终安全边界不变。

### 8.1 架构

```text
用户 / 定时任务 / CI / MCP
        ↓
主 Agent（责任：会话编排、工具路由、上下文预算、最终报告）
        ├── 统一 Agent 注册表（agent registry）
        └── 子 Agent
             └── Approval Agent（首个子 Agent，承担自动审批路径）
```

- 主 Agent 按 `capability + scope` 路由任务到子 Agent；
- 子 Agent 写路径与用户写路径一致，不设豁免；
- 统一审批中心负责"是否确认、由谁确认（本人自确认 / 子 agent 自动）、为什么、结果"。

### 8.2 Agent 注册契约（扩展口）

```text
agent_id          唯一标识
name / desc       显示名与职责说明
capabilities      可处理的操作类型与工具集声明
scope             允许触达的集群 / namespace / 资源范围（后续可收敛到 AuthzGrant 校验）
executor          是否可下发写操作（Approval Agent 为 false，仅签发决策）
policy_binding    绑定的审批策略版本
lifecycle         注册 / 下架 / 能力变更均写审计
```

- 预留统一注册表与调度口，供后续能力型子 Agent（可观测性 / 安全巡检 / 成本治理等）接入；
- 每次注册、下架、能力变更必须写审计，避免出现未经治理的 Agent 新执行通道。

### 8.3 Approval Agent 职责边界

- **策略执行器，不是策略制定者**：风险目录、白名单、denyOps、Secret 策略、scope 授权
  均由 admin 在平台配置，Agent 只按规则裁决；
- 无独立写动力：只能对"已通过全部前置校验的白名单提案"签发放行；
- 每条自动放行输出结构化决定：`decided_by=approval-agent`、`whitelist_key / plan_hash /
  reason` 必填，不输出自由文本"放行了"；
- 可关闭：总开关关闭时 Approval Agent 不可用，自动路径退化为自确认；
- 不作为安全兜底：SAR 与准入控制仍在链路上，Agent 放行不等于永久信任。

### 8.4 与三期单体 Agent 的收敛关系

- 三期 Agent 为单体，审批决策散在 loop / chatApprover / Session Grant；
- 四期把"审批决策"从主循环抽出到统一策略中枢与 Approval Agent，主循环只负责编排与交互；
- 会话内自确认、推送、pending 通道保持三期实现，仅接入新的决策层，不做大拆除。

---

## 七、需要确认的架构决策

1. **是否把 `remediationGate` 的 PVC 修复偏好从 prompt 下沉到 Tool Schema/executor 校验**：即“patch storageClassName 优先、delete pvc 必须同时列出 patch 方案”继续保持为产品策略，但只维护一份文本。
2. **是否开始向模型注入 `readOnly / risk / requiresApproval` 元数据**：建议通过 description 前缀或兼容 metadata 实现，不动 OpenAI Tool 结构。
3. **是否把置信度枚举从 `CONFIRMED / HIGH_CONFIDENCE / POSSIBLE / UNKNOWN` 调整为 `Confirmed / Supported / Inferred / Unknown`**：这会影响现有 JSON schema、前端展示和后端兼容，需要你确认。
4. **是否允许 Agent 在个别场景通过 shell 构造命令（经人工审批后）执行，还是准备收窄到纯粹 parsed-argv 白名单**：这决定 Phase C Code Guard 的强度。
5. **是否在 `Finding` 上增加证据角色和冲突/时间字段**：这会进入扫描报告和 Web UI 数据结构，属于模型层改动。
6. **`llm.SystemPrompt` 与 `agent.Prompt` 是否需要合并成一份**：建议先不合并，只按职责避免重复。
