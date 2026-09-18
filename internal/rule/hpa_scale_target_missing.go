// Package rule 实现规则引擎（FR-013）。
// hpa_scale_target_missing.go：HPA scaleTargetRef 指向不存在的资源（P7.6）。
package rule

import (
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// HpaScaleTargetMissingRule HPA 的 spec.scaleTargetRef 指向不存在的 workload（Deployment/StatefulSet/ReplicaSet）。
type HpaScaleTargetMissingRule struct{}

func (HpaScaleTargetMissingRule) Name() string    { return "HpaScaleTargetMissing" }
func (HpaScaleTargetMissingRule) NeedsLogs() bool { return false }

func (r HpaScaleTargetMissingRule) Evaluate(ctx *RuleContext) []*model.Finding {
	exists := map[string]bool{}
	for i := range ctx.Snapshot.Workloads {
		w := &ctx.Snapshot.Workloads[i]
		exists[w.Ref.Kind+"/"+w.Ref.Name] = true
	}
	var out []*model.Finding
	for i := range ctx.Snapshot.Hpas {
		h := &ctx.Snapshot.Hpas[i]
		if h.ScaleTarget == "" || exists[h.ScaleTarget] {
			continue
		}
		evs := []model.Evidence{
			evidence.ObjectField(h.Ref.Kind+"/"+h.Ref.Name+"/spec", "scaleTargetRef", h.ScaleTarget),
		}
		out = append(out, newFinding(ctx, r, h.Ref, fmt.Sprintf("HPA 引用的目标资源 %s 不存在", h.ScaleTarget), model.SeverityHigh, evs))
	}
	return out
}
