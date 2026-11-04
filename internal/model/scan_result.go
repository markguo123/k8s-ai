package model

// ScanMeta 是每次扫描的概览信息，写入所有报告。
type ScanMeta struct {
	ToolVersion   string `json:"toolVersion"`
	Cluster       string `json:"cluster"`
	ServerVersion string `json:"serverVersion"`
	ScanStartedAt string `json:"scanStartedAt"`
	ScanEndedAt   string `json:"scanEndedAt"`
	Duration      string `json:"duration"`
	Namespace     string `json:"namespace,omitempty"`
	Pod           string `json:"pod,omitempty"` // 单 Pod 目标扫描时非空
}

// ClusterSummary 是报告"集群资源汇总"节的数据（M1 垂直切片即可见）。
type ClusterSummary struct {
	Namespaces      int `json:"namespaces"`
	Pods            int `json:"pods"`
	Nodes           int `json:"nodes"`
	Services        int `json:"services"`
	EndpointSlices  int `json:"endpointSlices"`
	Workloads       int `json:"workloads"`
	Storage         int `json:"storage"`
	Ingresses       int `json:"ingresses"`
	NetworkPolicies int `json:"networkPolicies"`
	Components      int `json:"components"`
	Events          int `json:"events"`
}

// LLMSummary 记录一次扫描中 LLM 阶段的行为。
//
// 四期 P9 把「一个笼统的 degraded」拆成可追责的分档（需求 §5）：模型没配、请求失败、
// 超时、返回不是 JSON、schema 不过、证据校验不过，是六种完全不同的运维问题，
// 报告里必须能一眼看出这次到底是哪一种。旧字段一律保留，历史报告照旧能读。
type LLMSummary struct {
	Enabled         bool `json:"enabled"`
	Calls           int  `json:"calls"`
	Failed          int  `json:"failed"`
	Degraded        int  `json:"degraded"`
	TokensEstimated int  `json:"tokensEstimated"`

	Succeeded    int              `json:"succeeded,omitempty"`
	Skipped      int              `json:"skipped,omitempty"`   // 预算不足等原因未送诊
	Validated    int              `json:"validated,omitempty"` // 通过诊断校验
	Rejected     int              `json:"rejected,omitempty"`  // 校验不通过，退回确定性结论
	Mode         string           `json:"mode,omitempty"`      // rule_only|llm_enhanced|llm_degraded
	StatusCounts map[string]int   `json:"statusCounts,omitempty"`
	Degradations []LLMDegradation `json:"degradations,omitempty"`
}

// CollectionError 记录一次非致命采集失败（FR-004）。
type CollectionError struct {
	Resource  ResourceRef `json:"resource"`
	Operation string      `json:"operation"`
	Message   string      `json:"message"`
	Time      string      `json:"time"`
}

// Penalty 是一条健康评分扣分及原因（FR-020）。
type Penalty struct {
	FindingID string      `json:"findingId"`
	Resource  ResourceRef `json:"resource"`
	Severity  Severity    `json:"severity"`
	Points    int         `json:"points"`
	Reason    string      `json:"reason"`
}

// HealthScore 由程序计算（ADR-013）。
type HealthScore struct {
	Score              int       `json:"score"`
	Max                int       `json:"max"`
	Penalties          []Penalty `json:"penalties"`
	CorrelatedExcluded int       `json:"correlatedExcluded"`

	// 四期 P9：采集完整性参与评分（需求 §7「不能无条件给 100 分」）。
	DataIntegrity string  `json:"dataIntegrity,omitempty"` // complete|partial|insufficient
	Coverage      float64 `json:"coverage,omitempty"`
	Capped        bool    `json:"capped,omitempty"`
	CapReason     string  `json:"capReason,omitempty"`
}

// ScanResult 是根模型：序列化为 JSON/YAML 报告，并被 Markdown 渲染器消费。
// latest.json 中的 findings（指纹）与 diagnoses 是二期 Agent 的历史记忆契约：
// 二期 scan_cluster Tool 自动携带历史差异数据，无需用户手动挂载文件。
//
// 四期 P9 起它同时是统一体检报告 InspectionReport（type 别名，见 inspection.go）：
// 新增区块全部 omitempty，历史 JSON 报告缺字段解出零值，schemaVersion=0 视为 v1。
type ScanResult struct {
	Meta             ScanMeta          `json:"meta"`
	Summary          ClusterSummary    `json:"summary"`
	Findings         []Finding         `json:"findings"`
	Diagnoses        []Diagnosis       `json:"diagnoses"`           // Incident 根因的诊断（每个 Incident 一条）
	Incidents        []Incident        `json:"incidents,omitempty"` // 故障链聚合（Finding→Correlation→Incident）
	History          *HistoryDiff      `json:"history,omitempty"`   // 1.2：与上一轮体检的指纹对比
	HealthScore      HealthScore       `json:"healthScore"`
	Components       []ComponentInfo   `json:"components,omitempty"`
	CollectionErrors []CollectionError `json:"collectionErrors,omitempty"`
	LLMSummary       LLMSummary        `json:"llmSummary"`
	ReportPaths      []string          `json:"reportPaths,omitempty"`

	// 四期 P9 统一报告区块（需求 §7）。
	SchemaVersion    int                `json:"schemaVersion,omitempty"`
	Conclusion       *Conclusion        `json:"conclusion,omitempty"`
	Collection       *CollectionQuality `json:"collection,omitempty"`
	DiagnosisSummary *DiagnosisSummary  `json:"diagnosisSummary,omitempty"`
	Stages           []StageRecord      `json:"stages,omitempty"`
	Statistics       []DomainHealth     `json:"statistics,omitempty"`
	Remediation      []RemediationItem  `json:"remediation,omitempty"`
}
