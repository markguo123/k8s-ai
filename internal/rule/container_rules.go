package rule

import (
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
		for _, c := range p.Containers {
			if c.State != "Waiting" || (c.Reason != "CreateContainerConfigError" && c.Reason != "CreateContainerError") {
				continue
			}
			src := "Pod/" + p.Ref.Name + "/status.containerStatuses[" + c.Name + "]"
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
			out = append(out, newFinding(ctx, r, p.Ref, "容器 "+c.Name+" 创建失败（"+c.Reason+"）", model.SeverityMedium, evs))
			break
		}
	}
	return out
}
