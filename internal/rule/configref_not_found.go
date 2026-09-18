// Package rule 实现规则引擎（FR-013）。
// configref_not_found.go：Pod 引用的 ConfigMap/Secret 不存在（仅引用名存在性，不读内容值，P7.6，suggestion）。
package rule

import (
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// ConfigRefNotFoundRule 容器/卷引用的 ConfigMap 或 Secret 在集群内不存在（suggestion）。
type ConfigRefNotFoundRule struct{}

func (ConfigRefNotFoundRule) Name() string    { return "ConfigRefNotFound" }
func (ConfigRefNotFoundRule) NeedsLogs() bool { return false }

// Evaluate 校验 Pod 引用的 ConfigMap/Secret 名称在集群内存在（仅存在性/引用名，不读内容值）。
func (r ConfigRefNotFoundRule) Evaluate(ctx *RuleContext) []*model.Finding {
	cmSet := map[string]bool{}
	for _, cm := range ctx.Snapshot.ConfigMaps {
		cmSet[cm.Namespace+"/"+cm.Name] = true
	}
	secSet := map[string]bool{}
	for _, sec := range ctx.Snapshot.Secrets {
		secSet[sec.Ref.Namespace+"/"+sec.Ref.Name] = true
	}
	var out []*model.Finding
	for i := range ctx.Snapshot.Pods {
		p := &ctx.Snapshot.Pods[i]
		for _, cm := range p.ConfigMaps {
			key := cm.Namespace + "/" + cm.Name
			if cm.Namespace == "" || cmSet[key] {
				continue
			}
			evs := []model.Evidence{
				evidence.ObjectField("Pod/"+p.Ref.Name+"/spec", "configMap", key),
			}
			out = append(out, newFinding(ctx, r, p.Ref, fmt.Sprintf("Pod 引用的 ConfigMap %s 不存在", key), model.SeverityLow, evs))
		}
		for _, sec := range p.SecretRefs {
			key := sec.Namespace + "/" + sec.Name
			if sec.Namespace == "" || secSet[key] {
				continue
			}
			evs := []model.Evidence{
				evidence.ObjectField("Pod/"+p.Ref.Name+"/spec", "secret", key),
			}
			out = append(out, newFinding(ctx, r, p.Ref, fmt.Sprintf("Pod 引用的 Secret %s 不存在", key), model.SeverityLow, evs))
		}
	}
	return out
}
