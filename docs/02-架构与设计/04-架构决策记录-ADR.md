# k8s-ai 架构决策记录（ADR）

- 版本：v1.0 → v4.0.0（四期新增 ADR）
- 日期：2026-08-12（首版） / 2026-10-10（四期平台底座 ADR） / 2026-10-14（部署形态与前端交付口径） / 2026-10-22（新增 ADR-036 前后端分离部署形态） / 2026-10-23（新增 ADR-037 权限分层与会话隔离、ADR-038 Scope-aware Agent） / 2026-10-26（新增 ADR-039 会话 API 包更名） / 2026-10-27（新增 ADR-040 动态配置读取优先级、ADR-041 前端产物内嵌与镜像可复现构建、ADR-042 存储主口径与迁移入口拆分；ADR-031 v1.14、ADR-040 v1.2） / 2026-10-28（新增 ADR-043 存储边界与对象存储、ADR-044 容器执行通道与镜像基座；ADR-042 落实状态回写） / 2026-10-29（**ADR-044 修订 v1.1 口径翻转**：默认交付镜像内置 shell 与 kubectl，ADR-041 第 7 条同步修订，T9 收口措辞更新）
- 状态：四期企业平台落地中（ADR-020~044）

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
- **补充（2026-08-18）**：kubectl 命令的命名空间校验真正生效——命名空间级资源命令必须携带 `-n/--namespace`（含 `-n=`/`--namespace=` 形式，动词前/后均可）；命令自带 namespace 与 Finding 不一致视为编造/笔误整条丢弃；缺省时工具自动补全 Finding 命名空间（保证报告命令复制即用）；显式 `-A`/`--all-namespaces` 豁免；`logs` 动词按 Pod 处理参与同一套校验/补全。

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

## ADR-020 前端工程改为 Vite + Vue3 + TS 单仓（四期 P1.1/P2.1）

- **状态**：已接受（2026-10-08 落地 P1.1，2026-10-08 调整 P2.1 信息架构）
- **决策**：四期前端在 `web/` 下新建 Vite + Vue3 + TypeScript 工程，替代三期单体
  `internal/webui/index.html`；侧栏一级导航按 工作区（聊天/集群）/ 治理（审批中心/审计）/
  平台管理（Skill/配置中心/用户与权限）三组分栏，Admin 不再作为侧栏一级菜单，
  当前用户信息与「管理控制台」迁入右上角 User Menu。
- **理由**：三期单文件 SPA 便于零服务端改动交付，但平台化需要组件化、路由与权限三视图
  （Route/Component/Button）支持（需求 v1.2 §10/§11，决策 D-15）；Vue3 + TS + Vite 与
  现栈一致且工程门槛低。
- **后果**：前端与后端解耦为 `web/` 工程（dev server 5173 与 server 端口分离）；
  权限判定仅为 UX 展示，后端强校验原则不变；三期 Web Chat 事件契约在迁移期由映射层兼容。

## ADR-021 统一 Store + SQLite/MySQL 方言层（四期 P1.2/P1.3）

- **状态**：已接受（P1.2 2026-10-08，P1.3 2026-10-08）
- **决策**：`internal/store` 提供统一 `Store` 接口（User/Role/Session/Audit/Settings/Skill/
  Approval/Memory/Retention/Cluster/Credential + Turns/Steps），SQLite 为默认驱动，
  MySQL 通过共享 schema 与方言层（dialect）等价实现；迁移入口统一、幂等，schemaVersion=2。
- **理由**：SQLite 面向单进程/单实例，MySQL 面向多副本或已有数据库（需求 v1.2 §20）；
  业务层只依赖 Store 接口，两驱动行为等价由统一 CRUD 测试保证。
- **后果**：新增表需同步提升 schemaVersion 并补迁移 + 双方言测试；MySQL 集成用例由
  `K8S_AI_TEST_MYSQL_DSN` 环境变量启用。

## ADR-022 产品内登录认证为内存 Session Token（四期 P1.5）

- **状态**：已接受（P1.5 2026-10-08）
- **决策**：产品内认证采用 User/Role 模型 + argon2id 密码哈希 + 内存 Session Token
  （TTL 24h，无持久化）；`server` 命令 SQLite 首次引导用 `K8S_AI_ADMIN_PASSWORD`
  初始化 admin 角色与用户。
- **理由**：平台化优先打通登录闭环与越权门禁；Session 持久化/刷新令牌/SSO 属四期后续
  P3 范围（需求 v1.2 §18§19）。
- **后果**：认证中间件目前只注入用户上下文、不阻断未登录请求（占位阶段）；重启后会话
  失效需重新登录；产品内权限不进入 CLI 执行路径。

## ADR-023 后端强制权限门禁（Role→Permission→Policy，四期 P1.6）

- **状态**：已接受（P1.6 2026-10-08）
- **决策**：`internal/permission` 定义 Permission 常量、Rule/Policy、Enforcer（支持精确、
  `*` 全量通配与 `cluster.*` 段通配）；`internal/server` 对 `/api/v1` 统一门禁：
  未登录 401、越权 403、未登记路由默认 403；业务代码只声明所需 Permission，
  不散落 `role == "admin"` 判断。
- **理由**：前端权限仅 UX，后端必须是强校验边界（需求 v1.2 §17）；未登记默认拒绝
  避免新增接口漏配权限造成越权。
- **后果**：新增 API 路由需在注册处登记权限规则；产品内权限不影响 kubeconfig RBAC
  决定的真实读写能力。

## ADR-024 Turn/Step 生命周期用观察者持久化（四期 P1.7/P1.8）

- **状态**：已接受（P1.7 2026-10-08，P1.8 2026-10-10）
- **决策**：Agent 领域新增 `session.Turn`（6 态：pending/running/waiting/done/failed/
  canceled）与 `session.Step`（4 态：running/waiting/done/failed，类型 reason/tool/
  observe/approval/response）状态机，token 用量持久化；`internal/agent` 通过可挂载
  `Tracer` 观察者把生命周期广播给 service 层（`NewChatWithStore` 落 SQLite）。
  Agent 本身不依赖 Store，回调一律观察性质，写库失败只告警不阻塞主循环。
- **理由**：保持 Agent 循环纯业务化、可测试（AGENTS.md 分层），同时为多轮回放
  （P1.7 验收）与全链路可观测关联（P4.9）提供统一数据结构。
- **后果**：每轮 Chat 产生 1 Turn + N Step（reason/tool/observe/approval/response）；
  `session.Store`（三期文件会话）与 `store.Store`（四期持久化）并存；
  `NewChat` 旧签名保持兼容，`NewChatWithStore` 为持久化入口。

## ADR-025 平台主体映射 + namespace 授权与 RBAC 派生/对账（四期 P4.6）

- **状态**：已接受（2026-10-13 方案评审）
- **决策**：平台用户映射为 Kubernetes 主体 `k8s-ai:user:<id>`、组为 `k8s-ai:group:<id>`；
  授权以 namespace 为唯一边界，AuthzGrant 为唯一权威、Kubernetes Role/RoleBinding 为
  派生结果；受管 RBAC 带 `k8s-ai.io/managed=true` 与 ownerReference，定期 reconcile
  消除漂移与孤儿对象，grant 撤销/过期即删；执行前 SubjectAccessReview 只验权限。
- **理由**：强制点必须在 API Server；避免平台与集群出现"两套真相"；撤销/过期能立即回收
  集群权限。
