package rule

import (
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// ServiceNoEndpointRule Service 没有可用 Endpoint（FR-009）。
type ServiceNoEndpointRule struct{}

func (ServiceNoEndpointRule) Name() string    { return "ServiceNoEndpoint" }
func (ServiceNoEndpointRule) NeedsLogs() bool { return false }

func (r ServiceNoEndpointRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Services {
		svc := &ctx.Snapshot.Services[i]
		if len(svc.Selector) == 0 {
			continue // 无 selector 的 Service（如 ExternalName）不判定
		}
		ready := 0
		for _, es := range ctx.Index.SlicesOfService(svc.Ref.Key()) {
			ready += es.Ready
		}
		matching := len(ctx.Index.PodsOfService(svc.Ref.Key()))
		if ready > 0 {
			continue
		}
		evs := []model.Evidence{
			evidence.ObjectField("Service/"+svc.Ref.Name+"/spec.selector", "selector", fmt.Sprint(svc.Selector)),
			evidence.ObjectField("Service/"+svc.Ref.Name+"/endpoints", "readyEndpoints", "0"),
			evidence.Derived("Service/"+svc.Ref.Name, "matchingPods", fmt.Sprint(matching)),
		}
		if matching == 0 {
			evs = append(evs, evidence.Derived("Service/"+svc.Ref.Name, "selectorMismatch", "没有 Pod 匹配 selector"))
		}
		out = append(out, newFinding(ctx, r, svc.Ref, "Service 没有可用 Endpoint", model.SeverityMedium, evs))
	}
	return out
}
