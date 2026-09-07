package rule

import (
	"fmt"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// pendingMinAge 是 Pending 判定的最小存活时间：滚动发布时新 Pod 短暂 Pending 属正常，避免误报。
const pendingMinAge = 60 * time.Second

// terminatingMinAge 是 Terminating 判定的最小删除时长：正常删除几秒完成，超过该时长视为卡住。
const terminatingMinAge = 5 * time.Minute

// ageSince 解析 RFC3339 时间戳并返回距 now 的时长；解析失败返回 (0, false)。
func ageSince(ts string, now time.Time) (time.Duration, bool) {
	if ts == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return 0, false
	}
	return now.Sub(t), true
}

// containerEntry 关联容器状态与其类别（init/普通），供容器类规则统一遍历。
type containerEntry struct {
	c    model.ContainerInfo
	init bool
}

// allContainers 返回 Pod 的 init 容器与普通容器（init 在前）。
func allContainers(p *model.PodInfo) []containerEntry {
	out := make([]containerEntry, 0, len(p.InitContainers)+len(p.Containers))
	for _, c := range p.InitContainers {
		out = append(out, containerEntry{c: c, init: true})
	}
	for _, c := range p.Containers {
		out = append(out, containerEntry{c: c, init: false})
	}
	return out
}

// containerLabel 返回面向用户的容器称谓。
func containerLabel(e containerEntry) string {
	if e.init {
		return "init 容器 " + e.c.Name
	}
	return "容器 " + e.c.Name
}

// containerSrc 返回容器状态的证据来源路径。
func containerSrc(p *model.PodInfo, e containerEntry) string {
	if e.init {
		return "Pod/" + p.Ref.Name + "/status.initContainerStatuses[" + e.c.Name + "]"
	}
	return "Pod/" + p.Ref.Name + "/status.containerStatuses[" + e.c.Name + "]"
}

// CrashLoopBackOffRule 容器反复崩溃重启（FR-005 重点异常）。
type CrashLoopBackOffRule struct{}

func (CrashLoopBackOffRule) Name() string    { return "CrashLoopBackOff" }
func (CrashLoopBackOffRule) NeedsLogs() bool { return true }

