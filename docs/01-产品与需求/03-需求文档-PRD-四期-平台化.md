# k8s-ai 四期需求规格（平台化）

> 版本：v1.4  
> 状态：四期最终需求方案  
> 目标：在不推翻三期 Agent、Skill、SSA、审批、安全执行链路的前提下，将 k8s-ai 从 Agent 工具升级为可持续演进的平台化 Kubernetes 运维 Agent。

---

# 1. 四期定位

四期不是对三期业务能力的重写，而是一次**平台化架构升级**。

三期已经完成：

- Agent 基础运行能力
- 多轮 `Reason → Tool → Observe → Reason`
- Kubernetes 诊断与运维工具
- 内置 Skill
- Kubernetes 资源创建能力
- SSA 创建链路
- Approval / Risk / Authorization
- Code Guard / Executor 安全执行链路
- Web 单文件 HTML Agent UI
- CLI 基础能力

四期在此基础上完成：

```text
单文件 Web Agent
        ↓
平台化 Web Application
        ↓
Agent Runtime + Platform Services
        ↓
多用户 / 集群 / Skill / 配置 / 审批 / 审计 / Memory
```

**核心原则：**

1. 三期核心 Agent Runtime 不推翻。
2. 三期 Tool / Skill / Executor 不推翻。
3. 三期 SSA 创建链路不推翻。
4. 三期 Approval v2 不重新设计，只做平台化接入。
5. Code Guard 与 Executor 为双层强制边界：Code Guard 是审批与门禁的强制边界，internal/executor 是写操作的唯一执行边界。
6. Skill 不允许形成新的写执行通道。
7. Web 是平台入口，CLI 继续保持快速运维工具定位。
8. 四期优先建立稳定的平台骨架，再扩展业务能力。

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

必须保持三期已经实现的动态循环：

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

四期事件模型须与三期 WebChat 既有事件（answer-delta / reasoning-delta / tool-call / tool-result）兼容；迁移期提供事件映射层，保证 Web Shell 迁移期间三期 Chat 仍可运行，未完成映射前不得移除旧事件。

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

必须保留三期已有能力：

- 消息
- Tool Card
- Approval Card
- Reasoning/Thinking 展示
- Tool 输入输出
- 执行状态
- 结果展示

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

---

## 13.1 Skill 数据模型

至少包括：

```text
name
version
author
source
description
domain
resource_kinds
required_inputs
rules
risk_policy
examples
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

## 13.2 Skill 来源

```text
builtin
custom
third-party
```

四期：

- builtin 完整支持
- custom YAML 导入
- third-party 只实现 Loader / Validation Contract
- 不做在线市场

---

# 14. 资源创建能力

三期已有资源创建、SSA、Approval、安全执行能力继续复用。

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

配置中心分组：

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

三角色只决定「权限上限」，不决定「谁来审批」：

```text
user      获得 admin 授权的 namespace 后，拥有该 namespace 内除销毁类外的全部读写能力；
          未授权 namespace 零操作（也不能发起查询/诊断）；销毁类（delete namespace /
          delete pvc / delete pv）不授予，发起时确定性拒绝并提示联系管理员处理
operator  与 user 相同的 namespace 授权，另加跨 namespace 与节点级运维；销毁类需 admin
          显式委派（记录在案、可回收、有过期时间），委派后仍需二次确认并强制留痕
admin     平台与集群全部能力；销毁类操作二次确认，操作仍落审计
```

审批与角色解耦：是否确认由平台统一策略决定；审批主体只有操作者本人自确认或审批子
agent（Approval Agent）自动两类，不引入「他人在线审批」，任何情况下不能审批自己的
请求（会话内操作者自确认是其自身操作，不受该约束）。

产品内权限仅作用于 Web 页面、API 入口与审批策略，不进入 CLI 执行路径；namespace /
resource 真实权限由平台 AuthzGrant 派生到 Kubernetes RBAC 并在 API Server 强校验。
Agent 在 Chat 内的诊断行为与角色统一，不因用户角色产生诊断差异；读取放开不等于
写入放开，变更与销毁仍走统一确认与执行门禁。

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
chat.read
chat.write
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

角色只是默认 Permission 集合。

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

审批主体只有两类：

```text
本人自确认            会话内操作者弹框确认，复用三期 Approval v2 与 WebUI pending 通道
审批子 agent 自动     Approval Agent 仅接管自动审批路径，只处理白名单「可自动」项
```

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
SQLite
```

可选：

```text
MySQL
```

保持业务层不依赖具体数据库。

不提前引入 Redis。

SQLite 面向单进程/单实例部署；MySQL 面向多副本或已有数据库场景。两种驱动共用统一 schema 版本与迁移入口，业务层只依赖 Store 接口。四期 migration 不得导致三期已保存的 session/audit/skill/config/报告无提示丢失；无法迁移的数据必须显式说明。

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

四期生产形态只有一种：**Kubernetes**。单进程二进制运行保留为三期兼容与本地开发调试口径，不再是四期的生产部署目标。

## 平台主进程与前端交付

