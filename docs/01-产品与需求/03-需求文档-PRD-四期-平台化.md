# k8s-ai 四期需求规格（平台化）

> 版本：v1.17
> 日期：2026-10-27
> 状态：四期最终需求方案  
> 目标：交付面向企业的多用户 Kubernetes AI 运维平台，在同一权限、会话、审计与安全执行体系下提供 Chat、诊断、集群管理、Skill、审批和治理能力。

---

# 1. 四期定位

四期是企业平台本体。平台统一承载多用户身份、会话、Agent Runtime、集群访问、Skill、
审批、审计、Memory 与配置管理；既有能力作为平台内部组件复用，不以历史版本兼容为设计目标。

```text
平台化 Web Application
        ↓
Agent Runtime + Platform Services
        ↓
多用户 / 集群 / Skill / 配置 / 审批 / 审计 / Memory
```

**核心原则：**

1. Agent Runtime 是平台核心应用服务，CLI、Server、Web UI 与自动化任务复用同一套能力。
2. Code Guard 与 Executor 是双层强制边界：Code Guard 负责审批与门禁，Executor 是写操作唯一执行边界。
3. Skill 不允许形成新的写执行通道。
4. Web 是企业平台入口；CLI 是独立的快速运维界面，不进入平台用户与授权体系。
5. 平台身份、会话所有权、平台 Permission 与 namespace 资源授权必须分层建模，不能互相替代。
6. 所有权限结论以后端强制校验为准，前端菜单、路由和按钮显隐只负责 UX。

---

# 2. 总体目标

## 2.1 Web 平台化

前端从当前单文件 HTML 重构为：

- Vite
- Vue 3
- TypeScript
- Vue Router
- Pinia
- Element Plus（默认）

建立正式 SPA 架构。

---

## 2.2 Agent 平台化

建立统一 Agent Session 模型：

```text
Session
  └── Turn
       └── Step
            ├── Reason
            ├── Tool Call
            ├── Observation
            ├── Approval
            └── Response
```

Agent 不采用固定步骤流。

平台 Agent 必须采用统一的动态循环：

```text
Reason
 ↓
Tool
 ↓
Observe
 ↓
Reason
 ↓
Tool
 ↓
Approval
 ↓
Tool
 ↓
Observe
 ↓
...
```

Web 只负责将 Agent Runtime 的真实状态可视化。

---

## 2.3 平台能力

四期提供：

- 单集群接入
- Skill 管理
- 配置中心
- 用户与角色
- 审批中心
- 审计中心
- Session / Agent Runtime 持久化
- 长期记忆
- 数据保留
- SQLite / MySQL 存储
- K8s 部署
- 一键安装

---

# 3. 四期范围

## 3.1 四期做

### Web

- Vue3 + TypeScript SPA
- 页面信息架构
- Chat 页面重构
- 管理后台
- 登录
- 路由权限
- API 权限

### Agent Runtime

- Session
- Turn
- Step
- SSE 事件模型
- Tool Call 状态
- Approval 状态
- Agent 执行状态持久化
- Agent 运行时间线

### 集群

- 单集群
- kubeconfig 上传/粘贴
- 连接校验
- 集群健康状态
- 集群版本
- 当前 Context
- Credential 安全存储

### Skill

- 内置 Skill
- 自定义 Skill
- 技能类型（structured / procedural）
- 标准 SKILL.md 文本 / 技能包 zip 导入与静态资产（assets）存储
- Chat 回合前意图检索注入（procedural）
- Skill 启停
- Skill 导入
- Skill 校验
- Skill 版本
- Skill 来源
- Skill 命中记录
- Skill 风险结果
- 三方 Skill Loader 接口预留

### 配置

- 模型
- Skill
- Approval
- Memory
- Retention
- MCP 展示位
- 运维配置

### 用户

- user
- operator
- admin
- Session Token
- namespace 授权（用户零权限起步，admin 划分 namespace）
- 销毁类权限（delete namespace / delete pvc / delete pv 默认仅 admin，operator 由 admin 显式委派）
- 权限矩阵
- 后端 API 强校验

### Approval

- Approval Center
- Pending
- History
- Approve / Reject
- Approval Policy 可视化
- Authorization Grant 展示
- Approval Audit

### Audit

记录：

- 登录
- Session
- Tool Call
- Approval
- Grant
- 写操作
- 配置变更
- Skill 运行
- 集群操作

### Memory

- Session Summary
- Long-term Memory
- Memory Scope
- Memory Policy

### Retention

- Session
- Audit
- Scan
- Skill Run
- Memory
- Archive / Delete

### Storage

- SQLite
- MySQL

---

# 4. 四期明确不做

- CLI 登录
- CLI 用户体系
- CLI RBAC
- 多集群 Web UI
- MCP 真实接入
- Redis
- 多租户
- 组织管理
- 复杂 IAM
- Skill 在线市场
- Skill 远程仓库完整实现
- Skill 包 `scripts/` 执行引擎（平台不运行技能包内脚本，仅静态存储）
- 沙箱执行、多技能 zip 批量导入（均延后到后续阶段）
- 应用层手写 namespace RBAC（如手动编辑 RoleBinding / ClusterRole）

namespace / resource 真实权限由平台 AuthzGrant 派生、下行到 Kubernetes RBAC，并在 API
Server 强校验；不设计与应用库平行运行的独立权限体系，也不做多租户/组织管理/复杂 IAM。

---

# 5. 总体架构

最终架构：

```text
                         k8s-ai
                           │
             ┌─────────────┴─────────────┐
             │                           │
           CLI                          Web
             │                           │
       Direct Service              HTTP / SSE
             │                           │
             └─────────────┬─────────────┘
                           │
                    Application Core
                           │
       ┌───────────────────┼──────────────────┐
       │                   │                  │
 Agent Runtime       Platform Services     Governance
       │                   │                  │
 Session               Cluster             Approval
 Turn                  Skill               Audit
 Step                  Config              Policy
 Tool                  User                Grant
 Memory                Storage
       │
       └───────────────────┬──────────────────┘
                           │
                    Existing Phase 3
                           │
                 Tool / Skill / Executor
                           │
                       Code Guard
                           │
                      Kubernetes
```

---

# 6. CLI 与 Web

CLI 与 Web：

- 共用同一二进制
- 共用核心 Service
- 共用配置 Schema
- 共用 Tool
- 共用 Skill
- 共用 Approval 底座
- 共用 Storage 抽象

CLI：

```text
k8s-ai scan
k8s-ai chat
...
```

直接调用内部 Service。

不得：

```text
CLI → HTTP → Web → Service
```

Web：

```text
Browser
 ↓
HTTP API / SSE
 ↓
Application Service
```

CLI 不进入 Web 用户体系。

---

# 7. Agent Runtime

这是四期新增的核心平台基础设施。

## 7.1 Session

Session 至少包含：

```text
session_id
user_id
cluster_id
created_at
updated_at
status
```

---

## 7.2 Turn

一个用户输入对应一个 Turn：

```text
turn_id
session_id
user_message
status
created_at
completed_at
```

---

## 7.3 Step

Agent 的每一次执行步骤：

```text
step_id
turn_id
type
status
started_at
completed_at
input_summary
output_summary
```

Step 类型：

```text
REASON
TOOL_CALL
OBSERVATION
APPROVAL
RESPONSE
ERROR
```

---

# 8. SSE 事件模型

Web Chat 必须采用统一 SSE 事件协议。

至少支持：

```text
session.created

turn.started
turn.completed

reason.started
reason.completed

tool.started
tool.completed

observation.created

approval.required
approval.approved
approval.rejected

message.delta
message.completed

session.completed
session.failed
```

每个事件至少携带：

```text
session_id
turn_id
step_id
timestamp
event_type
```

Web 不允许自己猜测 Agent 状态。

**页面展示必须以 Agent Runtime/SSE 实际事件为准。**

平台只维护一套原生事件契约；服务端事件顺序、终态、字段语义和脱敏规则统一由本 PRD、
SSE_EVENT 契约与 ADR-032 定义，不另建第二套事件通道。

## 事件顺序与展示语义（强制）

事件清单之外，以下四条是协议的一部分，任一侧实现都必须遵守（见 ADR-032 / D-22）：