func (r CrashLoopBackOffRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Pods {
		p := &ctx.Snapshot.Pods[i]
		for _, e := range allContainers(p) {
			c := e.c
			if c.State != "Waiting" || c.Reason != "CrashLoopBackOff" {
				continue
			}
			src := containerSrc(p, e)
			evs := []model.Evidence{
				evidence.ObjectField(src, "restartCount", fmt.Sprint(c.RestartCount)),
				evidence.ObjectField(src, "lastState.reason", c.LastReason),
				evidence.ObjectField(src, "lastState.exitCode", fmt.Sprint(c.LastExitCode)),
			}
			if v, ok := c.Limits["memory"]; ok {
				evs = append(evs, evidence.ObjectField(src, "memory.limit", v))
			}
			evs = append(evs, podEventEvidence(ctx, p.Ref)...)
			out = append(out, newFinding(ctx, r, p.Ref, containerLabel(e)+" 反复崩溃重启（CrashLoopBackOff）", model.SeverityMedium, evs))
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
		for _, e := range allContainers(p) {
			c := e.c
			if c.LastReason != "OOMKilled" && !(c.State == "Terminated" && c.Reason == "OOMKilled") {
				continue
			}
			src := containerSrc(p, e)
			evs := []model.Evidence{
				evidence.ObjectField(src, "lastState.reason", c.LastReason),
				evidence.ObjectField(src, "lastState.exitCode", fmt.Sprint(c.LastExitCode)),
			}
			if v, ok := c.Limits["memory"]; ok {
				evs = append(evs, evidence.ObjectField(src, "memory.limit", v))
			}
			evs = append(evs, podEventEvidence(ctx, p.Ref)...)
			out = append(out, newFinding(ctx, r, p.Ref, containerLabel(e)+" 因 OOMKilled 被杀", model.SeverityMedium, evs))
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
		for _, e := range allContainers(p) {
			c := e.c
			if c.State != "Waiting" || (c.Reason != "ImagePullBackOff" && c.Reason != "ErrImagePull") {
				continue
			}
			src := containerSrc(p, e)
			evs := []model.Evidence{
				evidence.ObjectField(src, "waiting.reason", c.Reason),
				evidence.ObjectField(src, "waiting.message", c.Message),
				evidence.ObjectField(src, "image", c.Image),
			}
			evs = append(evs, podEventEvidence(ctx, p.Ref)...)
			out = append(out, newFinding(ctx, r, p.Ref, containerLabel(e)+" 镜像拉取失败（"+c.Reason+"）", model.SeverityMedium, evs))
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
		// 时间门槛：刚创建（<60s）的 Pending 多为滚动发布正常暂态，跳过。
		// 时间戳缺失（解析失败）时保守判定，仍然上报。
		if age, ok := ageSince(p.CreationTimestamp, ctx.Now); ok && age < pendingMinAge {
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

// PodTerminatingRule Pod 删除后长时间卡在 Terminating（finalizer 卡住/卷卸载失败）。
type PodTerminatingRule struct{}

func (PodTerminatingRule) Name() string    { return "PodTerminating" }
func (PodTerminatingRule) NeedsLogs() bool { return false }

func (r PodTerminatingRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Pods {
		p := &ctx.Snapshot.Pods[i]
		if p.DeletionTimestamp == "" {
			continue
		}
		age, ok := ageSince(p.DeletionTimestamp, ctx.Now)
		if !ok || age < terminatingMinAge {
			continue
		}
		evs := []model.Evidence{
			evidence.ObjectField("Pod/"+p.Ref.Name+"/metadata", "deletionTimestamp", p.DeletionTimestamp),
			evidence.ObjectField("Pod/"+p.Ref.Name+"/metadata", "terminatingFor", age.Truncate(time.Minute).String()),
		}
		if len(p.Finalizers) > 0 {
			evs = append(evs, evidence.ObjectField("Pod/"+p.Ref.Name+"/metadata", "finalizers", fmt.Sprint(p.Finalizers)))
		}
		evs = append(evs, podEventEvidence(ctx, p.Ref)...)
		out = append(out, newFinding(ctx, r, p.Ref, "Pod 卡在 Terminating 状态", model.SeverityMedium, evs))
	}
	return out
}

// highRestartThreshold 是 HighRestartCount 的重启次数门槛：Running 但反复重启属隐性故障。
const highRestartThreshold = 5

// EvictedPodRule Pod 被驱逐（phase=Failed + reason=Evicted），通常是节点压力的后果。
// 根因在节点（DiskPressure/MemoryPressure），不在此 Pod 自身，故 NeedsLogs=false。
type EvictedPodRule struct{}

func (EvictedPodRule) Name() string    { return "EvictedPod" }
func (EvictedPodRule) NeedsLogs() bool { return false }

func (r EvictedPodRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Pods {
		p := &ctx.Snapshot.Pods[i]
		if p.Phase != "Failed" || p.Reason != "Evicted" {
			continue
		}
		src := "Pod/" + p.Ref.Name + "/status"
		evs := []model.Evidence{
			evidence.ObjectField(src, "phase", p.Phase),
			evidence.ObjectField(src, "reason", p.Reason),
		}
		evs = append(evs, podEventEvidence(ctx, p.Ref)...)
		out = append(out, newFinding(ctx, r, p.Ref, "Pod 被驱逐（Evicted）", model.SeverityMedium, evs))
	}
	return out
}

// HighRestartCountRule 容器仍在 Running 但重启次数超阈值（隐性崩溃/抖动）。
// CrashLoopBackOff 漏掉的盲区：状态不是 Waiting 而是 Running，但反复重启。
type HighRestartCountRule struct{}

func (HighRestartCountRule) Name() string    { return "HighRestartCount" }
func (HighRestartCountRule) NeedsLogs() bool { return true } // 抖动根因在日志里

func (r HighRestartCountRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Pods {
		p := &ctx.Snapshot.Pods[i]
		for _, e := range allContainers(p) {
			c := e.c
			if c.RestartCount < highRestartThreshold {
				continue
			}
			// Running 状态下高频重启才有意义；CrashLoopBackOff（Waiting）由专属规则管。
			if c.State != "Running" {
				continue
			}
			src := containerSrc(p, e)
			evs := []model.Evidence{
				evidence.ObjectField(src, "restartCount", fmt.Sprint(c.RestartCount)),
				evidence.ObjectField(src, "state", c.State),
			}
			if c.LastReason != "" {
				evs = append(evs, evidence.ObjectField(src, "lastState.reason", c.LastReason))
			}
			evs = append(evs, podEventEvidence(ctx, p.Ref)...)
			out = append(out, newFinding(ctx, r, p.Ref, containerLabel(e)+" 高频重启（restartCount="+fmt.Sprint(c.RestartCount)+"）", model.SeverityLow, evs))
			break // 每个 Pod 只出一条
		}
	}
	return out
}