四期平台主进程是 `k8s-ai server`，在同一进程、同一端口内提供：

```text
/api/v1/**        平台 API（含 Chat SSE）
/**               SPA 静态资源（web/dist 内嵌，需 history fallback 与缓存策略）
```

`k8s-ai web`（三期 webui）冻结为三期封版兼容入口：继续可用、只做兼容回归，不再承载四期新增能力，
四期前端不长期依赖其 `/api/sessions` 裸 JSON 契约。详见 ADR-029。

## 本地 / 单进程（三期兼容与开发调试）

```bash
k8s-ai web \
  --addr 0.0.0.0:8888 \
  --config /etc/k8s-ai/config.yaml
```

## Kubernetes（唯一生产形态）

```text
Deployment
ConfigMap
Secret
PVC
Service
Ingress
RBAC
```

平台进程默认 `replicas: 1`；扩副本需先满足会话/Token 持久化与 SSE、审批通道跨副本方案（见 ADR-030）。
配置经 ConfigMap 挂载并以 `--config` 传入，数据目录用 PVC 承载（或改用 MySQL），初始 admin 密码经
Secret 注入，日志走标准输出，不再使用 `nohup` 重定向到文件。

配置面（ADR-031）：平台级非敏感参数（监听地址、TLS、Store 驱动与路径、admin 用户名、Token 有效期）
由配置文件 `platform:` 段承载，`server` 必须支持 `--config`、`--addr`/`--db` 降级为覆盖手段；
敏感值（admin 首启口令、LLM api_key、含口令的 DSN）只从环境变量 / Secret 读取，YAML 中不得出现明文；
`config init` 必须输出全量模板（模板键集合 = 配置结构体键集合，由测试锁定）。

默认保持只读 RBAC。

写权限显式开启。

写执行依赖（关键约束）：当前镜像为 distroless static，不含 kubectl 与 shell，而三期写路径依赖 kubectl 工具。四期必须二选一：镜像内安装与集群版本兼容的固定版本 kubectl，或把写执行统一走 client-go；增强的执行器仍必须保持 Code Guard + internal/executor 双层边界，且为唯一写执行通道。

---

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

脚本负责编排。

不替代正式 YAML。

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

---

# 29. 数据与配置迁移

四期必须提供 Migration 方案。

至少考虑：

```text
旧 config.yaml
旧 Skill
旧 Session / Chat
旧 Approval 状态
旧 Report
旧配置目录
```

原则：

```text
旧版本
  ↓
Migration
  ↓
四期 Store
```

不能因为 Web 重构导致三期已有数据和配置无提示丢失。

无法迁移的数据必须明确说明。

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

先保证三期 Chat 能完整运行。

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
- 部署冒烟：以 K8s Deployment 为主，平台主进程 `k8s-ai server`（SPA + `/api/v1` + Chat SSE 同源、单副本）跑通 Chat/审批/健康检查；本地单进程作为辅助回归口径。

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

兼容性：

- CLI 行为不改变
- 三期 Tool 不破坏
- 三期 Skill 不破坏
- 三期 SSA 不破坏
- 三期 Approval / Code Guard 不降低安全性
- 单进程可运行
- Kubernetes 可运行

---

# 33. 四期核心架构原则

整个四期必须遵守以下原则：

```text
Agent = 决策
Skill = 方法
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

四期属于大型架构改造。

不得采用：

```text
把旧 HTML 直接拆成几个 Vue 文件
```

作为主要迁移方案。

应：

1. 先建立工程骨架。
2. 再建立 API Contract。
3. 再建立 Store。
4. 再建立 Auth。
5. 再建立 Agent Session/Event。
6. 再迁移 Chat。
7. 再逐步迁移管理页面。
8. 每阶段保持三期功能可运行。
9. 不允许为了前端开发修改三期核心安全链路。
10. 不允许为了新功能复制 Tool / Executor。
11. 不允许 Skill 建立独立写执行路径。
12. 每个阶段必须有测试和可运行结果。

四期开发过程中：

```text
Architecture First
Contract First
Incremental Migration
Backward Compatibility
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
| v1.4 | 2026-10-14 | 部署与交付口径修订（不改需求范围）：Kubernetes 为四期唯一生产形态，单进程运行降为三期兼容与本地开发调试口径；平台主进程 = `k8s-ai server`（`/api/v1` + Chat SSE + 内嵌 SPA 同源单端口），`k8s-ai web` 冻结为三期兼容入口；新增平台进程默认单副本约束与扩副本前置条件；见 ADR-029 / ADR-030 |
| v1.5 | 2026-10-14 | 补两条协议级需求（不改范围，只补此前隐含的约束）：① §8 增补"事件顺序与展示语义"——分片不是展示单元、终态必须最后、增量统一脱敏、思考面板语义；② §26 增补四期配置面——`platform:` 段承载平台级非敏感参数、敏感值只走环境变量/Secret、`server` 支持 `--config`、`config init` 输出全量模板。见 ADR-031 / ADR-032、D-21 / D-22 |
