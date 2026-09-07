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

// DaemonSetReplicaRule DaemonSet 期望节点数与就绪数不匹配。
// 复用 replicaFinding：DesiredReplicas 为已调度节点数，ReadyReplicas 为就绪数。
type DaemonSetReplicaRule struct{}

func (DaemonSetReplicaRule) Name() string    { return "DaemonSetReplica" }
func (DaemonSetReplicaRule) NeedsLogs() bool { return false }

func (r DaemonSetReplicaRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Workloads {
		w := &ctx.Snapshot.Workloads[i]
		if w.Ref.Kind != "DaemonSet" {
			continue
		}
		if f := replicaFinding(ctx, r, w); f != nil {
			out = append(out, f)
		}
	}
	return out
}

// DeploymentProgressDeadlineRule Deployment 发布卡住（ProgressDeadlineExceeded）。
// 比单纯副本不足更精确：副本数可能暂时够但发布已超时，仍属异常。
type DeploymentProgressDeadlineRule struct{}

func (DeploymentProgressDeadlineRule) Name() string    { return "DeploymentProgressDeadline" }
func (DeploymentProgressDeadlineRule) NeedsLogs() bool { return false }

func (r DeploymentProgressDeadlineRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Workloads {
		w := &ctx.Snapshot.Workloads[i]
		if w.Ref.Kind != "Deployment" {
			continue
		}
		for _, c := range w.Conditions {
			if c.Type == "Progressing" && c.Status == "False" && c.Reason == "ProgressDeadlineExceeded" {
				src := w.Ref.Kind + "/" + w.Ref.Name + "/status.conditions"
				evs := []model.Evidence{
					evidence.ObjectField(src, "Progressing", c.Reason+" "+c.Message),
				}
				out = append(out, newFinding(ctx, r, w.Ref, "Deployment 发布超时（ProgressDeadlineExceeded）", model.SeverityMedium, evs))
				break
			}
		}
	}
	return out
}

// JobFailedRule Job 失败。
type JobFailedRule struct{}

func (JobFailedRule) Name() string { return "JobFailed" }

// NeedsLogs 为 true：Job 失败的关键证据是其 Pod 的日志，
// LogTargets 通过关联索引反查 Job 下属 Pod 进行 Phase2 采集。
func (JobFailedRule) NeedsLogs() bool { return true }

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
