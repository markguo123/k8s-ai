package deterministic

import (
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func init() {
	Register(pvcPendingDiagnoser{})
	Register(pvStateDiagnoser{})
	Register(storageClassDiagnoser{})
	Register(volumeAttachmentDiagnoser{})
}

// noProvisioner 是「显式声明不做动态供应」的 provisioner 名，与规则层口径一致。
const noProvisioner = "kubernetes.io/no-provisioner"

// scByName 按名字查 StorageClass。快照里的 StorageClass 来自全量 list，
// 因此「查不到」本身是一条可核验的事实，而不是猜测。
func (c *Case) scByName(name string) *model.StorageInfo {
	if c.E.Snapshot == nil || name == "" {
		return nil
	}
	for i := range c.E.Snapshot.Storage {
		st := &c.E.Snapshot.Storage[i]
		if st.Kind == "StorageClass" && st.Ref.Name == name {
			return st
		}
	}
	return nil
}

// pvcByPVName 反查引用该 PV 的 PVC（PVC.spec.volumeName 是真实引用字段）。
func (c *Case) pvcByPVName(pvName string) []*model.StorageInfo {
	if c.E.Snapshot == nil || pvName == "" {
		return nil
	}
	var out []*model.StorageInfo
	for i := range c.E.Snapshot.Storage {
		st := &c.E.Snapshot.Storage[i]
		if st.Kind == "PVC" && st.VolumeName == pvName {
			out = append(out, st)
		}
	}
	return out
}

// pendingPVCsOnSC 返回引用该 StorageClass 且尚未绑定的 PVC（动态供应的消费方）。
func (c *Case) pendingPVCsOnSC(scName string) []*model.StorageInfo {
	if c.E.Snapshot == nil || scName == "" {
		return nil
	}
	var out []*model.StorageInfo
	for i := range c.E.Snapshot.Storage {
		st := &c.E.Snapshot.Storage[i]
		if st.Kind == "PVC" && st.StorageClassName == scName && st.Status != "Bound" {
			out = append(out, st)
		}
	}
	return out
}

func hasProvisioner(sc *model.StorageInfo) bool {
	return sc != nil && sc.Driver != "" && sc.Driver != noProvisioner
}

func storageDriverText(sc *model.StorageInfo) string {
	switch {
	case sc == nil:
		return "（快照中不存在）"
	case sc.Driver == "":
		return "未配置 provisioner"
	case sc.Driver == noProvisioner:
		return noProvisioner + "（静态供应，不会自动建卷）"
	default:
		return sc.Driver
	}
}

func storageStatusOf(st *model.StorageInfo) string {
	if st == nil {
		return ""
	}
	return st.Status
}

func storageField(st *model.StorageInfo, get func(*model.StorageInfo) string) string {
	if st == nil {
		return ""
	}
	return get(st)
}

func reasonText(st *model.StorageInfo) string {
	if st == nil || st.Reason == "" {
		return ""
	}
	return "（reason=" + clip(st.Reason, 120) + "）"
}

// hasPVCRef 判断 Pod 是否真的引用了该 PVC。
func hasPVCRef(p *model.PodInfo, pvc model.ResourceRef) bool {
	for _, ref := range p.PVCRefs {
		if ref.Key() == pvc.Key() {
			return true
		}
	}
	return false
}

// pvcPendingDiagnoser 处理 PVC 一直 Pending / Lost。
type pvcPendingDiagnoser struct{}

func (pvcPendingDiagnoser) ID() string { return "STORAGE_PVC_PENDING" }

func (pvcPendingDiagnoser) Scenario() string { return "PVC 长期 Pending 或 Lost" }

func (pvcPendingDiagnoser) Match(c *Case) bool { return c.HasRule("PVCPending") }

func (pvcPendingDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("PVCPending")
	pvc := c.Storage(f.Resource)
	if pvc == nil {
		return Result{
			AnchorFinding:   f.ID,
			Status:          model.RootCauseUnknown,
			Confidence:      ConfUnknown,
			ConfidenceBasis: "规则命中过该 PVC，但它不在本次快照的存储清单里（采集环节出问题），不能凭空判因",
			RootCause:       fmt.Sprintf("PVC %s 上报异常，但对象本身未采集到", f.Resource.Name),
			EvidenceGaps:    []string{"PVC 对象未成功采集，需先确认该资源的 list 调用是否失败"},
		}
	}
	pods := c.E.index().PodsOfPVC(f.Resource.Key())
	r := Result{
		AnchorFinding: f.ID,
		EvidenceRefs:  evIDs(f),
		Investigation: []model.Command{
			inv(fmt.Sprintf("kubectl %sdescribe pvc %s", nsArg(f.Resource.Namespace), f.Resource.Name)),
			inv(fmt.Sprintf("kubectl %sget pvc %s -o jsonpath='{.spec.storageClassName}/{.spec.volumeName}'",
				nsArg(f.Resource.Namespace), f.Resource.Name)),
		},
		Verification: []model.Command{
			ver(fmt.Sprintf("kubectl %sget pvc %s", nsArg(f.Resource.Namespace), f.Resource.Name)),
		},
		AlternativeCauses: []string{"CSI provisioner 未运行或未完成注册", "存储后端配额或凭据不足，创建请求被拒绝", "绑定模式为 WaitForFirstConsumer，Pod 本身调度不出去"},
	}
	if ev, ok := c.EventWith(f.Resource, "provisioning", "storageclass", "forbidden", "ExternalProvisioning", "failed to provision"); ok {
		r.EvidenceRefs = dedupe(append(r.EvidenceRefs, evOfKind(f, model.EvEvent)...))
		r.AlternativeCauses = append(r.AlternativeCauses, "事件中已出现 "+clip(ev.Reason+"/"+ev.Message, 120)+"，优先按该消息排查")
	}

	sc := c.scByName(pvc.StorageClassName)
	scFinding := c.FindingOn(model.ResourceRef{Kind: "StorageClass", Name: pvc.StorageClassName}, "StorageClassNoProvisioner")

	switch {
	case pvc.Status == "Lost":
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "PVC status.phase=Lost 是对象状态字段；Lost 表示其绑定的 PV 已不可用"
		r.RootCause = fmt.Sprintf("PVC %s 进入 Lost：它绑定的 PV %s 已被删除或不可用", pvc.Ref.Name, orDash(pvc.VolumeName))
		r.Mechanism = "PV 被删除（或回收策略把卷删掉）后控制面把 PVC 标记为 Lost；重建卷不会自动发生，Pod 会一直卡在等待绑定或挂载"
		r.EvidenceGaps = append(r.EvidenceGaps, "未采集 PV 的删除事件与变更记录，无法确认 PV 是被谁、何时移除的")
	case pvc.StorageClassName != "" && sc == nil:
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "PVC.spec.storageClassName 是真实引用字段，且本次快照已全量采集 StorageClass 清单，其中没有该名称"
		r.RootCause = fmt.Sprintf("PVC %s 的 spec.storageClassName=%s，但集群里不存在这个 StorageClass", pvc.Ref.Name, pvc.StorageClassName)
		r.Mechanism = "没有对应的 StorageClass，供应控制器不知道该用哪个驱动建卷，PVC 只能停在 Pending"
		r.EvidenceGaps = append(r.EvidenceGaps, "未确认是 StorageClass 被删、改名，还是 PVC 写错了名称，需要查变更记录")
	case scFinding != nil && !hasProvisioner(sc):
		// 引用到的 StorageClass 自己也有 finding：把它自己的证据并进来，才够格判 confirmed。
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "同一故障链里 StorageClass " + pvc.StorageClassName + " 的 provisioner 证据（对象字段）与 PVC Pending 互相印证"
		r.RootCause = fmt.Sprintf("PVC %s 引用的 StorageClass %s %s，动态供应不会产生 PV",
			pvc.Ref.Name, pvc.StorageClassName, storageDriverText(sc))
		r.Mechanism = "没有 provisioner 时 PVC 只能靠人工预置的 PV 绑定；没人建 PV，它就永远 Pending"
		r.EvidenceRefs = dedupe(append(r.EvidenceRefs, evIDs(scFinding)...))
		r.EvidenceGaps = append(r.EvidenceGaps, "未采集该 StorageClass 的 volumeBindingMode，无法区分延迟绑定与立即绑定对结果的影响")
	case pvc.VolumeName != "":
		pv := c.Storage(model.ResourceRef{Kind: "PV", Name: pvc.VolumeName})
		r.Status = model.RootCauseProbable
		r.Confidence = ConfInferred
		r.ConfidenceBasis = "PVC.spec.volumeName 指向具体 PV 但仍未 Bound，只能按容量/访问模式/节点亲和不匹配推断"
		r.RootCause = fmt.Sprintf("PVC %s 指定绑定 PV %s（该 PV 当前 %s），两者没有完成绑定",
			pvc.Ref.Name, pvc.VolumeName, orDash(storageStatusOf(pv)))
		r.Mechanism = "静态绑定要求容量、accessModes、节点亲和三项都吻合；任一项不符，PV 与 PVC 就各自停在 Available/Pending"
		r.EvidenceGaps = append(r.EvidenceGaps, "未采集 PV 的容量/accessModes/nodeAffinity 明细，无法指出具体差在哪一项")
	default:
		r.Status = model.RootCauseProbable
		r.Confidence = ConfInferred
		r.ConfidenceBasis = "PVC 处于 Pending 且引用的 StorageClass 已配置 provisioner，缺少供应侧日志与事件，只能判定到供应环节"
		r.RootCause = fmt.Sprintf("PVC %s 引用的 StorageClass %s（%s）已配置，但没有产出可绑定的 PV",
			pvc.Ref.Name, orDash(pvc.StorageClassName), storageDriverText(sc))
		r.Mechanism = "动态供应链路（external-provisioner → CSI driver → 存储后端）中有一环没完成建卷，PVC 因此停在 Pending"
		r.EvidenceGaps = append(r.EvidenceGaps, "未采集 CSI provisioner/attacher 组件日志与事件，无法定位是驱动、后端还是权限环节失败")
	}

	if len(evOfKind(f, model.EvEvent)) > 0 && r.Status == model.RootCauseConfirmed {
		r.Confidence = ConfObjectAndEvent
		r.ConfidenceBasis += "；同时有该 PVC 的事件佐证"
	}

	r.Impact = fmt.Sprintf("使用该卷的 Pod %d 个", len(pods))
	if len(pods) > 0 {
		r.Impact += "（" + refList(podRefs(pods), 5) + "），它们会卡在等待卷绑定或挂载阶段，无法进入 Running"
	} else {
		r.Impact += "；本次快照里没有 Pod 引用该 PVC，业务影响暂未显现"
	}
	chain := []string{resLabel(pvc.Ref) + "（" + orDash(pvc.Status) + "）"}
	if pvc.VolumeName != "" {
		chain = append(chain, "PV/"+pvc.VolumeName)
	}
	if pvc.StorageClassName != "" {
		chain = append(chain, "StorageClass/"+pvc.StorageClassName)
	}
	for _, p := range pods {
		chain = append(chain, resLabel(p.Ref))
	}
	r.ImpactChain = dedupe(chain)

	// 因果边：PVC 异常 → 真正引用它的 Pod（关系出处 spec.volumes[].persistentVolumeClaim）。
	for _, it := range c.Items {
		if it.ID == f.ID || it.Resource.Kind != "Pod" {
			continue
		}
		p := c.PodOf(*it)
		if p == nil || !hasPVCRef(p, pvc.Ref) {
			continue
		}
		if edge, ok := c.CausalEdge(f, it, "PVC 未完成绑定或已丢失，Pod 的卷挂载条件无法满足"); ok {
			edge.Confidence = model.RootCauseConfirmed
			r.CausalEdges = append(r.CausalEdges, edge)
		}
	}
	r.RemediationDirection = "先确认这个卷该不该自动供应：该有动态供应就去修 CSI 组件与 StorageClass，走静态供应就补上匹配的 PV 或改正 volumeName"
	r.RemediationText = "按核对结果三选一：修正 PVC 的 spec.storageClassName；为静态供应场景预置匹配的 PV；或恢复缺失的 CSI provisioner。本次体检只读，修复动作请由变更流程执行。"
	r.Remediation = []model.Command{
		rem(fmt.Sprintf("kubectl %sedit pvc %s  # 修正 spec.storageClassName 或 spec.volumeName",
			nsArg(f.Resource.Namespace), f.Resource.Name), model.RiskHigh),
	}
	return r
}

// pvStateDiagnoser 处理 PV Failed / Released。
type pvStateDiagnoser struct{}

func (pvStateDiagnoser) ID() string { return "STORAGE_PV_UNAVAILABLE" }

func (pvStateDiagnoser) Scenario() string { return "PV 处于 Failed 或 Released" }

func (pvStateDiagnoser) Match(c *Case) bool { return c.HasRule("PVFailed") }

func (pvStateDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("PVFailed")
	pv := c.Storage(f.Resource)
	claimers := c.pvcByPVName(f.Resource.Name)
	r := Result{
		AnchorFinding: f.ID,
		EvidenceRefs:  evIDs(f),
		Investigation: []model.Command{
			inv("kubectl describe pv " + f.Resource.Name),
			inv("kubectl get pv " + f.Resource.Name + " -o jsonpath='{.status.phase}/{.spec.persistentVolumeReclaimPolicy}'"),
		},
		Verification: []model.Command{ver("kubectl get pv " + f.Resource.Name)},
	}
	switch {
	case pv != nil && pv.Status == "Failed":
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "PV status.phase=Failed 是对象状态字段直接写明的结论" + reasonText(pv)
		r.RootCause = "PV " + pv.Ref.Name + " 处于 Failed" + reasonText(pv) + "，该卷已不可再被绑定使用"
		r.Mechanism = "回收（reclaim）或挂载阶段出错时控制面把 PV 标为 Failed；Failed 的 PV 不参与绑定，引用它的 PVC 会退回或停在 Pending"
		if pv.Reason == "" {
			r.EvidenceGaps = append(r.EvidenceGaps, "PV status 未给出 reason/message，无法区分是后端卷被删还是回收流程报错")
		} else {
			r.EvidenceGaps = append(r.EvidenceGaps, "未采集 CSI controller 日志，无法确认后端卷的实际状态")
		}
		r.RemediationDirection = "先确认底层卷是否还在：还在就修正 PV 定义重新可用，已被删除则清理 PV 并重建 PVC/PV，切勿直接删 PV 假装问题消失"
	case pv != nil && pv.Status == "Released":
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "PV status.phase=Released 是对象状态字段；Released 只出现在原 PVC 已删除且回收策略为 Retain 时"
		r.RootCause = "PV " + pv.Ref.Name + " 已 Released：原来的 PVC 已删除，Retain 策略把卷保留下来等待人工处理"
		r.Mechanism = "Retain 策略不会自动清理卷，PV 停在 Released；只要没人清掉 spec.claimRef，新 PVC 就无法复用它"
		r.EvidenceGaps = append(r.EvidenceGaps, "未采集 persistentVolumeReclaimPolicy，无法从快照确认回收策略")
		r.AlternativeCauses = []string{"这是预期的数据保留状态，业务上本来就要求保留旧卷数据"}
		r.RemediationDirection = "确认这批数据是否还要：要留就按保留处理；不再需要则清掉 PV 的 spec.claimRef 让它重新 Available，或直接删除 PV 释放卷"
	default:
		r.Status = model.RootCauseProbable
		r.Confidence = ConfInferred
		r.ConfidenceBasis = "规则按 status.phase 命中，但 PV 对象明细未取回，只能给出状态层面的结论"
		r.RootCause = fmt.Sprintf("PV %s 处于非 Available 状态（%s）", f.Resource.Name, f.Summary)
		r.Mechanism = "非 Available 的 PV 不参与新绑定，引用它的负载拿不到卷"
		r.EvidenceGaps = append(r.EvidenceGaps, "PV 对象明细未采集成功，需先排除采集失败再判定")
	}
	r.AlternativeCauses = append(r.AlternativeCauses, "存储后端侧卷已被回收，Kubernetes 侧只看到残留对象")

	r.Impact = fmt.Sprintf("当前按 volumeName 引用该 PV 的 PVC %d 个", len(claimers))
	if len(claimers) > 0 {
		var refs []model.ResourceRef
		for _, st := range claimers {
			refs = append(refs, st.Ref)
		}
		r.Impact += "（" + refList(refs, 5) + "）"
	}
	chain := []string{"PV/" + f.Resource.Name + "（" + orDash(storageStatusOf(pv)) + "）"}
	for _, st := range claimers {
		chain = append(chain, resLabel(st.Ref)+"（"+orDash(st.Status)+"）")
		for _, p := range c.E.index().PodsOfPVC(st.Ref.Key()) {
			chain = append(chain, resLabel(p.Ref))
		}
	}
	r.ImpactChain = dedupe(chain)

	// 因果边：PV 异常 → 通过 spec.volumeName 指定该卷的 PVC。
	for _, it := range c.Items {
		if it.ID == f.ID || it.Resource.Kind != "PVC" {
			continue
		}
		st := c.Storage(it.Resource)
		if st == nil || st.VolumeName != f.Resource.Name {
			continue
		}
		if edge, ok := c.CausalEdge(f, it, "PV 不可用，按 volumeName 指定该卷的 PVC 无法完成绑定"); ok {
			edge.Confidence = model.RootCauseConfirmed
			r.CausalEdges = append(r.CausalEdges, edge)
		}
	}
	r.RemediationText = "确认底层卷与数据去留后再操作：可复用就清理 PV 的 spec.claimRef，需重建则按变更流程删除 PV 并重建 PVC。本次体检只读，不会代执行。"
	r.Remediation = []model.Command{
		rem("kubectl patch pv "+f.Resource.Name+" -p '{\"spec\":{\"claimRef\":null}}'  # 让 Released 的 PV 重新可绑定", model.RiskHigh),
	}
	return r
}

// storageClassDiagnoser 处理 StorageClass 没有可用 provisioner。
type storageClassDiagnoser struct{}

func (storageClassDiagnoser) ID() string { return "STORAGE_CLASS_INVALID" }

func (storageClassDiagnoser) Scenario() string { return "StorageClass 无可用 provisioner" }

func (storageClassDiagnoser) Match(c *Case) bool { return c.HasRule("StorageClassNoProvisioner") }

func (storageClassDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("StorageClassNoProvisioner")
	sc := c.Storage(f.Resource)
	waiting := c.pendingPVCsOnSC(f.Resource.Name)
	r := Result{
		AnchorFinding:   f.ID,
		Status:          model.RootCauseConfirmed,
		Confidence:      ConfObjectStatus,
		ConfidenceBasis: "StorageClass.provisioner 是对象字段，本次快照已全量采集 StorageClass，字段缺失或为 no-provisioner 即事实",
		RootCause: fmt.Sprintf("StorageClass %s %s，无法为 PVC 动态创建 PV",
			f.Resource.Name, storageDriverText(sc)),
		Mechanism:    "动态供应完全靠 provisioner 决定由哪个控制器建卷；该字段为空或声明为 no-provisioner 时，没有任何组件会响应 PVC",
		Impact:       fmt.Sprintf("引用该 StorageClass 且尚未绑定的 PVC %d 个", len(waiting)),
		EvidenceRefs: evIDs(f),
		EvidenceGaps: []string{
			"未确认这是有意为之的静态供应方案，还是 provisioner 被漏配或组件未安装",
			"未采集 volumeBindingMode，无法判断延迟绑定是否也在影响绑定结果",
		},
		AlternativeCauses: []string{"该 StorageClass 本就是给静态 PV 预留的占位定义", "provisioner 由外部组件动态注册，当前正处于安装或升级窗口"},
		Investigation: []model.Command{
			inv("kubectl get sc " + f.Resource.Name + " -o yaml"),
			inv("kubectl get pvc -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,SC:.spec.storageClassName,PHASE:.status.phase"),
		},
		Verification:         []model.Command{ver("kubectl get sc " + f.Resource.Name)},
		RemediationDirection: "二选一：给这个场景配上真实 provisioner 并装好对应 CSI 组件；或者明确它只做静态供应，让业务改用带 volumeName 的静态绑定方式",
		RemediationText:      "StorageClass 的 provisioner 字段不可原地修改，需要新建一个配置正确的 StorageClass 并把业务切过去；本次体检只读，变更请走发布流程。",
		Remediation: []model.Command{
			rem("kubectl apply -f storageclass-fix.yaml  # 新建带正确 provisioner 的 StorageClass", model.RiskMedium),
		},
	}
	if len(waiting) > 0 {
		var refs []model.ResourceRef
		chain := []string{"StorageClass/" + f.Resource.Name + "（" + storageDriverText(sc) + "）"}
		for _, st := range waiting {
			refs = append(refs, st.Ref)
			chain = append(chain, resLabel(st.Ref)+"（"+orDash(st.Status)+"）")
		}
		r.Impact += "（" + refList(refs, 5) + "）会一直停在 Pending"
		r.ImpactChain = dedupe(chain)
	}
	// 因果边：StorageClass 无供应器 → 因它而 Pending 的 PVC（关系出处 spec.storageClassName）。
	for _, it := range c.Items {
		if it.ID == f.ID || it.Rule != "PVCPending" {
			continue
		}
		st := c.Storage(it.Resource)
		if st == nil || st.StorageClassName != f.Resource.Name {
			continue
		}
		if edge, ok := c.CausalEdge(f, it, "StorageClass 没有可用 provisioner，引用它的 PVC 永远不会被供应"); ok {
			edge.Confidence = model.RootCauseConfirmed
			r.CausalEdges = append(r.CausalEdges, edge)
		}
	}
	return r
}

// volumeAttachmentDiagnoser 处理 VolumeAttachment 长时间未 Attached。
type volumeAttachmentDiagnoser struct{}

func (volumeAttachmentDiagnoser) ID() string { return "STORAGE_VOLUME_ATTACHMENT_STUCK" }

func (volumeAttachmentDiagnoser) Scenario() string { return "VolumeAttachment 卡住未 Attached" }

func (volumeAttachmentDiagnoser) Match(c *Case) bool { return c.HasRule("VolumeAttachmentStuck") }

func (volumeAttachmentDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("VolumeAttachmentStuck")
	va := c.Storage(f.Resource)
	driver := orDash(storageField(va, func(s *model.StorageInfo) string { return s.Driver }))
	r := Result{
		AnchorFinding: f.ID,
		EvidenceRefs:  evIDs(f),
		Investigation: []model.Command{
			inv("kubectl describe volumeattachment " + f.Resource.Name),
			inv("kubectl get volumeattachment " + f.Resource.Name + " -o jsonpath='{.spec.attacher}/{.status.attachError.message}'"),
		},
		Verification:      []model.Command{ver("kubectl get volumeattachment " + f.Resource.Name)},
		AlternativeCauses: []string{"该 driver 的 node-plugin 未在目标节点运行或未完成注册", "存储后端 API 限流或凭据失效", "attachdetach 控制器积压，其他卷的挂载请求排在前面"},
	}
	if va != nil && va.Reason != "" {
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "VolumeAttachment status.attachError.message 是对象字段直接写明的失败原因"
		r.RootCause = fmt.Sprintf("VolumeAttachment %s 附加失败（driver=%s）：%s", f.Resource.Name, driver, clip(va.Reason, 200))
		r.Mechanism = "attachdetach 控制器把附加请求交给 attacher（driver=" + driver + "），attacher 返回错误后写入 status.attachError，Pod 会一直停在 ContainerCreating 等卷挂载"
		r.EvidenceGaps = append(r.EvidenceGaps, "未采集 CSI 控制面（csi-attacher 与 driver controller）日志，无法确认后端返回的具体错误")
	} else {
		r.Status = model.RootCauseProbable
		r.Confidence = ConfInferred
		r.ConfidenceBasis = "该 VolumeAttachment 创建后超过判定阈值仍未 Attached 且没有 attachError，只能判定为附加流程停滞"
		r.RootCause = fmt.Sprintf("VolumeAttachment %s 长时间未 Attached（driver=%s），且没有产生 attachError", f.Resource.Name, driver)
		r.Mechanism = "没有报错但长期不 Attached，通常是 attacher 侧根本没接到或没处理请求：驱动未注册、控制面未运行或节点侧插件缺失"
		r.EvidenceGaps = append(r.EvidenceGaps,
			"未采集 CSI 组件（node-plugin 与 csi-attacher）日志与事件",
			"VolumeAttachment 快照未含目标节点名，无法直接关联到具体节点上的驱动进程")
	}
	r.Impact = "对应的卷无法附加到节点，使用该卷的 Pod 会长期停在 ContainerCreating（MountVolume.SetUp failed 或 Waiting for volume to be attached）"
	r.ImpactChain = []string{"VolumeAttachment/" + f.Resource.Name + "（" + orDash(storageStatusOf(va)) + "，driver=" + driver + "）", "对应 PV 无法附加到节点", "使用该卷的 Pod 停在 ContainerCreating"}
	r.RemediationDirection = "先确认该 driver 的控制面与节点插件是否都在运行且注册成功，再看存储后端是否有凭据或限流问题；不要靠反复删除 VolumeAttachment 强行重试来掩盖驱动异常"
	r.RemediationText = "恢复对应 CSI 组件（重启缺失的 driver Pod、重新注册驱动、修正后端凭据）后，等待控制器自动重放附加请求。本次体检只读，不会代执行。"
	r.Remediation = []model.Command{
		rem("kubectl -n <csi-namespace> rollout restart deploy/<csi-controller>  # 恢复 CSI 控制面", model.RiskMedium),
	}
	return r
}