- **后果**：`internal/store` 新增 `AuthzGrant`；`internal/kubernetes/client.go` 支持按
  平台主体构造客户端（impersonation / OIDC 预留）并执行 SAR。

## ADR-026 多 Agent 注册表 + 首个 Approval Agent（四期 P4.10）

- **状态**：已接受（2026-10-13 方案评审）
- **决策**：平台演进为多 Agent 编排架构；建立统一 Agent 注册契约（agent_id / name /
  desc / capabilities / scope / executor / policy_binding / lifecycle）；首个实现为
  审批子 agent（Approval Agent），主 Agent 按 capability + scope 路由。
- **理由**：自动审批不是散落的 if 分支，而是独立执行主体；为后续可观测/安全/成本类
  能力型子 Agent 预留统一权限与审计接入口。
- **后果**：`internal/agent` 新增 registry 与调度契约；所有子 agent 写仍走统一
  风险目录 → 审批策略 → SAR → Code Guard → Executor → 准入控制 → 审计，不设豁免。

## ADR-027 销毁类仅 admin + operator 显式委派 + 确定性拒绝（四期 P3/P4）

- **状态**：已接受（2026-10-13 方案评审）
- **决策**：`delete namespace / delete pvc / delete pv` 默认仅 admin；operator 需要时由
  admin 显式委派（记录在案、可回收、有过期时间）；user 与未被委派的 operator 发起时
  确定性拒绝并提示联系管理员，不发起审批申请、不阻塞等待；有授权销毁类二次确认并留痕。
- **理由**：避免普通用户触发跨 namespace / 销毁级等待审批；销毁权限全部收敛给 admin，
  委派可回收，权限面可审计。
- **后果**：审批中心增加"确定性拒绝（联系管理员）"记录；权限矩阵与前端原型同步刷新。

## ADR-028 自动审批显式白名单 + 总开关/会话开关（四期 P4.5）

- **状态**：已接受（2026-10-13 方案评审）
- **决策**：自动审批采用显式白名单（`get/list/watch/describe/logs/events` 与
  `dry-run/diff/plan` 恒自动；`scale` 有界可自动；`rollout restart` 需确认；白名单外写
  至少自确认；delete namespace/pvc/pv、shell、未知/危险字段、回收类、无法解析命令
  永不自动）。总开关默认关闭，开启后会话级可开关；每次 AUTO 记录
  `whitelist_key + plan_hash + reason` 并写审计；总开关不能覆盖高危类。
- **理由**：以"可逆"判定自动范围模糊且危险；显式白名单可审计、可配置、可解释。
- **后果**：审批决策链增加"风险目录 → 自动审批白名单 → scope/RBAC → 自确认判定"；
  Approval 记录模型增补 `whitelist_key` 等字段。

## ADR-029 四期平台主进程为 `server`（默认内嵌 SPA，支持分离交付）

- **状态**：已接受（2026-10-14 口径收口；2026-10-22 按 ADR-036 扩为双形态）
- **决策**：四期平台采用「前后端工程分离 + 单二进制一体化交付」架构。前端是独立工程
  （`web/`，Vite + Vue3 + TS），后端为 Go 模块化单体。默认部署形态为一体化
  （形态 A）：同一进程、同一端口内提供 `/api/v1`（含 Chat SSE）与 SPA 静态资源
  （`web/dist` 经 `go:embed` 内嵌，需 SPA history fallback 与静态资源缓存策略）。
  同时按需支持前后端分离部署（形态 B）：前端独立发布 `web/dist` 由 Nginx 等静态服务托管，
  后端只提供 API、关闭静态资源路由（以最小开关改动实现，禁止大规模重构/拆微服务/
  重写前端 API 层）。两种形态共用同一套业务代码与 `/api/v1` API 契约。
  Session 与 SSE 是平台 API 的一部分，统一位于 `/api/v1/sessions*`，与认证共用同一主进程。
- **理由**：工程结构做前后端分离、把 `web/` 独立为产品代码的一部分，与「默认一体化交付、
  按需分离部署」并不冲突——软件产物和部署拓扑解耦。默认一体化可降低单机/POC/离线交付与
  日常升级成本；企业已有静态托管/网关/CDN 规范时需要分离交付。二者共享同一 `/api/v1` 契约，
  避免双套 API 平行维护。
- **后果**：Chat 相关端点以 `/api/v1` 形式重建（复用 `service.ChatService` 与 `internal/sse`
  事件出口）；分离模式下后端静态路由关闭、页面唯一由前端服务回流；开发计划 P6.1 与 PRD §26 的
  「单进程入口 = `k8s-ai web`」口径修订为「默认一体化、可选分离」；前端默认同域相对路径
  `/api/v1`（支持 `VITE_API_BASE` 覆盖），不把跨域/独立子域作为当前强制需求；落地任务见 P6.11，
  形态详见 ADR-036。

## ADR-030 平台进程默认单副本 + 横向扩展前置条件

- **状态**：已接受（2026-10-14 口径收口）
- **决策**：四期平台进程在 Kubernetes 中默认 `replicas: 1`。扩副本的前置条件必须同时满足：
  ① 存储切到 MySQL 驱动；② 会话/Token 状态持久化（不依赖进程内存态）；③ SSE 事件与审批
  pending 通道有跨副本方案（本期不引入 Redis）。未满足前调大副本视为超出四期范围。
- **理由**：SSE 订阅扇出与审批等待通道当前都是进程内 map，内存态 Session Token 重启即失效，
  SQLite 只支持单实例写；多副本会表现为随机丢事件、审批 404、登录态抖动，且难以定位。
- **后果**：部署清单与文档写明单副本及原因；多副本能力不在四期承诺范围内，需要时单独立项。

## ADR-031 平台配置面：`platform:` 静态段 + 7 组动态键唯一落 DB `settings`

- **状态**：已接受（2026-10-14 方案定稿；2026-10-21 v1.11 修订：明确 7 组动态键唯一权威源；2026-10-26 v1.12 修订：固化 server 静态 `config.yaml` 与 CLI 直用 `~/.k8s-ai/cli.yaml` 两份独立配置；2026-10-27 v1.13 修订：动态键运行时新增环境变量覆盖层；v1.14 修订：覆盖层语义收窄为「密钥态 env 恒优先 + 种子态 seed-once（DB 有值即让位）」，覆盖面冻结为 LLM 四键 + ops 四键，见 ADR-040 v1.2；①②⑥ 仍待 P6.12，⑤ 运行时消费前移至 P6.1 本批）
- **背景**：四期平台级参数（监听地址、平台库路径/DSN、管理员用户名、Token 有效期）当前只存在于
  `server` 命令行开关与进程环境变量中，配置文件同时覆盖 llm/agent/scan 等引擎参数。若引擎参数同时保留在
  YAML 与页面配置中心，会形成"文件 vs 页面"两处活配置漂移，并发修改互相覆盖；同时三期 `config init`
  模板漏了 `kubernetes.kubectl_path`、`llm.reasoning_fields`、`agent.approval_mode`、`agent.create.*`、
  `agent.secret.access` 等已支持键，按模板配置的人会以为这些能力不存在。