1. **分片不是展示单元**：`message.delta` 是传输分片，展示层按 turn 聚合，一个回合只有一块最终报告；
   `index` 表示同一 role 内从 0 开始的分片序号，仅用于去重与排序，不得参与展示 key，JSON 也不得用
   `omitempty` 把 0 隐藏掉。
2. **终态必须最后**：顺序固定为 `message.delta…` → `message.completed`（携带权威全文）→
   `session.completed` / `session.failed`。`turn.completed.output` 只作兜底，不单独成块；
   终态之后到达的分片由前端忽略。
3. **事件流统一脱敏**：写入 SSE 的内容（含增量）在封装前走与终态同一套脱敏策略。
4. **思考面板语义**：`reason.started` 必须早于本轮 reasoning 增量；空思考面板不占位；
   报告出现后折叠、用户可手动展开。

违反上述任一条都会产生用户可见故障（报告重复、状态永久停留在"正在生成"、思考内容丢失），
因此后端与前端各自必须有契约测试守住。

---

# 9. Web Chat

Chat 是平台默认首页。

平台 Chat 必须覆盖：

- 消息
- Tool Card
- Approval Card
- Reasoning/Thinking 展示
- Tool 输入输出
- 执行状态
- 结果展示

### 9.1 授权范围引导与拒绝语义

新会话在消息区顶部展示“当前授权范围”卡片，数据来自服务端解析后的有效
AuthzGrant，不由前端或 LLM 推断：

- 有 namespace 授权：列出已授权 namespace，并说明可在其中查询工作负载、Pod、
  事件、日志和诊断；
- 无 namespace 授权：说明当前没有可查询范围，需要管理员完成授权；
- admin：说明拥有全集群及集群级资源权限。

授权范围同时注入 Agent 上下文，但仅用于改善交互；真正门禁在每次工具调用执行前：

- namespace 工具必须显式命中已授权 namespace；
- Node、PV、StorageClass、Namespace 等集群级工具仅 admin 可用；
- 越界时不执行工具、不发起审批，直接向模型返回稳定的 `SCOPE_DENIED` 结果；
- 拒绝话术应说明当前请求缺少哪类范围、当前已授权 namespace 以及如何申请扩大授权，
  不使用简单生硬的“无权限”。

四期重构为组件化：

```text
ChatShell
├── ConversationList
├── MessageTimeline
├── AgentStep
├── ToolCallCard
├── ApprovalCard
├── ThinkingPanel
├── ObservationCard
└── Composer
```

不要求固定四层或固定步骤。

Agent 执行多少步，UI 就展示多少步。

---

# 10. 前端架构

前端体验基线（主题与令牌、布局与滚动、会话列表、聊天交互与 Run Bar、工具卡与 `tool_result`、Markdown 报告、登录页）见
`docs/02-架构与设计/14-方案-前端体验优化-四期.md`（2026-10-15 评审通过，落地任务 P7.1–P7.5）。前端必须遵守两条硬约束：

1. **主题与令牌**：`<html data-theme="dark|light">` 与 `<html class="dark">` 同步切换，模式支持 `light|dark|system`，
   默认深色，首屏由 `index.html` 内联脚本设置（不得闪烁）；组件禁止写死颜色（ADR-033 / D-23）。
2. **工具结果同源**：工具结果统一为 `tool_result{tool,summary,displayType,columns,items,raw,durationMs,risk,ok}`，
   `summary` 与 `displayType` 由后端在同一次执行时计算并同时下发与落库，前端只渲染不自算，
   保证 chat 流式与刷新后展示一致（ADR-034 / D-24）。

```text
ui/
├── app/
├── layouts/
├── router/
├── stores/
├── api/
├── composables/
├── components/
│   ├── chat/
│   ├── agent/
│   ├── approval/
│   ├── skill/
│   └── common/
├── views/
│   ├── chat/
│   ├── cluster/
│   ├── skill/
│   ├── approval/
│   ├── audit/
│   ├── settings/
│   └── users/
└── types/
```

前端不承载安全逻辑。

前端权限：

```text
Route Guard
+
Component Visibility
+
Button Visibility
```

仅用于 UX。

真正权限必须由后端 API 强制执行。

平台管理页面按平台 Permission 显隐：`user` 与 `operator` 默认平台 Permission 为零，
因此不展示 Skill 管理、配置中心、用户与权限、审计等管理入口。当前登录用户仍可进入聊天，
但只能查看和操作自己的会话。

会话权限由 `ownerUserId` 决定，不从角色名称推断：

- 创建会话时由认证上下文写入所有者，客户端不得提交或修改所有者；
- `user / operator` 只能列出、读取和操作自己的会话；
- `admin` 可列出、读取、发送消息、审批、取消、重命名、置顶和删除全部会话；
- 所有按会话 ID 访问的 HTTP 与 SSE 接口执行同一归属校验，非本人访问返回 `404`。

---

# 11. 页面信息架构

侧边栏：

```text
聊天
集群
Skill
审批中心
审计
配置中心
用户与权限
```

侧栏入口受平台 Permission 控制。`user / operator` 默认只保留其有权使用的入口；
配置中心、用户与权限及平台管理入口不出现在其菜单中。

---

# 12. 集群管理

四期只开放单集群 UI。

数据模型必须预留：

```text
cluster_id
```

Cluster 与 Credential 分离。

建议：

```text
Cluster
├── id
├── name
├── server
├── version
├── context
├── status
├── credential_ref
└── timestamps
```

Credential：

```text
Credential
├── id
├── encrypted_data
├── created_at
└── updated_at
```

kubeconfig：

- 可上传
- 可粘贴
- 后端解析
- 后端连接测试
- 加密存储
- 不进入 LLM
- 不进入 Session
- 不进入 Audit
- 不进入 Memory
- 不在前端回显

---

# 13. Skill 平台

Skill 是：

> 方法论、流程、规则、输入要求和风险约束。

Skill 不是执行器。

Skill 不得直接执行：

```text
kubectl
shell
client-go write
```

所有写操作必须进入现有：

```text
Tool
 ↓
Approval
 ↓
Code Guard
 ↓
Executor
```

### 13.0 Skill 两维正交

Skill 用两个正交维度描述，避免「来源」与「形态」混用：

```text
type    = structured | procedural        # 技能形态
source  = builtin | custom | third-party # 提供方，只表达来源、不表达类型
```

- `structured`：资源创建类**确定性规则**（rules / risk_policy / pre/post_checks）。
  `create_resource` 在 dry-run 阶段消费匹配技能的 findings，并可提升 Approval Risk
  （只升不降；custom / third-party 不得设 `risk.default`，只能经 `elevateWhen` 升档到 HIGH）。
- `procedural`：**标准 Agent 技能**（SKILL.md + frontmatter 的 name/description），
  支持两种命中场景：
  - **自动意图检索**：聊天回合前对用户意图做三级匹配——整句精确名称 → 整句子串
    （名称/描述/领域）→ 长句关键词打分（名称 3 分 / 领域 2 分 / 描述 1 分，每词只计
    一次最高权重），命中后把 SKILL.md 渐进式披露内容注入系统提示词；单轮最多
    同时注入 2 篇（`hit=auto`）。
  - **显式指定**：用户 / 主 Agent 在会话中 `@技能名` 显式指定本轮使用的技能，
    逐个精确解析（大小写不敏感）；任一名字不存在或未启用则整批拒绝该回合请求，
    不做静默降级；显式列表非空时不叠加自动检索结果（`hit=explicit`）。

procedural 消费路径：

```text
用户意图 → 意图检索 → 命中 SKILL.md → 注入系统提示词
        → Agent 把方法论文本翻译为受信工具调用
        → Tool → Approval → Code Guard → Executor → Kubernetes
```

安全边界（SKILL.md 正文与技能包内脚本都是不可信内容）：

- SKILL.md 正文仅作文本上下文注入，不解析、不执行其中任何「指令式」内容。
- 技能包内 `scripts/` 一律 inert（静态存储，平台不提供直接运行引擎）。
  用户方法论文本里的命令，由 Agent 翻译为受信工具调用后执行，并遵守既有 shell /
  exec 审批红线：交互式 `exec -i/-t`、`attach`、`port-forward` 等仍硬拒；
  `logs | grep` 走结构化 logs 或经人工审批的 shell。
