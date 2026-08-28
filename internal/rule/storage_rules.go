package rule

import (
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// PVCPendingRule PVC 申请不成功（Pending/Lost）。
type PVCPendingRule struct{}

func (PVCPendingRule) Name() string    { return "PVCPending" }
func (PVCPendingRule) NeedsLogs() bool { return false }

func (r PVCPendingRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Storage {
		st := &ctx.Snapshot.Storage[i]
		if st.Kind != "PVC" || (st.Status != "Pending" && st.Status != "Lost") {
			continue
		}
		evs := []model.Evidence{
			evidence.ObjectField("PVC/"+st.Ref.Name+"/status.phase", "phase", st.Status),
		}
		if st.Reason != "" {
			evs = append(evs, evidence.ObjectField("PVC/"+st.Ref.Name+"/status.conditions", "reason", st.Reason))
		}
		// 关联的受影响 Pod 数量。
		evs = append(evs, evidence.Derived("PVC/"+st.Ref.Name, "affectedPods", fmt.Sprint(len(ctx.Index.PodsOfPVC(st.Ref.Key())))))
		evs = append(evs, podEventEvidence(ctx, st.Ref)...)
		out = append(out, newFinding(ctx, r, st.Ref, "PVC 处于 "+st.Status+" 状态", model.SeverityHigh, evs))
	}
	return out
}

// PVFailedRule PV 异常：Failed 为真故障；Released 在 Retain 策略下属正常生命周期，仅低级别提示。
type PVFailedRule struct{}

func (PVFailedRule) Name() string    { return "PVFailed" }
func (PVFailedRule) NeedsLogs() bool { return false }

func (r PVFailedRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Storage {
		st := &ctx.Snapshot.Storage[i]
		if st.Kind != "PV" {
			continue
		}
		var sev model.Severity
		var title string
		switch st.Status {
		case "Failed":
			sev = model.SeverityHigh
			title = "PV 处于 Failed 状态"
		case "Released":
			// Retain 回收策略下 Released 是正常状态，仅提示可能需要人工清理。
			sev = model.SeverityLow
			title = "PV 处于 Released 状态（待清理或重新绑定）"
		default:
			continue
		}
		evs := []model.Evidence{
			evidence.ObjectField("PV/"+st.Ref.Name+"/status.phase", "phase", st.Status),
		}
		if st.Reason != "" {
			evs = append(evs, evidence.ObjectField("PV/"+st.Ref.Name+"/status", "reason", st.Reason))
		}
		evs = append(evs, podEventEvidence(ctx, st.Ref)...)
		out = append(out, newFinding(ctx, r, st.Ref, title, sev, evs))
	}
	return out
}

// FailedMountRule 挂载/附加卷失败（Events 驱动）。
type FailedMountRule struct{}

func (FailedMountRule) Name() string    { return "FailedMount" }
func (FailedMountRule) NeedsLogs() bool { return false }

func (r FailedMountRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Pods {
		p := &ctx.Snapshot.Pods[i]
		hit := false
		configNotFound := false
		var evs []model.Evidence
		for _, e := range ctx.Index.EventsFor(p.Ref) {
			if e.Reason == "FailedMount" || e.Reason == "FailedAttachVolume" {
				hit = true
				if isConfigNotFound(e.Message) {
					configNotFound = true
				}
				evs = append(evs, evidence.TruncateValue(evidence.Event(e), 2048))
			}
		}
		if !hit {
			continue
		}
		// 关联存储链（Pod→PVC→PV→SC）。
		for _, st := range ctx.Index.StorageChain(p) {
			evs = append(evs, evidence.Derived("storage", st.Kind, st.Ref.Name+"("+st.Status+")"))
		}
		sev := model.SeverityMedium
		if configNotFound {
			// ConfigMap/Secret 缺失导致挂载失败：服务完全不可用，提升为 HIGH。
			sev = model.SeverityHigh
		}
		out = append(out, newFinding(ctx, r, p.Ref, "存储卷挂载失败（FailedMount）", sev, evs))
	}
	return out
}