- **决策**：
  1. 配置文件新增 `platform:` 段**只承载静态/启动级**参数：`server.addr`、`server.tls.{enabled,cert_file,key_file}`、
     `store.driver`（sqlite | mysql）、`store.sqlite_path`、`store.dsn`、`auth.admin_username`、
     `auth.token_ttl`、`auth.session_store`（本期固定 memory）；它是可评审、可 diff、可回滚的部署描述。
  2. **7 组动态键（model/skill/approval/memory/retention/mcp/ops）彻底移出 `config.yaml`，唯一权威源是
     DB `settings` 表**：平台部署后由管理员在配置中心页面维护，server 启动只读 `platform:` 静态段，
     Chat/Agent/LLM 等运行时组件统一从 Store 读 DB；页面改动只写 DB、不回写 `config.yaml`，改配置文件
     也不覆盖 DB 动态键，不存在双权威。
  3. 敏感值只走环境变量 / Secret：`K8S_AI_ADMIN_PASSWORD`（首启引导 admin，不进配置文件）、
     `K8S_AI_LLM_API_KEY`、需要时 `K8S_AI_PLATFORM_STORE_DSN`；YAML 只放非敏感默认值。模型 api_key、
     MCP env、kubeconfig 等动态敏感值加密落 DB（`SecretCipher`，`K8S_AI_ENC_KEY`），明文永不回读。
  4. 默认加载路径按命令面区分：`k8s-ai server` 默认读静态 `config.yaml`（仅 `platform:` 段），
     `k8s-ai chat/scan` 默认读 `~/.k8s-ai/cli.yaml`，两者均支持 `--config` 覆盖并保留 `--addr` / `--db`
     覆盖手段与兼容。`config init` 默认只输出 server 静态模板（仅 `platform:` 段），`config init --cli`
     输出 CLI 直用全量模板（llm/agent/scan/kubernetes 等全量引擎键 + CLI 直用缺省，文件头注明仅 CLI 读取、
     平台不读取也不回写）；`cli.yaml` 缺失时回退读取旧 `config.yaml` 兼容存量用户。静态段优先级沿用
     **CLI > 环境变量 > YAML > 默认**。（v1.13 补充：本条只管**静态段**；7 组动态键的运行时取值另有优先级
     与环境变量覆盖面约定，见 ADR-040。）
  5. `config init --cli` 模板承载全量引擎键与 CLI 直用缺省，`config init` 默认模板只输出 server 静态/
     `platform:` 启动键；两个模板键集合分别由测试锁定，防止模板与代码再次漂移。`config validate` 支持
     `--mode server|cli` 分别校验静态段与全量引擎键，对未识别键给出显式告警，拼错的键不再被静默忽略。
  6. `migrate` 只作**显式一次性迁移**：三期旧 `config.yaml` 的动态键仅在管理员显式执行迁移时导入 settings
     表，导入后以 DB 为权威；不承担"启动时自动全量导入"的职责。平台 schema 升级另设 `k8s-ai db migrate`
     入口，见 ADR-042。
  7. （v1.13 新增，v1.14 修订）**动态键以 DB `settings` 为唯一权威源与唯一写入目标**，环境变量按两档注入：
     **密钥态**（`K8S_AI_LLM_API_KEY`、`K8S_AI_PLATFORM_STORE_DSN`）env 恒优先；**种子态**（`K8S_AI_LLM_{PROVIDER,ENDPOINT,MODEL}`
     与 ops 四键 `K8S_AI_{KUBECONFIG,KUBE_CONTEXT,NAMESPACE,KUBECTL_PATH}`）仅当 DB 该键无值时生效，页面保存过即以 DB 为准。
     覆盖面冻结为 LLM 四键 + ops 四键并由测试锁定，`TEMPERATURE/MAX_TOKENS/TIMEOUT` 不纳入 env 覆盖；内置默认仅兜底，
     容器内未配置时 Chat 显式报错不静默失败；页面保存只写 DB、即时生效。完整口径与理由见 ADR-040 v1.2。
- **理由**：企业化部署要求"一份 ConfigMap（静态）+ 一份 Secret（敏感）+ DB settings（动态）"即可完整描述
  平台运行参数，且该描述可评审、可 diff、可回滚；动态运营配置需要在线页面维护，唯一权威源只能落在 DB。
  终端二进制直用（`k8s-ai chat / k8s-ai scan`）不依赖平台 DB，仍以本地 `cli.yaml`（缺失时回退 `config.yaml`）
  + env + flags 静态输入运行，与平台配置中心执行面隔离，互不覆盖；两份配置文件完全独立、互不回写。
- **后果**：`server` 命令需接入配置加载（当前只读 flags）；`deploy/configmap.yaml` 与部署手册 §3、§4.9
  同步；`config init` 默认输出 server 静态 `config.yaml`，新增 `--cli` 输出 CLI 全量 `cli.yaml`；7 组动态键
  唯一落 DB（settings API/脱敏/加密与前端 SettingsView 已随 P3.9/P3.10 落地）。落地任务 P6.12。
- **落实状态（v1.13 更新）**：配置中心后端（settings CRUD/脱敏/加密）与前端 SettingsView、`platform:` 段
  接入与消费、本人改密端点已落地；**运行时动态消费前移并入 P6.1 本批**（含 env 覆盖层与未配置显式报错）；
  剩余待 P6.12：双份配置模板（默认 server 静态 + `--cli` 全量）、模板键集合测试锁定、`--mode server|cli`
  校验与未识别键告警、env 覆盖面键集合测试锁定。


## ADR-032 SSE 展示与顺序契约（分片不是展示单元，终态必须最后）

- **状态**：已接受（2026-10-14 随新前端 Chat 修复定稿；后端契约测试已落地）
- **背景**：新前端 Chat 实测出现报告三块重复、永久"正在生成…"、报告塌成纯文本、刷新后不订阅、
  思考正文全丢。根因不是单个组件 bug，而是 PRD §8 只列了事件清单、没有规定**顺序与展示语义**，
  实现层各自解释：后端把 `message.delta` 当独立展示单元并让 index 参与前端 key；终态事件先于最后
  一段正文下发；流式增量不脱敏；`reason.started` 晚于本轮的 reasoning 增量。
- **决策**（四条强制约束）：
  1. **分片不等于展示单元**：`message.delta` 是传输分片，展示层按 turn 聚合，一个回合只有一块最终
     报告；`index` 语义 = 同一 role 内从 0 开始的分片序号，仅用于去重与排序，**不得参与展示 key**；
     JSON 也不得用 `omitempty` 把 0 隐藏掉（`MessageData.Index` 已去掉 omitempty，计数按 role 分开）。
  2. **终态必须最后**：顺序固定为 `message.delta…` → `message.completed`（携带权威全文）→
     `session.completed` / `session.failed`。后端必须在转发协程收尾（`<-done`）之后才发终态事件；
     `turn.completed.output` 只作兜底、不单独成块；终态之后到达的分片由前端忽略。
  3. **事件流统一脱敏**：写入 SSE 的内容（含增量）在封装前走与终态同一套脱敏策略，避免同一段正文
     在流式过程中与终态事件里呈现不一致。
  4. **思考面板语义**：`reason.started` 必须早于本轮 reasoning 增量（Reason Step 在 LLM 调用之前
     开始）；前端按增量懒建面板、空面板不占位；报告出现后折叠、用户可手动展开。
  5. **历史视图与实时视图同构**：刷新后的展示项数量、顺序、分组必须与实时视图一致（允许的差异
     只有流式动画与耗时字段）。落实方式是**落库时就写对位置**：思考按「每轮 LLM 调用一条消息」
     就地追加，位置先于该轮的工具消息，因此回放天然是「思考 → 工具 → 思考 → 报告」；
     禁止把整轮思考合并成一条再插到报告之前（旧 `persistReasoning` 行为，已删除）。
- **理由**：违反其中任何一条都会产生用户可见故障，且很容易被"再刷新一次"掩盖；把契约写进 PRD 与
  ADR 后，后续任何一侧改动都有据可依，也能用测试守住。
