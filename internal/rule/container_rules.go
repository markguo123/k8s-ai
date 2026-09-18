package rule

import (
	"time"
	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// ContainerCreateErrorRule 容器创建失败：CreateContainerConfigError/CreateContainerError。
// 覆盖 Secret 缺失、ConfigMap 缺失、卷/挂载配置错误等"启动即失败"场景。
type ContainerCreateErrorRule struct{}

func (ContainerCreateErrorRule) Name() string    { return "ContainerCreateError" }
func (ContainerCreateErrorRule) NeedsLogs() bool { return false }

func (r ContainerCreateErrorRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Pods {
		p := &ctx.Snapshot.Pods[i]
		for _, e := range allContainers(p) {
			c := e.c
			if c.State != "Waiting" || (c.Reason != "CreateContainerConfigError" && c.Reason != "CreateContainerError") {
				continue
			}
			src := containerSrc(p, e)
			evs := []model.Evidence{
				evidence.ObjectField(src, "waiting.reason", c.Reason),
				evidence.ObjectField(src, "waiting.message", c.Message),
			}
			for _, cm := range p.ConfigMaps {
				evs = append(evs, evidence.Derived("related", "configMaps", cm.Namespace+"/"+cm.Name))
			}
			for _, s := range p.SecretRefs {
				evs = append(evs, evidence.Derived("related", "secretRefs", s.Namespace+"/"+s.Name))
			}
			evs = append(evs, podEventEvidence(ctx, p.Ref)...)
			sev := model.SeverityMedium
			if isConfigNotFound(c.Message) {
				// CreateContainerConfigError 因 ConfigMap/Secret 缺失导致
				// 容器无法创建：服务完全不可用，提升为 HIGH。
				sev = model.SeverityHigh
			}
			out = append(out, newFinding(ctx, r, p.Ref, containerLabel(e)+" 创建失败（"+c.Reason+"）", sev, evs))
			break
		}
	}
	return out
}
// containerHealthMinAge 判定容器健康超时的最小存活时长（Running 但长时未 Ready 属陈旧状态）。
const containerHealthMinAge = 3 * time.Minute

// ContainerHealthTimeoutRule 就绪探针长期未通过：容器 Running 但未 Ready 且 Pod 已存活超过阈值。
// 与 Unhealthy 规则不同，本规则不依赖事件，仅按状态陈旧度判定（副本数足但状态不健康）。
type ContainerHealthTimeoutRule struct{}

func (ContainerHealthTimeoutRule) Name() string    { return "ContainerHealthTimeout" }
func (ContainerHealthTimeoutRule) NeedsLogs() bool { return true }

func (r ContainerHealthTimeoutRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Pods {
		p := &ctx.Snapshot.Pods[i]
		age, ok := ageSince(p.CreationTimestamp, ctx.Now)
		if !ok || age < containerHealthMinAge {
			continue
		}
		for _, c := range p.Containers {
			if c.State != "Running" || c.Ready {
				continue
			}
			src := "Pod/" + p.Ref.Name + "/status.containerStatuses[" + c.Name + "]"
			evs := []model.Evidence{
				evidence.ObjectField(src, "ready", "false"),
			}
			for _, cm := range p.ConfigMaps {
				evs = append(evs, evidence.Derived("related", "configMaps", cm.Namespace+"/"+cm.Name))
			}
			for _, sec := range p.SecretRefs {
				evs = append(evs, evidence.Derived("related", "secretRefs", sec.Namespace+"/"+sec.Name))
			}
			evs = append(evs, podEventEvidence(ctx, p.Ref)...)
			out = append(out, newFinding(ctx, r, p.Ref, "容器 "+c.Name+" 就绪探针长期未通过（Running 但 NotReady）", model.SeverityLow, evs))
			break
		}
	}
	return out
}
