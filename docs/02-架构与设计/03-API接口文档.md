# k8s-ai API 接口契约设计

- 版本：v4.0.0（企业平台）
- 日期：2026-10-23
- 状态：四期企业平台落地中
- 范围：本文档统一描述四期企业平台的 Go 应用服务契约与 HTTP API，包括认证、会话与 SSE、
  扫描、集群、Skill、配置、用户/角色、授权、审批、审计与 Memory。所有产品 API 统一归属
  `/api/v1`，权限由后端强制校验。

## 1. 接口设计原则

1. **消费方定义接口，实现方返回结构体**（Go 惯用法）：如 scanner 定义自己需要的
   `Reader`，kubernetes 提供满足它的 `Client`。
2. 小接口：一个接口一个关注点；不为未来凭空抽象（AGENTS.md：avoid unnecessary
   abstractions）。
3. 所有接口方法首参为 `context.Context`，错误返回 `error`，可被 `errors.Is` 判等。
4. Domain 端口与 Infrastructure 实现的归属明确（见 §10）。
5. 工具统一走 `internal/tools` 的 `Tool` 契约，由 Agent Loop 编排；HTTP 会话层
   只负责转发事件与审批，不直接持有业务逻辑。

## 2. Kubernetes Reader（Infrastructure 实现）

定义位置：`internal/scanner` 消费方接口 + `internal/kubernetes` 实现（`Client` 结构体）。

```go
type Reader interface {
    ServerVersion(ctx context.Context) (string, error)

    // 全量 list（namespace 传 "" 表示所有命名空间）
    ListNamespaces(ctx context.Context) ([]corev1.Namespace, error)
    ListPods(ctx context.Context, namespace string) ([]corev1.Pod, error)
    ListNodes(ctx context.Context) ([]corev1.Node, error)
    ListServices(ctx context.Context, namespace string) ([]corev1.Service, error)
    ListEndpointSlices(ctx context.Context, namespace string) ([]discoveryv1.EndpointSlice, error)
    ListDeployments(ctx context.Context, namespace string) ([]appsv1.Deployment, error)
    ListReplicaSets(ctx context.Context, namespace string) ([]appsv1.ReplicaSet, error)
    ListStatefulSets(ctx context.Context, namespace string) ([]appsv1.StatefulSet, error)
    ListDaemonSets(ctx context.Context, namespace string) ([]appsv1.DaemonSet, error)
    ListJobs(ctx context.Context, namespace string) ([]batchv1.Job, error)
    ListCronJobs(ctx context.Context, namespace string) ([]batchv1.CronJob, error)
    ListPersistentVolumeClaims(ctx context.Context, namespace string) ([]corev1.PersistentVolumeClaim, error)
    ListPersistentVolumes(ctx context.Context) ([]corev1.PersistentVolume, error)
    ListStorageClasses(ctx context.Context) ([]storagev1.StorageClass, error)
    ListVolumeAttachments(ctx context.Context) ([]storagev1.VolumeAttachment, error)
    ListIngresses(ctx context.Context, namespace string) ([]networkingv1.Ingress, error)
    ListNetworkPolicies(ctx context.Context, namespace string) ([]networkingv1.NetworkPolicy, error)
    ListEvents(ctx context.Context, namespace string) ([]corev1.Event, error)

    // 定向读取：Tool 直接复用，scanner 不调用
    GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error)
    GetNode(ctx context.Context, name string) (*corev1.Node, error)
    GetDeployment(ctx context.Context, namespace, name string) (*appsv1.Deployment, error)
    GetService(ctx context.Context, namespace, name string) (*corev1.Service, error)

    GetPodLogs(ctx context.Context, namespace, pod, container string, opts model.LogOptions) ([]byte, error)
}
```

约束：
- `Client` 内部只允许调用 typed clientset 的 List/Get 方法与 `CoreV1().Pods(ns).GetLogs(...)`。
- 暴露给工具的一面无写方法；Secret 内容值永不返回，法则见 `08-安全设计.md`。
- `ListOptions{ResourceVersion:"0"}` 走 apiserver 缓存（ADR-009）。

## 3. Collector 与诊断编排（Application）

```go
// scanner 包
type Collector interface {
    Phase1(ctx context.Context, opts model.ScanOptions) (*model.ClusterSnapshot, error)
    Phase2(ctx context.Context, snapshot *model.ClusterSnapshot, targets []model.ResourceRef, opts model.ScanOptions) error
}
```

