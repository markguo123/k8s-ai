// Package rule 实现规则引擎（FR-013）。
// hpa_replicas_mismatch.go：HPA 期望副本与当前副本长期不一致（P7.6）。
package rule

import (
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// HpaReplicasMismatchRule HPA 期望副本数与当前副本数不一致（指标取不到、缩容未生效等）。
type HpaReplicasMismatchRule struct{}

func (HpaReplicasMismatchRule) Name() string    { return "HpaReplicasMismatch" }
func (HpaReplicasMismatchRule) NeedsLogs() bool { return false }

func (r HpaReplicasMismatchRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Hpas {
		h := &ctx.Snapshot.Hpas[i]
		if h.DesiredReplicas == 0 || h.DesiredReplicas == h.CurrentReplicas {
			continue
		}
		src := h.Ref.Kind + "/" + h.Ref.Name + "/status"
		evs := []model.Evidence{
			evidence.ObjectField(src, "desiredReplicas", fmt.Sprint(h.DesiredReplicas)),
			evidence.ObjectField(src, "currentReplicas", fmt.Sprint(h.CurrentReplicas)),
		}
		out = append(out, newFinding(ctx, r, h.Ref, fmt.Sprintf("HPA 期望副本 %d 与当前副本 %d 不一致", h.DesiredReplicas, h.CurrentReplicas), model.SeverityMedium, evs))
	}
	return out
}
