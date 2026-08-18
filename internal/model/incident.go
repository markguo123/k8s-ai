package model

// Incident 是将因果相关的 Findings 聚合后的诊断单元：
// Finding → Correlation → Incident → LLM 诊断（同一故障链只调用一次 LLM）。
// Root 为最底层可证实原因，Members 为派生症状（不单独分析/不重复扣分）。
type Incident struct {
	ID       string       `json:"id"` // 取 Root Finding 的稳定指纹
	Title    string       `json:"title"`
	Severity Severity     `json:"severity"`
	Root     FindingRef   `json:"root"`
	Members  []FindingRef `json:"members"`
}