- Phase1 只做 list，产出快照 + EventsIndex + CollectionErrors。
- Phase2 只对异常 Pod 取 current/previous logs（含脱敏与上限）。
- Collector 不知道规则、不知道 LLM。`create_resource` 走独立执行通道，不复用 Collector。

## 4. Rule 与 RuleRegistry（Domain）

```go
type Rule interface {
    Name() string
    NeedsLogs() bool
    Evaluate(ctx *RuleContext) []*model.Finding
}
type Registry interface {
    Register(r Rule)
    All() []Rule
    ByName(name string) Rule
    Filtered(enabled, disabled []string) []Rule
}
type Engine interface {
    Evaluate(ctx context.Context, snapshot *model.ClusterSnapshot, index *model.CorrelationIndex, opts model.ScanOptions) []*model.Finding
}
```

`RuleContext` 为只读输入，规则实现不允许修改快照。

## 5. LLMClient（Infrastructure 端口）

```go
type LLMClient interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}
type ChatRequest struct {
    Model       string
    Messages    []Message
    Temperature float64
    MaxTokens   int
    Tools       []ToolSpec // Tool Calling
}
type ChatResponse struct {
    Content string
    Usage   TokenUsage
}
```

`ChatRequest.Tools` 描述器携带稳定安全元数据前缀（`[READ ONLY]` / `[RISK: ...]` /
`[APPROVAL REQUIRED: ...]`），仅帮助模型选工具，不作为安全边界（见 tools 包
`MetadataDescriber`）。

## 6. 工具契约（`internal/tools`）

```go
type Tool interface {
    Name() string
    Description() string
    InputSchema() any
    Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
    IsReadOnly(input json.RawMessage) bool
    Risk(input json.RawMessage) model.RiskLevel
}
type ToolResult struct {
    Data      any
    Text      string
    Truncated bool
    Redacted  bool
}
```

可选接口：
- `MetadataDescriber`：为模型可见的 Schema 附加安全元数据前缀。
- `GrantTargeter`：写工具提供"本次不再询问"的精确授权目标（kind/name/namespace）。
- `GuardPlanner`：把工具自带门禁要素交给 `approval.CodeGuard` 裁决。

注册集合（`k8stools.RegisterAll` + 诊断 + 写工具 + 创建通道，`agent.disabled_tools`
可摘除）：

| 类别 | 工具名 |
|---|---|
| 只读资源 | get_pod / get_pods / get_node / get_nodes / get_node_usage / get_deployment / get_statefulset / get_daemonset / get_replicaset / get_service / get_endpoints / get_endpointslices / get_pvc / get_pv / get_storageclass / get_configmap / get_configmaps / get_namespace / get_resources / get_events / get_logs（含 previous） |
| 诊断 | scan_cluster / scan_pod / diagnose_pod / diagnose_node / diagnose_workload |
| 写操作 | kubectl（通用，审批门禁） |
| 资源创建 | create_resource（结构化 manifest，dry_run → apply） |

## 7. 审批契约（`internal/approval`）

```go
type Policy struct { RequireApproval, SkipPermissions bool; DenyOps []string; Mode Mode }
type Input struct { ReadOnly, Shell, Denied bool; Risk model.RiskLevel }
type Decision string // auto | require_approval | deny
```

判定链（`Policy.Evaluate`）：`Denied→DENY` → `Shell→REQUIRE_APPROVAL` → `ReadOnly→AUTO` →
`SkipPermissions→AUTO` → `!RequireApproval→AUTO` → 否则 `REQUIRE_APPROVAL`。

资源创建风险分级 `CreateRiskFor(CreateSubject)`（只升不降）：dry run → `SAFE`；含值 Secret →
`MEDIUM`；cluster-scoped / 未知 Kind / 危险字段 / Skill 风险规则 / 非良性 Kind → `HIGH`；
其余已映射 + namespaced + 良性 Kind → `MEDIUM`。

## 8. HTTP API（服务端）

实现于 `internal/server`，复用 `ScanService.Run`，任务注册表为内存实现，单次扫描并发限制。
`k8s-ai server --addr :8080` 启动。历史差异（internal/history）按 Finding 指纹对比
新增/持续/恢复，写入 `latest.json` 的 history 字段。四期 P1.4-P1.6 在该层叠加了
统一中间件与认证/权限门禁：

