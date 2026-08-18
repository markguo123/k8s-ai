package rule

import (
	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// UnhealthyRule 容器 Running 但未 Ready 且存在 Unhealthy 事件（探针失败信号）。
// 这是通用就绪信号规则（不针对具体应用故障）；根因分析统一由 Incident + LLM 完成。
type UnhealthyRule struct{}

func (UnhealthyRule) Name() string    { return "Unhealthy" }
func (UnhealthyRule) NeedsLogs() bool { return true }

func (r UnhealthyRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Pods {
		p := &ctx.Snapshot.Pods[i]
		unhealthy := false
		for _, e := range ctx.Index.EventsFor(p.Ref) {
			if e.Reason == "Unhealthy" {
				unhealthy = true
				break
			}
		}
		if !unhealthy {
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
			for _, s := range p.SecretRefs {
				evs = append(evs, evidence.Derived("related", "secretRefs", s.Namespace+"/"+s.Name))
			}
			evs = append(evs, podEventEvidence(ctx, p.Ref)...)
			out = append(out, newFinding(ctx, r, p.Ref, "容器 "+c.Name+" 健康检查失败（探针 Unhealthy）", model.SeverityMedium, evs))
			break
		}
	}
	return out
}