- **后果**：PRD §8 增补"事件顺序与展示语义"；前端设计方案 §5/§7 增补渲染红线；后端
  `internal/service/chat_sse_test.go::TestChatServiceEventOrdering` 锁定顺序与 index 语义；
  前端契约测试纳入 P2.12（MessageTimeline 事件聚合、MarkdownView 渲染）。
- **落实状态**：后端与四期前端均已落地（2026-10-14 实测：末回合报告 1 块、markdown 结构标签 30 个、
  无残留"正在生成…"、冷启动自动订阅）；第 5 条于 2026-10-15 补齐并用真模型实测
  （落库顺序为 思考→工具→思考→工具…→报告，与实时视图同构）。回归保护：
  `internal/service/chat_sse_test.go::TestChatServiceEventOrdering`、
  `chat_test.go::TestChatServicePersistsReasoningInRoundOrder`、
  `web/.../MessageTimeline.spec.ts`（8 条前端契约用例）。

## ADR-033 前端主题系统：`data-theme` + 令牌层，默认深色、支持跟随系统

- **状态**：已接受（2026-10-15；实现见 P7.1，方案见 `14-方案-前端体验优化-四期.md` §2）
- **背景**：`web/src/styles/index.css` 已有 `--app-*` 令牌层并与原型 `:root` 一一对应，但没有任何主题切换能力
  （无 `data-theme`、无 `prefers-color-scheme`、无持久化；仅引入了 Element Plus 的 dark 变量表）。四期要交付
  企业级控制台，需要亮/暗两套可切换外观，且刷新不得闪白。
- **决策**：
  1. 以 `<html data-theme="dark|light">` 承载应用令牌，同时同步 `<html class="dark">`（Element Plus 深色变量），
     两套变量必须同时切换，禁止各自为政。
  2. 模式为 `light | dark | system`，**默认 `dark`**（产品基线是深色控制台）；`system` 模式监听
     `matchMedia('(prefers-color-scheme: dark)')` 的 change 事件。持久化键 `k8s_ai_theme_mode`。
  3. 首屏由 `index.html` 内联脚本（早于样式与 bundle）同步设置属性，禁止等 Vue 挂载后再切，以消除刷新闪烁。
  4. 组件禁止写死颜色，一律走令牌；新增令牌必须同时给出亮/暗取值，并在方案文档 §2 登记。
  5. 滚动条样式纳入令牌层（WebKit 与 Firefox 双实现），杜绝系统白色滚动条。
- **理由**：令牌层已经存在，最小改动是"补一套亮色取值 + 一个切换开关"；若改用 UI 库的主题插件或 CSS-in-JS，
  等于推翻现有与原型对齐的令牌体系，收益为零、返工面大。默认深色而非 system，是因为 CLI/控制台场景以深色为主，
  且原型定稿即为深色底座。
- **后果**：所有页面（含 P3.9 配置中心、P3.12 用户与权限、P4 审批中心）自动获得双主题；新增样式必须双主题自检；
  组件级测试需覆盖"无存储时默认 dark""system 跟随媒体查询""切换后持久化"。

## ADR-034 工具结果统一契约：`tool_result` 同源于事件流与落库

- **状态**：已接受（2026-10-15；实现见 P7.4，方案见 `14-方案-前端体验优化-四期.md` §6）
- **背景**：工具结果的展示在两条路径上不一致，且已构成用户可见缺陷：
  事件流带的是 `truncateText(tr.Text, 240)`（`internal/agent/loop.go`），前端只能用 240 字文本做启发式判断，
  多数情况退化成 text/logs；落库带的是完整 `Data`（同一文件 `AddMessage(..., Data: tr.Data)`），刷新后才有结构化表格。
  耗时与风险根本没有落库，刷新后头部 `duration: null`、`risk: ''`（`web/src/components/chat/MessageTimeline.vue`）。
- **决策**：
  1. 工具结果统一为 `tool_result`：`{tool, summary, displayType, columns, items, raw, durationMs, risk, ok}`。
  2. `summary` 与 `displayType` **由后端在同一次工具执行时计算**，随事件流下发并同时落库；前端只渲染，
     禁止前端自算摘要，也禁止 chat 与刷新各算一套（这是"刷新前后一致"的根因解法）。
  3. `durationMs` / `risk` / `ok` 必须进入会话消息字段，否则刷新后无法复原头部信息。
  4. 前端保留"未知 displayType 回退到 text"的兜底分支，但不再对结果做启发式类型猜测。
  5. 表格展示的虚拟滚动与分页使用成熟实现（优先 Element Plus `el-table-v2`，其次 `vue-virtual-scroller`），不手写。
  6. 工具卡折叠态：`running` 默认展开、`error` 默认展开并显示错误详情、最终报告后未手动操作过的卡片自动折叠；
     **手动状态优先**，持久化 key 用 `toolCallId`（禁止数组下标）。
- **理由**：现状的不一致无法在前端侧修好——数据本身在出站时就丢了；把摘要与展示类型收敛到后端，才能保证
  chat、刷新、以及未来第三方消费方（含 `/api/v1` 迁移后的新端点）看到同一份事实，也才能让"概览只在头部"这类
  展示规则有单一数据源。
- **后果**：`internal/tools` 结果封装、`internal/sse` 的 tool 事件载荷、会话消息结构（新增字段）需同步改造；
  建议与 P2.14（Chat 端点迁 `/api/v1`）合并为一次纵向切片，避免同一契约改两遍；`Summary` 的生成规则（计数、
  单位、成功/失败前缀）纳入 P7.4 交付物并加单测。

## ADR-035 Web Shell 布局与滚动高度链契约（双栏独立滚动、输入区常驻）

- **状态**：已接受（2026-10-15；实现见 P7.2/P7.5，方案见 `14-方案-前端体验优化-四期.md` §3）
- **背景**：经典双栏要求"左栏独立滚动、右侧消息区独立滚动、底部输入区常驻"。实现过程中出现过用户可见缺陷：
  会话或消息一多，整页被内容撑高，输入框被顶出视口，必须滚动页面才能看到。根因是 `#app` 到 `.chat-page`
  的高度链没有闭合——`.app-main` 的高度来自 flex（其 `height` 属性仍是 `auto`），子级 `height: 100%` 于是
  解析成 `auto`；聊天网格又依赖隐式 `auto` 行，行高按内容撑开。
- **决策**：
  1. 高度链固定为：`html/body/#app` `height: 100%` → `.app-shell` `height: 100%` → `.app-body` `min-height: 0`
     → `.app-main` `min-height: 0` + `display: flex; flex-direction: column` → 路由页面 `flex: 1 1 0; min-height: 0`。
  2. 双栏网格必须显式 `grid-template-rows: minmax(0, 1fr)`；左栏 `.conversation-list` 用
     `height: 100%; min-height: 0` 承载内部 `overflow-y: auto` 的列表体；右侧 `.chat-main` 为纵向 flex，
     消息区 `flex: 1; min-height: 0; overflow: hidden`，输入区 `flex: none`。
  3. 禁止在路由页面用 `height: 100%` 依赖父级百分比；需要满高时一律 `flex: 1 1 0` + `min-height: 0`。
  4. 需要自身滚动的页面自带 `overflow-y: auto` 的滚动容器，不依赖 `.app-main` 整体滚动。
- **理由**：把"谁负责滚动"收敛到容器自身，页面级滚动条才能彻底消失。用 flex 而非百分比，是因为 flex 的
  使用尺寸对子级是确定的，而百分比只在父级 `height` 为确定值时才成立——这正是缺陷来源。
