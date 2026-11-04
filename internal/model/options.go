// Package model defines the pure domain models for k8s-ai.
// It must not import any internal package (ADR-015).
package model

import (
	"bytes"
	"fmt"
	"strings"
	"time"
)

// HardLogLineCap 单行硬上限：超过该值的超长行才截断（防 OOM）；
// 不超过上限的行完整保留，保证错误日志信息完整（P0 优化）。
const HardLogLineCap = 1 << 20 // 1 MiB

// TruncateLogs 按行保留日志：优先按行数（TailLines 由服务端处理），
// 单行不按 maxLineBytes 截断；总字节上限按行边界控制，
// 允许首条超长行（≤ HardLogLineCap）独占配额。
// maxBytes 为总字节上限（<=0 时使用 64KB 默认值）；
// maxLineBytes 为兼容参数，当前未使用（单行硬上限统一为 HardLogLineCap）。
func TruncateLogs(raw []byte, maxBytes, maxLineBytes int) []byte {
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	_ = maxLineBytes // 兼容参数：单行不再按此截断，统一使用 HardLogLineCap
	out := make([]byte, 0, len(raw))
	for len(raw) > 0 {
		nl := bytes.IndexByte(raw, '\n')
		var line []byte
		if nl >= 0 {
			line, raw = raw[:nl], raw[nl+1:]
		} else {
			line, raw = raw, nil
		}
		if len(line) > HardLogLineCap {
			line = line[:HardLogLineCap]
		}
		if len(out) > 0 && len(out)+len(line)+1 > maxBytes {
			break
		}
		out = append(out, line...)
		if nl >= 0 {
			out = append(out, '\n')
		}
	}
	return out
}

// Severity is the finding severity level. It is computed only by the rule
// engine; LLM output is advisory (ADR-004).
type Severity string

const (
	SeverityInfo     Severity = "INFO"
	SeverityLow      Severity = "LOW"
	SeverityMedium   Severity = "MEDIUM"
	SeverityHigh     Severity = "HIGH"
	SeverityCritical Severity = "CRITICAL"
)

// ParseSeverity converts a string into a Severity.
func ParseSeverity(s string) (Severity, error) {
	switch Severity(s) {
	case SeverityInfo, SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical:
		return Severity(s), nil
	default:
		return "", fmt.Errorf("invalid severity %q", s)
	}
}

// SeverityRank returns the ordering of a severity (higher is more severe).
func SeverityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 5
	case SeverityHigh:
		return 4
	case SeverityMedium:
		return 3
	case SeverityLow:
		return 2
	case SeverityInfo:
		return 1
	default:
		return 0
	}
}

// RiskLevel annotates generated kubectl commands.
type RiskLevel string

const (
	RiskSafe     RiskLevel = "SAFE"
	RiskLow      RiskLevel = "LOW"
	RiskMedium   RiskLevel = "MEDIUM"
	RiskHigh     RiskLevel = "HIGH"
	RiskCritical RiskLevel = "CRITICAL"
)

// CommandCategory classifies generated kubectl commands.
type CommandCategory string

const (
	CmdInvestigation CommandCategory = "investigation"
	CmdRemediation   CommandCategory = "remediation"
	CmdVerification  CommandCategory = "verification"
)

// EvidenceKind describes where an evidence value came from.
type EvidenceKind string

const (
	EvObjectField EvidenceKind = "object_field"
	EvEvent       EvidenceKind = "event"
	EvLog         EvidenceKind = "log"
	EvAnnotation  EvidenceKind = "annotation"
	EvDerived     EvidenceKind = "derived"
)

