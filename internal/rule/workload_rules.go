package rule

import (
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// replicaFinding 副本数不匹配类规则的共用判定（Deployment/StatefulSet）。
func replicaFinding(ctx *RuleContext, r Rule, w *model.WorkloadInfo) *model.Finding {
	desired := int32(0)
	if w.DesiredReplicas != nil {
		desired = *w.DesiredReplicas
	}
	ready := int32(0)
	if w.ReadyReplicas != nil {
		ready = *w.ReadyReplicas
	}
	if desired <= 0 || ready >= desired {
		return nil
	}
	src := w.Ref.Kind + "/" + w.Ref.Name + "/status"
	evs := []model.Evidence{
		evidence.ObjectField(src, "desiredReplicas", fmt.Sprint(desired)),
		evidence.ObjectField(src, "readyReplicas", fmt.Sprint(ready)),
	}
	if w.AvailableReplicas != nil {
		evs = append(evs, evidence.ObjectField(src, "availableReplicas", fmt.Sprint(*w.AvailableReplicas)))
	}
	if w.UpdatedReplicas != nil {
		evs = append(evs, evidence.ObjectField(src, "updatedReplicas", fmt.Sprint(*w.UpdatedReplicas)))
	}
	for _, c := range w.Conditions {
		if c.Status == "False" {
			evs = append(evs, evidence.ObjectField(src+"/conditions", c.Type, c.Reason+" "+c.Message))
		}
	}
	return newFinding(ctx, r, w.Ref, fmt.Sprintf("副本数不匹配：%d/%d ready", ready, desired), model.SeverityHigh, evs)
}

// DeploymentReplicaRule Deployment 可用副本不足。
type DeploymentReplicaRule struct{}

func (DeploymentReplicaRule) Name() string    { return "DeploymentReplica" }
func (DeploymentReplicaRule) NeedsLogs() bool { return false }

func (r DeploymentReplicaRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Workloads {
		w := &ctx.Snapshot.Workloads[i]
		if w.Ref.Kind != "Deployment" {
			continue
		}
		if f := replicaFinding(ctx, r, w); f != nil {
			out = append(out, f)
		}
	}
	return out
}

// StatefulSetReplicaRule StatefulSet 可用副本不足。
type StatefulSetReplicaRule struct{}

func (StatefulSetReplicaRule) Name() string    { return "StatefulSetReplica" }
func (StatefulSetReplicaRule) NeedsLogs() bool { return false }

func (r StatefulSetReplicaRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Workloads {
		w := &ctx.Snapshot.Workloads[i]
		if w.Ref.Kind != "StatefulSet" {
			continue
		}
		if f := replicaFinding(ctx, r, w); f != nil {
			out = append(out, f)
		}
	}
	return out
}

// JobFailedRule Job 失败。
type JobFailedRule struct{}

func (JobFailedRule) Name() string    { return "JobFailed" }
func (JobFailedRule) NeedsLogs() bool { return false }

func (r JobFailedRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Workloads {
		w := &ctx.Snapshot.Workloads[i]
		if w.Ref.Kind != "Job" {
			continue
		}
		for _, c := range w.Conditions {
			if c.Type == "Failed" && c.Status == "True" {
				evs := []model.Evidence{
					evidence.ObjectField("Job/"+w.Ref.Name+"/status.conditions", "Failed", c.Reason+" "+c.Message),
				}
				out = append(out, newFinding(ctx, r, w.Ref, "Job 执行失败", model.SeverityHigh, evs))
				break
			}
		}
	}
	return out
}
