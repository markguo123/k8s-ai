// Package evidence 负责证据的构建、排序、编号与截断（FR-014）。
// 证据值在采集边界已脱敏（ADR-006），本包不再处理敏感信息。
package evidence

import (
	"fmt"
	"sort"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// 证据排序权重：状态字段 > Events > 日志 > 注解 > 派生（FR-014）。
const (
	RankObjectField = 100
	RankEvent       = 80
	RankLog         = 60
	RankAnnotation  = 40
	RankDerived     = 20
)

// 默认单条证据值最大长度（日志片段等大文本截断）。
const defaultMaxValue = 4096

// ObjectField 构建对象字段证据。
func ObjectField(source, key, value string) model.Evidence {
	return model.Evidence{Kind: model.EvObjectField, Source: source, Key: key, Value: value, Rank: RankObjectField}
}

// Event 构建事件证据（值包含类型、消息与次数）。
func Event(e model.EventInfo) model.Evidence {
	return model.Evidence{
		Kind:   model.EvEvent,
		Source: "Event/" + e.InvolvedObject.Kind + "/" + e.InvolvedObject.Namespace + "/" + e.InvolvedObject.Name,
		Key:    e.Reason,
		Value:  fmt.Sprintf("[%s] %s (count=%d)", e.Type, e.Message, e.Count),
		Rank:   RankEvent,
	}
}

// Log 将采集到的容器日志转换为 0..2 条证据（current/previous）。
func Log(cl model.CollectedLog) []model.Evidence {
	var out []model.Evidence
	if len(cl.Current) > 0 {
		out = append(out, model.Evidence{
			Kind:      model.EvLog,
			Source:    "logs/current",
			Key:       "logs/current",
			Value:     string(cl.Current),
			Truncated: cl.Truncated,
			Rank:      RankLog,
		})
	}
	if len(cl.Previous) > 0 {
		out = append(out, model.Evidence{
			Kind:      model.EvLog,
			Source:    "logs/previous",
			Key:       "logs/previous",
			Value:     string(cl.Previous),
			Truncated: cl.Truncated,
			Rank:      RankLog,
		})
	}
	return out
}

// Annotation 构建注解证据。
func Annotation(key, value string) model.Evidence {
	return model.Evidence{Kind: model.EvAnnotation, Source: "annotations", Key: key, Value: value, Rank: RankAnnotation}
}

// Derived 构建派生证据（规则计算出的中间结论，如影响 Pod 数）。
func Derived(source, key, value string) model.Evidence {
	return model.Evidence{Kind: model.EvDerived, Source: source, Key: key, Value: value, Rank: RankDerived}
}

// AssignIDs 按 Rank 降序、Source 升序稳定排序并分配 E1..En。
// 稳定编号保证指纹与 1.2 趋势对比的跨扫描一致性（ADR-003）。
func AssignIDs(evs []model.Evidence) []model.Evidence {
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].Rank != evs[j].Rank {
			return evs[i].Rank > evs[j].Rank
		}
		return evs[i].Source < evs[j].Source
	})
	for i := range evs {
		evs[i].ID = fmt.Sprintf("E%d", i+1)
	}
	return evs
}

// TruncateValue 限制证据值长度，超长部分截断并标记 Truncated。
func TruncateValue(e model.Evidence, maxLen int) model.Evidence {
	if maxLen <= 0 {
		maxLen = defaultMaxValue
	}
	if len(e.Value) <= maxLen {
		return e
	}
	e.Value = e.Value[:maxLen] + "…(truncated)"
	e.Truncated = true
	return e
}

// logKeyWords 日志关键行关键词（覆盖 Go panic、nginx [emerg]、应用 error/failed 等）。
var logKeyWords = []string{"PANIC", "FATAL", "ERROR", "EMERG", "EXCEPTION", "FAILED", "INVALID"}

// KeyLogLine 返回日志中第一条命中关键字的行（限长 300），供规则降级与关联证据使用。
func KeyLogLine(log string) string {
	for _, line := range strings.Split(log, "\n") {
		u := strings.ToUpper(line)
		for _, k := range logKeyWords {
			if strings.Contains(u, k) {
				t := strings.TrimSpace(line)
				if len(t) > 300 {
					t = t[:300] + "…"
				}
				return t
			}
		}
	}
	return ""
}