- 平台能力无法覆盖的方法学步骤，明确拒绝并说明，记入 `SKILL_RUN` 审计；
  不允许技能伪造不存在的工具或绕过既有能力。
- Secret 内容值永不进入 LLM / 上下文 / 会话 / 审计；kubeconfig 红线不变。

---

## 13.1 Skill 数据模型

至少包括：

```text
name
version
author
source
type
description
domain
resource_kinds
required_inputs
rules
risk_policy
examples
assets                    # procedural 技能附带静态资产元数据（名称/类型/大小等）
enabled
validation_status
created_at
updated_at
```

运行状态：

```text
hit_count
last_hit
last_validation
```

---

## 13.2 Skill 来源与导入

`source` 与 `type` 正交：来源只表达提供方，技能形态由 `type` 表达。

```text
source = builtin | custom | third-party
```

四期：

- builtin 完整支持
- custom 导入，按 `type` 分两条消费路径：
  - `structured`：沿用现有 create_resource 校验与风险升档通道（当前已落地）
  - `procedural`：标准 SKILL.md 导入 → 静态资产存储 → Chat 回合前意图检索注入
- third-party 只实现 Loader / Validation Contract
- 不做在线市场、不做远程仓库完整实现

procedural 导入载体 v1：

- 载体：粘贴单/多篇 SKILL.md 文本 + 单个技能包 zip（内含 SKILL.md）
- zip 内静态资产仅收文本白名单：`md / yaml / json / txt / csv`
- 限制：文件总大小、包内文件数、解压深度、路径穿越、符号链接必须有界校验
- 兼容性：zip 允许「直接压缩技能文件夹」形态——根目录无 `SKILL.md` 且整包恰好归入唯一顶层目录时，自动剥离该目录前缀后继续校验，多顶层目录整包拒绝（多技能 zip 仍延后）；`SKILL.md` 允许首行为代码围栏（如 ```markdown ```）包裹 frontmatter，解析后正文不再带围栏
- 多技能打包 zip 批量导入、沙箱执行引擎，均延后到后续阶段

### 13.3 命中与使用场景

procedural 自定义 Skill 的消费支持两种入口，均可追踪、可审计：

```text
场景 1  自动意图检索  MatchIntent 三级匹配，单轮上限 2 篇（hit=auto）
场景 2  显式指定       会话输入 @技能名，精确解析（hit=explicit）
```

使用规则：

- 显式 `@技能名` 优先级高于自动检索：只要转发到后端的 `skills` 列表非空，
  本轮只注入显式选中的 Skill，不叠加自动检索结果；任一名称不存在或未启用时，
  整批拒绝该回合请求并提示，不做部分注入或静默降级。
- Agent 侧新增两个只读工具（`RiskSafe`、无需审批，Schema 标注
  `[READ ONLY][RISK: SAFE][APPROVAL REQUIRED: NO]`）：
  - `list_skills`：列出当前已启用的 Skill（内置/自定义，含类型与领域），用于
    “有哪些 skill / 这个 skill 干什么 / 该用哪套方法论”类咨询。
  - `get_skill`：按名称读取单篇 SKILL.md 的元数据与正文（正文按 96 KiB 截断并置
    `truncated`，assets 仅元数据且脚本 `inert=true`）；未知名或未启用的技能返回
    工具级错误文本，绝不进入执行路径。
- 每次命中同步更新运行状态并审计：
  - `hit_count` 累加、`last_hit` 更新（供 Skill 列表页展示命中次数 / 最近命中）；
  - 落库 `SKILL_RUN`（operation=retrieve）事件，`ResultSummary` 记录
    `{"hit":"auto"|"explicit"}`，标明命中来源是自动意图检索还是显式 `@技能名` 指定。
- SKILL.md 正文仍按既有不可信边界处理：只作文本上下文注入、不解析不执行，
  方法论文本里的命令由 Agent 翻译为受信工具调用（Tool → Approval → Code Guard →
  Executor），能力无法覆盖的步骤明确拒绝并记入审计。

---

# 14. 资源创建能力

资源创建、SSA、Approval 与安全执行是平台内部统一能力。

四期不为：

```text
Deployment
StatefulSet
Service
PVC
ConfigMap
...
```

分别创建大量执行 Tool。

原则：

```text
Skill = 怎么做
Tool = 能做什么
Agent = 决定什么时候做
```

例如：

```text
resource-create Skill
        ↓
生成/规划 Manifest
        ↓
统一 Resource Mutation Tool（复用 create_resource / kubectl 受控通道，不新增 k8s_apply 写工具）
        ↓
Approval v2
        ↓
Code Guard
        ↓
Executor
        ↓
Kubernetes
```

后续扩展资源类型时优先复用统一资源变更 Tool。

---

# 15. 配置中心

配置中心管理**平台运行时动态配置**，唯一权威源是 DB `settings` 表，与配置文件完全独立、互不覆盖：`config.yaml` 只承载静态/`platform:` 启动参数（由 `k8s-ai config init` 默认模板输出，最终形态经 K8s ConfigMap 挂载），CLI 直用配置独立为 `~/.k8s-ai/cli.yaml`（由 `k8s-ai config init --cli` 输出）。页面保存只写 DB（敏感值加密落库、读面脱敏），server 与 Chat/Agent 运行时统一从 Store 读取。

读取优先级（v1.16 明确，见 ADR-040）：**环境变量覆盖层 > DB `settings` > 内置默认**。其中 **DB `settings` 是动态键的默认唯一权威源与唯一写入目标**（ADR-031 不变）：正常路径取值只来自 DB、页面保存也只写 DB；env 覆盖层只在白名单键命中时临时压过 DB，不回写、不持久化。

环境变量只作部署期注入与灰度覆盖，覆盖面**收窄**为 `K8S_AI_LLM_*`（endpoint / model / api_key /
temperature / max_tokens / timeout）与 `K8S_AI_PLATFORM_STORE_DSN`，其余动态键只认 DB，不接受环境变量
覆盖。内置默认（`http://localhost:8000/v1` + `qwen-plus`）仅作最后兜底：容器内部署若 DB 与环境变量
均未提供模型配置，Chat 必须返回明确可读的错误提示，不得静默失败或拿假默认值继续跑。

页面保存仍只写 DB、不回写 `config.yaml`，保存后即时生效，不需要重启进程。

配置中心分组（固定为这 7 组，平台部署后由管理员在页面维护）：

```text
模型
Skill
Approval
Memory
Retention
MCP
运维
```

模型：

```text
provider
base_url
model
api_key
thinking
timeout
```

敏感字段：

- 加密存储
- API 不返回明文
- 前端不回显

### CLI 直用边界

`k8s-ai chat / k8s-ai scan [resource]` 属于**终端二进制直用**入口：默认读取 CLI 直用配置 `~/.k8s-ai/cli.yaml`（由 `k8s-ai config init --cli` 生成，承载 llm/agent/scan/kubernetes 等全量引擎键与缺省，文件头注明仅 CLI 读取），以 `--config` / 环境变量 / 命令行 flag 作为覆盖输入，**不依赖平台 DB**；`cli.yaml` 缺失时回退读取旧的 `config.yaml` 兼容存量用户。`server` 读取的静态 `config.yaml` 与 CLI 的 `cli.yaml` 是两份独立文件；平台页面修改 7 组动态键只写 `settings` 表，不回写、不覆盖任何本地配置文件。按执行面隔离，不存在同一份活配置的双权威。

---

# 16. Approval v2 平台化

四期不重新设计 Approval v2。

统一保持：

```text
Risk
 ↓
Approval Policy
 ↓
Authorization Context / Grant
 ↓
Decision
 ↓
Code Guard
 ↓
Executor
```

Risk 与 Decision 必须分离。

Decision 是 Risk 叠加 Approval Policy 与 Authorization Grant 后形成的结果；Code Guard 的 DENY 始终优先于 Policy 与 Grant。

Decision：

```text
AUTO
REQUIRE_APPROVAL
DENY
```

---

## 16.1 Web 配置

展示：

```text
Approval Mode
├── Strict
└── Smart

Risk Policy
Shell Policy
Deny Operations
Secret Policy
Grant Policy
```

---

## 16.2 Audit

Approval 必须记录：

```text
approval_id
session_id
turn_id
user_id
cluster_id
risk
decision
approval_mode
grant_id
match_reason
created_at
```

