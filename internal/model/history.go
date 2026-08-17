package model

// FindingRef 是历史对比中使用的 Finding 摘要（供人读日报与二期 Agent 记忆，ADR-019）。
type FindingRef struct {
	ID       string      `json:"id"`
	Rule     string      `json:"rule"`
	Severity Severity    `json:"severity"`
	Title    string      `json:"title"`
	Resource ResourceRef `json:"resource"`
}

// HistoryDiff 是本次扫描与上一份 latest.json 的指纹对比结果（新增/持续/恢复）。
type HistoryDiff struct {
	PreviousScanAt string       `json:"previousScanAt,omitempty"`
	Added          []FindingRef `json:"added"`
	Continued      []FindingRef `json:"continued"`
	Recovered      []FindingRef `json:"recovered"`
}
