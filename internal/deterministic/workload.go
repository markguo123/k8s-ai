package deterministic

import (
	"fmt"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func init() {
	Register(replicasDiagnoser{})
	Register(daemonSetDiagnoser{})
	Register(jobFailedDiagnoser{})
	Register(hpaDiagnoser{})
	Register(pdbDiagnoser{})
}

// topWorkload 沿真实 ownerReferences 找最上层控制器（Rollout 类命令要打在它身上）。
func topWorkload(c *Case, p *model.PodInfo) *model.WorkloadInfo {
	if p == nil {
		return nil
	}
	chain := c.E.index().OwnerChain(p)
	if len(chain) == 0 {
		return nil
	}
	for _, w := range chain {
		switch w.Ref.Kind {
		case "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob":
			return w
		}
	}
	return chain[len(chain)-1]
}

// podFault 是工作负载下属 Pod 的故障归类。
type podFault struct {
	crashing []*model.PodInfo // 崩溃/反复重启
	pending  []*model.PodInfo // 未调度或等待资源
	notReady []*model.PodInfo // 在跑但没就绪
	dead     []*model.PodInfo // Failed/Evicted
	healthy  []*model.PodInfo
}

func (pf podFault) total() int {
	return len(pf.crashing) + len(pf.pending) + len(pf.notReady) + len(pf.dead) + len(pf.healthy)
}

// classifyPods 按真实容器状态归类（判断依据是 status 字段，不是副本数字）。
func classifyPods(c *Case, pods []*model.PodInfo) podFault {
	var pf podFault
	for _, p := range pods {
		switch {
		case strings.EqualFold(p.Phase, "Failed") || strings.EqualFold(p.Reason, "Evicted"):
			pf.dead = append(pf.dead, p)
		case strings.EqualFold(p.Phase, "Pending"):
			pf.pending = append(pf.pending, p)
		case podReady(p):
			pf.healthy = append(pf.healthy, p)
		default:
			crash := false
			for _, ctr := range containersOf(p) {
				if strings.EqualFold(ctr.State, "waiting") && matchAny(ctr.Reason, "CrashLoopBackOff", "CreateContainerError", "RunContainerError", "Error") {
					crash = true
					break
				}
				if strings.EqualFold(ctr.State, "terminated") && ctr.ExitCode != 0 {
					crash = true
					break
				}
			}
			if crash {
				pf.crashing = append(pf.crashing, p)
			} else {
				pf.notReady = append(pf.notReady, p)
			}
		}
	}
	return pf
}

// replicasDiagnoser 处理 Deployment/StatefulSet 副本不足：不停在「副本数不够」，
// 而是继续沿 ownerReferences 查清是哪些 Pod、以什么方式没就绪。
type replicasDiagnoser struct{}

func (replicasDiagnoser) ID() string { return "WORKLOAD_REPLICAS_UNAVAILABLE" }

func (replicasDiagnoser) Scenario() string {
	return "Deployment/StatefulSet 副本不足或滚动更新卡住"
}

func (replicasDiagnoser) Match(c *Case) bool {
	return c.HasRule("DeploymentReplica") || c.HasRule("StatefulSetReplica") || c.HasRule("DeploymentProgressDeadline")
}

func (replicasDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("DeploymentReplica")
	if f == nil {
		f = c.ByRule("StatefulSetReplica")
	}
	if f == nil {
		f = c.ByRule("DeploymentProgressDeadline")
	}
	wl := c.Workload(f.Resource)
	pods := c.E.index().PodsOfWorkload(f.Resource.Key())
	pf := classifyPods(c, pods)
	target := strings.ToLower(f.Resource.Kind) + "/" + f.Resource.Name

	r := Result{
		AnchorFinding: f.ID,
		EvidenceRefs:  evIDs(f),
		Impact: fmt.Sprintf("%s %s，Pod 侧就绪 %d 个（其中崩溃 %d / 未调度 %d / 未就绪 %d / 已失败 %d）",
			resLabel(f.Resource), replicasText(desiredOf(wl), readyOf(wl)),
			len(pf.healthy), len(pf.crashing), len(pf.pending), len(pf.notReady), len(pf.dead)),
		Investigation: []model.Command{
			inv(fmt.Sprintf("kubectl %sdescribe %s %s", nsArg(f.Resource.Namespace), target, f.Resource.Name)),
			inv(fmt.Sprintf("kubectl %sget pods -l <该工作负载的 selector> -o wide", nsArg(f.Resource.Namespace))),
			inv(fmt.Sprintf("kubectl %sget events --field-selector involvedObject.name=%s --sort-by=.lastTimestamp", nsArg(f.Resource.Namespace), f.Resource.Name)),
		},
		Verification: []model.Command{
			ver(fmt.Sprintf("kubectl %srollout status %s", nsArg(f.Resource.Namespace), target)),
		},
	}
	if wl != nil {
		r.EvidenceRefs = dedupe(r.EvidenceRefs)
	}

	switch {
	case len(pf.crashing) > 0:
		p := pf.crashing[0]
		r.Status = model.RootCauseProbable
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "工作负载副本字段 + 下属 Pod 的真实容器状态（ownerReferences 关系）；崩溃本身的根因见 Pod 侧结论"
		r.RootCause = fmt.Sprintf("%s 副本不足的直接原因是其下属 Pod 反复崩溃（如 %s，共 %d 个 Pod 崩溃）；请按 Pod 侧崩溃原因处理，副本数本身不是根因",
			resLabel(f.Resource), resLabel(p.Ref), len(pf.crashing))
		r.Mechanism = "控制器按期望副本创建 Pod，但 Pod 内进程反复退出，Ready 副本数始终补不上；滚动更新时新 ReplicaSet 因就绪不足而停住"
		r.EvidenceRefs = dedupe(append(r.EvidenceRefs, evIDs(c.FindingOn(p.Ref, ""))...))
		r.AlternativeCauses = []string{"新版本镜像或配置本身有问题（回滚可验证）", "共享依赖故障导致所有副本同时崩溃"}
		r.RemediationDirection = "先看 Pod 崩溃根因；若确认是新版本引入，回滚上一版本比继续等待更快恢复："
		r.RemediationText = "确认崩溃与新版本相关时执行回滚，并用 rollout status 确认副本补齐；若是共享依赖故障，先修依赖。"
		r.Remediation = []model.Command{
			rem(fmt.Sprintf("kubectl %srollout undo %s", nsArg(f.Resource.Namespace), target), model.RiskMedium),
		}
	case len(pf.pending) > 0:
		p := pf.pending[0]
		r.Status = model.RootCauseProbable
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "工作负载副本字段 + 下属 Pod 处于 Pending（ownerReferences 关系）；调度失败细节见 Pod 侧结论"
		r.RootCause = fmt.Sprintf("%s 副本不足是因 %d 个 Pod 停留在 Pending 未被调度（如 %s）",
			resLabel(f.Resource), len(pf.pending), resLabel(p.Ref))
		r.Mechanism = "Pod 未通过调度（资源不足/污点/亲和性/等待存储卷），因此从始到终没有容器，也就永远不会就绪"
		r.EvidenceRefs = dedupe(append(r.EvidenceRefs, evIDs(c.FindingOn(p.Ref, ""))...))
		r.RemediationDirection = "先解决调度前提（节点资源/污点/存储卷），副本会自动补齐；单纯重启或加大副本数只会 Pending 更多 Pod"
		r.RemediationText = "按 Pending 根因处理：扩容节点、补 tolerations 或修复 PVC；完成后等待副本自愈。"
	case len(pf.notReady) > 0:
		p := pf.notReady[0]
		r.Status = model.RootCauseProbable
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "Pod 在运行但容器未就绪（容器 ready 字段）；就绪失败的具体原因见 Pod 侧探针结论"
		r.RootCause = fmt.Sprintf("%s 副本不足是因 %d 个 Pod 已运行但探针未通过（如 %s）",
			resLabel(f.Resource), len(pf.notReady), resLabel(p.Ref))
		r.Mechanism = "容器在运行但就绪探针持续失败，Pod 不会被计入 ReadyReplicas，也不会被 Service 转发"
		r.EvidenceRefs = dedupe(append(r.EvidenceRefs, evIDs(c.FindingOn(p.Ref, ""))...))
		r.RemediationDirection = "核对就绪探针与应用实际健康检查接口/端口是否一致，或应用确实未就绪；不要在未确认前放开探针来「凑就绪」"
		r.RemediationText = "修复探针配置或应用本身，使就绪探针通过；滚动更新卡住时可先暂停 rollout 防止继续扩散。"
	case pf.total() == 0:
		r.Status = model.RootCauseProbable
		r.Confidence = ConfInferred
		r.ConfidenceBasis = "快照里该工作负载名下没有任何 Pod（ownerReferences 反查为空），但期望副本不为 0"
		r.RootCause = fmt.Sprintf("%s 声明了期望副本但本次快照中查不到它的任何 Pod", resLabel(f.Resource))
		r.Mechanism = "上层控制器没能创建出 Pod（常见于 ResourceQuota 拒绝创建、ReplicaSet 被缩到 0、或新 ReplicaSet 因模板校验失败被拒绝）"
		r.EvidenceGaps = append(r.EvidenceGaps,
			"未采集 ReplicaSet 与配额对象，无法区分 Pod 是被拒绝创建还是创建后被删除",
			"若 Pod 刚被删除，可能已超过事件保留窗口，需要看控制器事件确认")
		r.AlternativeCauses = []string{"ResourceQuota/LimitRange 拒绝创建", "Pod 模板不合法被 API Server 拒绝", "有人手工 scale 到 0"}
		r.RemediationDirection = "先看控制器事件确认是否报 Forbidden/failed to create pod，再查配额与模板合法性；确认是人为 scale 则恢复副本数"
		r.RemediationText = "按控制器事件给出的拒绝原因处理：配额不足则提配额或释放同命名空间额度，模板非法则修正后重新发布。"
		r.Remediation = []model.Command{
			rem(fmt.Sprintf("kubectl %sget resourcequota", nsArg(f.Resource.Namespace)), model.RiskSafe),
		}
	case wl != nil && desiredIsZero(wl):
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "spec.replicas=0 是对象里的真实声明，不是采集缺失"
		r.RootCause = fmt.Sprintf("%s 的期望副本被设为 0（spec.replicas=0），因此没有 Pod 属于预期行为", resLabel(f.Resource))
		r.Mechanism = "期望副本为 0 时控制器主动维持零副本，报告里的「副本不足」其实是容量被人工或 HPA 归零"
		r.EvidenceGaps = append(r.EvidenceGaps, "未确认是人工 scale、HPA 缩容还是发布流程设 0，需要看变更记录")
		r.RemediationDirection = "确认容量归零是不是预期：人为缩容则按需恢复副本数，若由 HPA 触发则检查其 minReplicas 与指标配置"
	default:
		r.Status = model.RootCauseProbable
		r.Confidence = ConfInferred
		r.ConfidenceBasis = "副本字段不一致，但其 Pod 在本次快照中均显示就绪（可能是采集时刻瞬时差异）"
		r.RootCause = fmt.Sprintf("%s 上报的就绪副本（%s）低于期望，但本次快照内其 Pod 均处于就绪状态",
			resLabel(f.Resource), replicasText(desiredOf(wl), readyOf(wl)))
		r.EvidenceGaps = append(r.EvidenceGaps,
			"status 与 Pod 状态存在时间差，可能是滚动更新瞬时状态，建议复扫确认")
		r.AlternativeCauses = []string{"采集时刻正好处于滚动更新中", "Pod 已就绪但 status 尚未回写"}
		r.RemediationDirection = "先复扫确认是否仍存在；瞬时不一致不要动线上配置"
	}

	// 因果边：下属 Pod 的症状 → 工作负载副本不足（关系来自 ownerReferences）。
	for _, it := range c.Items {
		if it.ID == f.ID || it.Resource.Kind != "Pod" {
			continue
		}
		p := c.PodOf(*it)
		if p == nil {
			continue
		}
		owned := false
		for _, w := range c.E.index().OwnerChain(p) {
			if w.Ref.Key() == f.Resource.Key() {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}
		if edge, ok := c.CausalEdge(it, f, "该 Pod 未就绪，工作负载 ReadyReplicas 无法达到期望值"); ok {
			r.CausalEdges = append(r.CausalEdges, edge)
		}
	}
	if r.RootCause != "" {
		r.Summary = r.RootCause
	}
	return r
}

func desiredOf(w *model.WorkloadInfo) *int32 {
	if w == nil {
		return nil
	}
	return w.DesiredReplicas
}

func readyOf(w *model.WorkloadInfo) *int32 {
	if w == nil {
		return nil
	}
	return w.ReadyReplicas
}

func desiredIsZero(w *model.WorkloadInfo) bool {
	d, ok := ptrVal(w.DesiredReplicas)
	return ok && d == 0
}

// daemonSetDiagnoser 处理 DaemonSet 未覆盖全部节点（往往是某些节点 NotReady/被排斥）。
type daemonSetDiagnoser struct{}

func (daemonSetDiagnoser) ID() string { return "WORKLOAD_DAEMONSET_GAP" }

func (daemonSetDiagnoser) Scenario() string { return "DaemonSet 未调度到全部节点" }

func (daemonSetDiagnoser) Match(c *Case) bool { return c.HasRule("DaemonSetReplica") }

func (daemonSetDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("DaemonSetReplica")
	wl := c.Workload(f.Resource)
	pods := c.E.index().PodsOfWorkload(f.Resource.Key())

	var missingNodes []string
	for _, n := range c.E.index().Nodes {
		ready, ok := condTrue(n.Conditions, "Ready")
		if ok && !strings.EqualFold(ready.Status, "True") {
			missingNodes = append(missingNodes, n.Ref.Name)
			continue
		}
		if n.Unschedulable {
			missingNodes = append(missingNodes, n.Ref.Name+"(不可调度)")
		}
	}
	present := map[string]bool{}
	for _, p := range pods {
		present[p.NodeName] = true
	}
	var emptyNodes []string
	for _, n := range c.E.index().Nodes {
		if !present[n.Ref.Name] && !strings.Contains(n.Ref.Name, "(不可调度)") {
			emptyNodes = append(emptyNodes, n.Ref.Name)
		}
	}

	r := Result{
		AnchorFinding: f.ID,
		EvidenceRefs:  evIDs(f),
		Status:        model.RootCauseProbable,
		Confidence:    ConfInferred,
		Mechanism:     "DaemonSet 按节点逐个创建 Pod，节点不可用、被 taint 排斥或模板不兼容时，对应节点上就永远没有这个 Pod",
		Impact:        "缺失节点上该组件（常见为 CNI/CSI/日志采集/监控代理）不工作，节点侧可观测性或网络能力存在盲区",
		ImpactChain:   append([]string{resLabel(f.Resource) + "（" + replicasText(desiredOf(wl), readyOf(wl)) + "）"}, nodeLabels(missingNodes, emptyNodes)...),
		Investigation: []model.Command{
			inv(fmt.Sprintf("kubectl %sdescribe ds %s", nsArg(f.Resource.Namespace), f.Resource.Name)),
			inv("kubectl get nodes -o wide"),
			inv(fmt.Sprintf("kubectl %sget pods -l <DS 的 selector> -A -o wide --field-selector spec.nodeName=<缺失节点名>", nsArg(f.Resource.Namespace))),
		},
		EvidenceGaps:      []string{"未采集 taint/toleration 匹配结果与节点 OS 架构标签，无法断定被排斥的具体约束"},
		AlternativeCauses: []string{"缺失节点带该 DS 不容忍的 taint", "节点架构/OS 与 Pod 的 nodeSelector 不匹配", "节点 NotReady 导致 Pod 无法创建"},
		RemediationDirection: "先按节点维度定位缺口（describe ds 会列出每个节点的期望/就绪），再判断是节点问题还是调度约束问题；" +
			"不要用删除 DS 的方式「重建覆盖」，那会让所有节点的该组件同时消失",
		RemediationText: "节点问题先修节点；调度约束问题则补 tolerations/nodeSelector 或按设计排除该节点并记录原因。",
		Verification: []model.Command{
			ver(fmt.Sprintf("kubectl %sget ds %s -o wide", nsArg(f.Resource.Namespace), f.Resource.Name)),
		},
	}
	if len(missingNodes) > 0 {
		r.Status = model.RootCauseProbable
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "Node Ready/Unschedulable 状态字段与 DS 副本缺口同时存在（节点对象为本次采集事实）"
		r.RootCause = fmt.Sprintf("%s 未覆盖的节点中存在不可用节点：%s；这些节点上不会创建 DS Pod",
			resLabel(f.Resource), strings.Join(missingNodes, "、"))
		for _, it := range c.Items {
			if it.ID == f.ID || it.Resource.Kind != "Node" {
				continue
			}
			if edge, ok := c.CausalEdge(it, f, "节点不可用，DaemonSet 无法在该节点建立 Pod"); ok {
				r.CausalEdges = append(r.CausalEdges, edge)
			}
		}
	} else {
		r.RootCause = fmt.Sprintf("%s 副本未达期望，但快照内全部节点均 Ready 且可调度（疑为调度约束排斥或采集瞬时差异）", resLabel(f.Resource))
		r.EvidenceGaps = append(r.EvidenceGaps,
			"节点全部可用，缺口更可能来自 taint/nodeSelector 不匹配，需按节点核对容忍度")
	}
	if len(emptyNodes) > 0 {
		r.EvidenceGaps = append(r.EvidenceGaps, "以下 Ready 节点上没有该 DS Pod，需核对 taint 与 nodeSelector："+strings.Join(emptyNodes, "、"))
	}
	r.Summary = r.RootCause
	return r
}

func nodeLabels(missing, empty []string) []string {
	var out []string
	if len(missing) > 0 {
		out = append(out, "不可用节点："+strings.Join(missing, "、"))
	}
	if len(empty) > 0 {
		out = append(out, "无 DS Pod 的节点："+strings.Join(empty, "、"))
	}
	return out
}

// jobFailedDiagnoser 处理 Job 失败（Job 的根因几乎总在其 Pod 的退出码与日志里）。
type jobFailedDiagnoser struct{}

func (jobFailedDiagnoser) ID() string { return "WORKLOAD_JOB_FAILED" }

func (jobFailedDiagnoser) Scenario() string { return "Job 执行失败" }

func (jobFailedDiagnoser) Match(c *Case) bool { return c.HasRule("JobFailed") }

func (jobFailedDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("JobFailed")
	pods := c.E.index().PodsOfWorkload(f.Resource.Key())
	var failed *model.PodInfo
	var exit int32
	var container string
	var withLogs *model.PodInfo
	for _, p := range pods {
		for _, ctr := range containersOf(p) {
			if strings.EqualFold(ctr.State, "terminated") && ctr.ExitCode != 0 && failed == nil {
				failed, exit, container = p, ctr.ExitCode, ctr.Name
			}
			if withLogs == nil && hasLogs(p) {
				withLogs = p
			}
		}
	}
	r := Result{
		AnchorFinding: f.ID,
		EvidenceRefs:  evIDs(f),
		Mechanism:     "Job 的 Pod 以非 0 退出码结束，达到 backoffLimit 后 Job 被标记 Failed，不会自动重试",
		Impact:        "该批处理任务未完成，下游依赖其产物的流程会拿到空结果或旧结果",
		Investigation: []model.Command{
			inv(fmt.Sprintf("kubectl %sdescribe job %s", nsArg(f.Resource.Namespace), f.Resource.Name)),
			inv(fmt.Sprintf("kubectl %slogs job/%s --all-containers --tail=200", nsArg(f.Resource.Namespace), f.Resource.Name)),
		},
		Verification: []model.Command{
			ver(fmt.Sprintf("kubectl %sget job %s -o jsonpath='{.status.succeeded} {.status.failed}'", nsArg(f.Resource.Namespace), f.Resource.Name)),
		},
		AlternativeCauses: []string{"任务依赖的外部服务/数据源不可用", "任务超时被终止", "镜像或脚本变更引入错误", "并发数与 backoffLimit 设置过小"},
		RemediationDirection: "Job 失败先看日志再看退出码：修脚本/依赖后用 kubectl job delete + 重新创建（或 CronJob 手工 trigger）重跑，" +
			"注意重跑是否有副作用（幂等性），不要把不幂等的任务直接重放",
		RemediationText: "按日志里的真实错误修复任务逻辑或依赖，再重建 Job；若为偶发超时，调高 activeDeadlineSeconds 与 backoffLimit。",
	}
	if failed != nil {
		hint := exitCodeHints[exit]
		r.Status = model.RootCauseProbable
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = fmt.Sprintf("失败 Pod %s 的容器 %s 终止退出码 %d（status 字段）", resLabel(failed.Ref), container, exit)
		if hint != "" {
			r.RootCause = fmt.Sprintf("Job %s 的 Pod %s 中容器 %s 以退出码 %d 退出：%s",
				f.Resource.Name, resLabel(failed.Ref), container, exit, hint)
		} else {
			r.RootCause = fmt.Sprintf("Job %s 的 Pod %s 中容器 %s 以退出码 %d 退出（非 0 即失败）",
				f.Resource.Name, resLabel(failed.Ref), container, exit)
		}
		r.EvidenceRefs = dedupe(append(r.EvidenceRefs, evIDs(c.FindingOn(failed.Ref, ""))...))
		if withLogs != nil {
			r.Status = model.RootCauseConfirmed
			r.Confidence = ConfObjectAndEvent
			r.ConfidenceBasis = "容器退出码 + 已采集到的容器日志同时可用，失败原因可在日志中直接核对"
			r.EvidenceRefs = dedupe(append(r.EvidenceRefs, evOfKind(c.FindingOn(withLogs.Ref, ""), model.EvLog)...))
		} else {
			r.EvidenceGaps = append(r.EvidenceGaps, "未采集到该 Job Pod 的日志，退出码只能说明失败类别，不能说明具体错误")
		}
	} else {
		r.Status = model.RootCauseUnknown
		r.Confidence = ConfClue
		r.ConfidenceBasis = "Job 被判定失败，但本次快照内取其 Pod 均无失败容器（Pod 可能已被清理）"
		r.RootCause = fmt.Sprintf("Job %s 处于失败状态，但快照内已无可定位的失败 Pod", resLabel(f.Resource))
		r.EvidenceGaps = append(r.EvidenceGaps,
			"Job 失败后 Pod 可能已被 TTL 清理，退出码与日志均已不可得，需要看 Job.status.conditions 与历史日志归档")
	}
	r.Summary = r.RootCause
	return r
}

func (c *Case) hpa(ref model.ResourceRef) *model.HpaInfo {
	if c.E.Snapshot == nil {
		return nil
	}
	for i := range c.E.Snapshot.Hpas {
		if c.E.Snapshot.Hpas[i].Ref.Key() == ref.Key() {
			return &c.E.Snapshot.Hpas[i]
		}
	}
	return nil
}

func (c *Case) pdb(ref model.ResourceRef) *model.PdbInfo {
	if c.E.Snapshot == nil {
		return nil
	}
	for i := range c.E.Snapshot.Pdbs {
		if c.E.Snapshot.Pdbs[i].Ref.Key() == ref.Key() {
			return &c.E.Snapshot.Pdbs[i]
		}
	}
	return nil
}

// hpaDiagnoser 处理 HPA 伸缩目标失效 / 扩不上去。
type hpaDiagnoser struct{}

func (hpaDiagnoser) ID() string { return "WORKLOAD_HPA_NOT_SCALING" }

func (hpaDiagnoser) Scenario() string { return "HPA 伸缩目标失效或无法扩容" }

func (hpaDiagnoser) Match(c *Case) bool {
	return c.HasRule("HpaScaleTargetMissing") || c.HasRule("HpaReplicasMismatch")
}

func (hpaDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("HpaScaleTargetMissing")
	targetMissing := f != nil
	if f == nil {
		f = c.ByRule("HpaReplicasMismatch")
	}
	h := c.hpa(f.Resource)
	// scaleTargetRef 是 HPA 里唯一真实的伸缩目标引用，按它拼核对命令。
	targetCmd := fmt.Sprintf("kubectl %sget hpa %s -o jsonpath='{.spec.scaleTargetRef}'",
		nsArg(f.Resource.Namespace), f.Resource.Name)
	if h != nil && h.ScaleTarget != "" {
		if kind, name, ok := strings.Cut(h.ScaleTarget, "/"); ok && kind != "" && name != "" {
			targetCmd = fmt.Sprintf("kubectl %sget %s %s",
				nsArg(f.Resource.Namespace), strings.ToLower(kind), name)
		}
	}
	r := Result{
		AnchorFinding: f.ID,
		EvidenceRefs:  evIDs(f),
		Investigation: []model.Command{
			inv(fmt.Sprintf("kubectl %sdescribe hpa %s", nsArg(f.Resource.Namespace), f.Resource.Name)),
			inv(targetCmd),
		},
		Verification: []model.Command{
			ver(fmt.Sprintf("kubectl %sget hpa %s", nsArg(f.Resource.Namespace), f.Resource.Name)),
		},
		AlternativeCauses: []string{"metrics-server 未就绪导致指标缺失", "目标负载被其他流水线接管并回滚了副本数", "集群节点资源不足以承载扩容出的 Pod"},
	}
	switch {
	case targetMissing:
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "HPA 的 spec.scaleTargetRef 是真实引用字段，本次快照内确认目标工作负载不存在"
		r.RootCause = f.Summary
		r.Mechanism = "HPA 控制器找不到 scaleTargetRef 指向的工作负载，整个伸缩链路失效，指标再高也不会扩容"
		r.Impact = "自动伸缩失效：负载升高时容量不会跟进，回落后也不会缩容，等于隐性容量风险"
		r.EvidenceGaps = append(r.EvidenceGaps, "未确认目标是改名、被删还是 HPA 写错命名空间，需要查变更记录")
		r.RemediationDirection = "二选一：把 scaleTargetRef 改回真实存在的负载名，或删除已经废弃的 HPA；不要留着失效 HPA 假装还有自动伸缩"
		r.RemediationText = "修正 HPA 的 spec.scaleTargetRef（Kind/Name/Namespace 三者都要对），改完确认 desiredReplicas 开始跟随指标。"
	case h != nil && h.MaxReplicas > 0 && h.CurrentReplicas >= h.MaxReplicas && h.DesiredReplicas >= h.MaxReplicas:
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "HPA status.currentReplicas 与 status.desiredReplicas 均已顶到 spec.maxReplicas（对象字段）"
		r.RootCause = fmt.Sprintf("HPA %s 已顶到上限：currentReplicas=%d desiredReplicas=%d maxReplicas=%d，无法继续扩容",
			f.Resource.Name, h.CurrentReplicas, h.DesiredReplicas, h.MaxReplicas)
		r.Mechanism = "期望副本被 maxReplicas 截断，超出部分不再扩容，此时指标仍高说明容量确实不够"
		r.Impact = "负载继续增长时不会再有新增副本，服务会在到达上限后出现排队/超时"
		r.EvidenceGaps = append(r.EvidenceGaps, "未确认扩容出的 Pod 是否全部就绪，也未确认集群是否有空余节点资源")
		r.RemediationDirection = "先确认现有副本是不是真在干活（若 Pod 起不来，扩到上限也没用），再考虑上调 maxReplicas 并同步保证节点容量"
		r.RemediationText = "确认容量确实不足后上调 maxReplicas（并确保集群有可调度节点），或优化单副本处理能力以抬高天花板。"
	default:
		r.Status = model.RootCauseProbable
		r.Confidence = ConfInferred
		r.ConfidenceBasis = "HPA 上报的期望副本与实际副本存在差异，但未顶到上限；差异更可能来自被扩容 Pod 未就绪或外部回滚"
		r.RootCause = fmt.Sprintf("HPA %s 的期望副本与实际副本不一致（%s）", f.Resource.Name, f.Summary)
		r.Mechanism = "HPA 已写下期望副本，但目标负载的实际副本没有跟上（扩容出的 Pod 未就绪，或副本数被其他流程改回）"
		r.Impact = "伸缩动作未完成，容量处于中间态"
		r.EvidenceGaps = append(r.EvidenceGaps, "未采集 metrics-server 状态与目标负载的 Pod 就绪明细，无法区分「扩了但起不来」与「被外部回滚」")
		r.AlternativeCauses = append(r.AlternativeCauses, "ArgoCD/流水线把副本数回滚到 Git 中声明的值", "PodDisruptionBudget 或配额限制新建 Pod")
		r.RemediationDirection = "先看目标负载里扩容出的 Pod 是否 Pending/未就绪，再看是否有 GitOps 流程在覆盖 replicas"
	}
	if r.RootCause == "" {
		r.RootCause = f.Summary
	}
	r.Summary = r.RootCause
	// 因果边：HPA 异常 → 同链上因此产生的副本/容量问题。
	for _, it := range c.Items {
		if it.ID == f.ID {
			continue
		}
		if edge, ok := c.CausalEdge(f, it, "HPA 伸缩失效导致实际副本与期望不一致"); ok {
			r.CausalEdges = append(r.CausalEdges, edge)
		}
	}
	return r
}

// pdbDiagnoser 处理 PDB 预算耗尽阻塞滚动更新/驱逐。
type pdbDiagnoser struct{}

func (pdbDiagnoser) ID() string { return "WORKLOAD_PDB_BLOCKING" }

func (pdbDiagnoser) Scenario() string { return "PDB 预算耗尽阻塞滚动更新或节点维护" }

func (pdbDiagnoser) Match(c *Case) bool { return c.HasRule("PdbBlocking") }

func (pdbDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("PdbBlocking")
	p := c.pdb(f.Resource)
	protected := 0
	if c.E.Snapshot != nil {
		for i := range c.E.Snapshot.Pods {
			pod := &c.E.Snapshot.Pods[i]
			if pod.Ref.Namespace != f.Resource.Namespace || p == nil || len(p.Selector) == 0 {
				continue
			}
			if labelsSuperset(p.Selector, pod.Labels) {
				protected++
			}
		}
	}
	r := Result{
		AnchorFinding:   f.ID,
		Status:          model.RootCauseConfirmed,
		Confidence:      ConfObjectStatus,
		ConfidenceBasis: "PDB 的 status.disruptionsAllowed 与 spec.minAvailable/maxUnavailable 均为对象字段，selector 命中的 Pod 数由真实标签匹配得出",
		RootCause: fmt.Sprintf("PDB %s 当前允许的中断数为 %s，其 selector 覆盖 %d 个 Pod，可用预算为 0，滚动更新/驱逐动作会被 API Server 拒绝",
			f.Resource.Name, allowedText(p), protected),
		Mechanism:    "PDB 的允许中断数为 0 时，eviction 子资源一律拒绝任何 Pod 下线，滚动更新与节点 drain 因此卡住",
		Impact:       "滚动更新停在第一个待替换的 Pod 上；节点维护 drain 也会一直等待（不是失败，是一直等）",
		EvidenceRefs: evIDs(f),
		Investigation: []model.Command{
			inv(fmt.Sprintf("kubectl %sdescribe pdb %s", nsArg(f.Resource.Namespace), f.Resource.Name)),
			inv(fmt.Sprintf("kubectl %sget pods -l <PDB 的 selector> -o wide", nsArg(f.Resource.Namespace))),
		},
		AlternativeCauses: []string{"minAvailable 设置等于副本总数（等于零预算）", "副本本身未就绪导致可用数低于预算线", "多副本集中在同一节点，节点 drain 必然违反预算"},
		EvidenceGaps:      []string{"未确认当前是否有正在进行的 rollout/drain 动作，阻塞是发生在计划中还是已实际卡住"},
		RemediationDirection: "优先把预算调到合理值（如 minAvailable: 80% 或 maxUnavailable: 1）或先补齐副本再维护；" +
			"不要用 --force 绕过 PDB，那等于在容量不足时强行减副本",
		RemediationText: "调整 PDB 预算或增加副本数以留出中断余量；紧急维护场景应改用分批、按节点打散的方式推进。",
		Verification: []model.Command{
			ver(fmt.Sprintf("kubectl %sget pdb %s", nsArg(f.Resource.Namespace), f.Resource.Name)),
		},
	}
	r.Summary = r.RootCause
	return r
}

func allowedText(p *model.PdbInfo) string {
	if p == nil {
		return "未知"
	}
	return fmt.Sprintf("%d", p.DisruptionsAllowed)
}

// labelsSuperset 判断 selector 的每个键值是否都在 labels 中成立。
func labelsSuperset(selector, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}