- **后果**：所有路由页面复用同一高度链；新增页面若引入自己的 flex/网格嵌套，必须补齐 `min-height: 0`；
  改布局后按方案 §3.2 自检（40 条会话下 `document.documentElement.scrollHeight === window.innerHeight`，
  且 `.composer` 底部不超过视口、`.list-body` 确实可滚动）。

## ADR-036 部署形态：默认一体化 + 可选前后端分离

- **状态**：已接受（2026-10-22 口径统一）
- **背景**：前端 `web/` 是独立工程、独立构建；后端是 Go 模块化单体。此前 ADR-029 仅描述
  「同进程同源单端口」，容易被理解为工程与交付都必须一体化，无法回应企业已有静态托管/网关/CDN
  与前后端独立发版诉求。
- **决策**：平台支持两种部署形态，同一套业务代码与 `/api/v1` 契约：
  1. **形态 A（默认）一体化部署**：Go 单二进制 `go:embed web/dist`，同进程同端口提供
     `/api/v1`（含 Chat SSE）与 SPA；作为单机/POC/测试、企业常规部署、离线交付与快速安装升级的
     标准形态，继续优先维护和验证。
  2. **形态 B（可选）前后端分离部署**：前端独立构建 `web/dist`，由独立 Nginx 或既有静态托管/
     网关/CDN 承载；后端关闭静态资源路由，只提供 `/api/v1`（含 Chat SSE）与
     `/healthz`/`/readyz`/`/version`。用于客户要求前端独立发布回滚、企业统一静态托管规范等场景。
  3. **路由与 API**：分离形态默认**同域名、按路径分流**（`/assets/*` 与 `/` → 前端，
     `/api/v1/*` → 后端），前端使用相对路径 `/api/v1`（构建期可用 `VITE_API_BASE` 覆盖）；
     跨域与独立子域不作为当前强制需求。API 正式边界统一为 `/api/v1`；历史遗留接口只在必要时逐步收敛。
  4. **实施边界**：为支持分离形态只允许最小开关改动（如后端关闭静态路由），不得拆微服务、重写前端
     API 层、复制业务逻辑或改变既有业务行为；若成本明显上升需重新评审。
  5. **版本与副本**：保留完整发行版本的统一管理，同时允许后续前后端独立发布；当前只要求基本版本标识
     与必要 API 契约测试，不提前建设复杂兼容体系。运行副本维持单副本（ADR-030），多副本/高可用/
     分布式协调不属于本次目标。
- **理由**：工程分离与交付形态解耦，既保留一体化在单机、离线、低成本升级上的优势，也为企业级
  独立交付提供标准路径；与 GitLab、Rancher 等产品的「单包/分离」双形态实践一致。
- **后果**：部署手册 §4.9 补齐两种形态的构建、K8s 部署、升级回滚与现状缺口；开发计划新增/
  调整 P6.11（一体化内嵌）与 P6.13（分离交付，最小改动），P6.2/P6.4/P6.7 准出同时覆盖两形态；
  形态 A 仍是默认与优先验证价值主线。

## ADR-037 权限分层、会话所有权与管理员只读边界

- **状态**：已接受（2026-10-23）
- **背景**：企业平台同时存在平台管理能力、Chat 会话和 Kubernetes 资源访问。此前容易把角色
  Permission、Session 可见性和 namespace 授权混为一谈，导致普通角色既可能因默认 Permission
  获得超出职责的平台入口，也可能因 Permission 归零而无法使用自己的 Chat；同时会话接口缺少稳定
  owner，无法满足多用户数据隔离。
- **决策**：
  1. 角色固定为 `user / operator / admin`。`user / operator` 的平台 Permission 默认为空；
     登录、创建会话和访问自己的会话属于认证基础能力，不占用平台 Permission。
  2. 会话增加不可变 `ownerUserId`。创建时由认证主体写入，客户端不得提交或修改。
     `user / operator` 只能列出、读取和操作自己的会话。
  3. admin 对全部会话拥有完整读写、运行控制与审批能力。普通角色对非本人会话按 ID
     操作统一返回 `404`。
  4. 所有 Session HTTP 与 SSE API 要求有效认证，正式边界统一为 `/api/v1/sessions*`，
     并在列表、历史、运行控制、审批、取消、改名、置顶和删除上执行同一 owner 校验。
  5. `user / operator` 默认没有 Kubernetes 资源权限；namespace 授权才是资源权限权威。
     operator 不因角色自动跨 namespace；跨 namespace 必须逐个授权。
  6. Node、PV、StorageClass 等集群范围读取不作为普通角色默认能力；admin 或显式集群级授权方可访问。
  7. 前端菜单、路由和按钮按 Permission/角色仅做 UX 显隐，后端继续执行 Permission、owner 与 scope 判定。
- **理由**：三类授权回答不同问题，合并会造成越权和体验缺口。将 owner 固化到会话数据并用
  同一服务端判定覆盖所有入口，才能防止普通角色通过猜测 ID、SSE 直连或管理菜单绕过限制；
  admin 作为平台最高权限角色保留跨用户会话的完整操作能力。
- **后果**：
  - `/api/v1/sessions*` 必须接入统一认证；未认证返回 `401`，非本人返回 `404`；
  - Session 保存 `ownerUserId`，历史、SSE、审批和运行控制按 owner 校验；
  - 前端隐藏配置中心、用户与权限、审计及其他无 Permission 的平台管理入口；
  - 数据模型、API 契约、安全设计、PRD 与开发计划同步更新，并纳入 P6.6 安全红线专项测试；
  - namespace 授权的 RBAC 派生、对账和执行前 SAR 仍由 P4.6 完成，不能把“角色零权限”
    误认为资源隔离已经生效。

## ADR-038 Scope-aware Agent：授权范围引导 + 工具执行门禁

- **状态**：已接受（2026-10-23）
- **背景**：仅把授权范围写进 Prompt 时，LLM 仍可能调用 Node/PV 或跨 namespace 工具，
  导致用户在没有授权的情况下获得集群信息；而只返回生硬的“无权限”又无法帮助用户理解
  自己可以做什么。
- **决策**：
  1. `GET /api/v1/auth/scope` 返回服务端解析的有效 AuthzGrant 范围：允许的 namespace、
     是否可访问集群级资源、是否 admin、写/销毁能力。
  2. 新会话由前端展示授权范围卡片；该卡片是 UI 引导，不落为 assistant 消息，不进入
     LLM 历史。
  3. 每轮 Chat 同时把范围注入 Prompt，用于让模型主动限定调查范围；
     但 Prompt 不作为安全边界。
  4. 每次 Tool 执行前强制 `AccessScope.Check`：namespace 必须显式命中；
     cluster-scoped 资源仅 admin；shell 或无法解析的 kubectl 命令拒绝；
     manifest 逐对象校验 scope。越界时不执行工具、不弹审批，返回 `SCOPE_DENIED`
     并写审计。
  5. 拒绝话术说明缺失的范围类型、当前已授权 namespace 和扩大授权方式。
- **后果**：Agent 在无授权时只能拒绝或引导，不会借用平台全局 kubeconfig 越权查询；
  Kubernetes RBAC/SAR 仍是最终资源权限边界。P6.6 增加“仅授权 mysql-dev 的用户
  不能调用 Node/其他 namespace 工具”的回归。

## ADR-039 会话 API 包更名 internal/webui → internal/sessionapi

