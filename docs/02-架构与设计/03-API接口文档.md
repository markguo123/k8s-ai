# k8s-ai API 接口契约设计

- 版本：v3.0.0 → v4.0.0（四期落地跟踪）
- 日期：2026-08-12（首版） / 2026-10-08（补齐二期、三期） / 2026-10-10（四期中间件/认证/权限落地）
- 状态：一期~三期已封版（2026-10-08，v3.0.0 基线）；四期落地中
- 范围：本文档统一描述 k8s-ai 的 Go 层接口契约与 HTTP API。一期只读巡检提供 `/api/v1/*`
  最小服务；二期加入 Web Chat 会话与 SSE 流（`/api/sessions/*`）；三期在会话通道上叠加
  结构化资源创建（`create_resource`，dry run → apply 两阶段）；四期新增统一响应信封、
  认证与后端权限门禁，并为 Session/Turn/Step 提供持久化底座。

## 1. 接口设计原则

1. **消费方定义接口，实现方返回结构体**（Go 惯用法）：如 scanner 定义自己需要的
   `Reader`，kubernetes 提供满足它的 `Client`。
2. 小接口：一个接口一个关注点；不为未来凭空抽象（AGENTS.md：avoid unnecessary
   abstractions）。
3. 所有接口方法首参为 `context.Context`，错误返回 `error`，可被 `errors.Is` 判等。
4. Domain 端口与 Infrastructure 实现的归属明确（见 §10）。
5. 二期起工具统一走 `internal/tools` 的 `Tool` 契约，由 Agent Loop 编排；HTTP 会话层
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

    // 定向读取：二期 Tool 直接复用，一期 scanner 不调用
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
- Collector 不知道规则、不知道 LLM。三期 `create_resource` 走独立执行通道，不复用 Collector。

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
    Tools       []ToolSpec // 二期 Tool Calling；一期恒为 nil
}
type ChatResponse struct {
    Content string
    Usage   TokenUsage
}
```

二期起 `ChatRequest.Tools` 描述器携带稳定安全元数据前缀（`[READ ONLY]` / `[RISK: ...]` /
`[APPROVAL REQUIRED: ...]`），仅帮助模型选工具，不作为安全边界（见 tools 包
`MetadataDescriber`）。

## 6. 工具契约（二期 `internal/tools`）

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
- `GuardPlanner`：把工具自带门禁要素交给 `approval.CodeGuard` 裁决（三期 R4.2）。

注册集合（`k8stools.RegisterAll` + 诊断 + 写工具 + 三期创建通道，`agent.disabled_tools`
可摘除）：

| 类别 | 工具名 |
|---|---|
| 只读资源 | get_pod / get_pods / get_node / get_nodes / get_node_usage / get_deployment / get_statefulset / get_daemonset / get_replicaset / get_service / get_endpoints / get_endpointslices / get_pvc / get_pv / get_storageclass / get_configmap / get_configmaps / get_namespace / get_resources / get_events / get_logs（含 previous） |
| 诊断 | scan_cluster / scan_pod / diagnose_pod / diagnose_node / diagnose_workload |
| 写操作 | kubectl（通用，审批门禁） |
| 三期创建 | create_resource（结构化 manifest，dry_run → apply） |

## 7. 审批契约（二期/三期 `internal/approval`）

```go
type Policy struct { RequireApproval, SkipPermissions bool; DenyOps []string; Mode Mode }
type Input struct { ReadOnly, Shell, Denied bool; Risk model.RiskLevel }
type Decision string // auto | require_approval | deny
```

判定链（`Policy.Evaluate`）：`Denied→DENY` → `Shell→REQUIRE_APPROVAL` → `ReadOnly→AUTO` →
`SkipPermissions→AUTO` → `!RequireApproval→AUTO` → 否则 `REQUIRE_APPROVAL`。

三期创建风险分级 `CreateRiskFor(CreateSubject)`（只升不降）：dry run → `SAFE`；含值 Secret →
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
- **认证**：`login` 公开、`logout/me` 要求已登录；当前为内存 Session Token（24h），
  认证中间件只注入用户上下文，不阻断未登录请求（占位阶段）。
- **权限门禁（P1.6）**：`/api/v1` 路由由 `permission.DefaultAPIPolicy` 判定，业务代码只声明
  Permission；未登录 401、越权 403、未登记路由默认 403。

```text
GET  /healthz
GET  /readyz
GET  /version
POST /api/v1/auth/login
POST /api/v1/auth/logout
GET  /api/v1/auth/me
POST /api/v1/scans          // body: ScanOptions；返回 202 {scanId}；已有任务时 409
GET  /api/v1/scans/{id}     // 状态: pending/running/succeeded/failed + result
```

> 四期产品内权限仅作用于 Web 页面、API 入口与审批策略，不进入 CLI 执行路径；
> namespace / resource 的真实权限仍由 kubeconfig 对应 RBAC 决定（需求 v1.2 §17）。

## 9. HTTP + SSE API（二期/三期 Web Chat，`internal/webui`）

浏览器通过 SSE 消费会话事件流（先 `history` 快照，后实时增量），写操作通过
`send-message` / `approve` / `cancel` 三组端点编排，实现运行中可停止。
四期起（P1.9）同一 SSE 通道双轨承载：三期旧事件线格式原样保留供迁移期继续消费，
统一新事件（`internal/sse`）按 `session_id` 扇出到会话缓冲，供 Web Shell 新前端驱动展示。

```text
GET  /                                  // 单文件 SPA（嵌入式 index.html）
GET  /api/sessions                      // 会话列表（id/name/state/messageCount/lastModified）
POST /api/sessions                      // 创建或载入会话
POST /api/sessions/{id}/rename
DELETE /api/sessions/{id}               // 同时取消运行并清理 broadcaster/pending
GET  /api/sessions/{id}/stream          // SSE: 先 history 快照，再实时事件
POST /api/sessions/{id}/send-message    // 发起一轮 Chat，返回 202；会话运行中返回 409
POST /api/sessions/{id}/approve         // 回传审批 choice；响应不携带 choice（Secret 值不留痕）
POST /api/sessions/{id}/cancel          // 取消当前运行（context cancel）
```

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

数据面：`internal/sse` 提供 Marshal / Frame（多行 data 兼容）/ MarshalLegacy 与
`MapFromLegacy` / `MapToLegacy` 双向映射层。三期旧事件（answer-delta /
reasoning-delta / tool-call / tool-response / choice-request 等）在映射期内继续可消费，
未完成映射前不得移除。所有出站 `params` / `result` / 回答负载在封装前统一经
`store.RedactJSONPayload` 脱敏，Secret / kubeconfig / API Key 明文永不进入 SSE。

## 10. 一期~三期依赖矩阵

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
- 二期起 Web UI 与 CLI、Server 复用同一 `service.ChatService`，不依赖长驻 Agent。
