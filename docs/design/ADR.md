# k8s-ai 一期架构决策记录（ADR）

- 版本：v1.0
- 日期：2026-08-12
- 状态：待评审

## ADR-001 两阶段扫描

- **状态**：已接受
- **决策**：Phase1 只做全量 list；规则发现异常后，Phase2 只对异常 Pod 取日志。
- **理由**：API Server 压力与 LLM 上下文都与 Pod 总数解耦；规范 §18/§32 隐含该要求。
- **后果**：规则必须能在无日志的 Phase1 数据上判定异常（状态 + Events 足够）；Phase2 只补日志证据。

## ADR-002 Scanner 不产 Finding

- **状态**：已接受
- **决策**：scanner 只产出 ClusterSnapshot；异常发现全部收敛到 rule.Engine。
- **理由**：避免"扫描器内嵌判断 + 规则重复实现"两套逻辑漂移。
- **后果**：scanner 不知道规则与 LLM；新增异常类型只需加规则。

## ADR-003 Finding 指纹与 owner 归一化

- **状态**：已接受
- **决策**：`ID = SHA256(kind+group+ns+归一化名+rule+证据签名)`；Pod 有 owner 时用 owner 的 kind+name。
- **理由**：Deployment Pod 名随机，直接按 Pod 名做指纹会导致 1.2 趋势把"持续问题"误判为"恢复+新增"。
- **后果**：同一 Deployment 的 CrashLoop 在多次扫描间指纹稳定；裸 Pod 仍按 Pod 名。

## ADR-004 严重级与评分仅由规则计算

- **状态**：已接受
- **决策**：Finding.Severity 与 HealthScore 只来自 rule/report；LLM 输出的严重等级仅展示参考。
- **理由**：规范 §19/§30 冲突的解决（评审 C3）。
- **后果**：LLM 输出 schema 中 severity 字段只读不改写。

## ADR-005 LLM 输出程序化校验 + 降级

- **状态**：已接受
- **决策**：LLM 输出必须过 JSON Parse → Schema → Evidence ID → kubectl 命令四层校验；失败重试一次后降级为规则结论。
- **理由**：仅靠 Prompt 无法保证不编造（AGENTS.md：LLM 不得编造资源/证据）。
- **后果**：Diagnosis 增加 `LLMUsed/Error` 字段；报告始终可生成。

## ADR-006 脱敏在采集边界 + 渲染边界

- **状态**：已接受
- **决策**：日志/Events/注解进入 Evidence 前脱敏（security.Redactor）；报告写入前二次脱敏。
- **理由**：报告会外发（邮件/飞书/GitLab）；只做一次无法覆盖两条传播路径。
- **后果**：Evidence.Value 带 `Redacted` 标记；测试断言 LLM 请求与报告零敏感值。

## ADR-007 单一 ScanService 编排

- **状态**：已接受
- **决策**：CLI/Server(1.2)/CronJob 全部调用 `service.ScanService.Run`。
- **理由**：AGENTS.md 强制复用同一应用服务；避免三套流程漂移。
- **后果**：cli 是唯一 Presentation；service 是唯一组合根。

## ADR-008 删除应用内 schedule 配置

- **状态**：已接受
- **决策**：应用不实现调度；定时执行由 deploy/cronjob.yaml 承担，应用只支持 `--report-mode daily`。
- **理由**：规范 C1 冲突；避免调度语义双份。
- **后果**：config.yaml 无 schedule 节；CronJob 负责 cron/TZ/重试策略。

## ADR-009 Phase1 只用 list，不用 watch/informer

- **状态**：已接受
- **决策**：一期为一次性扫描，只使用 `ListOptions{ResourceVersion:"0"}` 的 list；RBAC 保留 watch 仅供二期。
- **理由**：避免 informer 常驻连接与状态管理；list 走 apiserver 缓存压力小。
- **后果**：无 watch 测试负担；二期如需增量可再引入 informer。

## ADR-010 Events 按 namespace 一次 list + 本地索引

- **状态**：已接受
- **决策**：每 namespace 一次 `ListEvents`，按 involvedObject.UID 建 EventsIndex，本地 O(1) 查询。
- **理由**：杜绝按 Pod 逐条查事件的 N+1；评审要求。
- **后果**：事件采集并发受 scan.concurrency 限制；单 ns 事件超阈值时截断旧事件。

## ADR-011 一期只用核心组 events