- **状态**：已接受（2026-10-26）
- **背景**：三期 `a027590` 已删除 `internal/cli/web.go` 与内嵌单文件页面
  `internal/webui/index.html`（含备份副本），四期前端改由 `web/` 独立工程构建，
  并由 `k8s-ai server` 挂载 `web/dist` 同源提供。原 `webui` 包内已无任何静态资源，
  只剩 `/api/v1/sessions*` 的 JSON + SSE 接口，包名与职责不符，且容易被误读为
  “旧版 Web 界面仍在维护”。
- **决策**：
  1. 包 `internal/webui` 整体更名为 `internal/sessionapi`，包注释明确其职责为企业平台
     会话（Chat Session）HTTP/SSE API：会话增删改查、置顶改名、Chat 运行与停止、
     审批回传与事件流。
  2. 命名取 `sessionapi` 而非 `chatapi`：全部路由都在 `/api/v1/sessions*` 之下，与
     `internal/session`（模型与存储层）构成“模型 / API” 两层；`chatapi` 会与
     `service.ChatService`、`internal/agent` 的语义重叠。
  3. 只改包名与引用（`internal/cli/server.go` 装配、`tests/arch` 依赖方向白名单、
     `tests/platform` 测试装配），HTTP 路径、请求响应契约、SSE 事件与审计动作名一律不变。
  4. 配置键 `webui.addr` / `WebUIConfig` 与 settings 目录键 `webui_addr` 属配置与 API 面，
     本次不动，登记为遗留项另行收口。
- **后果**：`internal/webui` 不再存在；历史文档、历史 Changelog 与借鉴声明中的旧包名按
  历史记录保留不改。AGENTS.md 分层清单里的 “Web UI” 层名待与配置键一并统一。

## ADR-040 动态配置读取优先级：DB `settings` 为权威源，环境变量按密钥态/种子态两档注入

- **状态**：已接受（2026-10-27 首版；2026-10-27 **v1.2 修订**：环境变量覆盖由「对白名单键一律最高优先」改为
  **密钥态恒优先 + 种子态 seed-once**，覆盖面冻结为 LLM 四键 + ops 四键；配合 ADR-031 v1.14；运行时读取侧落 P6.1，
  两档语义改造、键集合锁定与前端覆盖态落 P6.12）
- **背景**：ADR-031 定下「7 组动态键唯一权威源是 DB `settings`」，但运行时从未真正读取它——`internal/settings`
  的分组常量只被 settings API 与自身测试引用，Chat 走 `service.NewChatWithStoreAndConfig(cfg, st)`，实际取值
  来自配置文件里的 `llm.*` 与内置默认（`llm.endpoint=http://localhost:8000/v1`、`llm.model=qwen-plus`）。
  后果有两个：配置中心页面改模型不生效；容器内 ConfigMap 已按口径不含 `llm.*`、页面又尚未配置时，Chat 拿假
  默认去连一个不存在的地址，报出与真实原因无关的错误。P6 部署联调会第一时间撞上，必须在 P6-A 本批解决，
  「页面即生效」不能继续标注为未实现。
  **v1.2 修订起因**：首版把 env 定成「对白名单键一律最高优先」，留下一个静默陷阱——页面保存路径
  （`settings.PatchGroup`）完全不感知 env，被覆盖的键写库成功、返回 200、写审计，运行时却仍用 env 旧值，而前端
  尚未渲染覆盖态，现场表现为「改了没生效」且无从判断。既然配置中心已是模型参数的唯一日常写入面，就必须让
  「页面配过即以页面为准」，只在密钥这类必须由 Secret 说话的键上保留 env 最高优先。
- **决策**：
  1. **DB `settings` 是动态键的唯一权威源与唯一写入目标**（ADR-031 不变）：正常路径取值来自 DB、页面保存只写 DB。
     环境变量不是第三处配置面，只按两档生效：
     - **密钥态（env 恒优先）**：`K8S_AI_LLM_API_KEY`、`K8S_AI_PLATFORM_STORE_DSN`。密钥只走环境变量 / Secret，
       是部署方的轮换通道；env 命中时页面值不生效，页面须标注来源，保存此类键时返回显式提示，不得静默成功。
     - **种子态（seed-once：DB 该键无值时生效，有值即让位）**：`K8S_AI_LLM_{PROVIDER,ENDPOINT,MODEL}` 与
       `K8S_AI_{KUBECONFIG,KUBE_CONTEXT,NAMESPACE,KUBECTL_PATH}`。用于冷启动与离线首启窗口、同镜像多环境换址；
       管理员在页面保存过该键后 env 不再覆盖，DB 为最终值。
     一句话口径：**页面配过的以页面为准，页面没配过的用部署变量兜底，密钥永远以 Secret 为准。**
  2. 覆盖面**冻结**为上述 LLM 四键 + ops 四键（`K8S_AI_PLATFORM_STORE_DSN` 不属动态键，走 config 层 viper 绑定）。
     `K8S_AI_LLM_{TEMPERATURE,MAX_TOKENS,TIMEOUT}` **明确不纳入** env 覆盖：`temperature`/`max_tokens`/`timeout`
     分别是 KindFloat/KindInt/KindDuration，为它们要常驻三套类型转换与校验分支，收益不划算。新增 env 覆盖必须
     走 ADR 修订，键集合由测试锁定，防止退化成第三处活配置。
  3. 内置默认只作最后兜底。DB 与 env 都未提供模型配置时，Chat 返回可读错误并指明修复入口（配置中心 →
     模型，或 `K8S_AI_LLM_*`），禁止静默套用假默认继续执行。
  4. 页面保存只写 DB、不回写 `config.yaml`，保存后即时生效、不需重启。凡当前生效值来自环境变量的键，配置中心必须
     渲染覆盖态徽标，并区分两档文案：密钥态「由部署 Secret 提供，页面值暂不生效」；种子态「由部署变量提供首启
     种子值，保存后以页面为准」。默认部署清单只注入 `K8S_AI_LLM_API_KEY`，非敏感 env 覆盖只在 `deploy/example/`
     演示，从来源上避免双写。
  5. `config.yaml` 仍不承载动态键（ADR-031 不变）。env 注入是部署期手段，不是第三份配置文件。
- **理由**：容器化部署必须在镜像与 ConfigMap 之外注入 LLM 端点与密钥，尤其是「库还没配好」的首启窗口；但覆盖面
  必须窄到可枚举、可测试，且不能在日常运维里制造「两处都能配、页面配了不算数」的错觉。两档语义把「必须由 Secret
  说话」的少数键与「只为兜底引导」的多数键分开，既保住密钥通道，也让页面成为已配置项的最终权威。
- **后果**：`server` 装配时把 settings service 注入 Chat/Agent/LLM 构造路径；`internal/settings` 的 `EnvVarOverride`
  需新增档位字段，`ActiveEnvOverrides` / `envOverrideValues` 判定种子态要先读 DB 当前值；前端 SettingsView 渲染
  覆盖态；`deploy/configmap.yaml` 只保留静态段；P6.12 落键集合与两档行为测试。
- **落实状态**：**部分实现**。P6.1（提交 e034e56）已落运行时读取侧、白名单只读覆盖、`Item.EnvOverride` 接口字段。
  **待 P6.12**：两档语义改造、键集合与两档行为测试锁定、前端覆盖态徽标与保存提示、默认清单只注入 api_key。

## ADR-041 前端产物内嵌与镜像可复现构建

