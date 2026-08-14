package report

import (
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// penaltyWeights 默认罚分表（FR-020，ADR-013）。
var penaltyWeights = map[model.Severity]int{
	model.SeverityCritical: 30,
	model.SeverityHigh:     15,
	model.SeverityMedium:   5,
	model.SeverityLow:      1,
	model.SeverityInfo:     0,
}

// ComputeHealthScore 计算健康评分：100 − Σ罚分；Correlated 不扣分；封顶 0。
// 评分只由程序计算，LLM 不参与（ADR-004）。
func ComputeHealthScore(findings []model.Finding) model.HealthScore {
	hs := model.HealthScore{Score: 100, Max: 100}
	for _, f := range findings {
		if f.Correlated {
			hs.CorrelatedExcluded++
			continue
		}
		points := penaltyWeights[f.Severity]
		if points == 0 {
			continue
		}
		hs.Penalties = append(hs.Penalties, model.Penalty{
			FindingID: f.ID,
			Resource:  f.Resource,
			Severity:  f.Severity,
			Points:    points,
			Reason:    fmt.Sprintf("%s/%s %s（%s）", f.Resource.Namespace, f.Resource.Name, f.Rule, f.Severity),
		})
		hs.Score -= points
	}
	if hs.Score < 0 {
		hs.Score = 0
	}
	return hs
}
