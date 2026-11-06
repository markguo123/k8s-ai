# AegisOps 体检能力接入 OpsPilot 合作方案

- 日期：2026-11-05
- 受众：项目负责人、OpsPilot 团队及管理层
- 定位：对外合作建议，不改变 AegisOps 当前产品路线和研发基线

## 1. 建议结论

建议把 AegisOps 的 体检能力抽成**独立能力服务**，以 **Remote HTTP MCP 作为主接入方式**，优先支持 OpsPilot 已具备的 Streamable HTTP；Go CLI 仅作为离线批处理、CI 或特殊部署环境的备用入口。

OpsPilot 本身在 Agent 编排、综合问题排查与处置方面具备更强的产品能力。AegisOps 不承担、也不替代这些职责，定位是可独立部署并通过 MCP 或 CLI 接入的 **Kubernetes 增强体检能力**，为 OpsPilot 补充标准化巡检结果、故障关联与证据化事实。

![AegisOps Kubernetes 体检诊断总体架构图](<../assets/images/aegisops Kubernetes 体检诊断总体架构图.png>)

报告展示

![image-20261106175727708](C:\Users\Huawei\AppData\Roaming\Typora\typora-user-images\image-20261106175727708.png)

## 2. AegisOps 体检的差异化设计

相较以资源清单、阈值告警和单项检查为主的传统容器平台巡检，AegisOps 的体检能力重点不是“列出更多异常”，而是把异常收敛成可解释的故障结论。

1. **从告警清单升级为故障链和根因。** 基于 OwnerReference、Selector、Spec/Status 等真实关系，将 Pod、工作负载、存储、网络和节点异常关联成 Incident，定位根因及其影响链。
2. **结论有证据，过程可复核。** 每个关键结论引用 Kubernetes Events、对象状态、容器状态、日志和配置片段；证据不足时明确标记 `unknown` 或 `probable`，不以推测冒充确定结论。
3. **确定性诊断打底，LLM 只做增强。** 没有模型也能完成 CrashLoop、镜像拉取、调度失败、挂载失败、节点压力、PVC、Service/Ingress、HPA、PDB、CoreDNS 等常见场景诊断；LLM 输出还要经过证据和资源真实性校验，不能覆盖已确认事实。
4. **数据质量优先。** 采集失败不会被解释为“资源正常”；健康评分会受采集完整性封顶，并明确展示数据缺口。
5. **轻量、可嵌入。** 不依赖额外监控平台，直接通过 Kubernetes API 做只读采集，支持全集群、Namespace、单 Pod 三种范围和快速、标准、深度三档体检；JSON、Markdown、Web 展示共用同一份诊断结果。

核心差异可以概括为一句话：**传统巡检回答“哪里报警”；AegisOps 回答“为什么坏、影响了谁、依据是什么、下一步查什么”。**

## 3. 报告输出与留痕

完整报告是 AegisOps 体检的重要交付物，不只是体检结果的附属展示。相较常见云平台内置巡检以资源状态、阈值告警和固定模板为主，AegisOps 的报告直接包含根因推理、故障影响链、证据引用、处置建议、采集质量和诊断模式，并支持确定性诊断与可选 LLM 增强。

报告内容生成、Markdown 渲染和归档均在 AegisOps 服务端完成；Web 前端只负责交互式展示。因此，MCP/CLI 可以输出完整报告数据、Markdown 正文或受控下载链接，但不会透传 AegisOps 的 Vue 交互式页面。

| 交付形态 | 内容 | 适用场景 |
| --- | --- | --- |
| 结构化 JSON | 健康评分、结论、Incident、根因、影响链、证据与处置建议 | OpsPilot 二次分析或自建报告页面 |
| Markdown | 后端生成的完整人读报告 | 工单附件、邮件、知识库和审计留痕 |
| 受控下载链接 | AegisOps 已归档的报告文件 | 客户下载、问题复盘和报告留存 |
| Web 报告页面 | 故障链、证据和处置建议的交互式展示 | AegisOps 原生页面或受控跳转查看 |

对于需要报告留痕的客户，AegisOps 输出的不是一份简单的资源清单，而是一份可解释、可复核、可归档的故障诊断报告。

## 4. 对 OpsPilot 的增强价值

AegisOps 不与 OpsPilot 的问题排查和处置能力竞争，而是作为一个可选的独立巡检工具，为 OpsPilot 提供更高信息密度、更易复核的 Kubernetes 体检输入。

