package rule

import (
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// CrashLoopBackOffRule 容器反复崩溃重启（FR-005 重点异常）。
type CrashLoopBackOffRule struct{}

func (CrashLoopBackOffRule) Name() string    { return "CrashLoopBackOff" }
func (CrashLoopBackOffRule) NeedsLogs() bool { return true }

func (r CrashLoopBackOffRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Pods {
		p := &ctx.Snapshot.Pods[i]
		for _, c := range p.Containers {
			if c.State != "Waiting" || c.Reason != "CrashLoopBackOff" {
				continue
			}
			src := "Pod/" + p.Ref.Name + "/status.containerStatuses[" + c.Name + "]"
			evs := []model.Evidence{
				evidence.ObjectField(src, "restartCount", fmt.Sprint(c.RestartCount)),
				evidence.ObjectField(src, "lastState.reason", c.LastReason),
				evidence.ObjectField(src, "lastState.exitCode", fmt.Sprint(c.LastExitCode)),
			}
			if v, ok := c.Limits["memory"]; ok {
				evs = append(evs, evidence.ObjectField(src, "memory.limit", v))
			}
			evs = append(evs, podEventEvidence(ctx, p.Ref)...)
			out = append(out, newFinding(ctx, r, p.Ref, "容器 "+c.Name+" 反复崩溃重启（CrashLoopBackOff）", model.SeverityMedium, evs))
			break // 每个 Pod 只出一条
		}
	}
	return out
}

// OOMKilledRule 容器因内存超限被杀。
type OOMKilledRule struct{}

func (OOMKilledRule) Name() string    { return "OOMKilled" }
func (OOMKilledRule) NeedsLogs() bool { return true }

func (r OOMKilledRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Pods {
		p := &ctx.Snapshot.Pods[i]
		for _, c := range p.Containers {
			if c.LastReason != "OOMKilled" && !(c.State == "Terminated" && c.Reason == "OOMKilled") {
				continue
			}
			src := "Pod/" + p.Ref.Name + "/status.containerStatuses[" + c.Name + "]"
			evs := []model.Evidence{
				evidence.ObjectField(src, "lastState.reason", c.LastReason),
				evidence.ObjectField(src, "lastState.exitCode", fmt.Sprint(c.LastExitCode)),
			}
			if v, ok := c.Limits["memory"]; ok {
				evs = append(evs, evidence.ObjectField(src, "memory.limit", v))
			}
			evs = append(evs, podEventEvidence(ctx, p.Ref)...)
			out = append(out, newFinding(ctx, r, p.Ref, "容器 "+c.Name+" 因 OOMKilled 被杀", model.SeverityMedium, evs))
			break
		}
	}
	return out
}

// ImagePullBackOffRule 镜像拉取失败。
type ImagePullBackOffRule struct{}

func (ImagePullBackOffRule) Name() string    { return "ImagePullBackOff" }
func (ImagePullBackOffRule) NeedsLogs() bool { return true }

func (r ImagePullBackOffRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Pods {
		p := &ctx.Snapshot.Pods[i]
		for _, c := range p.Containers {
			if c.State != "Waiting" || (c.Reason != "ImagePullBackOff" && c.Reason != "ErrImagePull") {
				continue
			}
			src := "Pod/" + p.Ref.Name + "/status.containerStatuses[" + c.Name + "]"
			evs := []model.Evidence{
				evidence.ObjectField(src, "waiting.reason", c.Reason),
				evidence.ObjectField(src, "waiting.message", c.Message),
				evidence.ObjectField(src, "image", c.Image),
			}
			evs = append(evs, podEventEvidence(ctx, p.Ref)...)
			out = append(out, newFinding(ctx, r, p.Ref, "容器 "+c.Name+" 镜像拉取失败（"+c.Reason+"）", model.SeverityMedium, evs))
			break
		}
	}
	return out
}

// PendingPodRule Pod 一直处于 Pending（无法调度/启动）。
type PendingPodRule struct{}

func (PendingPodRule) Name() string    { return "PendingPod" }
func (PendingPodRule) NeedsLogs() bool { return false }

func (r PendingPodRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Pods {
		p := &ctx.Snapshot.Pods[i]
		if p.Phase != "Pending" {
			continue
		}
		evs := []model.Evidence{
			evidence.ObjectField("Pod/"+p.Ref.Name+"/status.phase", "phase", p.Phase),
		}
		for _, cond := range p.Conditions {
			if cond.Type == "PodScheduled" && cond.Status == "False" {
				evs = append(evs, evidence.ObjectField("Pod/"+p.Ref.Name+"/status.conditions", "PodScheduled", cond.Message))
			}
		}
		evs = append(evs, podEventEvidence(ctx, p.Ref)...)
		out = append(out, newFinding(ctx, r, p.Ref, "Pod 处于 Pending 状态", model.SeverityMedium, evs))
	}
	return out
}
