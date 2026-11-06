# AegisOps API 接口契约设计

- 版本：v4.0.0（企业平台）
- 日期：2026-10-23（初版）/ 2026-11-03（发起体检契约收窄与报告三面登记，见 17-方案） / 2026-11-04（报告清单清理两面登记，见方案 15 v1.12；补体检发起面 v1.1：format 单选、阶段账、停止体检、报告总览、按需导出 Markdown）/ 2026-11-05（补结构化报告的 canonical 证据与诊断投影契约）
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
- 暴露给工具的一面无写方法；默认工具仅返回 Secret 元数据。`read_on_demand`
  模式下 `get_secret_value` 的明文只进入当前轮模型，API/UI 结果仍为脱敏指纹，
  规则见 `08-安全设计.md` 与 ADR-048。
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
`aopsctl server --addr :8080` 启动。历史差异（internal/history）按 Finding 指纹对比
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
POST /api/v1/scans          // 开始体检：body 为白名单 DTO（scope/namespace/pod/preset/clusterId/format）；format=json|markdown 单选，一次只归档选中的那一种；202 {scanId}；已有任务 409；需 scan.execute
GET  /api/v1/scans/{id}     // 状态: pending/running/succeeded/failed/cancelled + result + stages（十段阶段账：状态/耗时/子步骤）；仅任务发起人本人或 admin
POST /api/v1/scans/{id}/cancel // 停止体检（P9.11）：发起人本人或 admin；状态立即写 cancelled、running 阶段冻结为 failed「已停止」；已结束 409；停他人任务与不存在同形 404；落 scan.cancel 审计
GET  /api/v1/settings/{group}   // 配置中心（P1.10）：目录默认值与已存值合并；需 settings.read
PATCH /api/v1/settings/{group}  // body 为扁平 {key: value}，任一非法整批 400；需 settings.write
GET  /api/v1/users            // 用户列表（PasswordHash/口令永不回读）；需 user.manage（P3.11）
POST /api/v1/users            // 创建用户 {username,password(>=8),roleId,displayName?,status?}；需 user.manage
PATCH /api/v1/users/{id}      // 部分更新 {displayName?,password?,roleId?,status?}；需 user.manage；禁止停用/降级自己
DELETE /api/v1/users/{id}     // 删除用户；需 user.manage；内置 admin 不可删，禁止删除自己
GET  /api/v1/roles            // 角色列表（user/operator/admin 及权限视图）；需 user.manage
GET  /api/v1/reports            // 报告中心分页列表（cluster/kind/scanId/scopeCategory/时间过滤，不返回对象 key、不加载正文；每行附 createdByName 即发起人）；需登录，可见性由后端三层归属判定下推 SQL
GET  /api/v1/reports/{id}/content // 报告正文流式下载或 presign 直连；越权与 id 不存在同形 404；available=false 返回 410；后端不匹配 409
GET  /api/v1/reports/{id}/report  // 结构化体检报告详情（P9）：普通 JSON 响应，供 Web 报告详情页渲染；授权与 /content 同一套 scope 判定；415=无结构化形态、413=正文超 8MiB
GET  /api/v1/reports/summary      // 报告列表顶部的类别总览（P9.13）：{total,cluster,business,other,markdown,json,available}；过滤条件与列表同源，统计全部可见行而非当页；需登录
GET  /api/v1/reports/{id}/export   // 按需导出一份 Markdown（P9.13）：用同一份归档数据当场渲染，不写回对象存储；附件下载 filename*=UTF-8''；授权与 /content 同一套判定
DELETE /api/v1/reports/{id}     // 清理单条报告业务引用（P8.14）：同事务删 reports 行 + Release，正文由 blobs gc 在宽限期后回收，请求路径不删对象；越权与 id 不存在同形 404；每条删除落 report.delete 审计
POST   /api/v1/reports/cleanup  // 批量清理 {"ids":[...]}（单次上限 200）：逐 id 独立事务与归属判定，返回 {"deleted":[...],"skipped":[{"id":..,"reason":"not_found"|"internal"}]}；批量不放宽任何判定；需登录
GET  /api/v1/storage/health     // 对象存储健康唯一可观测入口；需 settings.read；driver=off 返回 200 带 status=disabled
```

`roleId` 必须指向 `user / operator / admin` 之一；创建表单默认选择 `user`。

**发起体检与报告读取（2026-11-03 契约冻结；报告三面 P8.5/P8.11 已实现，体检发起面 P8.13 已实现，2026-11-05 按代码回写）**：
`POST /api/v1/scans` 的请求体是**白名单 DTO**，只接受 `scope`（`cluster`/`namespace`/`pod`）、`namespace`、`pod`、
`preset`（`quick`/`standard`/`deep`）、`clusterId`、`format`（`json`/`markdown`，空按 `json`）六个字段，未知字段与连接类字段（`kubeconfig`、`context`、`inCluster`、
`reportDirectory`、`concurrency`、`qps`、`timeout`、`reportMode`）一律 400 拒绝；权限位为新增的 `scan.execute`
（admin 持 `*` 天然通过、operator 角色种子含、user 默认不含）；`scope=cluster` 仅 admin，`namespace` 与 `pod`
在非 admin 时目标 namespace 必须落在调用者的有效 AuthzGrant 内，越界 403 且点名是哪个 ns，绝不静默裁剪成授权子集；
`clusterId` 由服务端解析后覆盖归属，不接受请求体值。`GET /api/v1/scans/{id}` 只对任务发起人本人与 admin 开放，
任何持 `cluster.read` 的用户凭 job id 读他人任务结果属越权（结果含他人命名空间的问题清单）。
报告三面的权限与错误映射在 `15-方案` §13.1 与 §18 定稿（三层归属判定、越权与 id 不存在同形 404、
对象后端与配置不符返回 409 + 迁移指引）；体检三维度、三档预设与授权闸门在 `17-方案` §3 至 §8。
**已落地**：`POST /scans` 挂新增权限位 `scan.execute`、`GET /scans/{id}` 改 `AccessAuthenticated` 并在 handler 内判归属，
非发起人且非 admin 读他人任务返回与「不存在」**同形的 404**（job id 同样是随机串，403 等于确认它存在）；请求体已收窄为上文的白名单 DTO，`clusterId` 非空一律 400。

**报告清单清理（2026-11-04 契约冻结；P8.14 已实现，2026-11-05 按代码回写）**：`DELETE /api/v1/reports/{id}` 与 `POST /api/v1/reports/cleanup` 均登记为
`AccessAuthenticated`，**不新增权限位**，归属在 handler 内判定（owner 删自己的、`system` 体检报告按 cluster 授权、admin 跨 owner 可删）。
删除动作只删 `reports` 业务引用并在同一事务内 `Release`，**请求路径不删后端对象**，正文由 `aopsctl blobs gc` 在宽限期后回收；
`available=false`（已返回 410）的报告**仍然可删**，删除侧不复用下载侧的可用性闸门。每一条删除落 `report.delete` 审计，
越权与 id 不存在同形 404。批量端点不构成第二条授权通道：它逐 id 复用同一套归属判定与同一条事务契约。
口径见 `15-方案` **v1.13** §0 与 §13.1/§15/§18。

**结构化报告详情（2026-11-04 契约冻结；P9 已实现）**：`GET /api/v1/reports/{id}/report` 与
`/content` 是两种用途，不能互相顶替——`/content` 是「下载原文」，强制附件下载、禁 MIME 嗅探，
浏览器里永远拿不到内容；`/report` 是「在页面里渲染报告详情页」，走普通 JSON 信封。
授权完全复用 `reportScopeFor`，两个端点的可见集合天然一致，详情页不构成第二条越权读取通道；
跨 owner 打开详情页同样计一次 `report.download` 审计。

响应体 `{report, schemaVersion, legacy, inspection}`：`report` 是用户点开的这一行（复用
`service.ReportView`，不含 blob key / 对象存储布局），`inspection` 是统一 `InspectionReport`
（`model.ScanResult`）本体，`schemaVersion` 对 v1 老报告归一化为 1，`legacy=true` 表示缺
结论/采集质量/诊断账等区块，前端按此降级渲染。

`inspection` 的关键字段契约：

- `findings[].role`：`anchor|symptom|impact|advisory|independent`，由后端在 Incident 聚合后注入；
  前端只负责展示，不得按标题重新推断角色。
- `diagnoses[].evidenceChain[]`、`incidents[].evidenceRefs[]`、`causalEdges[].evidenceRefs[]`：
  新数据统一使用 `FindingID#EID`；历史报告中的裸 `EID` 仅供读取兼容。