- **统一响应信封**：`/api/v1/*` 响应统一携带 `request_id / timestamp / data / error / pagination`；
  非 `2xx` 走 `error.code + error.message`，分页解析兼容 `page/page_size` 与 `pageSize`。
- **request_id 链路**：请求入口生成并透传 `X-Request-ID`，供审计与全链路关联（P4.9 复用）。
- **CORS**：默认放行 `localhost:5173`（Vite 前端），可在服务端配置调整。
- **认证**：`healthz/readyz/version/login` 公开；其余业务 API 与 Chat Session API
  均要求有效登录态，认证中间件负责注入用户与角色上下文。
- **权限门禁（P1.6）**：`/api/v1` 路由由 `permission.DefaultAPIPolicy` 判定，业务代码只声明
  Permission；未登录 401、越权 403、未登记路由默认 403。

```text
GET  /healthz
GET  /readyz
GET  /version
POST /api/v1/auth/login
POST /api/v1/auth/logout
GET  /api/v1/auth/me
GET  /api/v1/auth/scope      // 当前用户有效 namespace / 集群级资源范围
POST /api/v1/scans          // body: ScanOptions；返回 202 {scanId}；已有任务时 409
GET  /api/v1/scans/{id}     // 状态: pending/running/succeeded/failed + result
GET  /api/v1/settings/{group}   // 配置中心（P1.10）：目录默认值与已存值合并；需 settings.read
PATCH /api/v1/settings/{group}  // body 为扁平 {key: value}，任一非法整批 400；需 settings.write
GET  /api/v1/users            // 用户列表（PasswordHash/口令永不回读）；需 user.manage（P3.11）
POST /api/v1/users            // 创建用户 {username,password(>=8),roleId,displayName?,status?}；需 user.manage
PATCH /api/v1/users/{id}      // 部分更新 {displayName?,password?,roleId?,status?}；需 user.manage；禁止停用/降级自己
DELETE /api/v1/users/{id}     // 删除用户；需 user.manage；内置 admin 不可删，禁止删除自己
GET  /api/v1/roles            // 角色列表（user/operator/admin 及权限视图）；需 user.manage
```

`roleId` 必须指向 `user / operator / admin` 之一；创建表单默认选择 `user`。

`GET /api/v1/auth/scope` 返回：

```text
namespaces      已授权 namespace 列表（始终为数组，无授权时为 []）
clusterScoped   是否可访问集群级资源
isAdmin         是否管理员
canWrite        是否可执行 namespace 内写操作
canDestroy      是否具备销毁类委派
```

> 配置分组为 `model|skill|approval|memory|retention|mcp|ops`（需求 v1.3 §15）。敏感键
> （`model.api_key`、`ops.kubeconfig`、`mcp.servers`）只在 PATCH 接收，GET 恒回
> `value: null` + `hasValue`；成功变更写 `CONFIG_CHANGE` 审计，载荷只含分组与键名。

> 四期产品内权限仅作用于 Web 页面、API 入口与审批策略，不进入 CLI 执行路径；
> namespace / resource 真实权限由平台 AuthzGrant 派生到 Kubernetes RBAC 并在 API Server
> 强校验（需求 v1.3 §17）：平台 AuthzGrant 为授权权威，Role/ClusterRoleBinding 为其派生
> 并由平台打标、定期对账；执行前做 SAR（仅验权限），操作安全由 Code Guard + 准入控制把关。
> 
> 用户/角色管理（P3.11）：系统固定 `user / operator / admin` 三角色；创建用户密码最短 8 位、
> 角色必须命中三角色白名单；内置 admin 受保护（不可删除/停用/降级）；禁止把当前登录用户自己
> 停用或降级；用户/角色接口全部需 `user.manage`，口令/哈希永不进入响应、审计与日志。
> namespace 授权（AuthzGrant）与 API Token（ApiToken）管理端点在本批之后的 P3.11 续批落地。

## 9. Chat Session HTTP + SSE API

平台 Chat API 正式归入 `/api/v1`。浏览器通过 SSE 消费会话事件流（先 `history` 快照，
后实时增量），运行控制通过 `send-message` / `approve` / `cancel` 编排。
所有 Session HTTP 与 SSE 请求必须携带有效 Bearer Token；未登录统一 `401`。

