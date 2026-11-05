package deterministic

import (
	"fmt"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func init() {
	Register(nodeNotReadyDiagnoser{})
	Register(nodePressureDiagnoser{})
}

// nodeNotReadyDiagnoser 处理节点 NotReady（很多「Pod 莫名消失/工作负载副本不足」的真根因在这里）。
type nodeNotReadyDiagnoser struct{}

func (nodeNotReadyDiagnoser) ID() string { return "NODE_NOTREADY" }

func (nodeNotReadyDiagnoser) Scenario() string { return "节点 NotReady" }

func (nodeNotReadyDiagnoser) Match(c *Case) bool { return c.HasRule("NodeNotReady") }

func (nodeNotReadyDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("NodeNotReady")
	n := c.Node(f.Resource.Name)
	name := f.Resource.Name
	r := Result{
		AnchorFinding: f.ID,
		Mechanism:     "节点上的 kubelet 未按期上报心跳，控制面把 Node 标记为 NotReady；该节点上的 Pod 失去运行环境，新 Pod 也不会再被调度上来",
		EvidenceRefs:  evIDs(f),
		Investigation: []model.Command{
			inv("kubectl describe node " + name),
			inv("kubectl get events -A --field-selector involvedObject.name=" + name + " --sort-by=.lastTimestamp"),
			inv("kubectl get pods -A --field-selector spec.nodeName=" + name + " -o wide"),
			inv("节点侧确认 kubelet 与容器运行时：systemctl status kubelet && journalctl -u kubelet --since=-30m"),
		},
		AlternativeCauses: []string{
			"kubelet 进程停止或崩溃",
			"容器运行时（containerd/docker）异常导致 kubelet 上报失败",
			"节点与控制面之间网络分区（心跳丢失但业务仍在跑）",
			"节点宕机、被断电或已从集群摘除",
		},
		EvidenceGaps: []string{
			"kubelet 与容器运行时日志在节点本地，只读体检无法采集，节点内部根因需登节点确认",
			"无法区分「节点真挂了」与「仅心跳链路断了」，驱逐是否已发生需看 Pod 状态",
		},
		RemediationDirection: "先判节点是否真的失联：能登录就查 kubelet/运行时日志并恢复服务，恢复后用 kubectl uncordon 放回调度；" +
			"确认硬件或系统层不可恢复时，走排水+替换节点流程。删除 Node 对象不会修复底层故障，只会让记录消失",
		RemediationText: "恢复节点上的 kubelet 与运行时后，节点自动回到 Ready，其上的 Pod 由控制器补齐；" +
			"对无法恢复的节点，先确认工作负载在其他节点已有足够副本，再执行 drain/cordon。",
		Verification: []model.Command{
			ver("kubectl get node " + name + " -o wide"),
			ver("kubectl get events -A --field-selector involvedObject.name=" + name + " -w"),
		},
	}

	reason, detail := "", ""
	if n != nil {
		if cond, ok := condOf(n.Conditions, "Ready"); ok {
			reason, detail = cond.Reason, cond.Message
		}
	}
	_, heartbeatLost := c.EventWith(f.Resource, "NodeStatusUnknown", "Kubelet stopped posting node status", "Rebooted")
	if reason != "" || detail != "" {
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "Node.status.conditions[Ready] 由控制面写入，Reason/Message 直接给出不可用原因"
		r.RootCause = fmt.Sprintf("节点 %s NotReady（Ready=False，reason=%s，message=%s）", name, orDash(reason), clip(detail, 180))
	} else {
		r.Status = model.RootCauseProbable
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "Node Ready 条件为 False，但条件里没有 reason/message，具体故障层未能从只读数据判定"
		r.RootCause = fmt.Sprintf("节点 %s NotReady（Ready=False，未提供 reason/message）", name)
	}
	if heartbeatLost {
		r.Confidence = ConfObjectAndEvent
		r.ConfidenceBasis = "Node Ready=False 与节点状态失联事件（如 Kubelet stopped posting node status）互相印证"
		r.EvidenceRefs = dedupe(append(r.EvidenceRefs, evOfKind(f, model.EvEvent)...))
	}

	// 影响面：该节点上的 Pod 与它们归属的工作负载（真实 spec.nodeName 关系）。
	pods := c.E.index().PodsOfNode(name)
	r.Impact = fmt.Sprintf("该节点上运行 %d 个 Pod，NotReady 期间这些副本不可用且不会被自动迁走", len(pods))
	chain := []string{"Node/" + name + "（NotReady）"}
	wlSet := map[string]*model.WorkloadInfo{}
	for _, p := range pods {
		if !podReady(p) {
			chain = append(chain, resLabel(p.Ref))
		}
		for _, w := range c.E.index().OwnerChain(p) {
			wlSet[w.Ref.Key()] = w
		}
	}
	if len(wlSet) > 0 {
		var refs []model.ResourceRef
		for _, w := range wlSet {
			refs = append(refs, w.Ref)
		}
		r.Impact += fmt.Sprintf("；受影响工作负载 %s", refList(dedupeRefs(refs), 6))
	}
	if len(pods) > 0 {
		chain = append(chain, "该节点上 "+fmt.Sprint(len(pods))+" 个 Pod 失去运行环境")
	}
	r.ImpactChain = dedupe(chain)

	// 因果边：节点异常 → 同故障链里由该节点承载的 Pod 症状（关系来自 spec.nodeName）。
	for _, it := range c.Items {
		if it.ID == f.ID || it.Resource.Kind != "Pod" {
			continue
		}
		p := c.PodOf(*it)
		if p == nil || !strings.EqualFold(p.NodeName, name) {
			continue
		}
		if edge, ok := c.CausalEdge(f, it, "节点 NotReady，其上 Pod 失去运行环境/心跳上报中断"); ok {
			edge.Confidence = model.RootCauseConfirmed
			r.CausalEdges = append(r.CausalEdges, edge)
		}
	}
	r.Remediation = []model.Command{
		rem("kubectl uncordon "+name+"  # 节点恢复后放回调度", model.RiskMedium),
	}
	return r
}

// nodePressureDiagnoser 处理节点磁盘/内存/PID 压力（驱逐与调度受限的共同上游原因）。
type nodePressureDiagnoser struct{}

func (nodePressureDiagnoser) ID() string { return "NODE_PRESSURE" }

func (nodePressureDiagnoser) Scenario() string { return "节点磁盘/内存/PID 压力" }

func (nodePressureDiagnoser) Match(c *Case) bool {
	return c.HasRule("NodeDiskPressure") || c.HasRule("NodeMemoryPressure") || c.HasRule("NodePIDPressure")
}

func (nodePressureDiagnoser) Diagnose(c *Case) Result {
	var (
		cond   model.ConditionInfo
		f      *model.Finding
		kindOf string
	)
	for _, rule := range []struct{ rule, label string }{
		{"NodeDiskPressure", "DiskPressure"},
		{"NodeMemoryPressure", "MemoryPressure"},
		{"NodePIDPressure", "PIDPressure"},
	} {
		if got := c.ByRule(rule.rule); got != nil && f == nil {
			f = got
			kindOf = rule.label
			if n := c.Node(got.Resource.Name); n != nil {
				if condTrue, ok := condTrue(n.Conditions, rule.label); ok {
					cond = condTrue
				}
			}
		}
	}
	if f == nil {
		return Result{}
	}
	name := f.Resource.Name
	pods := c.E.index().PodsOfNode(name)
	evicted := 0
	for _, p := range pods {
		if strings.EqualFold(p.Reason, "Evicted") || strings.EqualFold(p.Phase, "Failed") {
			evicted++
		}
	}

	r := Result{
		AnchorFinding:   f.ID,
		Status:          model.RootCauseConfirmed,
		Confidence:      ConfObjectStatus,
		ConfidenceBasis: fmt.Sprintf("Node.status.conditions[%s]=True 由 kubelet 依据真实水位写入", kindOf),
		RootCause: fmt.Sprintf("节点 %s 触发 %s（reason=%s，message=%s）", name, kindOf,
			orDash(cond.Reason), clip(cond.Message, 180)),
		Mechanism:    "节点资源水位越过 kubelet 驱逐阈值，kubelet 开始驱逐 Pod 回收资源，同时调度器不再往该节点放新 Pod",
		Impact:       fmt.Sprintf("该节点共 %d 个 Pod，其中 %d 个已处于被驱逐/失败状态；容量实际下降", len(pods), evicted),
		ImpactChain:  append([]string{"Node/" + name + "（" + kindOf + "）"}, podNameList(pods, 6)...),
		EvidenceRefs: evIDs(f),
		Investigation: []model.Command{
			inv("kubectl describe node " + name),
			inv("kubectl top node " + name),
			inv("kubectl get pods -A --field-selector spec.nodeName=" + name + " -o wide"),
			inv("节点侧确认占用来源：df -h /var/lib/containerd && du -sh /var/lib/containerd/* ；或 crictl rmi --prune 前先评估"),
		},
		AlternativeCauses: []string{"镜像与容器可写层堆积占满磁盘", "日志未轮转（容器 stdout 文件或节点日志目录）", "内存 requests 超卖导致节点整体内存吃紧", "已泄漏的僵尸进程占满 PID"},
		EvidenceGaps:      []string{"节点磁盘/内存实际水位与占用明细在节点本地，只读体检取不到"},
		RemediationDirection: "先降低该节点压力再谈调度：磁盘类清镜像/未轮转日志并调整 imageGCHighThresholdPercent，" +
			"内存类核对该节点 Pod 的 requests 总和是否超卖、必要时扩容节点；不要靠反复删除被驱逐的 Pod 来「恢复」",
		RemediationText: "压力解除后 kubelet 自动恢复调度；被驱逐的 Pod 由控制器在健康节点上重建。清理镜像或日志属于节点变更，需按运维窗口执行。",
		Verification: []model.Command{
			ver("kubectl describe node " + name + " | grep -A5 Conditions"),
			ver("kubectl get events -A --field-selector involvedObject.name=" + name),
		},
	}
	// 因果边：压力节点 → 该节点上被驱逐/异常的 Pod 症状。
	for _, it := range c.Items {
		if it.ID == f.ID || it.Resource.Kind != "Pod" {
			continue
		}
		p := c.PodOf(*it)
		if p == nil || !strings.EqualFold(p.NodeName, name) {
			continue
		}
		if edge, ok := c.CausalEdge(f, it, "节点 "+kindOf+" 触发 kubelet 驱逐/资源挤占，该 Pod 受影响"); ok {
			edge.Confidence = model.RootCauseConfirmed
			r.CausalEdges = append(r.CausalEdges, edge)
		}
	}
	return r
}

func podNameList(pods []*model.PodInfo, max int) []string {
	var out []string
	for i, p := range pods {
		if max > 0 && i >= max {
			out = append(out, fmt.Sprintf("…（共 %d 个 Pod）", len(pods)))
			break
		}
		out = append(out, resLabel(p.Ref)+"（"+orDash(p.Phase)+"）")
	}
	return out
}

func dedupeRefs(refs []model.ResourceRef) []model.ResourceRef {
	seen := map[string]bool{}
	var out []model.ResourceRef
	for _, r := range refs {
		if seen[r.Key()] {
			continue
		}
		seen[r.Key()] = true
		out = append(out, r)
	}
	return out
}