- `incidents[].rootCause*` / `confidence*` / `evidenceGaps[]` 是最终 Diagnosis 的报告投影；
  `diagnoses[]` 才是根因唯一事实源，前端不得在两者不一致时自行挑一套。
- `causalEdges[].method`：`correlation|deterministic|llm`；关联层边固定 `probable`。

实现要点：一次扫描归档成 `latest-md` 与 `latest-json` 两行，页面点开的是 Markdown 行，
因此服务端顺着 `reports.scan_id` 找同一轮的 JSON 产物，不要求前端猜 kind；找不到时返回
**415 `unsupported_media_type`**（诚实提示改走原文下载），而不是拿 Markdown 硬凑一个结构化对象；
同一轮 JSON 行本身读不出来时原样上抛，映射成 410/503，让运维去找存储而不是找渲染。
正文读取上限 8 MiB，超出返回 **413 `payload_too_large`**。口径见 `18-方案` §9.2、§11.2。

**体检发起面 v1.1（2026-11-04 契约冻结；P9.11/P9.12/P9.13 已实现）**：

- `format` 只决定**归档哪一种产物**：`json` 供报告详情页按区块渲染结论与证据，`markdown` 是一份可读正文。
  一次体检只归档选中的那一种，`latest-md` 与 `latest-json` 不再成对出现；取值非法 400，留空按 `json`。
