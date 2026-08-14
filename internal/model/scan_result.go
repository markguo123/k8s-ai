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
type LLMSummary struct {
	Enabled         bool `json:"enabled"`
	Calls           int  `json:"calls"`
	Failed          int  `json:"failed"`
	Degraded        int  `json:"degraded"`
	TokensEstimated int  `json:"tokensEstimated"`
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
}

// ScanResult 是根模型：序列化为 JSON/YAML 报告，并被 Markdown 渲染器消费。
// latest.json 中的 findings（指纹）与 diagnoses 是二期 Agent 的历史记忆契约：
// 二期 scan_cluster Tool 自动携带历史差异数据，无需用户手动挂载文件。
type ScanResult struct {
	Meta             ScanMeta          `json:"meta"`
	Summary          ClusterSummary    `json:"summary"`
	Findings         []Finding         `json:"findings"`
	Diagnoses        []Diagnosis       `json:"diagnoses"`
	HealthScore      HealthScore       `json:"healthScore"`
	Components       []ComponentInfo   `json:"components,omitempty"`
	CollectionErrors []CollectionError `json:"collectionErrors,omitempty"`
	LLMSummary       LLMSummary        `json:"llmSummary"`
	ReportPaths      []string          `json:"reportPaths,omitempty"`
}