核心目标：

> 能解释“为什么这一次自动执行 / 为什么要求审批 / 为什么被拒绝”。

---

# 17. 用户与权限

角色：

```text
user
operator
admin
```

三角色固定为 `user / operator / admin`。角色、会话所有权和 namespace 资源授权是三层
相互独立的授权模型：

```text
平台 Permission   决定能看到哪些平台页面、调用哪些平台管理 API；
会话所有权        决定能访问哪些 Chat 会话及其历史、审批和运行控制；
namespace 授权    决定能在哪些 Kubernetes namespace 内执行哪些资源操作。
```

### 17.1 角色权限基线

| 角色 | 平台 Permission | 会话权限 | namespace 资源权限 |
| --- | --- | --- | --- |
| `user` | 默认零 | 登录、创建会话并访问自己的会话 | 默认零；由 admin 按集群和 namespace 显式授权 |
| `operator` | 默认零 | 登录、创建会话并访问自己的会话 | 默认零；必须按集群和 namespace 显式授权，不因角色自动获得跨 namespace 能力 |
| `admin` | 全量 | 对全部会话可读写、运行控制与审批 | 全量；可管理平台用户、角色、授权、配置与集群接入 |

- `user` 在已授权 namespace 内拥有除销毁类外的全部操作能力。
- 创建用户时角色只能选择 `user / operator / admin`，默认 `user`；不得出现其他角色选项。
- `operator` 与 `user` 使用相同的 namespace 授权边界；区别是 operator 可按操作策略承担更高等级的
  运维能力，并可由 admin 单独委派销毁类能力。跨 namespace 操作仍需逐个 namespace 获得授权。
- 销毁类 `delete namespace / delete pvc / delete pv` 默认仅 admin 可执行；可由 admin 单独委派给
  operator，委派必须记录授权人、范围和有效期，执行时仍需二次确认并留痕。
- 未获得 namespace 授权时，不能查询、诊断或操作该 namespace；未获得集群级授权时，不能读取
  Node、PV、StorageClass 等集群范围对象。
- 角色不决定普通业务会话“谁来审批”。普通角色只能确认自己的会话，或由审批子 Agent 按显式白名单
  自动执行；`admin` 对全部会话的发送消息、运行控制和审批属于平台管理员能力，不建模为
  “为他人指定审批人”，但必须记录操作者为该 admin 并写入审计。

审批与角色解耦：是否确认由平台统一策略决定；审批主体只有操作者本人自确认或审批子
agent（Approval Agent）自动两类，不引入「他人在线审批」，任何情况下不能审批自己的
请求（会话内操作者自确认是其自身操作，不受该约束）。

### 17.2 会话访问矩阵

平台所有 `/api/v1/sessions*` HTTP 与 SSE 端点都要求有效登录态。

| 操作 | user | operator | admin |
| --- | --- | --- | --- |
| 创建自己的会话 | 允许 | 允许 | 允许 |
| 列出自己的会话 | 允许 | 允许 | 允许 |
| 列出全部会话 | 禁止 | 禁止 | 允许（只读） |
| 读取自己的会话历史与事件流 | 允许 | 允许 | 允许 |
| 读取他人会话历史与事件流 | 禁止 | 禁止 | 允许 |
| 发送消息、审批、取消运行 | 仅自己的会话 | 仅自己的会话 | 全部会话 |
| 重命名、置顶、删除 | 仅自己的会话 | 仅自己的会话 | 全部会话 |

约束：

- `ownerUserId` 在会话创建时由后端从认证主体写入，创建后不可修改。
- 客户端提交的 owner、角色或权限字段一律不可信。
- 对非本人会话按 ID 发起操作返回 `404`，避免泄露会话是否存在。
- admin 对他人会话的读写、运行控制与审批必须写审计。
- 系统不得创建无 owner 的会话；发现异常无 owner 记录时由 admin 处置。

### 17.3 Permission 与资源授权边界

产品内平台 Permission 仅作用于 Web 页面、平台 API 入口与审批策略，不进入 CLI 执行路径。
namespace / resource 的真实权限由平台 AuthzGrant 派生到 Kubernetes RBAC，并在 API Server
强校验；执行前还必须完成 scope 判定与 SAR。

Agent 在 Chat 内的诊断行为与角色统一，不因用户角色产生诊断差异；差异只来自授权 scope。
读取授权不等于写入授权，变更与销毁仍走统一审批、Code Guard、Executor 与准入控制。

权限不要在业务代码中大量直接判断：

```go
role == "admin"
```

应采用：

```text
Role
 ↓
Permission
 ↓
Policy
```

基础 Permission 示例：

```text
cluster.read
cluster.manage
skill.read
skill.manage
approval.read
approval.execute
audit.read
settings.read
settings.write
user.manage
```

`chat.read / chat.write` 不作为平台 Permission 使用；登录后的会话能力由上述会话访问矩阵约束。
角色只是平台默认 Permission 集合，不能代替会话所有权或 namespace 授权。

---

# 18. 审批中心

页面：

```text
待审批
审批历史
规则说明
```

支持：

- approve
- reject
- 查看风险
- 查看 Tool
- 查看参数摘要
- 查看资源
- 查看 Cluster
- 查看 Grant
- 查看审计关联

## 18.1 审批主体

普通业务会话的审批主体只有两类：

```text
本人自确认            会话内操作者弹框确认，复用平台 Approval 与 WebUI pending 通道
审批子 agent 自动     Approval Agent 仅接管自动审批路径，只处理白名单「可自动」项
```

`admin` 对他人会话的读取、发送消息、运行控制和审批是平台管理员例外能力，不作为第三方审批人
选择机制；相关动作统一以该 admin 为操作者写审计。

不存在第三类「他人选择审批人」。审批中心的增量在于：自动路径决策主体是非人类（策略 /
子 agent，落 AUTO 记录）；定时任务 / CI / MCP 等无人在线的执行面由中心统一定义决策
主体；资质约束为 scope 级（需持有对应 scope 的 approval.execute 且非请求发起人），
禁止第三方代确认、禁止把销毁类转成「找人在线审批」。

## 18.2 自动审批白名单

自动审批采用显式白名单，不依赖「可逆」等模糊定义：

```text
恒自动（读取/提案）    get / list / watch / describe / logs / events、
                       dry-run / diff / plan（只产提案，不落地）
可自动（有界）         scale：仅授权 namespace 内可伸缩资源，副本数受上下限与
                       单次变动阈值约束
需确认                 rollout restart（可能引发雪崩，默认不自动）
永不自动               白名单外写操作至少自确认；delete namespace / pvc / pv、
                       shell 构造、未知/危险字段、回收类、无法可靠解析的命令恒人工
```

总开关默认关闭；开启后用户可在会话内自行开关，每次 AUTO 放行必须写审批中心记录并
落审计，总开关不能覆盖高危类。平台 AuthzGrant 为授权权威，Kubernetes RBAC 为派生，
定期对账回收，执行前 SAR 只验权限、操作安全由 Code Guard + 准入控制把关。

---

# 19. 审计中心

Audit 至少覆盖：

```text
LOGIN
LOGOUT
SESSION
TOOL_CALL
APPROVAL
GRANT
RESOURCE_MUTATION
CONFIG_CHANGE
SKILL_RUN
CLUSTER_CHANGE
```

字段：

```text
audit_id
timestamp
user_id
session_id
turn_id
step_id
cluster_id
event_type
resource_kind
resource_name
operation
risk
decision
approval_id
grant_id
match_reason
parameter_summary
result_summary
status
```

敏感字段：

- 脱敏
- 哈希
- 摘要化

不得保存 Secret 明文。

---

# 20. Memory

Memory 与 Chat History 分离。

流程：

```text
Session History
      ↓
Session Summarizer
      ↓
Memory Candidate
      ↓
Memory Policy
      ↓
Long-term Memory
```

长期记忆可包含：

```text
用户目标
已做决策
关键参数
未完成事项
常用环境上下文
```

禁止：

```text
Secret
kubeconfig 明文
未脱敏日志
未脱敏事件
未批准操作详情
```

Memory 支持：

```text
user
namespace
cluster
```

范围隔离。

---

# 21. 数据保留

数据类型：

```text
session
audit
scan
skill_run
memory
config_history
user_metadata
kubeconfig credential
```

