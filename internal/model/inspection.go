package model

// 统一体检报告模型（四期 P9，方案 18）。
//
// InspectionReport 不是一套新结构，而是 ScanResult 的版本化别名：CLI、Web、Markdown、
// JSON 归档读的都是同一份诊断结果（需求 §7「优先扩展现有 ScanResult，避免引入完全重复
// 的结构」）。新增区块一律 omitempty，历史 JSON 报告（v1）缺字段解出来就是零值，
// 不需要迁移。
type InspectionReport = ScanResult

// ReportSchemaVersion 是当前写出的报告 schema 版本。
// 读取侧按「缺 schemaVersion = v1」处理，禁止用当前版本号去解释旧报告。
const ReportSchemaVersion = 2

// 采集完整性档位（需求 §7.7 / §7 健康评分约束）。
//
// 判据只看「计划采集的采集到了没有」，不做「大概齐」的模糊折算：采集失败的类型如果
// 参与了根因判断，评分封顶就必须把这条信息带出来。
const (
	IntegrityComplete     = "complete"     // 全部计划资源采集成功
	IntegrityPartial      = "partial"      // 有非关键类型失败
	IntegrityInsufficient = "insufficient" // 关键类型（Pod/Node/Workload/Event）失败
)

// CriticalCollectionKinds 是关键采集类型：它们缺失时诊断结论不成立，评分与报告都要
// 明确标注「数据不完整」。Events 也算关键——调度/挂载类根因几乎全靠事件定位。
var CriticalCollectionKinds = []string{
	"pods", "nodes", "workloads", "namespaces", "events",
}

// CollectionQuality 是一次体检的采集质量账（需求 §7.7）。
//
// 三条硬口径：
//  1. planned 由「本次档位实际要采的资源类型」推导，不靠事后猜；
//  2. failed/skipped 只增不减，采集失败永远不能被解释成「资源正常」；
//  3. coverage 是成功率，用于健康评分封顶，不用于粉饰报告。
type CollectionQuality struct {
	Planned      int      `json:"planned"`
	Collected    int      `json:"collected"`
	Failed       int      `json:"failed"`
	Skipped      int      `json:"skipped"`
	Coverage     float64  `json:"coverage"`
	Integrity    string   `json:"integrity"`
	FailedKinds  []string `json:"failedKinds,omitempty"`
	SkippedKinds []string `json:"skippedKinds,omitempty"`
	CriticalGaps []string `json:"criticalGaps,omitempty"`
	MissingData  []string `json:"missingData,omitempty"` // 证据缺口（日志未采/未授权等）
	Errors       []string `json:"errors,omitempty"`      // 人读错误摘要（已去重、截断）
}

// Complete 报告采集是否完整（用于「不能无条件给 100 分」的判定）。
func (q *CollectionQuality) Complete() bool {
	return q == nil || q.Integrity == IntegrityComplete
}

// LLM 诊断状态（需求 §5「降级状态必须细分」）。
//
// 一个笼统的 degraded 会让「模型没配」和「模型返回了假证据」看起来一样，而后者是需要
// 追责模型网关配置的问题，前者只是部署形态。
const (
	LLMStatusNotConfigured   = "not_configured"
	LLMStatusSuccess         = "success"
	LLMStatusRequestFailed   = "request_failed"
	LLMStatusTimeout         = "timeout"
	LLMStatusInvalidResponse = "invalid_response"
	LLMStatusSchemaInvalid   = "schema_validation_failed"
	LLMStatusEvidenceInvalid = "evidence_validation_failed"
	LLMStatusPartialSuccess  = "partial_success"
	LLMStatusDegraded        = "degraded"
	LLMStatusBudgetSkipped   = "budget_skipped"
)

// LLMDegradation 记录单个 Incident 的 LLM 处置结果。
type LLMDegradation struct {
	IncidentID        string `json:"incidentId,omitempty"`
	FindingID         string `json:"findingId,omitempty"`
	Status            string `json:"status"`
	Reason            string `json:"reason,omitempty"`
	KeptDeterministic bool   `json:"keptDeterministic,omitempty"` // 是否保留确定性诊断结论
}

// DiagnosisSummary 是一次体检的诊断账（需求 §7.8）。
//
// 报告必须能回答「这个结论是谁给的」：规则数、确定性诊断数、LLM 成败与降级原因，
// 少了这一层，报告就无法区分「程序判定」与「模型猜测」。
type DiagnosisSummary struct {
	RuleFindings  int              `json:"ruleFindings"`
	Deterministic int              `json:"deterministic"` // 确定性诊断给出结论的 Incident 数
	Confirmed     int              `json:"confirmed"`     // 其中 confirmed
	Probable      int              `json:"probable"`      // 其中 probable
	Unknown       int              `json:"unknown"`       // 其中 unknown（明确无法定位）
	LLMCalls      int              `json:"llmCalls"`
	LLMSucceeded  int              `json:"llmSucceeded"`
	LLMFailed     int              `json:"llmFailed"`
	LLMDegraded   int              `json:"llmDegraded"`
	LLMRejected   int              `json:"llmRejected"` // 诊断校验不通过、退回确定性结论
	Hybrid        int              `json:"hybrid"`      // 确定性 + LLM 合并
	Mode          string           `json:"mode"`        // rule_only | llm_enhanced | llm_degraded
	Degradations  []LLMDegradation `json:"degradations,omitempty"`
}

// StageRecord 是体检执行阶段账（需求 §9：真实阶段状态 + 耗时）。
//
// 禁止伪造百分比：这里只有「跑了没、跑了多久、结果如何、跳过的原因」。
type StageRecord struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Status   string `json:"status"` // running | done | failed | skipped
	Detail   string `json:"detail,omitempty"`
	Elapsed  string `json:"elapsed,omitempty"`
	Millis   int64  `json:"millis"`
	Progress string `json:"progress,omitempty"` // 当前子步骤描述（不含百分比）
}

// Conclusion 是体检总体结论（需求 §7.2）。
//
// 全部字段由程序从既有事实推导，不调用 LLM：结论区一旦变成模型自由发挥的地方，
// 报告里就没有一段可以无条件相信的文字了。
type Conclusion struct {
	Level       string   `json:"level"` // healthy | attention | critical | unknown
	Headline    string   `json:"headline"`
	Summary     string   `json:"summary"`
	TopRisks    []string `json:"topRisks,omitempty"`
	Priorities  []string `json:"priorities,omitempty"`
	DataCaveats []string `json:"dataCaveats,omitempty"` // 影响结论可信度的数据缺口
}

// DomainHealth 是一类资源的体检小结（需求 §7.6 ResourceStatistics）。
type DomainHealth struct {
	Domain    string `json:"domain"`
	Total     int    `json:"total"`
	Unhealthy int    `json:"unhealthy"`
	Critical  int    `json:"critical"`
	Detail    string `json:"detail,omitempty"`
}

// RemediationItem 是一条可复制的处置建议（排查 / 修复 / 验证三类）。
type RemediationItem struct {
	Kind     string `json:"kind"`             // investigation | remediation | verification
	Format   string `json:"format,omitempty"` // text | command
	Text     string `json:"text"`
	Risk     string `json:"risk,omitempty"`
	Target   string `json:"target,omitempty"`
	Required bool   `json:"required,omitempty"`
}
