# k8s-ai 一期 Go 接口契约设计

- 版本：v1.0
- 日期：2026-08-12
- 状态：待评审

## 1. 接口设计原则

1. **消费方定义接口，实现方返回结构体**（Go 惯用法）：如 scanner 定义自己需要的 `Reader`，kubernetes 提供满足它的 `Client`。
2. 小接口：一个接口一个关注点；不为未来凭空抽象（AGENTS.md：avoid unnecessary abstractions）。
3. 所有接口方法首参为 `context.Context`，错误返回 `error`，可被 `errors.Is` 判等。
4. Domain 端口与 Infrastructure 实现的归属明确（见 §5）。

## 2. Kubernetes Reader（Infrastructure 实现）

定义位置：`internal/scanner` 消费方接口 + `internal/kubernetes` 实现（`Client` 结构体）。

```go
// scanner 包内定义（消费方视角）
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
    // ... 其余 Get* 同类

    // 日志：返回已按上限截断的字节
    GetPodLogs(ctx context.Context, namespace, pod, container string, opts model.LogOptions) ([]byte, error)
}
```

约束：
- `Client` 内部只允许调用 typed clientset 的 List/Get 方法与 `CoreV1().Pods(ns).GetLogs(...)`。
- 禁止暴露任何写方法、Dynamic Client、exec/portforward。
- `ListOptions{ResourceVersion: "0"}` 走 apiserver 缓存（ADR-009）。

## 3. Collector（Application）

```go
// scanner 包
type Collector interface {
    Phase1(ctx context.Context, opts model.ScanOptions) (*model.ClusterSnapshot, error)
    Phase2(ctx context.Context, snapshot *model.ClusterSnapshot, targets []model.ResourceRef, opts model.ScanOptions) error
}
```

职责：
- Phase1：只做 list，产出快照 + EventsIndex + CollectionErrors。
- Phase2：只对 targets（异常 Pod）取 current/previous logs，结果挂到 snapshot.Pods[].Logs（含脱敏与上限）；后续阶段据此生成 Evidence。
- Collector **不知道规则、不知道 LLM**。

## 4. Rule 与 RuleRegistry（Domain）

```go
// rule 包
type Rule interface {
    Name() string
    NeedsLogs() bool // 命中后是否触发 Phase2 日志采集
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

`RuleContext`（rule 包）包含 snapshot、关联索引、严重级策略等只读输入。规则实现只允许读取，不允许修改快照。

## 5. LLMClient（Infrastructure 实现，Domain 依赖其接口）

```go
// llm 包定义接口与实现
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

type Message struct {
    Role    string    // system / user / assistant
    Content string
    ToolCalls []ToolCall // 二期
}
```

实现 `http.Client` 与 Chat Completions 协议；重试/429/超时策略见 CONCURRENCY.md。诊断编排只依赖 `LLMClient` 接口，不依赖实现细节。

## 6. CommandExecutor（二期预留，只声明不实现）

```go
// model 或独立包：一期仅存在接口，无任何实现
type CommandExecutor interface {
    Execute(ctx context.Context, cmd Command) (ExecutionResult, error)
}
```

一期代码库中不存在实现；架构测试断言 `Execute` 无实现且无调用点。

## 7. Reporter（Domain/Application）

```go
// report 包
type Renderer interface {
    Format() string // markdown / json / yaml
    Render(result *model.ScanResult, opts model.ReportOptions) ([]byte, error)
}

type Reporter interface {
    Write(ctx context.Context, result *model.ScanResult, opts model.ReportOptions) ([]string, error)
}
```

- Renderer 为纯函数（输入 ScanResult → 输出字节），不写文件，便于 golden file 测试。
- Reporter 负责目录创建、命名（latest / 时间戳）、二次脱敏、写入。

## 8. Scanner / ScanService（Application 编排）

```go
// service 包
type ScanService interface {
    Run(ctx context.Context, opts model.ScanOptions) (*model.ScanResult, error)
}
```

`ScanService.Run` 是 CLI、Server（1.2）、CronJob 的**唯一**入口（ADR-007）。内部编排：

```text
构造根 ctx（signal + scan.timeout）
→ 创建 Reader（kubernetes.Factory）
→ ServerVersion 连通性校验
→ Collector.Phase1 → ClusterSnapshot
→ correlation.Build → CorrelationIndex
→ rule.Engine.Evaluate → []Finding
→ Collector.Phase2（仅日志目标）→ 证据补全
→ diagnosis.Diagnose（预算+降级）→ []Diagnosis
→ report（健康评分 + 渲染 + 写入）→ ScanResult
```

## 9. 依赖矩阵

| 接口 | 定义方 | 实现方 | 属于 | 谁依赖 |
|---|---|---|---|---|
| Reader | scanner（消费方） | kubernetes.Client | Infrastructure 端口 | scanner、service |
| Collector | scanner | scanner.Collector | Application | service |
| Rule | rule | 各规则实现 | Domain | rule.Engine |
| Registry/Engine | rule | rule | Domain | service |
| LLMClient | llm | llm.Client | Infrastructure 端口 | diagnosis |
| CommandExecutor | model（预留） | 无 | 预留 | 无 |
| Renderer | report | report 各渲染器 | Domain | report.Reporter |
| Reporter | report | report | Domain/Application | service |
| ScanService | service | service | Application | cli、server |

## 10. 谁不能依赖谁

- Domain 不能 import Infrastructure（`model/rule/evidence/diagnosis` 不得 import `kubernetes/llm/config/server`）。
- Application 不得 import Presentation（`service` 不得 import `cli`）。
- 实现方不得 import 定义方（`kubernetes` 不得 import `scanner`——Reader 接口由 scanner 定义，但 kubernetes 只提供结构体，靠编译期断言满足接口）。
- 一期不引入 DI 框架：`service` 组合根手工装配依赖。

## 11. HTTP API（1.2 已实现）

```text
GET  /healthz
GET  /readyz
GET  /version
POST /api/v1/scans          // body: ScanOptions；返回 {scanId}
GET  /api/v1/scans/{id}     // 状态: pending/running/succeeded/failed + result
```

实现于 `internal/server`：复用 `ScanService.Run`，任务注册表为内存实现，单次扫描并发限制（已有任务运行时 POST 返回 409）；`k8s-ai server --addr :8080` 启动。历史差异数据（internal/history）按 Finding 指纹对比新增/持续/恢复，写入 latest.json 的 history 字段（二期 Agent 记忆契约，ADR-019）。

## 12. 二期预留约束

1. **latest.json 记忆契约**：`latest.json` 中的 `findings[].id`（指纹）与 `diagnoses[]`（二期语境中的 diagnosis_context）设计保持不变，专供二期 Agent 的 `--attach-report` 或后台自动调用 scan Tool 时读取，用于继承历史诊断记忆。
2. **scan_cluster Tool 自动携带历史差异**：二期 Agent 的 `scan_cluster` Tool 在返回结果时，必须自动携带 `latest.json` 中的历史指纹对比信息（新增/持续/恢复 + 上次诊断上下文），**不需要用户手动挂载文件**。
3. 一期只产出结构化契约（latest.json），不实现任何会话/记忆功能；该约束仅用于指导二期规划。