Retention Policy：

```text
enabled
retention_days
max_count
cleanup_mode
cleanup_frequency
```

必须提供统一 Retention Scheduler：

```text
Retention Scheduler
├── Session cleanup
├── Audit cleanup
├── Scan cleanup
├── Skill Run cleanup
├── Memory cleanup
└── Archive cleanup
```

不同数据类型必须可以独立配置。

---

# 22. Storage

统一 Store 接口。

```text
UserStore
SessionStore
RoleStore
AuditStore
SettingsStore
SkillStore
ApprovalStore
MemoryStore
RetentionStore
ClusterStore
CredentialStore
```

默认：

```text
MySQL
```

可选（本地单进程 / 离线兜底）：

```text
SQLite
```

保持业务层不依赖具体数据库。

不提前引入 Redis。

**四期主存储为 MySQL**（v1.16 修订，见 ADR-042）：企业环境已有数据库实例，平台库默认落 MySQL，SQLite 降为
本地单进程开发与离线交付兜底，不再是生产默认。两种驱动共用统一 schema 版本与迁移入口，业务层只依赖 Store
接口。含口令的 DSN 只经环境变量 / Secret 注入，YAML 中出现非空 `platform.store.dsn` 时 `config validate`
必须告警。

**数据库前置（v1.17 明确）**：产品**不执行** `CREATE DATABASE`。库、账号与权限由 DBA 预建，`server` 与 `k8s-ai db migrate` 只在已授权的库内建表与升级 schema。库默认字符集建议 utf8mb4；即使库默认是 utf8mb3，产品建表 DDL 也显式带 `DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`（`internal/store/dialect.go`），不依赖库默认。平台账号只授予该库的 DML 与建表/改表/索引/DROP，不需要 `SUPER`、`FILE` 等全局权限。

**MySQL 门禁实跑（v1.17）**：`K8S_AI_TEST_MYSQL_DSN` 注入测试库后，连通、schema 迁移幂等、重启续用三类用例必须真实执行，不得长期停留在 skipped。门禁实例地址、端口与凭据只在本地或 CI 注入，严禁写入文档、清单与测试代码；`deploy/example/` 的 MySQL 清单属示例口径、非产品默认，只含占位值。

主 MySQL **不等于**解锁多副本：单副本的真因是内存态会话/Token、SSE 与审批 pending 通道，与数据库驱动无关。

多副本不止取决于数据库：除 MySQL 外，还需要会话/Token 状态持久化与 SSE、审批等待通道的跨副本方案（本期不引入 Redis）。在这些条件满足之前，平台进程保持单副本（见 ADR-030）。

---

# 23. API Contract

统一：

```text
/api/v1
```

API 必须统一定义：

```text
request_id
error.code
error.message
timestamp
pagination
```

认证：

```text
POST /auth/login
POST /auth/logout
GET  /auth/me
```

集群：

```text
GET    /clusters
POST   /clusters
POST   /clusters/test
GET    /clusters/current
DELETE /clusters/current
```

Skill：

```text
GET   /skills
GET   /skills/{name}
POST  /skills/import
PATCH /skills/{name}/enabled
```

配置：

```text
GET   /settings/{group}
PATCH /settings/{group}
```

用户：

```text
GET    /users
POST   /users
PATCH  /users/{id}
DELETE /users/{id}
GET    /roles
PATCH  /roles/{name}/permissions
```

Approval：

```text
GET  /approvals/pending
POST /approvals/{id}/approve
POST /approvals/{id}/reject
GET  /approvals/history
```

Audit：

```text
GET /audit
```

支持：

```text
time
type
user
cluster
session
event
```

Chat：

```text
SSE
```

必须复用统一 Agent Session/Event 模型。

---

# 24. API 与 Service 边界

Handler 不直接操作：

```text
Kubernetes
LLM
Store
Executor
```

统一：

```text
HTTP
 ↓
Handler
 ↓
Application Service
 ↓
Domain / Agent Runtime
 ↓
Tool / Store / Policy
```

CLI 直接：

```text
CLI
 ↓
Application Service
```

---

# 25. 可观测性

四期必须建立统一：

```text
request_id
session_id
turn_id
step_id
```

Agent、API、Tool、Approval、Executor、Audit 都应该可以关联。

目标：

能够回答：

```text
这个请求是谁发起的？
哪个 Session？
哪个 Turn？
Agent 调了哪些 Tool？
为什么调用？
风险是多少？
是否审批？
哪个 Grant 命中了？
最终执行了什么？
结果是什么？
```

---

# 26. 部署

平台采用「前后端工程分离 + 单二进制一体化交付」架构，支持两种部署形态：**默认一体化（形态 A）**
与**可选前后端分离（形态 B）**，两者共享同一套业务代码与 `/api/v1` API 契约。Kubernetes 是四期
生产主场景，默认单副本稳定运行。

## 形态 A：一体化部署（默认）

Go 单二进制内嵌前端构建产物，由 `k8s-ai server` 在同一进程、同一端口提供：

```text
/api/v1/**        平台 API（含 Chat SSE）
/**               SPA 静态资源（web/dist 经 go:embed 内嵌，需 history fallback 与缓存策略）
```

适用：单机部署、POC、测试环境、企业常规部署、离线交付、快速安装与升级。该形态是当前优先维护和
验证的标准部署形态，不因引入可选部署模式而改变既有架构。

## 形态 B：前后端分离部署（可选、按需启用）

前端独立构建为静态资源（`web/dist`），由独立 Nginx（或企业既有静态资源托管、网关、CDN）托管；
后端服务关闭静态资源路由，只提供 `/api/v1`（含 Chat SSE）与 `/healthz`、`/readyz`、`/version`。
默认**同域名、按路径分流**：`/assets/*` 与 `/`（SPA fallback）→ 前端服务，`/api/v1/*` → 后端服务。
前端访问 API 使用相对路径 `/api/v1`（构建期可用 `VITE_API_BASE` 覆盖；该变量当前尚未被前端消费，
接入随 P6.13 收口）；跨域与独立子域不作为当前强制需求。允许前后端独立发布、滚动与回滚，通过
`/api/v1` API 契约测试守住兼容面。

该形态属于可选部署能力。不得为支持该形态而大规模重构后端、拆分微服务、重写前端 API 层或改变已有
业务逻辑；实现以最小开关改动为原则（如后端关闭静态路由）。若成本明显增加，需重新评审。

适用：客户明确要求前端独立发布/回滚、企业已有统一静态资源托管/网关/CDN 规范、后续确需前后端独立
维护和交付。

## 本地 / 单进程（开发调试与一体化回退）

一体化形态本地直接启动：

```bash
k8s-ai server \
  --addr 0.0.0.0:8888 \
  --config /etc/k8s-ai/config.yaml
```

开发阶段前端使用 Vite dev server，API 统一指向平台 `server`；生产使用形态 A 内嵌 SPA
或形态 B 的静态托管，不引入独立 Chat 服务入口。

## Kubernetes（生产主场景，两种形态均可）

```text
形态 A：Deployment（单容器，内嵌 SPA）+ Service + Ingress + ConfigMap + Secret + PVC + RBAC
形态 B：Deployment k8s-ai-web（Nginx + dist）
       + Deployment k8s-ai-server（纯 API）
       + 两个 Service + 一个 Ingress + ConfigMap + Secret + PVC + RBAC
```

形态 B 的 Ingress 按路径分流；SSE 反代必须关闭代理缓冲并放大读超时。两种形态的平台进程均默认
`replicas: 1`；扩副本需先满足会话/Token 持久化与 SSE、审批通道跨副本方案（见 ADR-030）。多副本/
高可用/分布式协调不属于四期目标。

配置经 ConfigMap 挂载并以 `--config` 传入。平台库默认 MySQL，DSN 经 Secret 注入；SQLite 兜底路径的数据目录
用 PVC 承载，报告与导出文件按需可选挂载 PVC，未指定存储类时不创建 PVC。初始 admin 密码只在**空库首启**时经
Secret 注入，已完成初始化的库重启不再要求该变量。日志走标准输出，不再使用 `nohup` 重定向到文件。

探针语义（v1.16 明确）：`/healthz` 是进程级 liveness，不触碰外部依赖；`/readyz` 必须真实反映可用状态，
等于 Store 连通性探测 + schema 迁移完成标记，任一不满足即返回非 200。清单与实现都不得把两者写成同义的空壳。

