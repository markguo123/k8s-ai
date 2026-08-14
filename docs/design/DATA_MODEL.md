# k8s-ai 一期数据模型设计

- 版本：v1.0
- 日期：2026-08-12
- 状态：待评审

## 1. 总则

1. 所有模型定义在 `internal/model`，**不 import 任何内部包**，只依赖标准库。
2. 模型带 JSON tag，`ScanResult` 的 JSON 序列化即 1.2 趋势对比的数据契约，**字段一经发布不得随意改名**。
3. 模型分两类：**报告模型**（进入 ScanResult/JSON 报告）与**内部模型**（仅进程内使用，如 EventIndex、Rank）。
4. 生命周期统一由 `service.ScanService` 编排：创建 → 填充 → 消费 → 序列化；禁止绕过 service 直接构造完整结果。

## 2. 枚举与值对象

```go
type Severity string
const (
    SeverityInfo     Severity = "INFO"
    SeverityLow      Severity = "LOW"
    SeverityMedium   Severity = "MEDIUM"
    SeverityHigh     Severity = "HIGH"
    SeverityCritical Severity = "CRITICAL"
)

type RiskLevel string
const (
    RiskSafe     RiskLevel = "SAFE"
    RiskLow      RiskLevel = "LOW"
    RiskMedium   RiskLevel = "MEDIUM"
    RiskHigh     RiskLevel = "HIGH"
    RiskCritical RiskLevel = "CRITICAL"
)

type EvidenceKind string
const (
    EvObjectField EvidenceKind = "object_field"
    EvEvent       EvidenceKind = "event"
    EvLog         EvidenceKind = "log"
    EvAnnotation  EvidenceKind = "annotation"
    EvDerived     EvidenceKind = "derived"
)

type CommandCategory string
const (
    CmdInvestigation CommandCategory = "investigation"
    CmdRemediation   CommandCategory = "remediation"
    CmdVerification  CommandCategory = "verification"
)

type ResourceRef struct {
    Kind      string `json:"kind"`
    Group     string `json:"group,omitempty"`
    Namespace string `json:"namespace,omitempty"`
    Name      string `json:"name"`
    UID       string `json:"uid,omitempty"` // 仅内部关联使用；不参与指纹
}

type Budget struct {
    MaxTokensPerFinding int
    MaxTotalTokens      int
    MaxFindings         int
}
```

## 3. ScanResult（报告根模型）

```go
type ScanResult struct {
    Meta             ScanMeta          `json:"meta"`
    Findings         []Finding         `json:"findings"`
    Diagnoses        []Diagnosis       `json:"diagnoses"`
    HealthScore      HealthScore       `json:"healthScore"`
    CollectionErrors []CollectionError `json:"collectionErrors,omitempty"`
    LLMSummary       LLMSummary        `json:"llmSummary"`
    ReportPaths      []string          `json:"reportPaths,omitempty"`
}

type ScanMeta struct {
    ToolVersion   string `json:"toolVersion"`
    Cluster       string `json:"cluster"`        // kubeconfig context 名
    ServerVersion string `json:"serverVersion"`
    ScanStartedAt string `json:"scanStartedAt"`
    ScanEndedAt   string `json:"scanEndedAt"`
    Duration      string `json:"duration"`
    Namespace     string `json:"namespace,omitempty"` // 空=全集群
}

type LLMSummary struct {
    Enabled        bool   `json:"enabled"`
    Calls          int    `json:"calls"`
    Failed         int    `json:"failed"`
    Degraded       int    `json:"degraded"` // 因超时/校验失败降级的 Finding 数
    TokensEstimated int   `json:"tokensEstimated"`
}
```

生命周期与所有权：

| 字段 | 创建者 | 填充者 | 消费者 |
|---|---|---|---|
| Meta | service | service（扫描起止时） | report、cli |
| Findings | rule | rule / evidence / scanner(Phase2 证据) | diagnosis、report |
| Diagnoses | diagnosis | diagnosis | report、cli |
| HealthScore | report | report | report、cli |
| CollectionErrors | scanner | scanner / diagnosis | report |
| LLMSummary | service | diagnosis 汇总 | report |

## 4. ClusterSnapshot（内部采集模型）