- **状态**：已接受（2026-10-27；实现落 P6.11，形态 B 镜像落 P6.13）
- **背景**：现有 Dockerfile 只有 `golang:alpine → distroless` 两阶段，不含前端构建。若直接 `go:embed web/dist`，
  `dist` 目录不存在时编译直接失败；而 `web/.gitignore` 按整目录忽略 `dist`，既提交不了占位文件、`!dist/.gitkeep`
  反向放行也无效；`.dockerignore` 未排除 `web/node_modules`，构建上下文会把宿主机依赖树整个灌进去。
- **决策**：
  1. 内嵌用 `go:embed all:web/dist`，仓库提交 `web/dist/.gitkeep` 占位；`web/.gitignore` 由整目录 `dist`
     改为 `dist/*` + `!dist/.gitkeep`。
  2. **前后端两个独立 Dockerfile**：仓库根 `Dockerfile` 构建后端镜像，`deploy/web.Dockerfile` 构建 Nginx
     前端静态镜像，两者各自独立构建、互不依赖、可分别升级（形态 B 的后端直接复用同一后端镜像，靠
     `platform.server.static_enabled: false` 关闭静态托管）。后端 Dockerfile 默认三阶段：**node 构建阶段**
     （`npm ci && npm run build` 产出 dist）→ golang 构建阶段
     （embed 该 dist）→ 运行阶段（基座与工具层由 ADR-044 决定，见其 v1.1）。干净检出执行 `make docker` 即可产出含前端的镜像，这是默认路径。
  3. 离线与加速路径用 BuildKit **named build context 覆盖同名 stage**：
     `docker build --build-context webdist=./web/dist .`，把 node 阶段的产物替换成本地已构建的 `web/dist`。
     同一份 Dockerfile 即可切换，不需要 `--prebuilt-dist` 这类无法跳过 stage 的伪开关；若构建环境 BuildKit
     不可用，退化方案是另存 `deploy/Dockerfile.prebuilt-dist`。
  4. node 阶段需要 npm registry：纯离线环境必须走第 3 条或配置内网镜像源，手册要显式写明。
  5. `dist` 为空时构建不失败：静态路由检测到空目录时返回明确提示（未构建前端，请 `npm run build` 或改用
     形态 B）；未知 `/api/v1/*` 必须返回 JSON 404，不得被 SPA history fallback 吞成 `index.html`。
  6. `.dockerignore` 增加 `web/node_modules`。
  7. 运行阶段的基座与工具层由 ADR-044 决定：首版为 distroless static nonroot（无 shell、无 kubectl）；**2026-10-29 起改为 `debian:bookworm-slim` + bash/coreutils/jq/curl/kubectl，仍保持非 root（ADR-044 v1.1）**，写执行仍只走 client-go 与 `internal/executor` 唯一出口，本 ADR 的构建阶段设计（node 构建 dist → golang embed → 运行阶段）不受影响。
- **后果**：`make docker` 与本地构建脚本需要处理前端产物；CI 需要 node 层缓存；形态 B 的 `web.Dockerfile`
  复用同一份 `web/dist`；`bin/` 直编产物在无前端时走空 dist 提示分支。
- **落实状态**：待实现（P6.11）。

## ADR-042 平台存储主口径 MySQL 与迁移入口拆分 `k8s-ai db migrate`

- **状态**：已接受（2026-10-27；修订 D-04；实现落 P6.1 / P6.5）
- **背景**：D-04 原口径是「SQLite 默认 + MySQL 可选」，且 `PlatformStoreConfig.DSN` 注释写着「本期不消费」，
  `internal/cli/server.go` 直接硬编码 `store.OpenSQLite`，`platform.store.*` 实际无人读取。四期企业交付以
  MySQL 为主存储，SQLite 只服务本地开发与离线兜底。另一方面，K8s 部署需要可独立执行的 schema 迁移入口
  （Job / initContainer），不能只依赖 server 启动时的隐式迁移；而 `k8s-ai migrate` 这个名字已被三期的
  「旧配置一次性导入」占用，语义必须分开。
- **决策**：
  1. 平台库默认 **MySQL**，`store.driver` 取 `mysql | sqlite`，`server` 与迁移命令都按驱动分派
     （MySQL 走 `store.OpenMySQL`，SQLite 走 `store.OpenSQLite`）。
  2. DSN 含口令，只经 `K8S_AI_PLATFORM_STORE_DSN` / Secret 注入；`platform.store.dsn` 键位保留但值只接受
     env/Secret，`config validate` 对 YAML 中出现非空 dsn 给出告警。MySQL DSN 必须带 `parseTime=true`，
     缺失即启动报错，不在运行期才炸。
  3. 新增 **`k8s-ai db migrate`**：只做 schema 升级、单向且幂等，可作 K8s Job 或 initContainer；`server`
     启动内保留同一迁移的幂等兜底。与 `k8s-ai migrate`（三期旧配置一次性导入）职责分离，`--help` 与手册
     都要写清，initContainer 只用前者。
  4. PVC 转为**可选**：`STORAGE_CLASS` 为空则不创建 PVC；PVC 只承载报告/导出文件与 SQLite 兜底数据目录。
  5. MySQL 集成测试（`TestStoresMySQL`，`K8S_AI_TEST_MYSQL_DSN` 门控）必须进 CI 门禁实跑；该用例此前在
     CI 中恒被跳过，主存储切换不能带着从未验证的驱动上线。门禁环境接入方式固定为两条：集群内部署走集群内
     Service 地址，本地与 CI 门禁用外部可达地址，两者都只是 `K8S_AI_TEST_MYSQL_DSN` 的注入值；库与账号由 DBA
     预建，产品不执行 `CREATE DATABASE`。若服务端库默认字符集不是 utf8mb4，表级 DDL 已显式
     `DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`（`internal/store/dialect.go`），不依赖库默认。
     `deploy/example/` 另出一份 MySQL 接入示例清单（占位值、示例口径、非产品默认，不含现场地址与凭据）。
  6. 主 MySQL **不构成**扩副本前置：单副本的真因是内存态会话/Token、SSE 与审批 pending 通道（ADR-030 不变）。
- **后果**：`go-sql-driver/mysql` 由 indirect 转为直接依赖；需要一台可长期使用的 MySQL 实例作为门禁环境
  （人工前置，否则 MySQL 主路径无法验收）；`deploy/` 清单默认 `driver: mysql` + DSN Secret + 迁移 Job，
  所有「SQLite 默认」表述同步修订。
- **落实状态**：已落地（P6.1 驱动分派与 DSN 消费、`k8s-ai db migrate` 与启动幂等兜底见 e034e56；P6.2/P6.5 清单与
  `deploy/base/db-migrate-job.yaml` 见 c21b3a8，并在真机形态 A 完成一次端到端部署；MySQL 门禁实跑通过）。
  真机纠偏：会话正文列必须由 TEXT 放宽为 MEDIUMTEXT（v5 迁移幂等放宽），否则整轮会话静默丢失，见 5ce77d7。

## ADR-043 存储边界与对象存储：正文外置到 `internal/blobstore`，默认 `db` 后端零行为变化

- **状态**：已接受（2026-10-28；实现落新增主线 P8 / M8；附件上传通道 P8.7 延后 v4.2）
- **背景**：
  1. 会话正文整份 JSON 存 MySQL `sessions.data`，且会话列表每次请求 `SELECT data FROM sessions` 全表读正文
     （`internal/store/sqlite.go`），索引列写好却不用于 List；会话规模上去后列表页必然把整库正文读进内存。
  2. 真机已因此出过两次事故：正文超 TEXT 上限触发 `Error 1406 Data too long`，整轮会话静默丢失（热修 MEDIUMTEXT +
     v5 幂等放宽，见 5ce77d7）；扫描报告写 Pod 内 emptyDir，重启即丢且 Web 面看不到任何报告。
  3. 会话上传图片/文件是确定要做的能力，大正文继续进关系表不是可选项。