前端产物与镜像（v1.16 明确，见 ADR-041）：`web/dist` 以 `go:embed all:web/dist` 内嵌，仓库保留
`web/dist/.gitkeep` 占位；镜像构建默认在 Dockerfile 的 node 阶段现场构建前端，保证干净检出即可复现，
离线交付用 BuildKit named build context 覆盖该阶段以复用本地 `web/dist`。`web/dist` 为空时构建不失败，
静态路由返回明确提示；未知 `/api/v1/*` 必须回 JSON 404，不得被 SPA fallback 吞成 `index.html`。

Chat SSE 反代由服务端配合：SSE 响应带 `X-Accel-Buffering: no`，并在 Ingress / Nginx 关闭响应缓冲、放大读超时。

清单与脚本中**禁止出现任何具体环境信息**（IP、域名、存储类名、镜像地址一律走参数或占位符）：本产品是通用
交付物，不面向单一环境。

配置面（ADR-031，v1.13 修订）：`config.yaml` 只承载**静态/启动级**参数（`platform:` 段：
`server.addr`、`server.tls.*`、`store.driver`、`store.sqlite_path`、`store.dsn`、
`auth.admin_username`、`auth.token_ttl`、`auth.session_store`），是一份可评审、可 diff、可回滚的部署描述；
敏感值（admin 首启口令、LLM api_key、含口令的 DSN）只从环境变量 / Secret 注入，YAML 中不得出现明文。

CLI 直用配置（ADR-031 v1.12 修订）：`k8s-ai chat / scan` 默认读取独立的 `~/.k8s-ai/cli.yaml`，由 `k8s-ai config init --cli` 生成，承载 llm/agent/scan/kubernetes 等全量引擎键与缺省，文件头注明仅 CLI 读取、平台不读取也不回写；`cli.yaml` 缺失时回退读取旧 `config.yaml` 兼容存量用户。`config init` 默认仍输出 server 静态 `config.yaml`（只含 `platform:` 段）。默认加载路径按命令面区分：`server` → `config.yaml`，`chat/scan` → `cli.yaml`，`--config` 均可覆盖。`config validate` 支持 `--mode server|cli` 分别校验静态段与全量引擎键。

**7 组动态配置（model/skill/approval/memory/retention/mcp/ops）从 `config.yaml` 移出，唯一落在
DB `settings` 表**：平台部署后由管理员在配置中心页面维护，server 启动只读 `platform:` 静态段，
Chat/Agent/LLM 等运行时组件统一从 Store 读 DB；页面改动不会回写 `config.yaml`，改文件也不会覆盖
DB 动态键；环境变量可作最高优先覆盖层，覆盖面收窄为 `K8S_AI_LLM_*` 与 `K8S_AI_PLATFORM_STORE_DSN`
（见 §15、ADR-040）。平台 schema 升级入口是 `k8s-ai db migrate`（单向、幂等，可作 K8s Job 或
initContainer），`server` 启动内保留同一迁移的幂等兜底；`k8s-ai migrate` 只承担三期旧配置的一次性导入，
两者职责不混用。

默认保持只读 RBAC。

写权限显式开启。

写执行依赖（关键约束）：容器镜像保持最小化，写执行统一走 client-go；kubectl 受控通道只用于
显式配置 kubectl 的非容器场景。增强的执行器仍必须保持 Code Guard + internal/executor
双层边界，且为唯一写执行通道。


# 27. 一键安装

```text
deploy/install.sh
```

参数：

```text
IMAGE
TAG
NAMESPACE
STORAGE_CLASS
INGRESS_HOST
ENABLE_WRITE
ADMIN_PASSWORD
```

脚本负责编排，不替代正式 YAML：正式清单必须能独立 `kubectl apply` 跑通，脚本只做参数替换、顺序编排与
幂等重跑。

密钥与口令语义（v1.16 冻结）：

```text
K8S_AI_ENC_KEY   已存在则复用，绝不覆盖；缺失才生成，并在本地备份（权限 600）；
                 轮换只走显式 --rotate，重跑脚本不会改密钥
ADMIN_PASSWORD   缺省时随机生成，只在本次安装打印一次，不再重复输出
ENABLE_WRITE     true 时显式 apply rbac-write.example.yaml 并二次确认
STORAGE_CLASS    为空则不创建 PVC（MySQL 主存储下 PVC 为可选）
```

脚本必须幂等：重复执行不重置密钥、不轮换口令、不改已存在的 Secret 数据；执行前支持 `kubectl diff` 预演。

---

# 28. 安全红线

必须保持：

1. Secret 不进入 LLM。
2. Secret 不进入 Audit。
3. Secret 不进入 Memory。
4. kubeconfig 不进入 LLM。
5. kubeconfig 不进入普通 Session。
6. kubeconfig 不进入 Audit。
7. 前端隐藏不是安全机制。
8. 所有 API 后端强制鉴权。
9. Skill 不新增执行通道。
10. 所有写操作必须经过既有 Approval / Code Guard / Executor。
11. Code Guard 永远是最终安全边界。
12. 产品角色不替代 Kubernetes RBAC。
13. `skip_permissions` 等配置不能绕过 Code Guard。
14. Shell 的安全约束不能因为 Web 平台化而降低。
15. 产品授权以 namespace 为唯一边界，AuthzGrant 为权威、Kubernetes RBAC 为派生并对账
    回收；执行前 SAR 只验权限，操作安全仍由 Code Guard + 准入控制把关。
16. 销毁类（delete namespace / pvc / pv）仅 admin，operator 需 admin 显式委派；未授权
    发起时确定性拒绝并提示联系管理员，不建审批、不阻塞等待。
17. 自动审批仅限显式白名单「可自动」项，默认关闭 + 会话开关；白名单外至少自确认、
     高危类与 shell 永不自动，总开关不能覆盖高危类。
18. `K8S_AI_ENC_KEY` 一旦丢失，已加密落库的凭据（模型 api_key、MCP env、kubeconfig）不可恢复；
    密钥必须有离线备份，轮换必须走显式流程并重新加密存量。
19. 产品清单、镜像与安装脚本不得硬编码任何具体环境信息（IP、域名、存储类、镜像仓库地址），
    环境差异只允许由部署参数或占位符替换提供。

---

# 29. 数据与配置迁移

企业平台的 Migration 只维护同一平台内的单向、幂等升级：

```text
平台 schema vN
  ↓
平台 schema vN+1
  ↓
四期 Store
```

迁移不得导致平台既有 User、Session、Audit、Skill、Approval、Config 或 Report 数据无提示丢失；
无法迁移的数据必须显式报告，不能静默丢弃。

无法迁移的数据必须明确说明。

迁移入口（v1.16 明确，见 ADR-042）：schema 升级由 `k8s-ai db migrate` 承担，仅做 schema、单向且幂等，
可作 K8s Job 或 initContainer；`server` 启动内保留同一迁移的幂等兜底，Job 失败即阻塞启动。配置内容导入
仍由 `k8s-ai migrate` 一次性显式执行，两个入口不混用。

---

# 30. 四期实施阶段

不要一次性完成所有功能。

## Phase A：平台底座

完成：

```text
Vite/Vue
API Contract
Store
Auth
Session
Agent Runtime
SSE Event
Config Service
Audit Model
Migration
```

---

## Phase B：Web Shell

完成：

```text
App Layout
Router
Pinia
Login
Chat
SSE
Agent Timeline
Tool Card
Approval Card
```

先保证平台 Chat 能完整运行。

---

## Phase C：平台能力

完成：

```text
Cluster
Skill
Config
User
Role
Permission
```

---

## Phase D：Governance

完成：

```text
Approval Center
Approval v2 Web
Authorization Grant
Audit
Audit Timeline
```

---

## Phase E：Memory

完成：

```text
Session Summary
Long-term Memory
Memory Policy
Retention Scheduler
```

---

## Phase F：Production

完成：

```text
Kubernetes Deployment
RBAC
PVC
Install Script
Security Test
E2E
Migration Test
Documentation
Release
```

---

# 31. 版本规划

## v4.0

平台基础能力：

- Vue3
- API
- Auth
- Store
- Session
- Agent Runtime
- Chat
- Cluster
- Skill
- Config
- User