- **状态**：已接受
- **决策**：只采集 `core/v1.Event`（含 count/firstTimestamp/lastTimestamp）。
- **理由**：规范要求这些字段；events.k8s.io v1 无 count/firstTimestamp 等价字段。
- **后果**：RBAC 只授 core events；后续如需 v1 events 再评估。

## ADR-012 Metrics Server 推迟到 1.2

- **状态**：已接受
- **决策**：1.1 不读取 metrics.k8s.io，不依赖 Metrics Server。
- **理由**：规范 C6 冲突；指标非核心诊断闭环的必要输入。
- **后果**：RBAC 无 metrics 资源；报告无 CPU/内存用量（容量数据仍来自 Node status）。

## ADR-013 健康评分公式

- **状态**：已接受
- **决策**：`Score = max(0, 100 − Σ罚分)`；CRITICAL 30 / HIGH 15 / MEDIUM 5 / LOW 1；Correlated 不扣分。
- **理由**：可解释、可测试；避免 LLM 随意计算（规范 §30）。
- **后果**：每条 Penalty 带 Reason；公式与权重集中在 report 包可配置。

## ADR-014 CommandExecutor 只声明不实现

- **状态**：已接受
- **决策**：一期仅定义接口（model），无实现、无调用点；架构测试断言。
- **理由**：规范 §26 允许定义接口；二期安全执行再实现。
- **后果**：LLM 输出的命令永远是字符串，报告渲染即终点。

## ADR-015 依赖方向单向 + model 为领域根

- **状态**：已接受
- **决策**：`cli → service → {domain, infra 端口}`；model 不依赖任何内部包；禁止反向 import。
- **理由**：满足 AGENTS.md 分层；防止循环依赖与架构腐化。
- **后果**：依赖方向由 tests/arch 守护；组合根只有 service。

## ADR-016 Phase1 精简字段 + 分页

- **状态**：已接受
- **决策**：快照保存归一化精简字段（不保存完整 v1 对象）；list 用 `Limit=500 + Continue`。
- **理由**：控制大集群内存与单次响应体。
- **后果**：规则只消费归一化字段；若未来需要完整对象，Phase2 定向 get。

## ADR-017 LLM 上下文自适应预算

- **状态**：已接受
- **决策**：`可送诊数 = min(max_findings, floor(max_total_tokens / max_input_tokens))`，预算不足时下调。
- **理由**：多 Finding 串行可能超总预算（评审 C10）；固定上限不可行。
- **后果**：超出预算的 Finding 保留规则结论 + 降级标注。

## ADR-018 envtest 独立 tag

- **状态**：已接受
- **决策**：envtest 集成测试以 `//go:build integration` 隔离，默认 CI 不执行。
- **理由**：避免测试依赖 kube-apiserver 二进制；保持 `go test ./...` 快速稳定。
- **后果**：`make test-integration` 手动触发；fake 单测是主要防线。

## ADR-019 历史差异数据面向二期 Agent 记忆

- **状态**：已接受
- **决策**：P12 的"对比层 + 迭代诊断层"主要消费者是二期 Chat Agent（scan_cluster Tool 自动读取 latest.json 的历史指纹对比与上次诊断上下文，作为会话记忆），而非手动反复 scan 的人类用户；人读日报趋势降级为次要产出。
- **理由**：人类手动对比两份报告的边际价值低，而 Agent 记忆是该数据的放大器；一期只产出结构化契约（latest.json），符合 AGENTS.md"为二期预留接口"的要求，不提前实现会话功能。
- **后果**：latest.json 的 findings[].id 与 diagnoses[] 设计保持稳定并加注释标注；二期规划约束"scan_cluster 自动携带历史差异，无需用户手动挂载"写入 API.md §12。

## 待决策事项（需在编码前确认）

| # | 问题 | 建议 | 影响 |
|---|---|---|---|
| T1 | RBAC 是否保留 watch | 保留（二期兼容） | 权限面略大 |
| T2 | 大集群分页页大小 | 500 | 请求数/响应体权衡 |
| T3 | Pod 指纹 owner 归一化 | 接受（ADR-003） | 趋势准确性 |
| T4 | `--fail-on` 语义 | 按严重级计数（如 ≥1 HIGH） | 退出码 2 |
| T5 | LLM 输出 schema 固定版 | 固定 JSON Schema v1 | 校验强度 |
| T6 | 报告时间戳时区 | CronJob 显式 TZ；本地默认本地时区 | 日报可读性 |
| T7 | 单 ns 事件截断阈值 | 5000 条 | 内存 |
| T8 | envtest 是否纳入默认 CI | 否（ADR-018） | 测试复杂度 |
