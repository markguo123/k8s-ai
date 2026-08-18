package rule

import (
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// IngressBackendRule Ingress backend 指向不存在的 Service（网络层信号）。
// 通用信号规则；根因分析统一由 Incident + LLM 完成。
type IngressBackendRule struct{}

func (IngressBackendRule) Name() string    { return "IngressBackend" }
func (IngressBackendRule) NeedsLogs() bool { return false }

func (r IngressBackendRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Ingresses {
		ing := &ctx.Snapshot.Ingresses[i]
		for _, backend := range ing.Backends {
			ns, svc, ok := parseBackend(backend)
			if !ok {
				continue
			}
			key := model.ResourceRef{Kind: "Service", Namespace: ns, Name: svc}.Key()
			if _, exists := ctx.Index.Services[key]; exists {
				continue
			}
			evs := []model.Evidence{
				evidence.ObjectField("Ingress/"+ing.Ref.Name+"/spec.rules.http.paths", "backend", backend),
				evidence.Derived("related", "missingService", ns+"/"+svc),
			}
			out = append(out, newFinding(ctx, r, ing.Ref, "Ingress backend 指向不存在的 Service "+ns+"/"+svc, model.SeverityMedium, evs))
			break
		}
	}
	return out
}

// parseBackend 解析 "ns/svc:port" 格式。
func parseBackend(b string) (ns, svc string, ok bool) {
	idx := strings.LastIndex(b, ":")
	if idx < 0 {
		return "", "", false
	}
	rest := b[:idx]
	svc = rest
	ns = ""
	if slash := strings.LastIndex(rest, "/"); slash >= 0 {
		ns = rest[:slash]
		svc = rest[slash+1:]
	}
	return ns, svc, svc != ""
}