## v4.1

治理能力：

- Approval Center
- Approval v2 UI
- Audit
- Authorization Grant
- Operation Timeline

## v4.2

长期数据：

- Memory
- Session Summary
- Retention
- Cleanup
- Archive

---

# 32. 验收标准

必须满足：

```text
go test ./...
go vet ./...
tests/arch
frontend lint
frontend build
```

全部通过。

另需补充验证：
- Agent Runtime 单测覆盖多轮循环、审批暂停/恢复、拒绝后改方案、Code Guard DENY 优先级。
- SSE 事件流 mock 测试：session/turn/step 状态推进与事件字段完整性。
- 前端路由权限与后端 API 越权测试（越权返回 401/403）。
- kubeconfig 与 Secret 不进入审计、会话、记忆的红线专项测试。
- 部署冒烟：K8s 两种形态各跑通登录→Chat→审批→创建→审计/E2E，默认单副本；形态 A 为 `k8s-ai server`（SPA + `/api/v1` + Chat SSE 同源）单 Deployment，形态 B 为「Nginx+dist 前端 + 纯 API 后端」双 Deployment；本地单进程作为辅助回归口径。

Web：

- 可以登录
- Session 正常
- Chat 正常
- SSE 正常
- Agent 多步执行正常
- Tool Card 正常
- Approval Card 正常
- 页面权限正常

平台：

- Cluster 可连接
- kubeconfig 安全
- Skill 可管理
- Config 可持久化
- User / Role 正常
- Approval 正常
- Audit 完整
- Memory 正常
- Retention 正常

平台验收：

- 会话按 owner 隔离，普通角色不可读写他人会话
- 平台 Permission、会话所有权、namespace 授权三层判定均生效
- Tool / Skill / SSA / Approval / Code Guard 保持统一执行链路
- 单进程与 Kubernetes 两种交付形态可运行

---

# 33. 四期核心架构原则

整个四期必须遵守以下原则：

```text
Agent = 决策
Skill = 方法（structured 确定性规则入 create_resource；procedural SKILL.md 由 Agent 翻译为受信工具调用）
Tool = 能力
Policy = 权限/审批决策
Executor = 执行
Code Guard = 最终安全边界
Store = 持久化
Web = 平台入口
CLI = 快速运维入口
Audit = 全链路事实记录
Memory = 长期上下文
```

禁止职责反向侵入。

尤其：

```text
Skill ≠ Executor
Web ≠ Security Boundary
Risk ≠ Approval Decision
Memory ≠ Chat History
Role ≠ Kubernetes RBAC
CLI ≠ Web API Client
```

---

# 34. Codex 开发约束

企业平台开发必须保持纵向切片和统一边界：

1. 先冻结 API、Store、Session/Event 与权限契约。
2. 再实现平台服务与 Agent Runtime 接入。
3. 再实现 Web/CLI/自动化任务的统一应用服务复用。
4. 不允许为了新功能复制 Tool / Executor。
5. 不允许 Skill 建立独立写执行路径。
6. 每个阶段必须有自动化测试和可运行结果。

四期开发过程中：

```text
Architecture First
Contract First
Incremental Delivery
Single Authorization Model
Security Boundary Unchanged
```

作为最高优先级原则。

---

# 35. 修订记录

