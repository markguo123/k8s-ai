// Package model defines the pure domain models for k8s-ai.
// It must not import any internal package (ADR-015).
package model

import (
	"fmt"
	"strings"
	"time"
)

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

	// Rules 启停（FR-013）。
	RulesEnabled  []string
	RulesDisabled []string

	// LLM 诊断配置（P7 接入；endpoint/api_key 只用于构建客户端，不落日志）。
	LLM LLMOptions
}

// LLMOptions 是 LLM 诊断的执行参数（FR-015/FR-016）。
type LLMOptions struct {
	Enabled         bool
	Endpoint        string
	APIKey          string
	Model           string
	Temperature     float64
	MaxTokens       int
	MaxInputTokens  int // 单 Finding 上下文上限（默认 8192）
	MaxTotalTokens  int // 诊断阶段总预算（默认 32768）
	MaxFindings     int // 最多送诊 Finding 数（默认 30，自适应下调）
	Timeout         time.Duration
	DisableThinking bool // 关闭思考模式（Qwen/vLLM chat_template_kwargs.enable_thinking=false）
}

// ReportOptions 传给报告写入器。
type ReportOptions struct {
	Directory string
	Format    string // markdown | json | yaml
	Mode      string // none（仅终端）| latest（latest.md + latest.json）| daily（追加时间戳文件）
}

// Key 返回资源的稳定内部键（kind/namespace/name），供关联索引与指纹使用。
func (r ResourceRef) Key() string {
	return strings.Join([]string{r.Kind, r.Namespace, r.Name}, "/")
}
