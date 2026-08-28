package rule

import (
	"fmt"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// endpointGracePeriod 是 0 端点的宽限期：滚动发布时新 Pod 尚未 Ready 属正常暂态，
// 若所有匹配 Pod 都是该窗口内新建的则跳过判定。
const endpointGracePeriod = 2 * time.Minute

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
		// 滚动发布宽限：匹配 Pod 全部是窗口内新建的（尚未 Ready 属正常暂态），跳过避免误报。
		if allMatchingPodsFresh(ctx, svc) {
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

// allMatchingPodsFresh 判断匹配该 Service 的 Pod 是否全部在宽限期内新建。
// 无匹配 Pod 或任一 Pod 创建时间缺失/超出窗口时返回 false（保守上报）。
func allMatchingPodsFresh(ctx *RuleContext, svc *model.ServiceInfo) bool {
	pods := ctx.Index.PodsOfService(svc.Ref.Key())
	if len(pods) == 0 {
		return false
	}
	for _, p := range pods {
		age, ok := ageSince(p.CreationTimestamp, ctx.Now)
		if !ok || age >= endpointGracePeriod {
			return false
		}
	}
	return true
}