- `GET /api/v1/scans/{id}` 的 13 项 `stages` 与报告里的 `StageRecord` 同源同结构（`name`/`title`/`status`/`elapsed`/
  `progress`/`detail`，`status` ∈ `running`|`done`|`failed`|`skipped`），因此页面上的实时阶段进度与报告末尾的
  阶段账不可能长得不一样；无阶段时字段省略，老客户端不受影响。没有百分比字段，也就不会有编造的预估进度。
- `POST /api/v1/scans/{id}/cancel` 只停得动 `pending`/`running` 的任务，终态任务回 409。取消经 context 向上传播，
  不强杀 goroutine；`s.running` 要等任务真正退出才放开，所以「停止」不会顺手放行第二次并发体检
  （两个采集器同时打同一个 API Server 比不允许多跑更糟）。挂在 `running` 的阶段冻结为 `failed` + 「已停止」。
- `GET /api/v1/reports/summary` 与列表复用同一套三层归属判定与同一组过滤参数（`clusterId`/`kind`/`scanId`/
  `scopeCategory`），**分页参数不参与统计**：总览回答的是「我看得见的报告里各类有多少」，翻页时它必须不变。
- `GET /api/v1/reports/{id}/export` 是 `/report` 的按需产物：读同一份 JSON 归档、走同一个授权判定、当场渲染
  一份 Markdown 附件，**不写回对象存储**，归档里始终只有一个事实源。归档本来就是 Markdown 的行原样返回（幂等，不做二次渲染），页面上因此只在 JSON 行给这个入口。
- 报告类别口径：`scopeCategory=cluster`（集群体检）/ `business`（业务体检，namespace 与 pod）/
  `other`（范围未记录的早期存量与命令行产物）。`系统体检` 是**预留类别**，本期不实现、不接受该取值。

口径见 `18-方案` §10.1、§10.2、§11.1、§15。

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

`send-message` 请求体只接受 `query` 与可选的 `skills`。Web 端不接受
`skipPermissions`：审批跳过只能由部署侧的 CLI/自动化能力显式配置，不能由浏览器请求决定。

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
seq          // 会话内单调递增；用于重连回放去重
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

`observation.created` 的命令类结果额外携带：

```text
command
stdout
stderr
exitCode
errorCode     // 如 RBAC_DENIED / SCOPE_DENIED / NOT_FOUND / TIMEOUT
displayType   // terminal | table | kv | events | yaml | logs | text
data          // 结构化数据；terminal 视图是 kubectl 的权威展示面
```

运行中会话重连时，服务端先发送历史快照，再补发当前回合缓存事件；前端按 `seq`
去重。历史快照仍是已持久化消息的权威来源，缓存事件只补齐尚未落库的运行过程。

`approval.required` 的结构化载荷新增：

```text
title / tool / operation / risk
target{cluster, namespace, group, version, kind, name}
changes[{path, operation, before, after, description}]
checks[{name, status, message}]
verification[]
command
options[]
```

`prompt` 继续保留给 CLI 和旧客户端回退；Web 新版审批卡以目标、变更、检查、
验证和命令字段为主。HIGH/CRITICAL 的 `options` 不包含
`yes_and_dont_ask_again`。

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
