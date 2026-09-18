package model

// Evidence is a single piece of real evidence attached to a Finding.
// Values are redacted at the collection boundary and truncated to hard caps
// (FR-014, ADR-006).
type Evidence struct {
	ID        string       `json:"id"` // E1..En, stable after ranking
	Kind      EvidenceKind `json:"kind"`
	Source    string       `json:"source"`
	Key       string       `json:"key"`
	Value     string       `json:"value"`
	Truncated bool         `json:"truncated,omitempty"`
	Redacted  bool         `json:"redacted,omitempty"`
	Rank      int          `json:"-"` // internal only
}

// Finding is produced by the rule engine. ID is a stable fingerprint
// (ADR-003). Severity is rule-computed only (ADR-004).
type Finding struct {
	ID         string        `json:"id"`
	Rule       string        `json:"rule"`
	Severity   Severity      `json:"severity"`
	Title      string        `json:"title"`
	Summary    string        `json:"summary"`
	Resource   ResourceRef   `json:"resource"`
	Evidence   []Evidence    `json:"evidence"`
	Related    []ResourceRef `json:"related,omitempty"`
	Correlated bool          `json:"correlated,omitempty"`
	FirstSeen  string        `json:"firstSeen,omitempty"` // 1.2 trend
	LastSeen   string        `json:"lastSeen,omitempty"`  // 1.2 trend
	Doc        string        `json:"doc,omitempty"`       // P7.7 字段文档（--with-doc）
	Category   string        `json:"category,omitempty"`  // P7.8 问题域，报告聚合（引擎注入）
	Kind       string        `json:"kind,omitempty"`      // P7.8 关联资源 Kind，报告聚合（引擎注入）
	Suggestion bool          `json:"suggestion,omitempty"` // P7.8 是否建议（suggestion）类规则
}