// ResourceRef identifies a Kubernetes resource. UID is for internal
// correlation only and never participates in fingerprinting or LLM input.
type ResourceRef struct {
	Kind      string `json:"kind"`
	Group     string `json:"group,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
}

// Budget bounds LLM diagnosis input size (ADR-017).
type Budget struct {
	MaxTokensPerFinding int
	MaxTotalTokens      int
	MaxFindings         int
}

// LogOptions control log collection caps (FR-022).
// LogOptions 控制日志采集的上限与筛选（FR-022）。
type LogOptions struct {
	Container    string
	TailLines    *int64
	SinceSeconds *int64
	Previous     bool // true = 取 previous logs
	MaxBytes     int
	MaxLineBytes int
}

// ScanOptions is the CLI/Server/CronJob-shared execution contract passed to
// service.ScanService.Run.
type ScanOptions struct {
	// Kubernetes connection.
	Kubeconfig string
	Context    string
	Namespace  string
	PodTarget  string // 非空 = 单 Pod 目标扫描（scan pod <name> -n <ns>）
	InCluster  bool
	Timeout    time.Duration // overall scan budget
	QPS        float32
	Burst      int
	// RequestTimeout is the per-request Kubernetes API timeout.
	RequestTimeout time.Duration

	// FailOn makes the process exit with code 2 when at least one finding
	// reaches the given severity (FR-026).
	FailOn Severity

	// Phase 1 / Phase 2 collection (FR-004, FR-024).
	Concurrency         int
	Phase2Concurrency   int
	CollectLogs         bool
	CollectPreviousLogs bool
	CollectEvents       bool
	MaxLogLines         int
	MaxLogBytes         int
	MaxLogLineBytes     int
	PodLogsTimeout      time.Duration
	Since               time.Duration // 日志采集时间窗口（--since）

	// Report output (FR-019).
	ReportDirectory string
	ReportFormat    string
	ReportMode      string
	WithDoc         bool // P7.7 是否附带规则字段文档（--with-doc，默认为 false 不增加输出）

	// Attribution 是对象存储归档的归属（P8.5，方案 15 §6.6）。CLI 不填即按系统任务处理，
	// 行为与之前一致；Server 必须在解码后覆盖，不信任请求体。
	Attribution ReportAttribution

	// Rules 启停（FR-013）。
	RulesEnabled  []string
	RulesDisabled []string

	// LLM 诊断配置（P7 接入；endpoint/api_key 只用于构建客户端，不落日志）。
	LLM LLMOptions

	// Progress 是可选的进度回调（不设置则静默）。
	// 回调不得阻塞/返回错误；耗时任务的"仍在执行"提示由实现端定时推送。
	// 用于 CLI 阶段进度 Spinner、Server 模式 WebSocket 状态推送、
	// 以及二期 Agent 的 Thinking / Tool Calling 状态指示器（ADR-007：共用服务层）。
	Progress ScanProgress
}

// ScanProgress 是 scan 过程中的单向进度通知。不定义 Error，失败由 Run 返回。
// 设计上只使用标准库类型，避免 model 包引入 UI 依赖。
type ScanProgress interface {
	// PhaseStarted 阶段开始：name 是稳定阶段 ID，title 是终端可读标题。
	PhaseStarted(name, title string)
	// PhaseSub 阶段内部的当前子项（用于 LLM 阶段显示 "诊断 2/5 incident xxx"）。
	PhaseSub(name string, index, total int, detail string)
	// PhaseDone 阶段结束：ok=false 表示该阶段产生错误/降级（但 Run 可能继续）。
	PhaseDone(name, detail string, ok bool, elapsed time.Duration)
}

// LLMOptions 是 LLM 诊断的执行参数（FR-015/FR-016）。
type LLMOptions struct {
	Enabled        bool
	Endpoint       string
	APIKey         string
	Model          string
	Temperature    float64
	MaxTokens      int
	MaxInputTokens int // 单 Finding 上下文上限（默认 8192）
	MaxTotalTokens int // 诊断阶段总预算（默认 32768）
	MaxFindings    int // 最多送诊 Finding 数（默认 30，自适应下调）
	Timeout        time.Duration
	Thinking       map[string]string // 标准思考参数，例如 {type: enabled}

	// Progress 是 LLM 诊断级的进度回调。ScanService 会把上层
	// ScanProgress 传入这里来驱动"单个 Incident 开始/结束"事件。
	// 如果 DiagnoseIncidents 独立使用，也可单独设置。
	Progress LLMProgress
}

// LLMProgress 是 LLM 诊断阶段的细粒度事件（每个 Incident 粒度）。
// LongTask 用于 >10s 仍在执行时的"抚慰"提示（经验 1073789）。
type LLMProgress interface {
	IncidentStarted(id string, index, total int)
	IncidentDone(id string, index, total int, llmUsed bool, err error)
	LongTask(id string, index, total int, elapsed time.Duration)
}

// ReportOptions 传给报告写入器。
type ReportOptions struct {
	Directory string
	Format    string // markdown | json | yaml
	Mode      string // none（仅终端）| latest（latest.md + latest.json）| daily（追加时间戳文件）
}

// ReportAttribution 是报告归档的归属信息（方案 15 §6.6）：对象存储里 reports 行的
// created_by 是「谁能读这份报告」的唯一依据，而扫描任务本身是进程内对象、没有 owner
// 列可继承，所以归属必须由调用方显式带进来。
//
// Actor 为空表示系统任务（定时巡检、CLI 本地扫描），归档时记 objectstore 的系统归属。
// Server 侧必须在解码请求体之后无条件覆盖这三个字段：它们由登录态与任务 ID 决定，
// 让请求体里的值漏进来就等于允许调用方伪造报告归属、把报告挂到别人名下。
type ReportAttribution struct {
	// Actor 是触发扫描的用户 ID；空 = 系统任务。
	Actor string
	// ClusterID 是本次扫描的目标集群 ID；空表示走 kubeconfig 默认目标。
	ClusterID string
	// ScanID 是扫描任务 ID（Server 侧即 ScanJob.ID）；空表示无任务标识。
	ScanID string
	// ScopeKind / ScopeValue 是巡检范围（cluster|namespace|pod 与取值，P8.13 方案 17 §7.1）。
	// 只回答「这份报告是扫什么出来的」，用于页面展示与列表过滤：既不参与对象 key 组装，
	// 也不参与读鉴权。CLI 不填即空串，报告页面显示「未记录」。
	ScopeKind  string
	ScopeValue string
}

// 报告巡检范围取值（P8.13，方案 17 §7.1）：reports.scope_kind 的合法值域。
//
// 放在 model 是因为发起侧（Server 的巡检 DTO）与归档侧（service 写 reports 行）都要用，
// 而这两处中间隔着 service 包——值域定义属于领域根，不属于任何一侧。
const (
	ScopeCluster   = "cluster"
	ScopeNamespace = "namespace"
	ScopePod       = "pod"
)

// ValidReportScope 判断一组巡检范围能否落库。
//
// 空 kind 是合法值：v8 之前的存量行、以及 CLI 本地扫描（不填归属）都落在这一类，页面显示
// 「未记录」。非法值不合法就非法，不做「大概是用户手滑」的猜测式纠正。
func ValidReportScope(kind, value string) bool {
	switch kind {
	case "":
		return true
	case ScopeCluster:
		// 全集群没有取值：编一个 "all" 出来会让过滤条件多一种没人写的写法。
		return value == ""
	case ScopeNamespace, ScopePod:
		// pod 的取值形态是 <ns>/<pod>；形态校验归发起侧（Server 组装），这里只管有没有。
		return value != ""
	default:
		return false
	}
}

// Key 返回资源的稳定内部键（kind/namespace/name），供关联索引与指纹使用。
func (r ResourceRef) Key() string {
	return strings.Join([]string{r.Kind, r.Namespace, r.Name}, "/")
}
