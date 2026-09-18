// Package rule 实现规则引擎（FR-013）。
// meta.go：规则结构化元数据（P7.1，v4.0）。
package rule

import (
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// RuleType 规则的判定性质（SPEC §28.1）。
type RuleType string

const (
	// RuleTypeDetection 检测类：集群存在明确异常，产出 Finding（进入 Correlation 流程）。
	RuleTypeDetection RuleType = "detection"
	// RuleTypeSuggestion 建议/优化类：无异常但可改进（缺省/宽松配置等），默认低优先级或单独聚合。
	RuleTypeSuggestion RuleType = "suggestion"
)

// RuleMeta 是单条规则的结构化元数据，注册时注入（SPEC §28.1）。
// 设计要点：与规则检测逻辑解耦，由 RegisterWithMeta 在注册层统一管理。
type RuleMeta struct {
	RuleID       string         `json:"rule_id"`       // 稳定唯一 ID，报告/过滤/引用使用
	Category     string         `json:"category"`      // 问题域：Workload/Storage/Network/Security/Config/Reliability
	Kind         string         `json:"kind,omitempty"` // 关联资源 Kind
	RuleType     RuleType       `json:"type"`          // detection / suggestion
	SeverityBase model.Severity `json:"severity_base"` // 基准严重级，仍受 FR-011 SeverityPolicy 动态调整
	NeedsLogs    bool           `json:"needs_logs"`    // 命中后是否触发 Phase2 深度日志采集
	DocRef       string         `json:"doc_ref,omitempty"` // 关联的 K8s API 字段路径（文档增强，可空）
}

// RuleWithMeta 携带元数据的已注册规则。
type RuleWithMeta struct {
	Rule Rule
	Meta RuleMeta
}

// RuleMetaFilter 用于按元数据过滤规则；零值字段视为通配（不过滤）。
type RuleMetaFilter struct {
	Category string
	Kind     string
	RuleType RuleType
	Severity model.Severity
}

// normalizeMeta 补全默认元数据：缺失字段用默认值回填，保证元数据完整性（P7.2）。
func normalizeMeta(r Rule, m RuleMeta) RuleMeta {
	if m.RuleID == "" {
		m.RuleID = r.Name()
	}
	if m.Category == "" {
		m.Category = "General"
	}
	if m.RuleType == "" {
		m.RuleType = RuleTypeDetection
	}
	if m.SeverityBase == "" {
		m.SeverityBase = model.SeverityMedium
	}
	if !m.NeedsLogs {
		m.NeedsLogs = r.NeedsLogs()
	}
	return m
}

// validateMeta 校验元数据基本合法性。
func validateMeta(r Rule, m RuleMeta) error {
	if r == nil || r.Name() == "" {
		return fmt.Errorf("rule must be non-nil and have a name")
	}
	if m.RuleID == "" {
		return fmt.Errorf("rule_id is required for rule %s", r.Name())
	}
	if m.RuleType != RuleTypeDetection && m.RuleType != RuleTypeSuggestion {
		return fmt.Errorf("invalid rule type %q for rule %s", m.RuleType, r.Name())
	}
	if _, err := model.ParseSeverity(string(m.SeverityBase)); err != nil {
		return fmt.Errorf("invalid severity_base for rule %s: %w", r.Name(), err)
	}
	return nil
}
