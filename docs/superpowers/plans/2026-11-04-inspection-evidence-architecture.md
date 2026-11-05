# 体检证据链与故障链架构收口 Implementation Plan

> **For agentic workers:** Use TDD for every task. Stop and verify after each task.

**Goal:** 在不重写扫描器的前提下，统一证据引用、根因事实源与故障链语义，减少歧义和重复因果边。

**Architecture:** 保留现有 Scanner → Rule → Relations → Incident → Deterministic → LLM → Validate → Report 分层，先修数据契约与事实源，再优化 Incident 因果边生成。

**Tech Stack:** Go、client-go、Vue 3、Vitest

---

### Task 1: 全局 EvidenceRef

- [x] 添加 `FindingID#EvidenceID` 统一引用工具与测试。
- [x] Incident 与 CausalEdge 只保存 canonical refs。
- [x] Diagnosis 校验后输出 canonical refs，同时兼容旧 E-ID 输入。
- [x] LLM 上下文与报告渲染使用 canonical refs。

### Task 2: 单一根因事实源

- [x] 最终合并 Diagnosis 后，将根因、状态、置信度、来源同步为 Incident 的最终投影。
- [x] 添加服务层回归测试，确保旧规则根因不会覆盖 LLM/确定性最终结论。

### Task 3: Finding 角色

- [x] 增加 `anchor / symptom / impact / advisory / independent` 角色。
- [x] 在 Incident 与关联分析完成后统一分配。
- [x] 报告和 Web 用角色解释“主信号、派生影响、建议”，不再根据标题猜。

### Task 4: 因果边剪枝

- [x] 将 Incident 内两两合法关系改为从 Root 生长的一棵候选因果树。
- [x] 每个 symptom 最多保留一条最能解释它的传播边。
- [x] 保留确定性诊断器写入的更具体边，避免重复。

### Task 5: 诊断账修正

- [x] `DiagnosisSummary.RuleFindings` 写入本次真实 Finding 数。
- [x] 补服务层断言，防止报告显示 0 条规则异常。

---

### Verification（2026-11-05）

- `go test ./...`：通过。
- `go vet ./...`：无告警。
- `go build ./...`：通过。
- `web npm run test`：30 个测试文件、205 个用例通过。
- `web npm run typecheck` / `npm run lint` / `npm run build`：通过。
- 文档已统一到最终 `Diagnosis` 单一事实源、`FindingID#EID`、Finding 角色、故障树单入边与
  退出码 0 诊断语义；官方权威见 `docs/02-架构与设计/18-方案-体检诊断体系升级-四期.md` §16–§17。