会话 owner 由认证主体在创建时写入，客户端不得提交或修改。`user / operator` 只能列出、
读取和操作自己的会话；`admin` 对全部会话拥有完整读写、运行控制与审批能力。
普通角色对非本人会话按 ID 操作统一返回 `404`。

```text
GET    /api/v1/sessions                    // own=本人；admin=全部
POST   /api/v1/sessions                    // 创建会话，owner 来自当前登录主体
POST   /api/v1/sessions/{id}/rename        // own 或 admin
POST   /api/v1/sessions/{id}/pin           // own 或 admin
DELETE /api/v1/sessions/{id}               // own 或 admin；同时取消运行并清理 broadcaster/pending
GET    /api/v1/sessions/{id}/stream        // own 或 admin；SSE history + 实时事件
POST   /api/v1/sessions/{id}/send-message  // own 或 admin；返回 202；运行中返回 409
POST   /api/v1/sessions/{id}/approve       // own 或 admin；响应不携带 choice
POST   /api/v1/sessions/{id}/cancel        // own 或 admin
```

错误语义：

| 场景 | HTTP | 说明 |
| --- | --- | --- |
| 未携带/无效 Token | `401` | 不进入 Session 处理 |
| 非本人会话或会话不存在 | `404` | 普通角色不泄露会话是否存在 |
| 同一会话已有运行 | `409` | 不允许并发 Chat |

`token` 为会话级 token 用量合计（`{promptTokens, completionTokens, totalTokens}`）；
无记录时字段缺省，`history` 快照中显式为 `null`，前端隐藏统计而不是显示 0。

### 9.1 事件契约（`service.ChatEvent`）

| 类型 | 编码 | 说明 |
|---|---|---|
| state | `state` | Agent 状态快照 |
| answer-delta | `answer-delta` | 最终回答流式增量 |
| reasoning-delta | `reasoning-delta` | 推理过程增量（Web 分析面板） |
| tool-call | `tool-call` | 工具调用发起（名称/参数/风险等级） |
| tool-response | `tool-response` | 工具结果（脱敏、截断） |
| progress | `progress` | 阶段进度摘要 |
| answer | `answer` | 最终回答全文（收尾） |
| error | `error` | 错误事件 |
| choice-request | `choice-request` | 请求用户审批选择（审批卡片） |

> 后端当前主要下发 `answer-delta` 流；`reasoning-delta` / `tool-call` / `answer` 事件经 SSE
> 转发给前端驱动三个阶段（推理展开 → 工具卡 → 最终回答折叠）。

### 9.2 四期统一事件流（P1.9）

统一事件信封 JSON（snake_case，每个事件必带）：

```text
session_id
turn_id
step_id
timestamp
event_type
data        // 各事件专属载荷，可为 null
```

支持的事件类型：

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

数据面：`internal/sse` 提供 Marshal 与 Frame。平台只维护这一套原生事件契约，
不定义第二套旧事件通道。所有出站 `params` / `result` / 回答负载在封装前统一经
`store.RedactJSONPayload` 脱敏，Secret / kubeconfig / API Key 明文永不进入 SSE。

## 10. 平台依赖矩阵

| 接口 | 定义方 | 实现方 | 属于 | 谁依赖 |
|---|---|---|---|---|
| Reader | scanner | kubernetes.Client | Infrastructure 端口 | scanner、service、k8stools |
| Collector | scanner | scanner.Collector | Application | service |
| Rule | rule | 各规则实现 | Domain | rule.Engine |
| Registry/Engine | rule | rule | Domain | service |
| LLMClient | llm | llm.Client | Infrastructure 端口 | diagnosis、service.Chat |
| Tool | tools | k8s/diagnosis/write/create | Domain | agent / service.Chat |
| Policy/Decision | approval | approval | Domain | agent |
| ScanService | service | service | Application | cli、server |
| ChatService | service | service | Application | webui |
| Store | session | memory/file store | Infrastructure | service.Chat |

## 11. 谁不能依赖谁

- Domain 不能 import Infrastructure；Application 不得 import Presentation。
- 实现方不得 import 定义方（kubernetes 不得 import scanner）。
- `model` 不 import 任何内部包；`session` 不得反向依赖 `llm`（`TokenUsage` 同构复制）。
- Web UI 与 CLI、Server 复用同一 `service.ChatService`，不依赖长驻 Agent。