- **决策**：
  1. 新增 `internal/blobstore`，接口 `Put/Get/Stat/Delete/List/Driver` + 可选 `Presigner`/`Maintainer`，
     业务侧只见 `Ref{Key,SHA256,Size,MediaType,UpdatedAt}`，永不接触桶名与凭据。
  2. 后端四档 `off|db|dir|s3`，**默认 `db`（正文进 `blob_objects.payload`，与业务行同事务）= 默认交付零行为变化**；
     `dir` 面向单机 appliance 且带 flock 硬约束（与单副本口径 D-19/D-20 一致）；`s3` 面向平台化交付。
  3. key 固定 `{prefix}/{category}/{scope}/.../{sha256}{ext}`，内容寻址天然去重幂等；`ValidateKey` 是防穿越唯一闸门，
     总长上限 191（沿用 VARCHAR(191) 约定）。
  4. schema v6 新增 `blob_objects`/`attachments`/`reports` 与 `sessions.data_blob_key`/`data_size`；生命周期契约
     `Acquire/Release/ListCollectable/Forget`，GC 顺序固定，宽限期默认 168h，本期出 `k8s-ai blobs gc`，P5 接调度。
     P5 的 Retention 与 Memory 写入方必须 blob-aware，这是 P8 排在 P4/P5 之前的唯一硬理由。
  5. 配置全部归静态 `platform.blobs.*`，**不进动态键白名单、配置中心页面不展示**（改桶需重启，与 D-29/ADR-040 两档
     语义不冲突）；AK/SK 走 env/Secret，与 `K8S_AI_PLATFORM_STORE_DSN` 同机制。
  6. CLI 只允许 `off|dir`，`chat/scan` 不读 endpoint/AK/SK，保证「一个二进制丢到 master 节点」的直用形态不变。
  7. 写顺序恒为「校验 → Put → 同事务（业务行 + Acquire）」；会话外置失败回退 inline 仅 WARN，不阻塞 Agent。
  8. SDK 用 `minio-go/v7` 但只允许 S3 通用子集，禁 MinIO 私有 API；MinIO 服务端 AGPLv3 需法务过审后才作为默认交付，
     未过审 `ENABLE_MINIO` 默认 false、交付改走现场已有桶。
- **后果**：新增三表两列与一条 GC 链路；`dir` 后端把「不可横向扩容」从默认约束变成显式配置约束；报告产物
  出现「桶为权威、目录为现场副本」双写，退役另期处理；平台进程仍维持单副本，多副本前置项（SSE sink、
  `ProposalStore` 外移）另期登记。
- **落实状态**：待实现（P8.1 配置与 validate → P8.2 接口与 `db` 后端 → P8.3 `dir` → P8.4 `s3` → P8.5 报告 →
  P8.6 会话读写收敛 → P8.8 部署面 → P8.9 门禁与 CLI）。方案见 `15-方案-存储边界与对象存储-四期.md`。

## ADR-044 容器执行通道：默认交付镜像内置 shell 与 kubectl，安全由审批闸门与 RBAC 收口

- **状态**：已接受（2026-10-28 首版；2026-10-29 **修订 v1.1 整体改写**；实现落 P6.3，0.75d → 1.0d，顺序提前到 P8.1 之前）
- **背景**：交付镜像运行阶段曾是 `gcr.io/distroless/static-debian12:nonroot`，无 shell、无 kubectl，而 `internal/executor/kubectl.go` 的 shell 分支硬编码 `bash -c`、argv 分支 `LookPath("kubectl")`，容器内只能跑到 `exec` 那一刻才失败，把 `executable file not found` 原样回给 LLM 诱发无意义重试。首版方案选「基座不变 + shell 策略级关闭 + initContainer 投递 kubectl」，评审时用户否决：平台已有能力不能因为部署在 K8s 里就丢失，shell 是排障刚需，Agent 类产品的通行做法是权限大但必须人审批，而不是删掉工具。核对 AGENTS.md 后确认：它只要求含 shell 构造的命令经真人审批且不被 `--skip-permissions` 免除，**从未要求镜像内不存在 shell**，「物理关闭」是 D-06 被读严的结果。
- **决策**：
  1. 运行阶段基座换 `debian:bookworm-slim`，内置 `bash` + coreutils + `jq` + `curl` + 网络诊断工具 + `kubectl`（构建期按 `KUBECTL_VERSION` + sha256 校验下载，可用 `KUBECTL_DOWNLOAD_BASE` 指内网 mirror），保持 uid/gid 65532 非 root。
  2. 新增 `Dockerfile.distroless` 无 shell 变体，供安全基线明确禁止容器内 shell、或入口直接暴露公网的现场选用。
  3. 新增静态配置 `platform.exec.shell_enabled`（**默认 `true`**）：关闭时在 `Runner.RunKubectl` 判定为 shell 命令后、构造 `exec.Cmd` 之前返回可指导的工具级文案；启动探测 `bash`/`kubectl`，缺失只打 WARN/INFO，不阻塞启动。
  4. 安全边界由四件事承担而不是「没有解释器」：含 shell 语法的命令永远人工审批且 `--skip-permissions` 不免除；Secret 内容值 / `--raw` / 交互式动词 / `deny_ops` 红线不变；真实权限由集群 RBAC 决定（默认只读、写权限现场显式开启、不含 `pods/exec`）；输出脱敏与全量审计。清单侧配 Pod 加固：`runAsNonRoot`、drop ALL capabilities、`allowPrivilegeEscalation: false`、`readOnlyRootFilesystem` + `/tmp` 与 `/home/k8sai` emptyDir、resources，NetworkPolicy 可选。
  5. 取消首版的 initContainer 投递 kubectl 与 `K8S_AI_KUBECTL_PATH` 指向要求（配置键 `kubernetes.kubectl_path` 保留给自定义路径与非容器场景）；否决页面上传二进制、否决内置 `helm`/`python`/`node` 运行时。
  6. 「按需执行 Pod + `pods/exec`」不再是 shell 能力的替代路径，**降级为五期多租户隔离议题**：只有需要 per-session 降权身份与爆炸半径控制时才做，且必须与 TokenRequest 派生身份同批交付，不做半套。
- **后果**：镜像约 20MB → 约 180MB、CVE 面上升（交付前镜像扫描报告归档）；容器内具备本地命令执行能力，故上述加固项为必配；换来 K8s 形态与本地 CLI 形态的执行能力对齐（管道、聚合统计、heredoc、非交互 `kubectl exec`）。代码面为 `Options`/config 各一个布尔键、两处可指导文案与启动探测，`tests/arch` 增加「shell 执行点唯一且必须被闸门包裹」红线。
- **落实状态**：待实现（P6.3）。方案见 `16-方案-容器执行通道与镜像基座-四期.md` v1.1。

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
| T9 | 镜像写执行方案 D-06 签收 | **已收口（ADR-044 v1.1）**：默认镜像内置 shell 与 kubectl、容器内 shell 默认可用；写能力边界由人工审批 + 集群 RBAC + 红线 + 脱敏审计承担，`shell_enabled=false` 与无 shell 变体镜像留给硬合规现场 | K8s 形态下的写能力与排障能力 |