```go
type ClusterSnapshot struct {
    ServerVersion    string               `json:"serverVersion"`
    CollectedAt      string               `json:"collectedAt"`
    Namespaces       []NamespaceInfo      `json:"namespaces"`
    Pods             []PodInfo            `json:"pods"`
    Nodes            []NodeInfo           `json:"nodes"`
    Services         []ServiceInfo        `json:"services"`
    EndpointSlices   []EndpointSliceInfo  `json:"endpointSlices"`
    Workloads        []WorkloadInfo       `json:"workloads"`
    Storage          []StorageInfo        `json:"storage"`
    Ingresses        []IngressInfo        `json:"ingresses"`
    NetworkPolicies  []NetworkPolicyInfo  `json:"networkPolicies"`
    Components       []ComponentInfo      `json:"components"`
    EventsIndex      map[types.UID][]EventInfo `json:"-"` // 内部索引，不序列化
    CollectionErrors []CollectionError    `json:"collectionErrors,omitempty"`
}

type PodInfo struct {
    Ref         ResourceRef
    Phase       string
    NodeName    string
    StartTime   string
    Conditions  []ConditionInfo
    Containers  []ContainerInfo
    OwnerRefs   []ResourceRef
    Labels      map[string]string
    Annotations map[string]string // 已脱敏
    QoSClass    string
    Logs        map[string]CollectedLog // Phase2 后填充：containerName → 日志
}

type ContainerInfo struct {
    Name          string
    Image         string
    ImageID       string
    State         string
    Reason        string
    Message       string
    ExitCode      int32
    RestartCount  int32
    LastState     string
    LastReason    string
    LastExitCode  int32
    Ready         bool
    Requests      map[string]string // cpu/memory
    Limits        map[string]string
}

type CollectedLog struct {
    Current   []byte `json:"-"` // 已截断、已脱敏
    Previous  []byte `json:"-"`
    Truncated bool
    Error     string `json:"-"`
}

type EventInfo struct {
    Reason         string
    Message        string
    Type           string
    Count          int32
    FirstTimestamp string
    LastTimestamp  string
    InvolvedObject ResourceRef
}
```

要点：
- **快照不进 LLM、不进报告**：报告只展示摘要（资源汇总节），原始快照仅进程内使用。
- `Annotations`、`CollectedLog` 在进入快照前已脱敏（ADR-006）。
- 大集群内存控制：Phase1 只保留上述精简字段，不保存完整 v1 对象（ADR-016）。

## 5. Finding 与 Evidence

```go
type Finding struct {
    ID         string        `json:"id"`         // 指纹，稳定
    Rule       string        `json:"rule"`
    Severity   Severity      `json:"severity"`   // 仅规则计算
    Title      string        `json:"title"`
    Summary    string        `json:"summary"`
    Resource   ResourceRef   `json:"resource"`
    Evidence   []Evidence    `json:"evidence"`
    Related    []ResourceRef `json:"related,omitempty"`
    Correlated bool          `json:"correlated,omitempty"` // 下游衍生，评分排除
    FirstSeen  string        `json:"firstSeen,omitempty"`  // 1.2 趋势
    LastSeen   string        `json:"lastSeen,omitempty"`   // 1.2 趋势
}

type Evidence struct {
    ID        string       `json:"id"`              // E1..En，按排序后稳定生成
    Kind      EvidenceKind `json:"kind"`
    Source    string       `json:"source"`          // 对象+字段路径，如 "Pod/payment-api-xxx/status.containerStatuses[0]"
    Key       string       `json:"key"`             // 如 restartCount
    Value     string       `json:"value"`           // 已脱敏、已截断
    Truncated bool         `json:"truncated,omitempty"`
    Redacted  bool         `json:"redacted,omitempty"`
    Rank      int          `json:"-"`               // 不进 JSON 报告
}
```

### 5.1 指纹（Finding.ID）

指纹输入 = `kind + group + namespace + 归一化名称 + rule + 证据签名`，其中：

- **归一化名称**：Pod 有 owner（Deployment/StatefulSet/Job 等）时使用 owner 的 kind+name；无 owner 的裸 Pod 使用 Pod 名。原因：Deployment Pod 名随机，重启后 Pod 名变化，若直接按 Pod 名做指纹，"持续问题"在 1.2 会误判为"恢复+新增"（ADR-003）。
- **证据签名**：取排序后前 3 条非日志证据的 `key:value`（如 `lastReason:OOMKilled`）。日志内容不参与指纹（不稳定）。
- 输出：`SHA-256` 十六进制，截断为 16 字节（32 字符）可读。

指纹相关字段标注：