| 版本 | 日期 | 说明 |
| --- | --- | --- |
| v1.0-draft | 2026-09-30 | 初版：四期平台化需求与范围 |
| v1.1 | 2026-09-30 | 架构基线：Agent Runtime / SSE / Approval v2 平台化 / Skill 非执行器 / Storage / 部署双轨 |
| v1.2 | 2026-09-30 | 最终需求方案：双层安全边界、写工具统一受控通道、SSE 兼容迁移、镜像写执行方案、双驱动存储迁移、权限作用域、验收测试细化 |
| v1.3 | 2026-10-13 | 审批与授权方案修订（见 `docs/02-架构与设计/13-方案-企业级身份权限与审批中心.md`）：角色收敛为 user/operator/admin；授权以 namespace 为唯一边界、零权限起步、admin 划分 namespace；销毁类仅 admin 并支持 operator 委派、未授权直接提示联系管理员；审批主体只有本人自确认或审批子 agent 自动；自动审批显式白名单（默认关闭+会话开关）；AuthzGrant 权威 + RBAC 派生对账 + 执行前 SAR；平台演进为多 Agent 编排并引入 Approval Agent；修正 P4.8 审批人约束 |
| v1.4 | 2026-10-14 | 部署与交付口径修订（不改需求范围）：Kubernetes 为四期唯一生产形态，单进程运行降为三期兼容与本地开发调试口径；平台主进程 = `k8s-ai server`（`/api/v1` + Chat SSE + 内嵌 SPA 同源单端口），`k8s-ai web` 冻结为三期兼容入口；新增平台进程默认单副本约束与扩副本前置条件；见 ADR-029 / ADR-030。（**已被 v1.12 取代**：「唯一生产形态」改为「默认一体化 + 可选前后端分离」，见 ADR-036） |
| v1.5 | 2026-10-14 | 补两条协议级需求（不改范围，只补此前隐含的约束）：① §8 增补"事件顺序与展示语义"——分片不是展示单元、终态必须最后、增量统一脱敏、思考面板语义；② §26 增补四期配置面——`platform:` 段承载平台级非敏感参数、敏感值只走环境变量/Secret、`server` 支持 `--config`、`config init` 输出全量模板。见 ADR-031 / ADR-032、D-21 / D-22 |
| v1.6 | 2026-10-15 | 增补前端体验基线需求（不改范围）：§10 指向《14-方案-前端体验优化-四期.md》，并固化两条硬约束——主题与令牌（data-theme + 默认深色 + 支持跟随系统 + 首屏防闪烁）与工具结果同源（`tool_result` 由后端计算并同时下发与落库）。见 ADR-033/ADR-034、D-23/D-24、任务 P7.1–P7.5 |
| v1.7 | 2026-10-16 | Skill 双轨口径统一（不改需求范围，消除术语与设计矛盾）：Skill 拆为正交两维——`type`（structured / procedural）与 `source`（builtin / custom / third-party，只表达提供方、不再混用为类型）；structured 为资源创建类确定性规则（`create_resource` 消费 findings、Approval Risk 只升不降、custom/third-party 禁设 `risk.default`），procedural 为标准 Agent 技能（SKILL.md + frontmatter，聊天回合前意图检索后注入系统提示词、支持显式调用）；统一消费与安全边界（SKILL.md 正文为不可信文本上下文、技能包 `scripts/` 一律 inert 且平台不提供脚本执行引擎、方法论文本由 Agent 翻译为受信工具调用、交互式命令与 shell 审批红线及 Secret/kubeconfig 红线不变、能力缺失明确拒绝并记 `SKILL_RUN` 审计）；procedural 导入载体 v1 = 粘贴单/多篇 SKILL.md 文本 + 单个技能包 zip（assets 白名单 md/yaml/json/txt/csv，文件总大小/文件数/解压深度/路径穿越/符号链接有界校验），多技能 zip 与沙箱执行延后。见新增 §13.0 及 §13.1/§13.2、§4 非目标、《01-系统架构与技术方案》§11、开发计划-四期 P3.8.1–P3.8.4 |
| v1.8 | 2026-10-16 | procedural 导入载体 v1 落地状态回写（不改需求范围）：§13.2 所述粘贴单/多篇 SKILL.md 文本导入、单个技能包 zip 导入与静态资产存储均已实现——文本导入前置扫描多文档边界、frontmatter `name/description` 强制解析、KnownFields 严格解码（`POST /api/v1/skills/import-procedural`，2MiB 上限）；zip 导入仅收根目录唯一 `SKILL.md` 主文、assets 走 md/yaml/json/txt/csv 白名单落库为元数据、`scripts/` 一律 `inert=true` 不执行，并带解压总大小/条目数/路径长度/深度/路径穿越/盘符/symlink 防御（`POST /api/v1/skills/import-zip`，32MiB 上限）；Chat 回合前意图检索注入已把 SKILL.md 渐进披露内容注入 Agent`SkillContext` 并记 `SKILL_RUN(retrieve)` 审计。见开发计划-四期 v1.30，代码尚未提交、待真机验证后入库 |
| v1.9 | 2026-10-16 | procedural 导入兼容性收尾（不改范围）：修复两类真实导入报错——① 「直接压缩技能文件夹」的 zip（根目录无 SKILL.md、整包被单层目录包裹）在确认全部条目恰好归入唯一顶层目录后自动剥离该前缀再导入，多顶层目录整包拒绝；② SKILL.md 首行是代码围栏把 frontmatter 整个包起来的写法，按「围栏开行 + 下一行 `---`」识别并跳过开闭围栏。路径穿越/绝对路径/盘符/symlink/非白名单资产/总大小/条目数/深度限制全部保留并整包拒绝。见开发计划-四期 v1.31、§13.2，代码尚未提交、待真机验证后入库 |
| v1.10 | 2026-10-16 | procedural 命中双场景与只读工具落地状态回写（不改需求范围）：§13.3 两种入口已实现——① 自动意图检索重写为三级匹配（整句精确名称 → 整句子串 → 长句关键词打分：名称 3 / 领域 2 / 描述 1，每词只计一次最高权重），单轮最多注入 2 篇（`hit=auto`）；② 显式 `@技能名` 指定，`ChatRequest.skills` 随 send-message 转发，逐个 `FindByName` 精确解析（大小写不敏感），任一未知名/未启用整批拒绝、不做静默降级、不叠加自动检索（`hit=explicit`）。Agent 侧新增只读工具 `list_skills` / `get_skill`（`IsReadOnly`、`RiskSafe`、Schema 标注 READ ONLY / SAFE / APPROVAL NOT REQUIRED），`get_skill` 正文按 96 KiB 截断并置 `truncated`、assets 仅元数据且脚本 `inert=true`、未知名技能返回工具级错误文本；命中同步 `RecordHit`（`hit_count`/`last_hit`）并落库 `SKILL_RUN`（operation=retrieve，`ResultSummary={"hit":"auto"|"explicit"}`）审计。Web Chat 前端 `@` 输入按 `skill.read` 权限弹出已启用的 procedural Skill 并回填，提交时把已匹配的 `@名` 从 query 剥离、仅转发有效技能名。见开发计划-四期 v1.32，代码未提交、待真机验证后入库 |
| v1.11 | 2026-10-21 | 配置面口径修订（不改范围，拆离动态键权威源）：`config.yaml` 只承载静态/`platform:` 启动参数；7 组动态键（model/skill/approval/memory/retention/mcp/ops）唯一落 DB `settings` 表，由配置中心页面维护、server 与 Chat/Agent/LLM 运行时从 Store 读，不再每次启动自动导入；`config init` 模板只输出静态/platform 键与 CLI 直用缺省；`k8s-ai chat/scan` 终端直用仍读本地文件+env+flag、不依赖平台 DB。见 ADR-031 v1.11、§15/§26、P6.12 |
| v1.12 | 2026-10-22 | 部署架构口径统一（仅文档）：明确「前后端工程分离 + 单二进制一体化交付」，支持两种部署形态——形态 A 默认一体化（`go:embed web/dist` + SPA 同源单端口），形态 B 可选前后端分离（前端独立 Nginx 托管 `web/dist`、后端关闭静态路由只提供 API），两者共享同一 `/api/v1` 契约；分离模式默认同域路径分流、相对路径 `/api/v1`，不强制跨域/独立子域；允许独立发布/回滚并由 API 契约测试守住兼容面；维持单副本。见 ADR-029 / ADR-036、开发计划-四期 v1.38、部署手册 §4.9 |
| v1.13 | 2026-10-23 | 企业平台权限分层与会话所有权口径冻结：平台 Permission、Session owner、namespace AuthzGrant 三层独立；user/operator 平台 Permission 默认零，登录与自有会话属于认证基础能力；会话 owner 创建后不可变，普通角色仅访问自己的会话，admin 对全部会话拥有完整读写与运行控制能力；operator 不自动跨 namespace；无权限平台管理入口隐藏且后端强校验。见 ADR-037、开发计划 v1.41 |
| v1.14 | 2026-10-23 | 用户/权限复盘口径澄清：admin 对他人会话的读取、发送消息、运行控制和审批属于平台管理员例外能力，不建模为“为他人指定审批人”，但必须记录 admin 操作者并写审计；同步修正 §17/§18 中与 admin 全会话能力冲突的表述。停用账号 Token 即时失效、API Token 认证闭环、Grant 集群隔离/主体生命周期、组授权、admin 跨用户审计与销毁授权等遗留登记到开发计划 `U-01`–`U-10`，本轮保持现有行为。见开发计划 v1.44 |
| v1.15 | 2026-10-26 | CLI 直用配置口径冻结：终端直用（`k8s-ai chat/scan`）默认读取独立 `~/.k8s-ai/cli.yaml`，由 `k8s-ai config init --cli` 输出，承载 llm/agent/scan/kubernetes 等全量引擎键与 CLI 直用缺省、文件头注明仅 CLI 读取；`cli.yaml` 缺失回退旧 `config.yaml` 兼容存量。`config init` 默认输出 server 静态 `config.yaml`（只含 `platform:` 段），server 与 CLI 配置为两份独立文件、完全独立、互不覆盖，7 组动态键唯一权威源仍为 DB `settings` 表；`config validate` 支持 `--mode server|cli`。仅文档口径修订、实现放 P6.12。见 ADR-031 v1.12、开发计划 v1.45 |
| v1.16 | 2026-10-27 | P6 产品安装部署开工前口径冻结（见开发计划 v1.48、ADR-031 v1.13、ADR-040/041/042）：① 动态配置读取优先级定为**环境变量覆盖层 > DB `settings` > 内置默认**，env 覆盖面收窄为 `K8S_AI_LLM_*` 与 `K8S_AI_PLATFORM_STORE_DSN`，内置默认仅兜底且未配置时 Chat 显式报错，页面改动即时生效；② 平台主存储改为 **MySQL**，SQLite 降为本地/离线兜底，DSN 只经环境变量/Secret 注入，主 MySQL 不解锁多副本；③ schema 迁移入口拆出 `k8s-ai db migrate`（可作 Job/initContainer），server 启动保留幂等兜底，与 `k8s-ai migrate` 配置导入分工明确；④ 镜像构建默认走 Dockerfile node 阶段（干净检出可复现），`go:embed all:web/dist` + `web/dist/.gitkeep` 占位，离线走 BuildKit named build context，`dist` 空时静态路由明确提示，未知 `/api/v1/*` 回 JSON 404，SSE 带 `X-Accel-Buffering: no`；⑤ 首启 admin 口令仅空库必需，`/readyz` = Store 连通 + 迁移完成、`/healthz` 保持进程级；⑥ install.sh 密钥语义冻结（ENC_KEY 复用/缺失才生成并备份/`--rotate` 显式轮换、ADMIN_PASSWORD 随机一次性打印、ENABLE_WRITE 二次确认、幂等重跑）；⑦ 新增安全红线 18/19：ENC_KEY 丢失即凭据不可恢复、禁止在产品清单与脚本硬编码任何环境信息。 |
| v1.17 | 2026-10-27 | P6-A 口径批注确认（见开发计划 v1.49、ADR-040/041/042 增补）：① §15 澄清 **DB `settings` 是动态键的默认唯一权威源与唯一写入目标**，env 仅对白名单键作部署期只读覆盖、命中不回写、页面标注覆盖态；② §22 补数据库前置——产品不执行 CREATE DATABASE，库与账号由 DBA 预建并建议 utf8mb4，表级 DDL 显式 utf8mb4，平台账号只授该库 DML 与建表改表索引权限；③ §26 明确**前后端两个独立 Dockerfile**（仓库根 Dockerfile 构建后端镜像、deploy/web.Dockerfile 构建前端 Nginx 镜像，互不依赖），并明确 SQLite 兜底数据目录必须挂 PVC、MySQL 主口径下 PVC 仅承载报告与导出；④ 新增 deploy/example MySQL 接入示例清单（非产品默认、不含现场地址与口令），MySQL 连通、迁移幂等、重启续用门禁实跑用例计入 P6.1/P6.5/P6.8 验收；⑤ 测试库名 `k8s_ai_test`、产品默认库名 `k8s_ai`，凭据严禁入库。需求条目与优先级不变。 |
