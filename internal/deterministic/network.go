package deterministic

import (
	"fmt"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func init() {
	Register(serviceNoEndpointDiagnoser{})
	Register(endpointSliceDiagnoser{})
	Register(ingressBackendDiagnoser{})
	Register(dnsDiagnoser{})
	Register(networkPolicyDiagnoser{})
}

// dnsFailureSignals 是应用侧真正出现解析失败时的典型字样（只认真实日志/事件文本）。
var dnsFailureSignals = []string{
	"no such host",
	"server misbehaving",
	"lookup failed",
	"temporary failure in name resolution",
	"dns_server failed",
	"could not resolve",
	"Name or service not known",
}

// logMentions 在 Pod 已采集的日志里找命中任一子串的一行（deep 档位才有日志）。
func logMentions(p *model.PodInfo, subs ...string) (string, bool) {
	if p == nil {
		return "", false
	}
	for _, lg := range p.Logs {
		for _, blob := range [][]byte{lg.Previous, lg.Current} {
			if len(blob) == 0 {
				continue
			}
			for _, line := range strings.Split(string(blob), "\n") {
				if matchAny(line, subs...) {
					return strings.TrimSpace(line), true
				}
			}
		}
	}
	return "", false
}

func selectorText(sel map[string]string) string {
	if len(sel) == 0 {
		return "（无 selector）"
	}
	parts := make([]string, 0, len(sel))
	for k, v := range sel {
		parts = append(parts, k+"="+v)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// serviceReadyEndpoints 汇总该 Service 全部 EndpointSlice 的就绪/未就绪地址数。
func serviceReadyEndpoints(c *Case, svc model.ResourceRef) (ready, notReady int, slices []*model.EndpointSliceInfo) {
	slices = c.E.index().SlicesOfService(svc.Key())
	for _, es := range slices {
		ready += es.Ready
		notReady += es.NotReady
	}
	return ready, notReady, slices
}

// upstreamIngresses 返回把该 Service 当后端的 Ingress（关系出处 spec.rules[].http.paths[].backend）。
func upstreamIngresses(c *Case, svc model.ResourceRef) []model.ResourceRef {
	if c.E.Graph == nil {
		return nil
	}
	var out []model.ResourceRef
	for _, e := range c.E.Graph.In(svc.Key()) {
		if e.From.Kind == "Ingress" {
			out = append(out, e.From)
		}
	}
	return out
}

// serviceNoEndpointDiagnoser 处理 Service 没有任何可用 Endpoint。
type serviceNoEndpointDiagnoser struct{}

func (serviceNoEndpointDiagnoser) ID() string { return "NETWORK_SERVICE_NO_ENDPOINT" }

func (serviceNoEndpointDiagnoser) Scenario() string { return "Service 没有可用 Endpoint" }

func (serviceNoEndpointDiagnoser) Match(c *Case) bool { return c.HasRule("ServiceNoEndpoint") }

func (serviceNoEndpointDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("ServiceNoEndpoint")
	svc := c.Service(f.Resource)
	if svc == nil {
		return Result{
			AnchorFinding:   f.ID,
			Status:          model.RootCauseUnknown,
			Confidence:      ConfUnknown,
			ConfidenceBasis: "规则命中过该 Service，但它不在本次快照里，判因会用到未采集的数据",
			RootCause:       "Service " + f.Resource.Name + " 上报无可用 Endpoint，但对象本身未采集到",
			EvidenceGaps:    []string{"Service 对象未成功采集，需先确认该资源的 list 调用是否失败"},
		}
	}
	pods := c.E.index().PodsOfService(f.Resource.Key())
	readyEP, notReadyEP, slices := serviceReadyEndpoints(c, f.Resource)
	readyPods := 0
	for _, p := range pods {
		if podReady(p) {
			readyPods++
		}
	}

	r := Result{
		AnchorFinding: f.ID,
		EvidenceRefs:  evIDs(f),
		Investigation: []model.Command{
			inv(fmt.Sprintf("kubectl %sdescribe svc %s", nsArg(f.Resource.Namespace), f.Resource.Name)),
			inv(fmt.Sprintf("kubectl %sget endpointslice -l kubernetes.io/service-name=%s", nsArg(f.Resource.Namespace), f.Resource.Name)),
			inv(fmt.Sprintf("kubectl %sget pods -l %s", nsArg(f.Resource.Namespace), selectorFlag(svc.Selector))),
		},
		Verification: []model.Command{
			ver(fmt.Sprintf("kubectl %sget endpointslice -l kubernetes.io/service-name=%s", nsArg(f.Resource.Namespace), f.Resource.Name)),
		},
		AlternativeCauses: []string{"readinessProbe 配置与容器实际监听端口/路径不符", "Service 端口名与容器端口 name 不一致（gRPC/命名端口场景）", "Pod 位于另一个命名空间，selector 永远选不到"},
	}

	switch {
	case len(svc.Selector) == 0:
		r.Status = model.RootCauseUnknown
		r.Confidence = ConfUnknown
		r.ConfidenceBasis = "该 Service 没有 selector（ExternalName 或人工维护端点），快照信息不足以判定"
		r.RootCause = "Service " + f.Resource.Name + " 没有 selector，就绪端点数取决于外部端点或人工 Endpoint 对象"
		r.EvidenceGaps = append(r.EvidenceGaps, "无 selector 的 Service 需要看 Endpoint/ExternalName 与外部系统状态，本次只读快照不含这些信息")
	case len(pods) == 0:
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "Service.spec.selector 是真实字段，且与本次全量采集的 Pod 标签做过精确比对，命中数为 0"
		r.RootCause = fmt.Sprintf("Service %s 的 selector %s 在命名空间 %s 内没有匹配到任何 Pod",
			f.Resource.Name, selectorText(svc.Selector), f.Resource.Namespace)
		r.Mechanism = "Service 只按 label 选 Pod；一个都选不中时 endpointslice 控制器不会生成任何就绪地址，ClusterIP 后面是空的"
		r.EvidenceGaps = append(r.EvidenceGaps, "无法从只读数据判断是 selector 写错、负载标签改了，还是后端负载被删，需要结合变更记录")
		r.RemediationDirection = "两头对齐：要么把 Pod 模板的 labels 补成 selector 要求的键值，要么把 Service.selector 改成负载真实携带的标签；改完必须验证 endpointslice 出现就绪地址"
	case readyPods > 0:
		r.Status = model.RootCauseProbable
		r.Confidence = ConfInferred
		r.ConfidenceBasis = fmt.Sprintf("selector 命中 %d 个 Pod 且其中 %d 个 Ready，但 EndpointSlice 就绪地址为 0，只能推断端点同步或端口名环节出了问题", len(pods), readyPods)
		r.RootCause = fmt.Sprintf("Service %s 的 selector 命中 %d 个 Pod（%d 个 Ready），但就绪 Endpoint 数为 0",
			f.Resource.Name, len(pods), readyPods)
		r.Mechanism = "就绪判定由 kubelet 探针和 endpointslice 控制器共同决定：Pod 容器 Ready 但未进入就绪端点，通常是 Service 端口 name 与容器端口 name 不匹配，或端点控制器同步滞后"
		r.EvidenceGaps = append(r.EvidenceGaps,
			"快照未含 Service 端口与容器端口的 name 对应关系，无法确认是不是命名端口不匹配",
			fmt.Sprintf("EndpointSlice 未就绪地址数为 %d；若端点采集失败，该数字也不可信", notReadyEP))
		r.AlternativeCauses = append(r.AlternativeCauses, "EndpointSlice 采集环节失败，导致就绪地址数被读成 0")
		r.RemediationDirection = "先比对 Service.ports[].name 与容器 ports[].name 是否一致（有一方写了 name 就必须两边都有），再确认 endpointslice 控制器工作正常"
	case notReadyEP > 0:
		r.Status = model.RootCauseProbable
		r.Confidence = ConfEventMessage
		r.ConfidenceBasis = "EndpointSlice 里存在未就绪地址（status.conditions.ready 非 true），说明后端 Pod 存在但未通过就绪判定"
		r.RootCause = fmt.Sprintf("Service %s 的 %d 个候选端点全部未就绪（EndpointSlice notReady=%d），没有任何地址可接流量",
			f.Resource.Name, notReadyEP, notReadyEP)
		r.Mechanism = "端点存在但未就绪：Pod 被 endpointslice 控制器收录，却因为就绪探针失败或容器未 Ready 被标为 notReady，Service 不会把流量转给它"
		r.EvidenceGaps = append(r.EvidenceGaps, "未采集就绪探针失败原因（探针配置与容器日志），无法确认是应用未就绪还是探针配置错了")
		r.RemediationDirection = "按未就绪的那几个 Pod 逐个看就绪探针与启动日志：应用没起来就修应用，探针路径/端口/初始延迟写错就修探针"
	default:
		r.Status = model.RootCauseProbable
		r.Confidence = ConfInferred
		r.ConfidenceBasis = fmt.Sprintf("selector 命中 %d 个 Pod 但全部未就绪，且没有任何 EndpointSlice；无法排除端点数据未采集成功", len(pods))
		r.RootCause = fmt.Sprintf("Service %s 的 selector 命中 %d 个 Pod，但全部未就绪，端点列表因此为空",
			f.Resource.Name, len(pods))
		r.Mechanism = "后端 Pod 一个都没通过就绪判定，endpointslice 里没有就绪地址，ClusterIP 转发不到任何后端"
		r.EvidenceGaps = append(r.EvidenceGaps,
			"未确认 Pod 未就绪的具体原因（需按 Pod 层诊断结论确认）",
			fmt.Sprintf("本次快照里该 Service 的 EndpointSlice 数为 %d，若该资源采集失败，则端点为空不能算证据", len(slices)))
		r.RemediationDirection = "这个结论的真正落点在 Pod 层：先让后端 Pod 逐个 Ready，Service 端点会自动恢复，不要试图通过删建 Service 来解决"
	}
	if evs := evOfKind(f, model.EvEvent); len(evs) > 0 {
		r.EvidenceRefs = dedupe(append(r.EvidenceRefs, evs...))
	}
	r.Impact = fmt.Sprintf("通过 ClusterIP 访问 %s 的请求拿不到后端（连接被拒或超时）；就绪端点 %d，未就绪端点 %d",
		f.Resource.Name, readyEP, notReadyEP)

	chain := []string{"Service/" + f.Resource.Namespace + "/" + f.Resource.Name + "（就绪端点 " + fmt.Sprint(readyEP) + "）"}
	for _, p := range pods {
		chain = append(chain, resLabel(p.Ref)+"（"+orDash(p.Phase)+"）")
		for _, w := range c.E.index().OwnerChain(p) {
			chain = append(chain, resLabel(w.Ref)+"（"+replicasText(w.DesiredReplicas, w.ReadyReplicas)+"）")
		}
	}
	for _, ing := range upstreamIngresses(c, f.Resource) {
		chain = append(chain, resLabel(ing))
		r.Impact += fmt.Sprintf("；上游 %s 路由到该服务会返回 503", resLabel(ing))
	}
	r.ImpactChain = dedupe(chain)

	// 因果边：selector 命中的 Pod 出问题 → 该 Service 没有端点（关系出处 spec.selector）。
	for _, it := range c.Items {
		if it.ID == f.ID || it.Resource.Kind != "Pod" {
			continue
		}
		p := c.PodOf(*it)
		if p == nil || !labelsSuperset(svc.Selector, p.Labels) {
			continue
		}
		if edge, ok := c.CausalEdge(it, f, "后端 Pod 未就绪或不存在，Service 无法产生就绪 Endpoint"); ok {
			r.CausalEdges = append(r.CausalEdges, edge)
		}
	}
	r.RemediationText = "根因在后端选择与就绪状态：让 selector 命中至少一个 Ready Pod 后，EndpointSlice 会自动补上就绪地址。本次体检只读，不会代执行修改。"
	return r
}

// selectorFlag 把 selector 拼成 kubectl -l 参数。
func selectorFlag(sel map[string]string) string {
	if len(sel) == 0 {
		return "<labels>"
	}
	parts := make([]string, 0, len(sel))
	for k, v := range sel {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

// endpointSliceDiagnoser 处理 EndpointSlice 与 selector/就绪状态不一致。
//
// 这条刻意与 NETWORK_SERVICE_NO_ENDPOINT 分开：后者说「没有端点」，
// 这里说「端点数据和 Pod 真实就绪状态自相矛盾」，处置动作完全不同。
type endpointSliceDiagnoser struct{}

func (endpointSliceDiagnoser) ID() string { return "NETWORK_ENDPOINTSLICE_INCONSISTENT" }

func (endpointSliceDiagnoser) Scenario() string {
	return "EndpointSlice 与 selector / Pod 就绪状态不一致"
}

func (endpointSliceDiagnoser) Match(c *Case) bool {
	if !c.HasRule("ServiceNoEndpoint") {
		return false
	}
	f := c.ByRule("ServiceNoEndpoint")
	svc := c.Service(f.Resource)
	if svc == nil {
		return false
	}
	pods := c.E.index().PodsOfService(f.Resource.Key())
	readyPods := 0
	for _, p := range pods {
		if podReady(p) {
			readyPods++
		}
	}
	_, notReadyEP, slices := serviceReadyEndpoints(c, f.Resource)
	// 只在「数据自相矛盾」时介入：有 Ready Pod 但没有就绪端点。
	return readyPods > 0 && (notReadyEP > 0 || len(slices) == 0)
}

func (endpointSliceDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("ServiceNoEndpoint")
	svc := c.Service(f.Resource)
	pods := c.E.index().PodsOfService(f.Resource.Key())
	var readyRefs []model.ResourceRef
	for _, p := range pods {
		if podReady(p) {
			readyRefs = append(readyRefs, p.Ref)
		}
	}
	_, notReadyEP, slices := serviceReadyEndpoints(c, f.Resource)
	r := Result{
		AnchorFinding: f.ID,
		Status:        model.RootCauseProbable,
		Confidence:    ConfInferred,
		ConfidenceBasis: fmt.Sprintf("同一快照里 %d 个 Pod 的 containerStatuses.ready=true，而 EndpointSlice 就绪地址为 0（切片数 %d，未就绪地址 %d），两处状态互相矛盾",
			len(readyRefs), len(slices), notReadyEP),
		RootCause: fmt.Sprintf("EndpointSlice 与 Pod 就绪状态不一致：Service %s 有 %d 个 Ready Pod（%s），但就绪端点为 0",
			f.Resource.Name, len(readyRefs), refList(readyRefs, 3)),
		Mechanism:    "就绪端点由 endpointslice 控制器按 selector + Pod Ready + 端口名匹配三件事共同算出；三者中任一处口径不一致，就会出现 Pod Ready 但端点缺失或未就绪",
		Impact:       "客户端从 ClusterIP 拿不到可用后端，但看 Pod 又是健康的，故障现象与现象层数据脱节，最容易误判为网络问题",
		EvidenceRefs: evIDs(f),
		EvidenceGaps: []string{
			"快照未含 EndpointSlice 每个地址的 conditions.ready 与目标 Pod 的逐条对应关系，无法指出是哪一个端点被标未就绪",
			"未采集 endpointslice 控制器的日志与事件，无法区分端口名不匹配与控制器同步滞后",
			"若 EndpointSlice 本身采集失败，本结论不成立，需先看采集质量里该资源是否有失败",
		},
		AlternativeCauses: []string{"Service 端口写了 name 而容器端口没写同名 name（或反之）", "endpointslice 控制器（kube-controller-manager）同步滞后或刚发生过 Pod 抖动", "Topology Aware Routing 把该可用区的端点剔除了"},
		Investigation: []model.Command{
			inv(fmt.Sprintf("kubectl %sget endpointslice -l kubernetes.io/service-name=%s -o yaml", nsArg(f.Resource.Namespace), f.Resource.Name)),
			inv(fmt.Sprintf("kubectl %sget svc %s -o jsonpath='{.spec.ports}'", nsArg(f.Resource.Namespace), f.Resource.Name)),
			inv(fmt.Sprintf("kubectl %sget pods -o wide -l %s", nsArg(f.Resource.Namespace), selectorFlag(svc.Selector))),
		},
		Verification: []model.Command{
			ver(fmt.Sprintf("kubectl %sget endpointslice -l kubernetes.io/service-name=%s -o custom-columns=NAME:.metadata.name,ADDRESSES:.addresses[*].ip", nsArg(f.Resource.Namespace), f.Resource.Name)),
		},
		RemediationDirection: "先核对端口 name 两边是否一致，再看 endpointslice 里对应地址的 conditions；不要通过反复删 Service 制造端点",
		RemediationText:      "把 Service.ports[].name 与容器 ports[].name 对齐（要么都不写，要么完全同名），修正后 EndpointSlice 会自行收敛。本次体检只读，不会代执行。",
		ImpactChain: append([]string{"Service/" + f.Resource.Namespace + "/" + f.Resource.Name + "（就绪端点 0）"},
			refListToChain(readyRefs)...),
	}
	for _, it := range c.Items {
		if it.ID == f.ID || it.Resource.Kind != "Pod" {
			continue
		}
		p := c.PodOf(*it)
		if p == nil || !podReady(p) || !labelsSuperset(svc.Selector, p.Labels) {
			continue
		}
		if edge, ok := c.CausalEdge(it, f, "Pod 已 Ready 但未产生就绪端点，端点数据与就绪状态不一致"); ok {
			r.CausalEdges = append(r.CausalEdges, edge)
		}
	}
	return r
}

func refListToChain(refs []model.ResourceRef) []string {
	var out []string
	for _, r := range refs {
		out = append(out, resLabel(r))
	}
	return out
}

// ingressBackendDiagnoser 处理 Ingress 后端不可用与路由冲突。
type ingressBackendDiagnoser struct{}

func (ingressBackendDiagnoser) ID() string { return "NETWORK_INGRESS_BACKEND_UNAVAILABLE" }

func (ingressBackendDiagnoser) Scenario() string {
	return "Ingress 后端 Service 缺失或路由冲突"
}

func (ingressBackendDiagnoser) Match(c *Case) bool {
	return c.HasRule("IngressBackend") || c.HasRule("IngressHostConflict")
}

func (ingressBackendDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("IngressBackend")
	if f == nil {
		f = c.ByRule("IngressHostConflict")
	}
	missing := c.ByRule("IngressBackend")
	r := Result{
		AnchorFinding: f.ID,
		EvidenceRefs:  evIDs(f),
		Investigation: []model.Command{
			inv(fmt.Sprintf("kubectl %sdescribe ingress %s", nsArg(f.Resource.Namespace), f.Resource.Name)),
			inv(fmt.Sprintf("kubectl %sget svc -o wide", nsArg(f.Resource.Namespace))),
		},
		Verification: []model.Command{
			ver(fmt.Sprintf("kubectl %sget ingress %s -o wide", nsArg(f.Resource.Namespace), f.Resource.Name)),
		},
		AlternativeCauses: []string{"Ingress Controller 未部署或未接管该 class，导致规则根本没被下发", "后端 Service 存在但没有就绪 Endpoint（等价于 503）", "TLS 证书或 host 未解析到入口 LB，故障在 DNS 层而不是路由层"},
	}
	if missing != nil {
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "Ingress.spec.rules[].http.paths[].backend.service.name 是真实引用字段，且本次快照已全量采集 Service 清单，其中不存在该后端"
		r.RootCause = missing.Summary
		r.Mechanism = "Ingress Controller 按 backend 引用的 Service 生成上游端点；引用不存在的 Service 时，该 host/path 没有任何上游可转发，请求直接返回 503"
		r.Impact = "该 Ingress 上命中这条规则的所有 host/path 请求都会返回 503，且不会随 Pod 恢复而自愈，必须改配置"
		r.EvidenceGaps = append(r.EvidenceGaps, "未确认后端 Service 是被改名、删除，还是 Ingress 从别的命名空间复制过来后没改引用")
		r.EvidenceRefs = dedupe(append(r.EvidenceRefs, evOfKind(missing, model.EvObjectField)...))
		r.RemediationDirection = "把 backend 改成真实存在的 Service（含命名空间），或者把缺的那个 Service 建回来；改完用同一 host 打一次请求确认不再是 503"
		r.RemediationText = "修正 spec.rules[].http.paths[].backend.service.name 与 namespace，使其指向已存在且有待机端点的 Service。本次体检只读，变更请走发布流程。"
		r.Remediation = []model.Command{
			rem(fmt.Sprintf("kubectl %sedit ingress %s  # 修正 backend.service.name", nsArg(f.Resource.Namespace), f.Resource.Name), model.RiskHigh),
		}
	} else {
		r.Status = model.RootCauseProbable
		r.Confidence = ConfInferred
		r.ConfidenceBasis = "同一 Ingress 内出现重复 host 或同路径多规则，路由结果取决于控制器实现，无法从只读数据断言实际命中哪条"
		r.RootCause = f.Summary
		r.Mechanism = "同一 host/path 被多条规则覆盖时，Ingress Controller 按自身排序规则选一条生效，请求可能被转到预期之外的后端 Service，表现为偶发或局部 404/503"
		r.Impact = "受影响的是该 host 下的路径流量，表现为部分请求路由到错误后端，容易被误判成应用问题"
		r.EvidenceGaps = append(r.EvidenceGaps,
			"快照只含 host/path/backend 归一化信息，未含 IngressClass 与控制器实际下发结果，无法确认最终生效的是哪条规则")
		r.RemediationDirection = "消除 host/path 重叠：给每条规则划清路径前缀，或把冲突规则拆到不同 host / 不同 Ingress 上"
		r.RemediationText = "调整规则使同一 host 下不存在覆盖关系（更长路径优先或拆 host），再逐个 host 回放请求验证。"
	}
	// 影响链走真实 RoutesTo 边：Ingress → 后端 Service → 该 Service 的端点情况。
	chain := []string{resLabel(f.Resource) + "（Ingress）"}
	if c.E.Graph != nil {
		for _, e := range c.E.Graph.Out(f.Resource.Key()) {
			if e.To.Kind != "Service" {
				continue
			}
			ready, notReady, _ := serviceReadyEndpoints(c, e.To)
			chain = append(chain, resLabel(e.To)+fmt.Sprintf("（就绪端点 %d / 未就绪 %d）", ready, notReady))
			if ready == 0 {
				r.Impact += fmt.Sprintf("；后端 %s 当前没有就绪端点，同样会返回 503", resLabel(e.To))
			}
		}
	}
	r.ImpactChain = dedupe(chain)
	// 因果边：同链上后端 Service 的端点问题会直接打到这个 Ingress（关系出处 backend 引用）。
	for _, it := range c.Items {
		if it.ID == f.ID || it.Rule != "ServiceNoEndpoint" {
			continue
		}
		if edge, ok := c.CausalEdge(it, f, "后端 Service 没有就绪 Endpoint，Ingress 该路径必然返回 503"); ok {
			edge.Confidence = model.RootCauseConfirmed
			r.CausalEdges = append(r.CausalEdges, edge)
		}
	}
	return r
}

// dnsDiagnoser 处理集群 DNS / CoreDNS 异常。
//
// 只读体检拿不到 CoreDNS 自身的解析日志，也不该假装能证明「某个请求被 DNS 拒了」，
// 因此本诊断器最远只给 probable，并强制写清缺口。
type dnsDiagnoser struct{}

func (dnsDiagnoser) ID() string { return "PLATFORM_DNS_ABNORMAL" }

func (dnsDiagnoser) Scenario() string { return "集群 DNS / CoreDNS 异常" }

func (dnsDiagnoser) Match(c *Case) bool {
	if comp, ok := c.Component("CoreDNS"); ok && (!comp.Present || !comp.Ready) {
		return true
	}
	for _, it := range c.Items {
		p := c.PodOf(*it)
		if p == nil {
			continue
		}
		if _, ok := c.EventWith(it.Resource, dnsFailureSignals...); ok {
			return true
		}
		if _, ok := logMentions(p, dnsFailureSignals...); ok {
			return true
		}
	}
	return false
}

func (dnsDiagnoser) Diagnose(c *Case) Result {
	r := Result{
		Status:     model.RootCauseProbable,
		Confidence: ConfInferred,
		EvidenceGaps: []string{
			"只读体检不采集 CoreDNS Pod 的解析日志与错误计数，无法证明解析失败发生在 DNS 侧",
			"未采集 node-local DNS /上游 upstream resolver 配置（kube-dns Service 与节点 /etc/resolv.conf），无法排除上游 DNS 或 Search 域配置问题",
			"未采集 NetworkPolicy 与 Service 的 DNS 相关放行规则，无法排除 53 端口被策略拦截",
		},
		AlternativeCauses: []string{"应用使用了外部域名而集群上游 DNS 不可达", "Pod 的 dnsPolicy 被改成 Default，绕过了集群 DNS", "CoreDNS 副本数不足或 conntrack 表冲突导致偶发解析超时", "应用侧解析缓存或 JNDI/连接池配置问题，被误报成 DNS 故障"},
		Investigation: []model.Command{
			inv("kubectl -n kube-system get pods -l k8s-app=kube-dns -o wide"),
			inv("kubectl -n kube-system get svc kube-dns -o wide"),
			inv("kubectl run dnscheck --image=busybox:1.36 --rm -it --restart=Never -- nslookup kubernetes.default.svc.cluster.local  # 需在业务上下文里人工执行"),
		},
		Verification: []model.Command{
			ver("kubectl -n kube-system rollout status deploy/coredns"),
		},
		RemediationDirection: "先确认 CoreDNS 自身是否健康、再确认解析失败是否只在特定命名空间/特定域名出现；范围特征决定该修 CoreDNS、上游 DNS 还是 Pod 的 dnsPolicy",
	}
	var symptom string
	var symptomRef model.ResourceRef
	for _, it := range c.Items {
		p := c.PodOf(*it)
		if p == nil {
			continue
		}
		if ev, ok := c.EventWith(it.Resource, dnsFailureSignals...); ok {
			symptom, symptomRef = ev.Reason+"/"+ev.Message, it.Resource
			r.EvidenceRefs = dedupe(append(r.EvidenceRefs, evIDs(it)...))
			r.AnchorFinding = it.ID
			r.Confidence = ConfEventMessage
			r.ConfidenceBasis = "该 Pod 的事件里直接出现解析失败消息（事件是集群自身写入的原始记录）"
			break
		}
		if line, ok := logMentions(p, dnsFailureSignals...); ok && symptom == "" {
			symptom, symptomRef = line, it.Resource
			r.EvidenceRefs = dedupe(append(r.EvidenceRefs, evIDs(it)...))
			r.AnchorFinding = it.ID
			r.Confidence = ConfEventMessage
			r.ConfidenceBasis = "该 Pod 的容器日志里出现解析失败原文（deep 档位采集），说明解析确实没成功"
		}
	}
	comp, hasComp := c.Component("CoreDNS")
	switch {
	case symptom != "" && hasComp && (!comp.Present || !comp.Ready):
		r.RootCause = fmt.Sprintf("Pod %s 出现域名解析失败（%s），同时集群 CoreDNS 组件状态异常：%s",
			resLabel(symptomRef), clip(symptom, 120), orDash(comp.Detail))
		r.ConfidenceBasis += "；且 CoreDNS 组件探测结果同时异常，两侧证据指向同一环节"
		r.EvidenceGaps = append(r.EvidenceGaps, "缺少 CoreDNS 侧日志，无法确认是它挂了、配置错了还是被上游拖慢")
	case symptom != "":
		r.RootCause = fmt.Sprintf("Pod %s 出现域名解析失败（%s），但本次采集到的 CoreDNS 组件状态没有明显异常",
			resLabel(symptomRef), clip(symptom, 120))
		r.Mechanism = "解析失败发生在 Pod → 集群 DNS → 上游 DNS 这条链上，但目前只能确认第一跳失败，无法定位是哪一跳"
		r.Confidence = ConfEventMessage
	case hasComp && !comp.Present:
		r.Status = model.RootCauseProbable
		r.RootCause = "本次快照里没有发现 CoreDNS 组件（探测条件：" + orDash(comp.Detail) + "），集群可能缺少集群 DNS 或采用了其他 DNS 方案"
		r.ConfidenceBasis = "组件探测基于本次全量 Pod/Deployment 清单，「没探测到」是事实，但 DNS 方案本身可能是有意替换"
		r.Confidence = ConfInferred
		r.AlternativeCauses = append(r.AlternativeCauses, "集群使用 node-local DNS、eBPF DNS 或其他 DNS 实现，探测关键字不匹配")
	case hasComp && !comp.Ready:
		r.RootCause = "CoreDNS 组件存在但未就绪：" + orDash(comp.Detail)
		r.ConfidenceBasis = "组件就绪状态来自本次采集的 Pod/Deployment 状态字段"
		r.Confidence = ConfObjectStatus
		r.EvidenceGaps = append(r.EvidenceGaps, "未采集 CoreDNS Pod 的事件与日志，无法说明未就绪的原因")
		r.Mechanism = "CoreDNS 未就绪时，全集群的服务名解析都会受影响，故障面通常表现为多个命名空间同时报连接异常"
	default:
		return Result{} // 没有可引用的解析失败证据时不出结论，交给 unknown 兜底。
	}
	r.Mechanism = orEmpty(r.Mechanism, "服务名解析失败会让应用连不上依赖，症状常被误读成下游服务故障")
	r.Impact = "DNS 是集群级公共依赖：一旦异常，影响面不局限于本故障链，通常表现为多个负载同时报连接超时或 5xx"
	if len(r.EvidenceRefs) == 0 {
		r.EvidenceGaps = append(r.EvidenceGaps, "本结论未挂到本次故障链的具体证据 ID，只能作为方向性提示")
	}
	r.RemediationText = "按范围收敛：只有个别 Pod 报错先查它的 dnsPolicy 与域名拼写；大面积报错再查 CoreDNS 副本、上游 DNS 与 53 端口连通性。"
	return r
}

func orEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// networkPolicyDiagnoser 处理 NetworkPolicy 相关结论（含「能不能判定拦截」的边界声明）。
type networkPolicyDiagnoser struct{}

func (networkPolicyDiagnoser) ID() string { return "NETWORK_POLICY_POSTURE" }

func (networkPolicyDiagnoser) Scenario() string {
	return "NetworkPolicy 策略基线与可能的流量限制"
}

func (networkPolicyDiagnoser) Match(c *Case) bool { return c.HasRule("NetworkPolicyNoDefaultDeny") }

func (networkPolicyDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("NetworkPolicyNoDefaultDeny")
	ns := f.Resource.Name
	var policies []model.NetworkPolicyInfo
	if c.E.Snapshot != nil {
		for _, np := range c.E.Snapshot.NetworkPolicies {
			if np.Ref.Namespace == ns {
				policies = append(policies, np)
			}
		}
	}
	r := Result{
		AnchorFinding:   f.ID,
		Status:          model.RootCauseProbable,
		Confidence:      ConfInferred,
		ConfidenceBasis: fmt.Sprintf("本次快照在该命名空间全量采集到 %d 条 NetworkPolicy，其中没有 podSelector 为空的默认拒绝策略", len(policies)),
		RootCause: fmt.Sprintf("命名空间 %s 有 %d 条 NetworkPolicy，但缺少覆盖全部 Pod 的 default-deny 策略：%s",
			ns, len(policies), f.Summary),
		Mechanism:    "NetworkPolicy 是白名单模型：只要存在一条覆盖某 Pod 的策略，未被显式放行的流量就该被拒绝；没有默认拒绝时，横向流量在集群内是敞开的，既放大爆炸半径，也让后续的流量异常更难归因",
		Impact:       "属于安全基线风险而非当前故障：同命名空间与跨命名空间的任意 Pod 都能访问该命名空间内的工作负载端口",
		EvidenceRefs: evIDs(f),
		EvidenceGaps: []string{
			"快照只含 podSelector 与 policyTypes，未含 ingress/egress 具体规则，因此无法判断现有策略是否已经在拦截某条业务链路",
			"只读体检不执行连通性测试，任何「NetworkPolicy 导致访问不通」的判断都需要人工用探测 Pod 验证",
			"未确认集群 CNI 是否启用 NetworkPolicy 执行（部分 CNI 默认不执行策略，此时策略不生效）",
		},
		AlternativeCauses: []string{"该命名空间有意不做隔离（纯内网测试环境）", "CNI 不支持 NetworkPolicy，写了也不生效，隔离靠其他层实现"},
		Investigation: []model.Command{
			inv("kubectl -n " + ns + " get networkpolicy -o yaml"),
			inv("kubectl -n " + ns + " get pods --show-labels"),
		},
		Verification: []model.Command{
			ver("kubectl -n " + ns + " get networkpolicy"),
		},
		RemediationDirection: "先补一条 podSelector 为空的 default-deny（ingress），再按业务逐条加白名单；上线前用探测 Pod 验证放行结果，避免直接把生产流量封死",
		RemediationText:      "按「先默认拒绝、再按需放行」的顺序补齐策略，并在灰度命名空间验证连通性后再全量下发。本次体检只读，不会代执行。",
		Remediation: []model.Command{
			rem("kubectl apply -f default-deny.yaml  # 先灰度验证再全量下发", model.RiskMedium),
		},
	}
	chain := []string{"Namespace/" + ns + "（NetworkPolicy " + fmt.Sprint(len(policies)) + " 条，无默认拒绝）"}
	for _, np := range policies {
		chain = append(chain, resLabel(np.Ref))
	}
	r.ImpactChain = dedupe(chain)
	return r
}