| 价值 | 具体表现 |
| --- | --- |
| 增加一种独立巡检入口 | 在 OpsPilot 现有排查、处置和工具编排能力之外，补充主动式、批量化的 Kubernetes 体检入口。 |
| 提供高信息密度的诊断上下文 | 一次调用即可获得健康评分、重点故障、资源关系、影响链和证据，为 OpsPilot 建立全局视图提供标准化输入。 |
| 让问题排查更易复核 | 结论附带 evidence、可信度、采集质量和阶段记录；OpsPilot 可据此继续推理、展示或决定后续处置，而不是依赖不可追溯的摘要。 |
| 强化报告输出与留痕 | 提供可直接归档的 JSON/Markdown 报告和受控下载链接，满足客户工单留档、问题复盘和审计取证需要。 |
| 保持产品边界和选择权 | AegisOps 只提供体检事实和建议；是否调用、如何解释、是否审批及如何处置，仍由 OpsPilot 根据自身能力和策略决定。 |

## 5. 推荐集成方式

```text
OpsPilot Agent
    → 外部 MCP 工具目录 / tools/call
    → AegisOps MCP Adapter
    → 现有 ScanService
    → Kubernetes API（只读）
```

建议第一版暴露以下 MCP 工具：

| 工具 | 作用 |
| --- | --- |
| `start_inspection` | 按 `scope`、`namespace`、`pod`、`preset` 发起体检，立即返回 `scanId` |
| `get_inspection_status` | 返回任务状态、当前阶段、耗时和失败原因 |
| `get_inspection_report` | 返回健康评分、总体结论、重点 Incident、根因、影响链和数据质量 |
| `get_inspection_markdown` | 返回后端生成的完整 Markdown 报告，供工单、邮件和知识库留痕 |
| `get_inspection_artifact` | 返回受控下载链接，读取 AegisOps 已归档的报告文件 |
| `get_inspection_evidence` | 按 Incident/Finding 分页获取证据，避免大报告超过 MCP 结果上限 |
| `cancel_inspection` | 停止本次体检 |

体检必须采用**异步任务模式**，不能把一次 5～10 分钟的完整体检塞进一次工具调用。按 OpsPilot 当前实现，单次 MCP 调用存在默认 30 秒超时和约 1 MB 结果上限，因此应先返回 `scanId`，再由 OpsPilot 轮询状态、按需读取摘要和证据。

MCP 适配器应直接复用 AegisOps 现有应用服务和统一报告模型，不复制一套扫描逻辑；CLI、Web、MCP 最终共用同一个体检内核。建议以 Streamable HTTP 为首选传输方式，SSE 仅作兼容。

## 6. Go CLI 的定位

Go CLI 可以作为第二条适配器，适合离线环境、定时任务、CI 流水线或不方便接入 Remote MCP 的场景。典型输出应是稳定 JSON 到标准输出，并通过退出码表达“有发现/无发现/执行失败”。

但 CLI 不适合作为首版主路径：OpsPilot 需要在 Agent 运行环境中分发二进制、管理进程和长时间任务，还要自行处理报告文件与轮询。现有 OpsPilot 已支持外部 Remote MCP，直接接入的成本和产品体验更好。

## 7. 首版 PoC 与双方边界

首版建议只覆盖一个测试集群、一个 Namespace、标准体检，以及任务、报告、Markdown、下载链接和分页证据等核心工具，验证通过后再扩展多集群与定时体检。

AegisOps 提供：

- MCP Endpoint、工具 Schema、报告 Schema 和示例响应；
- 专用只读 ServiceAccount、集群/Namespace 授权范围和审计记录；
- 测试环境、故障样例及版本兼容说明。

OpsPilot 评估并实现：

- MCP 注册、工具发现、用户授权、审批策略和调用审计；
- Agent 提示词、状态轮询、报告展示方式，以及选择摘要、Markdown、下载链接或自行渲染结构化报告；
- 是否展示 AegisOps 的 LLM 增强结论，或仅消费确定性事实后由 OpsPilot 二次推理。

首版安全边界建议为：固定 HTTPS Endpoint 和 Bearer 凭据；只接入只读 Kubernetes 身份；禁止返回 Secret 内容，只保留名称、键名和引用；每次调用记录来源、范围、`scanId` 和结果状态。由于 OpsPilot 当前不会把用户 OIDC 透传给外部 MCP，第一版应采用**服务级只读身份**：OpsPilot 负责面向用户的权限控制，AegisOps 负责集群/Namespace 白名单和调用审计。

PoC 通过标准：

1. OpsPilot 能完成 MCP 注册、工具发现并成功发起一次体检。
2. 能在 30 秒内返回任务句柄，并持续获取真实阶段状态。
3. 能读取健康评分、重点故障、根因、影响链、证据引用和采集质量。
4. 能获取完整 Markdown 报告或受控下载链接，并完成工单附件或归档留痕。
5. 大报告可通过分页证据或文件下载读取，不发生超时、截断或上下文失控。
6. 任何失败都明确返回“未执行、执行失败或数据不完整”，不得表现为“集群健康”。

总体判断：**AegisOps 是 OpsPilot 的独立增强巡检组件。MCP 是推荐主线，CLI 是备用通道；AegisOps 提供可验证的 Kubernetes 体检结论和可归档的完整报告，OpsPilot 结合自身已有能力决定如何使用、展示和处置。**