| 字段 | 用于指纹 | 用于 1.2 趋势 | 禁止进入 LLM |
|---|---|---|---|
| Resource.Kind/Namespace/Name | 是 | 是 | 否（可进入，仅名称） |
| Resource.UID | 否 | 否 | 是（避免泄漏内部信息，且无诊断价值） |
| Rule | 是 | 是 | 是（可进入，规则名无敏感信息；为省 token 默认不进） |
| Evidence 签名（非日志） | 是 | 是 | 是（摘要形式进入） |
| Correlated | 否 | 是 | 否 |
| FirstSeen/LastSeen | 否 | 是 | 是（时间戳无诊断价值） |

### 5.2 Evidence 生命周期

```text
创建（scanner/rule 采集原始值）
  → 脱敏（security.Redactor，采集边界，ADR-006）
  → 截断（行数/字节上限，Truncated=true）
  → 排序（Ranker：object_field > event > log > annotation）
  → ID 分配（按排序结果 E1..En，稳定）
  → 指纹签名（rule 引用前 3 条）
  → 消费（diagnosis 取 top-K，LLM 可引用 E-ID）
  → 校验（LLM 引用必须真实存在）
  → 渲染（report 二次脱敏）
```

## 6. Diagnosis 与 Command

```go
type Diagnosis struct {
    FindingID      string           `json:"findingId"`
    Summary        string           `json:"summary"`
    RootCause      string           `json:"rootCause"`
    Confidence     float64          `json:"confidence"` // 0.0–1.0
    EvidenceChain  []string         `json:"evidenceChain"` // 引用的 E-ID，必须真实
    Impact         string           `json:"impact"`
    PossibleCauses []string         `json:"possibleCauses,omitempty"`
    Investigation  []Command        `json:"investigation,omitempty"`
    Remediation    []Command        `json:"remediation,omitempty"`
    Verification   []Command        `json:"verification,omitempty"`
    LLMUsed        bool             `json:"llmUsed"`
    Error          string           `json:"error,omitempty"` // 降级原因（LLM分析不可用/校验失败）
}

type Command struct {
    Category CommandCategory `json:"category"`
    Text     string          `json:"text"` // 完整可复制命令
    Risk     RiskLevel       `json:"risk"`
}
```

生命周期：diagnosis 创建；report 渲染；cli 展示。Command 仅文本，**任何代码路径都不得执行**（ADR-014）。

> **二期记忆契约**：latest.json 中的 diagnoses 数据（二期语境中的 diagnosis_context）设计保持不变，专供二期 Agent 的 `--attach-report` 或后台自动调用 scan Tool 时读取，用于继承历史诊断记忆；用户无需手动挂载文件。

## 7. CollectionError 与 HealthScore

```go
type CollectionError struct {
    Resource  ResourceRef `json:"resource"`
    Operation string      `json:"operation"` // list_pods / get_logs / list_events / ...
    Message   string      `json:"message"`   // 已脱敏
    Time      string      `json:"time"`
}

type Penalty struct {
    FindingID string       `json:"findingId"`
    Resource  ResourceRef  `json:"resource"`
    Severity  Severity     `json:"severity"`
    Points    int          `json:"points"`
    Reason    string       `json:"reason"`
}

type HealthScore struct {
    Score              int       `json:"score"`
    Max                int       `json:"max"` // 100
    Penalties          []Penalty `json:"penalties"`
    CorrelatedExcluded int       `json:"correlatedExcluded"`
}
```

计算规则（ADR-013）：`Score = max(0, 100 − Σ罚分)`，罚分表 CRITICAL 30 / HIGH 15 / MEDIUM 5 / LOW 1；`Correlated=true` 的 Finding 不计罚分，但计数进 `CorrelatedExcluded`；每条 Penalty 必须给出原因（如"Node node-03 NotReady（CRITICAL）"）。

## 8. 禁止进入 LLM 的字段清单（不变量）

1. Secret 数据（程序根本不采集）。
2. ConfigMap 数据值（默认不采集；仅相关时按敏感键脱敏后使用）。
3. kubeconfig 内容、api_key、任何配置中的凭据。
4. 未脱敏的 annotations / labels / 环境变量字面值 / 日志 / Events 消息。
5. 完整 Pod/资源 YAML（只发送摘要字段）。
6. ClusterSnapshot 整体（只发送按预算裁剪的 DiagnosisContext）。
7. CollectionErrors 消息（可能含敏感错误串；不进入 LLM 上下文）。
8. UID、时间戳、ReportPaths 等无诊断价值的内部字段。

该清单由诊断上下文构造器（diagnosis）强制执行，并由 LLM 请求泄密测试守护（TESTING.md）。
