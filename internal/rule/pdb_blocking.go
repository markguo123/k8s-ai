// Package rule 实现规则引擎（FR-013）。
// pdb_blocking.go：PDB 阻止 Pod 驱逐（disruptionsAllowed=0，P7.6）。
package rule

import (
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// PdbBlockingRule PDB 的 status.disruptionsAllowed 为 0，Pod 驱逐可能被卡住。
type PdbBlockingRule struct{}

func (PdbBlockingRule) Name() string    { return "PdbBlocking" }
func (PdbBlockingRule) NeedsLogs() bool { return false }

func (r PdbBlockingRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Pdbs {
		p := &ctx.Snapshot.Pdbs[i]
		if p.DisruptionsAllowed > 0 || len(p.Selector) == 0 {
			continue
		}
		evs := []model.Evidence{
			evidence.ObjectField(p.Ref.Kind+"/"+p.Ref.Name+"/status", "disruptionsAllowed", fmt.Sprint(p.DisruptionsAllowed)),
		}
		out = append(out, newFinding(ctx, r, p.Ref, "PDB 当前不允许任何 Pod 被驱逐（disruptionsAllowed=0）", model.SeverityHigh, evs))
	}
	return out
}
