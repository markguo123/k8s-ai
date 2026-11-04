package deterministic

import (
	"fmt"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func init() {
	Register(crashLoopDiagnoser{})
	Register(oomKilledDiagnoser{})
	Register(imagePullDiagnoser{})
	Register(pendingSchedulingDiagnoser{})
	Register(failedMountDiagnoser{})
	Register(evictedDiagnoser{})
	Register(probeFailingDiagnoser{})
	Register(configRefMissingDiagnoser{})
}

// crashingContainer 找出故障链里第一个处于崩溃/反复重启状态的容器。
func crashingContainer(c *Case) (*model.PodInfo, *model.ContainerInfo, *model.Finding) {
	for _, f := range c.Items {
		p := c.PodOf(*f)
		if p == nil {
			continue
		}
		for _, ctr := range containersOf(p) {
			waitingCrash := strings.EqualFold(ctr.State, "waiting") &&
				matchAny(ctr.Reason, "CrashLoopBackOff", "CreateContainerError", "RunContainerError", "InvalidImageName")
			timedCrash := ctr.RestartCount > 0 &&
				(strings.EqualFold(ctr.State, "terminated") || strings.EqualFold(ctr.LastState, "terminated"))
			if waitingCrash || timedCrash {
				return p, ctr, f
			}
		}
	}
	return nil, nil, nil
}

// crashLoopDiagnoser 处理「容器反复崩溃、Kubernetes 退避重启」这一最高频场景。
type crashLoopDiagnoser struct{}

func (crashLoopDiagnoser) ID() string { return "POD_CRASHLOOP" }

func (crashLoopDiagnoser) Scenario() string {
	return "容器持续崩溃（CrashLoopBackOff / 反复重启）"
}

func (crashLoopDiagnoser) Match(c *Case) bool {
	if !c.HasRule("CrashLoopBackOff") && !c.HasRule("HighRestartCount") && !c.HasRule("ContainerCreateError") {
		return false
	}
	p, _, _ := crashingContainer(c)
	return p != nil
}

func (crashLoopDiagnoser) Diagnose(c *Case) Result {
	p, ctr, f := crashingContainer(c)

	exit := ctr.LastExitCode
	if exit == 0 {
		exit = ctr.ExitCode
	}
	lastReason := ctr.LastReason
	if lastReason == "" {
		lastReason = ctr.Reason
	}
	hint := exitCodeHints[exit]

	detail := fmt.Sprintf("容器 %s 反复异常退出（当前状态 %s/%s，上次终止原因 %s，退出码 %d，已重启 %d 次）",
		ctr.Name, orDash(ctr.State), orDash(ctr.Reason), orDash(lastReason), exit, ctr.RestartCount)
	if hint != "" {
		detail += "：" + hint
	}

	r := Result{
		AnchorFinding: f.ID,
		RootCause:     detail,
		Mechanism:     "容器进程启动后异常退出，kubelet 按指数退避反复重启，Pod 始终无法进入 Ready，因此不可被 Service 转发",
		Impact:        fmt.Sprintf("Pod %s 无法稳定提供服务", resLabel(p.Ref)),
		ImpactChain:   impactChainForPod(c, p),
		EvidenceRefs:  evIDs(f),
		Investigation: []model.Command{
			inv(fmt.Sprintf("kubectl %sdescribe pod %s", nsArg(p.Ref.Namespace), p.Ref.Name)),
			inv(fmt.Sprintf("kubectl %slogs %s -c %s --previous --tail=200", nsArg(p.Ref.Namespace), p.Ref.Name, ctr.Name)),
			inv(fmt.Sprintf("kubectl %sget events --field-selector involvedObject.name=%s --sort-by=.lastTimestamp", nsArg(p.Ref.Namespace), p.Ref.Name)),
		},
		Verification: []model.Command{
			ver(fmt.Sprintf("kubectl %sget pod %s -w", nsArg(p.Ref.Namespace), p.Ref.Name)),
		},
	}

	// 日志是唯一能证明「应用侧到底报了什么」的证据；没采到日志就只能 probable。
	logEvs := evOfKind(f, model.EvLog)
	_, evt := c.EventWith(p.Ref, "BackOff", "Back-off", "Failed")
	fieldEvs := evOfKind(f, model.EvObjectField)
	switch {
	case len(fieldEvs) > 0 && (len(logEvs) > 0 || evt):
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectAndEvent
		r.ConfidenceBasis = "容器状态字段（waiting.reason / lastState.terminated）与事件或日志互相印证"
		r.EvidenceRefs = dedupe(append(fieldEvs, append(logEvs, evOfKind(f, model.EvEvent)...)...))
	case len(fieldEvs) > 0 && exit != 0:
		r.Status = model.RootCauseProbable
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "仅依据容器状态字段与退出码，未采集到可佐证的日志或事件"
	default:
		r.Status = model.RootCauseProbable
		r.Confidence = ConfInferred
		r.ConfidenceBasis = "依据规则命中的容器状态，缺少日志与事件佐证"
	}
	if !hasLogs(p) {
		r.EvidenceGaps = append(r.EvidenceGaps,
			"未采集容器日志（quick/standard 档位不采集日志），无法确认应用抛出的具体异常")
	}
	if node := c.Node(p.NodeName); node != nil {
		if _, ok := condTrue(node.Conditions, "Ready"); !ok {
			r.Status = model.RootCauseProbable
			r.Confidence = ConfInferred
			r.AlternativeCauses = append(r.AlternativeCauses,
				fmt.Sprintf("所在节点 %s 处于 NotReady，崩溃可能由节点侧运行时或存储异常引起", node.Ref.Name))
			r.EvidenceGaps = append(r.EvidenceGaps, "所在节点 NotReady，需先确认节点侧 kubelet/运行时状态再判定容器原因")
		}
	}
	r.AlternativeCauses = append(r.AlternativeCauses,
		"应用启动依赖（数据库/配置中心/下游服务）不可用",
		"启动探针超时阈值过短，慢启动被判定为失败")

	r.RemediationDirection = "先看到上一次退出的真实日志再动手：确认是应用异常、配置错误还是依赖不可用，最后才考虑调整探针或资源限制"
	r.RemediationText = "修复动作取决于日志里的真实异常。确认是配置/环境变量错误时改配置后滚动重启；确认是依赖不可用时先恢复依赖；" +
		"确认是启动慢则调大 startupProbe 的 failureThreshold。不要在没看到崩溃原因前直接放大副本或反复重启。"
	if wl := topWorkload(c, p); wl != nil {
		r.Remediation = []model.Command{
			rem(fmt.Sprintf("kubectl %srollout restart %s/%s", nsArg(wl.Ref.Namespace), strings.ToLower(wl.Ref.Kind), wl.Ref.Name), model.RiskMedium),
		}
	}
	return r
}

// oomKilledDiagnoser 处理容器被 OOM Kill 的场景（内存上限低于实际用量）。
type oomKilledDiagnoser struct{}

func (oomKilledDiagnoser) ID() string { return "POD_OOMKILLED" }

func (oomKilledDiagnoser) Scenario() string { return "容器因内存超限被 OOM Kill" }

func (oomKilledDiagnoser) Match(c *Case) bool {
	for _, f := range c.Items {
		p := c.PodOf(*f)
		if p == nil {
			continue
		}
		for _, ctr := range containersOf(p) {
			if strings.EqualFold(ctr.LastReason, "OOMKilled") || strings.EqualFold(ctr.Reason, "OOMKilled") {
				return true
			}
		}
	}
	return c.HasRule("OOMKilled")
}

func (oomKilledDiagnoser) Diagnose(c *Case) Result {
	var (
		owner *model.PodInfo
		bad   *model.ContainerInfo
		anch  *model.Finding
	)
	for _, f := range c.Items {
		p := c.PodOf(*f)
		if p == nil {
			continue
		}
		for _, ctr := range containersOf(p) {
			if strings.EqualFold(ctr.LastReason, "OOMKilled") || strings.EqualFold(ctr.Reason, "OOMKilled") {
				owner, bad, anch = p, ctr, f
				break
			}
		}
		if bad != nil {
			break
		}
		if anch == nil && c.HasRule("OOMKilled") {
			anch = f
		}
	}
	limit := ""
	if bad != nil {
		limit = bad.Limits["memory"]
	}
	target := resLabel(podRefOr(c.Root.Resource, owner))
	if bad != nil {
		target = resLabel(owner.Ref) + " 的容器 " + bad.Name
	}
	r := Result{
		AnchorFinding: findingID(anch, c.Root.ID),
		RootCause: fmt.Sprintf("%s 的内存用量超过 limits.memory=%s，被内核 OOM Kill（终止原因 OOMKilled）",
			target, orDash(limit)),
		Mechanism:    "容器内存工作集超过 cgroup 限制，内核直接发送 SIGKILL，kubelet 随后重启容器",
		Impact:       "Pod 反复重启，期间该副本不承载流量；副本数少时表现为服务抖动",
		EvidenceRefs: evIDs(anch),
		Investigation: []model.Command{
			inv(fmt.Sprintf("kubectl %sdescribe pod %s", nsArg(c.Root.Resource.Namespace), c.Root.Resource.Name)),
			inv(fmt.Sprintf("kubectl %stop pod %s --containers", nsArg(c.Root.Resource.Namespace), c.Root.Resource.Name)),
		},
		AlternativeCauses: []string{"应用存在内存泄漏", "JVM/运行时堆参数未随容器限制调整", "节点内存压力导致的挤占（需与节点 Pressure 状态区分）"},
		EvidenceGaps:      []string{"缺少容器内存实际用量曲线，无法给出精确的目标 limit 值"},
		RemediationDirection: "先确认是用量真的变大还是限制设得过小：用量合理就上调 limits.memory（并同步 requests），" +
			"用量异常增长则按内存泄漏处理，别用不断放大 limit 掩盖问题",
		RemediationText: "调整内存限制属于变更，需按容量评审后执行；Java 类应用同时确认 -Xmx 小于容器 limits.memory。",
		Verification: []model.Command{
			ver(fmt.Sprintf("kubectl %sget pod %s -o jsonpath='{.status.containerStatuses[*].lastState.terminated.reason}'", nsArg(c.Root.Resource.Namespace), c.Root.Resource.Name)),
		},
	}
	if owner != nil {
		r.ImpactChain = impactChainForPod(c, owner)
	}
	if wl := topWorkload(c, owner); wl != nil {
		r.Remediation = []model.Command{
			rem(fmt.Sprintf("kubectl %sset resources %s/%s --limits=memory=<新值> --requests=memory=<新值>",
				nsArg(wl.Ref.Namespace), strings.ToLower(wl.Ref.Kind), wl.Ref.Name), model.RiskMedium),
		}
	}
	fieldEvs := evOfKind(anch, model.EvObjectField)
	_, evt := c.EventWith(podRefOr(c.Root.Resource, owner), "OOMKilling", "OOMKilled", "Killing")
	if len(fieldEvs) > 0 && (evt || len(evOfKind(anch, model.EvEvent)) > 0) {
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectAndEvent
		r.ConfidenceBasis = "容器 lastState.terminated.reason=OOMKilled 与节点 OOM 事件互相印证"
		r.EvidenceRefs = dedupe(append(fieldEvs, evOfKind(anch, model.EvEvent)...))
	} else if len(fieldEvs) > 0 {
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "容器状态字段明确写明 OOMKilled（该字段由 kubelet 直接写入，可信）"
		r.EvidenceRefs = fieldEvs
	} else {
		r.Status = model.RootCauseProbable
		r.Confidence = ConfInferred
		r.ConfidenceBasis = "规则命中 OOMKilled，但快照里未取到对应容器状态字段证据"
	}
	return r
}

func podRefOr(fallback model.ResourceRef, p *model.PodInfo) model.ResourceRef {
	if p != nil {
		return p.Ref
	}
	return fallback
}

func findingID(f *model.Finding, fallback string) string {
	if f != nil {
		return f.ID
	}
	return fallback
}

// imagePullDiagnoser 把「拉不到镜像」细化成镜像不存在 / 凭据缺失 / 网络不可达 / 证书不受信。
type imagePullDiagnoser struct{}

func (imagePullDiagnoser) ID() string { return "POD_IMAGEPULL" }

func (imagePullDiagnoser) Scenario() string {
	return "镜像拉取失败（ImagePullBackOff / ErrImagePull）"
}

func (imagePullDiagnoser) Match(c *Case) bool { return c.HasRule("ImagePullBackOff") }

func (imagePullDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("ImagePullBackOff")
	p := c.PodOf(*f)
	image := ""
	if p != nil {
		for _, ctr := range containersOf(p) {
			if matchAny(ctr.Reason, "ErrImagePull", "ImagePullBackOff", "InvalidImageName") || image == "" {
				image = ctr.Image
				if ctr.Reason != "" {
					break
				}
			}
		}
	}
	ref := podRefOr(f.Resource, p)
	ev, hasEv := c.EventWith(ref, "ErrImagePull", "ImagePullBackOff", "failed to pull", "manifest unknown", "unauthorized")

	cause, gap := classifyPullMessage(ev.Message)
	r := Result{
		AnchorFinding: f.ID,
		Mechanism:     "kubelet 无法从镜像仓库取得该镜像，容器无内容可启动，Pod 停在 waiting",
		Impact:        fmt.Sprintf("Pod %s 无法启动，副本无法就绪", resLabel(ref)),
		EvidenceRefs:  evIDs(f),
		Investigation: []model.Command{
			inv(fmt.Sprintf("kubectl %sdescribe pod %s", nsArg(ref.Namespace), ref.Name)),
			inv(fmt.Sprintf("kubectl %sget pod %s -o jsonpath='{.spec.containers[*].image}'", nsArg(ref.Namespace), ref.Name)),
		},
		RemediationDirection: "按仓库返回的具体错误处理：镜像名/Tag 写错就改回存在的 Tag；私有仓库凭据问题补 imagePullSecrets；" +
			"网络或证书问题要在节点侧恢复对仓库的可达性，改 Deployment 镜像字段前先在能访问仓库的机器上验证 Tag 是否存在",
		RemediationText: "确认镜像 Tag 与仓库地址后修改工作负载的 image 字段并滚动更新；凭据缺失时补齐 Secret 并确认 ServiceAccount 引用了它。",
		Verification: []model.Command{
			ver(fmt.Sprintf("kubectl %sget pod %s -o jsonpath='{.status.containerStatuses[*].state.waiting.reason}'", nsArg(ref.Namespace), ref.Name)),
		},
	}
	if hasEv {
		r.RootCause = fmt.Sprintf("镜像 %s 拉取失败：%s（仓库返回：%s）", orDash(image), cause, clip(ev.Message, 220))
		if len(evOfKind(f, model.EvEvent)) > 0 {
			r.Status = model.RootCauseConfirmed
			r.Confidence = ConfEventMessage
			r.ConfidenceBasis = "kubelet 拉取镜像失败事件里带有仓库原始报错，可直接定位失败类别"
			r.EvidenceRefs = dedupe(append(evOfKind(f, model.EvEvent), evOfKind(f, model.EvObjectField)...))
		} else {
			r.Status = model.RootCauseProbable
			r.Confidence = ConfInferred
			r.ConfidenceBasis = "事件消息可判类别，但该 Finding 上未挂到事件类证据"
		}
	} else {
		r.RootCause = fmt.Sprintf("Pod %s 的镜像 %s 拉取失败（未取到仓库返回的原始错误）", resLabel(ref), orDash(image))
		r.Status = model.RootCauseProbable
		r.Confidence = ConfClue
		r.ConfidenceBasis = "只有容器 waiting.reason，没有仓库返回的原始错误消息"
	}
	if gap != "" {
		r.EvidenceGaps = append(r.EvidenceGaps, gap)
	}
	if !hasEv {
		r.EvidenceGaps = append(r.EvidenceGaps, "未采集到镜像拉取事件（events 未采集或已超过保留期），无法区分镜像不存在/凭据缺失/网络不可达")
	}
	r.AlternativeCauses = []string{"节点到镜像仓库的网络或 DNS 异常", "私有仓库凭据（imagePullSecrets）缺失或过期", "镜像 Tag 被清理"}
	return r
}

// classifyPullMessage 把仓库报错归到可行动的类别（宁可说不知道，也不要瞎归类）。
func classifyPullMessage(msg string) (cause, gap string) {
	switch {
	case msg == "":
		return "原因未知", "缺少仓库返回的原始错误"
	case matchAny(msg, "manifest unknown", "not found", "no such image", "repository does not exist"):
		return "镜像或 Tag 在仓库中不存在", ""
	case matchAny(msg, "unauthorized", "authentication required", "forbidden", "no basic auth credentials"):
		return "镜像仓库鉴权失败（凭据缺失或无权访问）", ""
	case matchAny(msg, "no such host", "dial tcp", "i/o timeout", "connection refused", "network is unreachable"):
		return "节点无法连通镜像仓库（网络或 DNS 不可达）", ""
	case matchAny(msg, "x509", "certificate", "tls"):
		return "镜像仓库 TLS 证书不受节点信任", ""
	case matchAny(msg, "invalid image name", "invalid reference format"):
		return "镜像地址格式非法", ""
	default:
		return "镜像拉取失败（未匹配到已知错误类别）", "仓库报错不属于已知类别，需人工确认"
	}
}

// pendingSchedulingDiagnoser 处理 Pod 一直 Pending 的调度类原因（含等待存储卷）。
type pendingSchedulingDiagnoser struct{}

func (pendingSchedulingDiagnoser) ID() string { return "POD_UNSCHEDULABLE" }

func (pendingSchedulingDiagnoser) Scenario() string {
	return "Pod 长期 Pending / 调度失败（FailedScheduling）"
}

func (pendingSchedulingDiagnoser) Match(c *Case) bool {
	if !c.HasRule("PendingPod") {
		return false
	}
	f := c.ByRule("PendingPod")
	p := c.PodOf(*f)
	return p == nil || strings.EqualFold(p.Phase, "Pending")
}

func (pendingSchedulingDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("PendingPod")
	p := c.PodOf(*f)
	ref := podRefOr(f.Resource, p)
	r := Result{
		AnchorFinding: f.ID,
		Mechanism:     "调度器找不到满足资源、污点、亲和性与存储约束的节点，Pod 不落到任何节点上，因此不会有容器和日志",
		Impact:        fmt.Sprintf("Pod %s 未运行，副本缺口导致容量下降", resLabel(ref)),
		EvidenceRefs:  evIDs(f),
		Investigation: []model.Command{
			inv(fmt.Sprintf("kubectl %sdescribe pod %s", nsArg(ref.Namespace), ref.Name)),
			inv("kubectl get nodes -o wide"),
			inv("kubectl describe node <候选节点名> | sed -n '/Allocated resources/,$p'"),
		},
		Verification: []model.Command{
			ver(fmt.Sprintf("kubectl %sget pod %s -o wide", nsArg(ref.Namespace), ref.Name)),
		},
	}

	// 优先级 1：等待存储卷（真实引用关系 → 建因果边）。
	if pvcF := c.ByRule("PVCPending"); pvcF != nil {
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectAndEvent
		r.ConfidenceBasis = "Pod 通过 spec.volumes 真实引用该 PVC，且 PVC 自身状态为 Pending（对象状态字段）"
		r.RootCause = fmt.Sprintf("Pod %s 等待存储卷就绪：引用的 %s 仍处于 Pending，Pod 必须等 PVC 绑定 PV 后才能调度",
			resLabel(ref), resLabel(pvcF.Resource))
		r.Mechanism = "Pod 的卷引用了未绑定的 PVC，调度器按 VolumeBinding 规则拒绝把 Pod 放上节点"
		r.EvidenceRefs = dedupe(append(evIDs(f), evIDs(pvcF)...))
		if edge, ok := c.CausalEdge(pvcF, f, "PVC 未绑定 PV，Pod 因等待卷绑定而无法调度"); ok {
			edge.Confidence = model.RootCauseConfirmed
			r.CausalEdges = append(r.CausalEdges, edge)
		}
		r.EvidenceGaps = append(r.EvidenceGaps, "未确认 PVC 卡住的具体原因（见 STORAGE_PVCPENDING 结论）")
		r.RemediationDirection = "先解决 PVC 绑定问题（StorageClass、容量、provisioner），Pod 会自行完成调度；不要通过删除 Pod 来重试"
		r.RemediationText = "修复存储侧的绑定条件后等待 PVC 变为 Bound；确认无法供应时改用已有可用 PV 或调整 StorageClass。"
		return r
	}

	// 优先级 2：调度器自己在 FailedScheduling 事件里写明了原因。
	if ev, ok := c.EventWith(ref, "FailedScheduling", "0/N nodes are available", "didn't match"); ok {
		label, mechanism := pendingSchedulingReason(ev.Message)
		if label != "" {
			r.Status = model.RootCauseConfirmed
			r.Confidence = ConfEventMessage
			r.ConfidenceBasis = "调度器写入的 FailedScheduling 事件消息直接给出不可调度原因"
			r.RootCause = fmt.Sprintf("Pod %s 调度失败：%s（调度器：%s）", resLabel(ref), label, clip(ev.Message, 240))
			r.Mechanism = mechanism
			r.RemediationDirection = remediationForScheduling(label)
			r.RemediationText = "按调度器给出的约束调整：释放或扩容节点资源、补齐容忍度、修正亲和性约束，或降低该 Pod 的资源请求。"
		} else {
			r.Status = model.RootCauseProbable
			r.Confidence = ConfEventMessage
			r.ConfidenceBasis = "有 FailedScheduling 事件，但消息不属于已知的可判定类别"
			r.RootCause = fmt.Sprintf("Pod %s 调度失败（调度器消息：%s）", resLabel(ref), clip(ev.Message, 240))
			r.EvidenceGaps = append(r.EvidenceGaps, "调度器消息不属于已知类别，需要人工确认约束细节")
		}
		r.EvidenceRefs = dedupe(append(evOfKind(f, model.EvEvent), evOfKind(f, model.EvObjectField)...))
		r.AlternativeCauses = []string{"节点全部处于 NotReady", "ResourceQuota 已用满", "调度器组件异常"}
		r.RemediationDirection = orDash(r.RemediationDirection)
		return r
	}

	// 兜底：没有调度器事件就不假装知道原因（需求 §4.2「允许并鼓励 unknown」）。
	r.Status = model.RootCauseUnknown
	r.Confidence = ConfClue
	r.ConfidenceBasis = "仅有 Pending 状态字段，缺少 FailedScheduling 事件与存储引用异常，无法定位不可调度原因"
	r.RootCause = fmt.Sprintf("Pod %s 处于 Pending，但未采集到调度失败原因", resLabel(ref))
	r.EvidenceGaps = append(r.EvidenceGaps,
		"未采集到 FailedScheduling 事件（events 未采集或已过期），无法判断是资源不足、污点、亲和性还是等待存储卷")
	r.RemediationDirection = "先补证据再看事件：kubectl describe pod 的 Events 段是唯一权威来源；确认档位是否关闭了 events 采集，必要时以 deep 档重跑体检"
	r.RemediationText = "在拿到调度器事件之前不要做任何扩容或删除操作；如需立即恢复容量，只能通过新增节点这类无损动作。"
	return r
}

func remediationForScheduling(label string) string {
	switch label {
	case "节点可分配资源不足":
		return "扩容节点或下调该负载的 requests（并检查是否有 Request 虚高的 Pod 挤占额度）；确认是否触发集群自动扩容"
	case "节点污点未被容忍":
		return "给该 Pod 补上对应 tolerations，或把负载调度到不带这些污点的节点池"
	case "节点不可调度":
		return "确认节点是被 cordon 还是故障：误操作可 kubectl uncordon 恢复，故障节点先修节点"
	case "存储卷未就绪":
		return "先修 PVC/StorageClass，卷就绪后调度会自动完成"
	case "亲和性/选择器约束不满足":
		return "核对 nodeSelector/affinity 与实际节点标签，放宽约束或给节点打上所需标签"
	default:
		return "按调度器消息里的具体约束逐项核对，再决定扩容、调整约束还是修节点"
	}
}

// failedMountDiagnoser 把 FailedMount 拆成「卷没附上 / 卷没绑定 / 引用的配置对象不存在」。
type failedMountDiagnoser struct{}

func (failedMountDiagnoser) ID() string { return "POD_FAILEDMOUNT" }

func (failedMountDiagnoser) Scenario() string {
	return "存储卷或配置挂载失败（FailedMount）"
}

func (failedMountDiagnoser) Match(c *Case) bool { return c.HasRule("FailedMount") }

func (failedMountDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("FailedMount")
	p := c.PodOf(*f)
	ref := podRefOr(f.Resource, p)
	ev, hasEv := c.EventWith(ref, "FailedMount", "MountVolume", "Unable to attach", "not found", "timed out")

	r := Result{
		AnchorFinding: f.ID,
		Mechanism:     "kubelet 在挂载阶段拿不到该卷（未绑定 / 未附加 / 引用的配置对象不存在），容器启动被卡在创建之前",
		Impact:        fmt.Sprintf("Pod %s 无法启动，副本无法就绪", resLabel(ref)),
		EvidenceRefs:  evIDs(f),
		Investigation: []model.Command{
			inv(fmt.Sprintf("kubectl %sdescribe pod %s", nsArg(ref.Namespace), ref.Name)),
			inv(fmt.Sprintf("kubectl %sget pod %s -o jsonpath='{.spec.volumes}'", nsArg(ref.Namespace), ref.Name)),
		},
		AlternativeCauses: []string{"CSI 组件异常导致卷无法附加", "节点上的 device/mount 路径残留", "云盘跨可用区无法挂载"},
		RemediationDirection: "先分清是「卷不存在」「卷没附加」还是「引用的配置对象被删了」：前两类要修存储链路，第三类补配置对象，" +
			"任何情况下都不要手动去节点上创建目录来绕过挂载",
		RemediationText: "按事件里的卷名定位到 PVC/ConfigMap/Secret，恢复缺失对象或修复 CSI 附加链路，然后等 kubelet 自动重试挂载。",
		Verification: []model.Command{
			ver(fmt.Sprintf("kubectl %sget pod %s -o jsonpath='{.status.conditions}'", nsArg(ref.Namespace), ref.Name)),
		},
	}
	msg := ev.Message
	switch {
	case c.ByRule("PVCPending") != nil:
		pvcF := c.ByRule("PVCPending")
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectAndEvent
		r.ConfidenceBasis = "Pod 真实引用该 PVC（spec.volumes），且 PVC 状态字段为 Pending"
		r.RootCause = fmt.Sprintf("挂载失败的真实原因是存储卷未就绪：引用的 %s 处于 Pending", resLabel(pvcF.Resource))
		if edge, ok := c.CausalEdge(pvcF, f, "PVC 未绑定，kubelet 挂载阶段拿不到卷"); ok {
			edge.Confidence = model.RootCauseConfirmed
			r.CausalEdges = append(r.CausalEdges, edge)
		}
	case c.ByRule("VolumeAttachmentStuck") != nil:
		vaF := c.ByRule("VolumeAttachmentStuck")
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "VolumeAttachment 对象长期未 Attached（状态字段）+ Pod 侧挂载失败事件"
		r.RootCause = fmt.Sprintf("云盘未完成附加：%s 长时间未 Attached，Pod 挂载等待超时", resLabel(vaF.Resource))
		if edge, ok := c.CausalEdge(vaF, f, "卷未附加到节点，kubelet 无法完成 mount"); ok {
			edge.Confidence = model.RootCauseConfirmed
			r.CausalEdges = append(r.CausalEdges, edge)
		}
		r.EvidenceRefs = dedupe(append(evIDs(f), evIDs(vaF)...))
	case c.ByRule("ConfigRefNotFound") != nil && matchAny(msg, "configmap", "secret", "not found"):
		cfgF := c.ByRule("ConfigRefNotFound")
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfObjectAndEvent
		r.ConfidenceBasis = "Pod 引用的配置对象在快照中确实不存在（引用名来自 spec），并有挂载失败事件印证"
		r.RootCause = fmt.Sprintf("挂载失败因引用的配置对象不存在：%s", cfgF.Summary)
		if edge, ok := c.CausalEdge(cfgF, f, "被引用的 ConfigMap/Secret 不存在，挂载来源为空"); ok {
			edge.Confidence = model.RootCauseConfirmed
			r.CausalEdges = append(r.CausalEdges, edge)
		}
		r.EvidenceRefs = dedupe(append(evIDs(f), evIDs(cfgF)...))
	case hasEv && matchAny(msg, "timed out", "timeout", "unable to attach", "not attached"):
		r.Status = model.RootCauseConfirmed
		r.Confidence = ConfEventMessage
		r.ConfidenceBasis = "kubelet 挂载事件明确写出附加/挂载超时"
		r.RootCause = fmt.Sprintf("卷挂载超时（%s）", clip(msg, 220))
		r.EvidenceGaps = append(r.EvidenceGaps, "未确认 CSI 组件与云盘侧状态，超时上游原因待查")
	case hasEv:
		r.Status = model.RootCauseProbable
		r.Confidence = ConfEventMessage
		r.ConfidenceBasis = "有 FailedMount 事件消息，但未匹配到已知的卷异常类别"
		r.RootCause = fmt.Sprintf("Pod 挂载卷失败：%s", clip(msg, 220))
		r.EvidenceGaps = append(r.EvidenceGaps, "事件消息不属于已知类别，需人工核对卷对象状态")
	default:
		r.Status = model.RootCauseProbable
		r.Confidence = ConfClue
		r.ConfidenceBasis = "仅有规则命中的挂载失败事实，未取到事件消息"
		r.RootCause = fmt.Sprintf("Pod %s 卷挂载失败（未采集到 FailedMount 事件细节）", resLabel(ref))
		r.EvidenceGaps = append(r.EvidenceGaps, "未采集到 FailedMount 事件，无法区分是未绑定、未附加还是配置对象缺失")
	}
	return r
}

// evictedDiagnoser 处理 Pod 被节点压力驱逐的场景（根因在节点，不在 Pod）。
type evictedDiagnoser struct{}

func (evictedDiagnoser) ID() string { return "POD_EVICTED" }

func (evictedDiagnoser) Scenario() string { return "Pod 被节点压力驱逐（Evicted）" }

func (evictedDiagnoser) Match(c *Case) bool { return c.HasRule("EvictedPod") }

func (evictedDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("EvictedPod")
	p := c.PodOf(*f)
	ref := podRefOr(f.Resource, p)
	r := Result{
		AnchorFinding: f.ID,
		Mechanism:     "节点触发磁盘/内存/PID 压力阈值后，kubelet 按驱逐策略终止 Pod 回收资源，Pod 留下 Evicted 记录",
		Impact:        "该副本被移除；节点持续压力时会连续驱逐多个 Pod，表现为批量重启与容量下降",
		EvidenceRefs:  evIDs(f),
		Investigation: []model.Command{
			inv(fmt.Sprintf("kubectl %sdescribe pod %s", nsArg(ref.Namespace), ref.Name)),
			inv("kubectl describe node <所在节点名>"),
		},
		AlternativeCauses: []string{"Pod 自身内存超出 limits 触发 OOM（应看容器 OOMKilled 而非 Evicted）", "节点被人工排水（cordon+drain）"},
		EvidenceGaps:      []string{"驱逐原因取自 Pod status/事件文本，未采集节点上实际的磁盘与内存水位"},
		RemediationDirection: "按节点侧压力处理（清理磁盘、扩容节点、下调该节点 Pod 的请求量），而不是反复重建被驱逐的 Pod；" +
			"重要业务应补 PodDisruptionBudget 与多节点打散",
		RemediationText: "先让节点压力消失（清理或扩容），再确认被驱逐的 Pod 已被控制器重建到健康节点。",
		Verification: []model.Command{
			ver(fmt.Sprintf("kubectl %sget pod %s -o jsonpath='{.status.phase} {.status.reason}'", nsArg(ref.Namespace), ref.Name)),
		},
	}
	impact := []string{resLabel(ref) + "（Evicted）"}
	if p != nil && p.NodeName != "" {
		impact = append(impact, "Node/"+p.NodeName+"（压力驱逐来源）")
		impact = append(impact, impactChainForPod(c, p)[1:]...)
		if pressure := c.Node(p.NodeName); pressure != nil {
			for _, t := range []string{"DiskPressure", "MemoryPressure", "PIDPressure"} {
				if cond, ok := condTrue(pressure.Conditions, t); ok {
					r.Status = model.RootCauseConfirmed
					r.Confidence = ConfObjectAndEvent
					r.ConfidenceBasis = fmt.Sprintf("节点 %s 的 %s=%s（状态字段）与 Pod 的 Evicted 记录同时存在", pressure.Ref.Name, cond.Type, cond.Status)
					if nf := c.FindingOn(pressure.Ref, ""); nf != nil {
						r.EvidenceRefs = dedupe(append(r.EvidenceRefs, evIDs(nf)...))
						if edge, ok := c.CausalEdge(nf, f, "节点资源压力触发 kubelet 驱逐该 Pod"); ok {
							edge.Confidence = model.RootCauseConfirmed
							r.CausalEdges = append(r.CausalEdges, edge)
						}
					}
					r.RootCause = fmt.Sprintf("Pod %s 因节点 %s 触发 %s 被 kubelet 驱逐", resLabel(ref), pressure.Ref.Name, cond.Type)
					break
				}
			}
		}
	}
	r.ImpactChain = dedupe(impact)
	if r.Status == "" {
		r.Status = model.RootCauseProbable
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "Pod 状态字段写明 Evicted，但节点侧压力状态未在本次快照中确认"
		r.RootCause = fmt.Sprintf("Pod %s 已被驱逐（status.reason=Evicted），节点侧压力来源尚未确认", resLabel(ref))
		r.EvidenceGaps = append(r.EvidenceGaps, "所在节点不在快照中或未报压力状态，无法确认驱逐的具体压力类型")
	}
	return r
}

// probeFailingDiagnoser 处理存活/就绪探针持续失败（服务在跑但被认为不健康）。
type probeFailingDiagnoser struct{}

func (probeFailingDiagnoser) ID() string { return "POD_PROBE_FAILING" }

func (probeFailingDiagnoser) Scenario() string {
	return "探针持续失败导致 Pod 不健康 / 摘流"
}

func (probeFailingDiagnoser) Match(c *Case) bool {
	return c.HasRule("Unhealthy") || c.HasRule("ContainerHealthTimeout")
}

func (probeFailingDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("Unhealthy")
	if f == nil {
		f = c.ByRule("ContainerHealthTimeout")
	}
	p := c.PodOf(*f)
	ref := podRefOr(f.Resource, p)
	ev, hasEv := c.EventWith(ref, "Unhealthy", "Liveness probe failed", "Readiness probe failed", "Startup probe failed")

	r := Result{
		AnchorFinding: f.ID,
		Mechanism:     "探针持续失败：就绪探针失败让 Pod 被摘出 Endpoint，存活探针失败让 kubelet 重启容器",
		Impact:        fmt.Sprintf("Pod %s 不承载流量或被反复重启；全部副本如此时 Service 无可用 Endpoint", resLabel(ref)),
		EvidenceRefs:  evIDs(f),
		ImpactChain:   impactChainForPod(c, p),
		Investigation: []model.Command{
			inv(fmt.Sprintf("kubectl %sdescribe pod %s", nsArg(ref.Namespace), ref.Name)),
			inv(fmt.Sprintf("kubectl %sget pod %s -o jsonpath='{.spec.containers[*].readinessProbe}{\"\\n\"}{.spec.containers[*].livenessProbe}'", nsArg(ref.Namespace), ref.Name)),
			inv(fmt.Sprintf("kubectl %sexec %s -- sh -c 'wget -qO- http://127.0.0.1:<容器端口><探针路径>'", nsArg(ref.Namespace), ref.Name)),
		},
		AlternativeCauses: []string{"探针路径/端口写错（配置问题）", "应用启动时间超过 startupProbe 容忍窗口", "依赖慢导致接口超时", "节点网络策略拦截了本机探针（少见）"},
		Verification: []model.Command{
			ver(fmt.Sprintf("kubectl %sget pod %s -o jsonpath='{.status.containerStatuses[*].ready}'", nsArg(ref.Namespace), ref.Name)),
		},
	}
	msg := ev.Message
	_, evtRefused := c.EventWith(ref, "connection refused")
	switch {
	case evtRefused:
		r.Status = model.RootCauseProbable
		r.Confidence = ConfEventMessage
		r.ConfidenceBasis = "探针失败事件写明 connection refused，说明容器内该端口没有监听；但端口为何没起来仍需日志确认"
		r.RootCause = fmt.Sprintf("探针连不上容器端口（connection refused）：%s", clip(msg, 220))
		r.EvidenceGaps = append(r.EvidenceGaps, "未确认容器内服务进程是否启动、监听端口与探针端口是否一致")
		r.RemediationDirection = "对齐探针端口/路径与容器实际监听：先 exec 进容器手工访问探针地址，再决定改探针配置还是修应用"
	case hasEv && matchAny(msg, "timeout", "deadline exceeded", "context deadline"):
		r.Status = model.RootCauseProbable
		r.Confidence = ConfEventMessage
		r.ConfidenceBasis = "探针超时类事件；超时上游原因（慢依赖/慢启动/GC）无法从只读采集判定"
		r.RootCause = fmt.Sprintf("探针请求超时：%s", clip(msg, 220))
		r.EvidenceGaps = append(r.EvidenceGaps, "缺少应用响应耗时与依赖状态证据，无法确认慢在哪一层")
		r.RemediationDirection = "先确认是应用真慢还是探针阈值太紧：真慢就修依赖/加超时，阈值太紧则调整 timeoutSeconds 与 failureThreshold"
	case hasEv:
		r.Status = model.RootCauseProbable
		r.Confidence = ConfObjectStatus
		r.ConfidenceBasis = "有探针失败事件消息，但内容不足以确定失败类别"
		r.RootCause = fmt.Sprintf("探针持续失败：%s", clip(msg, 220))
		r.EvidenceGaps = append(r.EvidenceGaps, "探针返回内容不属于已知类别，需人工核对探针配置")
	default:
		r.Status = model.RootCauseProbable
		r.Confidence = ConfClue
		r.ConfidenceBasis = "规则命中探针异常，但快照里没有对应事件消息"
		r.RootCause = fmt.Sprintf("Pod %s 探针异常（未采集到探针失败事件细节）", resLabel(ref))
		r.EvidenceGaps = append(r.EvidenceGaps, "未采集到探针失败事件，无法区分 refused / timeout / 非 200 返回")
	}
	r.RemediationText = "修复动作要么改探针配置使其匹配应用真实健康检查方式，要么修应用让探针路径恢复；" +
		"livenessProbe 在依赖抖动时会放大故障，必要时先只保留 readinessProbe。"
	return r
}

// configRefMissingDiagnoser 处理 Pod 引用了不存在的 ConfigMap/Secret。
type configRefMissingDiagnoser struct{}

func (configRefMissingDiagnoser) ID() string { return "WORKLOAD_CONFIG_REF_MISSING" }

func (configRefMissingDiagnoser) Scenario() string { return "引用的 ConfigMap/Secret 不存在" }

func (configRefMissingDiagnoser) Match(c *Case) bool { return c.HasRule("ConfigRefNotFound") }

func (configRefMissingDiagnoser) Diagnose(c *Case) Result {
	f := c.ByRule("ConfigRefNotFound")
	r := Result{
		AnchorFinding:   f.ID,
		Status:          model.RootCauseConfirmed,
		Confidence:      ConfObjectStatus,
		ConfidenceBasis: "引用名来自 Pod spec（真实引用字段），且本次快照内确认同名对象不存在",
		RootCause:       f.Summary,
		Mechanism:       "Pod spec 引用了快照中不存在的 ConfigMap/Secret，kubelet 无法构造卷或环境变量，Pod 停在创建阶段",
		Impact:          "该 Pod 无法启动；若是滚动更新，新版本会全部起不来",
		EvidenceRefs:    evIDs(f),
		Investigation: []model.Command{
			inv(fmt.Sprintf("kubectl %sget pod %s -o jsonpath='{.spec.volumes[*].configMap.name}{\"\\n\"}{.spec.containers[*].envFrom[*].configMapRef.name}'", nsArg(f.Resource.Namespace), f.Resource.Name)),
			inv(fmt.Sprintf("kubectl %sget configmap,secret", nsArg(f.Resource.Namespace))),
		},
		AlternativeCauses: []string{"对象被其他流程删除（改名/清理任务）", "部署时漏发配置清单", "命名空间写错，配置发到了别处"},
		EvidenceGaps:      []string{"Secret 仅核对名称与键集合，未读取内容值，无法判断键是否齐全"},
		RemediationDirection: "补齐缺失的 ConfigMap/Secret（或把引用改回已存在的名字），再让 Pod 重建；" +
			"如果是发布流程漏发配置，要修 CI 而不是手工建对象，否则下次发布还会复现",
		RemediationText: "按引用名创建缺失对象并确认键集合满足应用要求；确认来源后再决定是否删除手工创建的对象。",
		Verification: []model.Command{
			ver(fmt.Sprintf("kubectl %sget pod %s -o wide", nsArg(f.Resource.Namespace), f.Resource.Name)),
		},
	}
	if p := c.PodOf(*f); p != nil {
		r.ImpactChain = impactChainForPod(c, p)
	}
	if mount := c.ByRule("FailedMount"); mount != nil {
		if edge, ok := c.CausalEdge(f, mount, "被引用的配置对象不存在，挂载来源为空"); ok {
			edge.Confidence = model.RootCauseConfirmed
			r.CausalEdges = append(r.CausalEdges, edge)
		}
	}
	return r
}
