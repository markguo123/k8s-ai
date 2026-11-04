// Package service 是唯一的应用编排层：CLI/Server/CronJob 都复用
// ScanService.Run（ADR-007）。业务逻辑不依赖 CLI（AGENTS.md）。
package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/correlation"
	"github.com/k8s-ai/k8s-ai/internal/deterministic"
	"github.com/k8s-ai/k8s-ai/internal/diagnosis"
	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/history"
	"github.com/k8s-ai/k8s-ai/internal/incident"
	"github.com/k8s-ai/k8s-ai/internal/kubernetes"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/relations"
	"github.com/k8s-ai/k8s-ai/internal/report"
	"github.com/k8s-ai/k8s-ai/internal/rule"
	"github.com/k8s-ai/k8s-ai/internal/scanner"
	"github.com/k8s-ai/k8s-ai/internal/version"
)

// ScanService 是共享的应用服务契约。
type ScanService interface {
	// Run 执行一次完整扫描并返回结果。
	Run(ctx context.Context, opts model.ScanOptions) (*model.ScanResult, error)
	// Validate 校验配置与集群连通性。
	Validate(ctx context.Context, opts model.ScanOptions) (string, error)
}

type scanService struct {
	engine    rule.Engine
	registry  rule.Registry
	newClient func(model.ScanOptions) (*kubernetes.Client, error)
	newLLM    func(model.LLMOptions) llm.LLMClient
	// archive 非 nil 时（平台装配注入，见 ReportArchiveBinder）扫描产物额外归档到对象
	// 存储。CLI 单跑不设这个字段，行为与三期一致。
	archive *ReportArchiveWriter
}

// defaultLLMConcurrency 与 diagnosis.llmConcurrency 保持一致（CONCURRENCY.md）。
// 单独定义以避免 service 包依赖 diagnosis 包内部常量。
const defaultLLMConcurrency = 2

// New 返回生产 ScanService（注册一期全部默认规则）。
func New() ScanService {
	return newService(
		func(opts model.ScanOptions) (*kubernetes.Client, error) {
			return kubernetes.NewClient(clientOptions(opts))
		},
		realLLM,
	)
}

// newService 允许测试注入客户端工厂。
func newService(factory func(model.ScanOptions) (*kubernetes.Client, error), llmFactory func(model.LLMOptions) llm.LLMClient) ScanService {
	reg := rule.NewRegistry()
	for _, rw := range rule.AllDefaultWithMeta() {
		if err := reg.RegisterWithMeta(rw.Rule, rw.Meta); err != nil {
			// 元数据兜底由 normalizeMeta 保证合法，此处不应失败；仍做防御性处理。
			reg.Register(rw.Rule)
		}
	}
	return &scanService{engine: rule.NewEngine(reg), registry: reg, newClient: factory, newLLM: llmFactory}
}

// realLLM 用模型选项构造生产 LLM 客户端。
func realLLM(o model.LLMOptions) llm.LLMClient {
	return llm.New(llm.Options{
		Endpoint:    o.Endpoint,
		APIKey:      o.APIKey,
		Model:       o.Model,
		Temperature: o.Temperature,
		MaxTokens:   o.MaxTokens,
		Timeout:     o.Timeout,
		Thinking:    o.Thinking,
	})
}

// Run 编排：连通性校验 → Phase1 采集 → 关联索引 → 规则引擎 →
// Phase2 深度采集（日志）→ 证据挂载 → 结果组装。
func (s *scanService) Run(ctx context.Context, opts model.ScanOptions) (*model.ScanResult, error) {
	prog := opts.Progress // 允许 nil（静默）
	// 阶段账：终端进度事件和报告里的「诊断信息 → 执行阶段」出自同一份记录。
	rec := newStageRecorder(prog)
	phaseStart := rec.start
	phaseDone := rec.done

	// 总预算：scan.timeout 控制整次扫描时长（CONCURRENCY.md §2）。
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	started := time.Now()

	connStart := phaseStart(stageConnect, "连接 Kubernetes 集群并校验连通性")
	client, err := s.newClient(opts)
	if err != nil {
		phaseDone(stageConnect, "", false, connStart)
		return nil, err
	}
	serverVersion, err := client.ServerVersion(ctx)
	if err != nil {
		phaseDone(stageConnect, err.Error(), false, connStart)
		return nil, fmt.Errorf("connect to cluster: %w", err)
	}
	phaseDone(stageConnect, serverVersionLabel(serverVersion), true, connStart)

	// Phase1：全量轻量 list（无 N+1）。
	p1Start := phaseStart(stageCollect, "资源采集：集群资源全量 list（无 N+1）")
	collector := scanner.New(client)
	snap, err := collector.Phase1(ctx, opts)
	if err != nil {
		phaseDone(stageCollect, err.Error(), false, p1Start)
		return nil, err
	}
	snap.ServerVersion = serverVersion
	phaseDone(stageCollect, fmt.Sprintf("namespaces=%d pods=%d nodes=%d",
		len(snap.Namespaces), len(snap.Pods), len(snap.Nodes)), true, p1Start)

	// P3 关联索引 → P4 规则引擎：自动判定异常 Finding。
	corrStart := phaseStart(stageRules, "规则引擎：关联索引 + 规则评估")
	idx := correlation.Build(snap)
	findings := s.engine.Evaluate(ctx, snap, idx, opts)
	phaseDone(stageRules, fmt.Sprintf("%d 条 Finding", len(findings)), true, corrStart)
	slog.Info("scan: Phase1 采集完成", "namespaces", len(snap.Namespaces), "pods", len(snap.Pods), "findings", len(findings))

	// 单 Pod 目标扫描：只保留该 Pod 及其直接关联资源（owner/Node/PVC/Service）的 Finding。
	if opts.PodTarget != "" {
		findings = filterFindingsForPod(idx, findings, opts)
	}

	// Phase2：只对需要日志的异常 Pod 深度采集，并把日志挂到 Finding 证据。
	var logTargets []model.ResourceRef
	if opts.CollectLogs {
		logTargets = rule.LogTargets(s.registry, findings, idx)
		p2Start := phaseStart(stageEvidence, fmt.Sprintf("按需证据采集：容器日志 %d 个目标 Pod", len(logTargets)))
		if len(logTargets) > 0 {
			if err := collector.Phase2(ctx, snap, logTargets, opts); err != nil {
				// 根 ctx 取消时提前返回；其余失败已记入 collection_errors。
				if ctx.Err() != nil {
					phaseDone(stageEvidence, err.Error(), false, p2Start)
					return nil, ctx.Err()
				}
			}
			attachLogEvidence(findings, idx, s.registry)
		}
		phaseDone(stageEvidence, fmt.Sprintf("log targets=%d errors=%d",
			len(logTargets), len(snap.CollectionErrors)), true, p2Start)
	} else {
		rec.skip(stageEvidence, "本次档位不采集容器日志（collect_logs=false）")
	}

	// 资源关系分析（架构图第 3 阶段）：只按 OwnerReference / Selector /
	// Spec·Status 字段里的真实引用建边，后面所有因果判断都只能走这张图。
	relStart := phaseStart(stageRelations, "资源关系分析：OwnerReference/Selector/Spec 建图")
	graph := relations.Build(snap, idx)
	phaseDone(stageRelations, fmt.Sprintf("%d 条关系边（类型 %d 种）", graph.EdgeCount(), len(graph.KindCounts())), true, relStart)

	// 关联分析（架构图第 4 阶段）：把关联资源的异常证据补到上游 Finding 上。
	// 必须在 result 拷贝前执行，保证报告证据与 LLM 引用编号一致。
	enrichStart := phaseStart(stageCorrelate, "关联分析：补全关联资源证据")
	enrichRelatedEvidence(findings, idx)
	phaseDone(stageCorrelate, fmt.Sprintf("findings=%d", len(findings)), true, enrichStart)

	// Finding → Correlation → Incident：同一故障链聚合为一个诊断单元；
	// 健康评分只统计根因（派生症状不重复扣分）。
	// 故障聚合（架构图第 5 阶段）：因果边只认关系图上真实存在的路径。
	incStart := phaseStart(stageIncident, "故障聚合：Finding → Incident → 因果链")
	findingsV := derefFindings(findings)
	incs := incident.Build(findingsV, idx)
	incident.Relate(findingsV, incs, graph, idx)
	incident.AssignRoles(findings, incs)
	phaseDone(stageIncident, fmt.Sprintf("incidents=%d 因果边=%d", len(incs), countCausalEdges(incs)), true, incStart)

	// 采集质量账：放在深度采集之后统计，日志目标的成败一起算进来。
	// 这条记录决定健康评分的封顶——采集不完整时不允许报「集群健康」。
	collection := scanner.CollectionQuality(snap, opts, logTargets)

	// 确定性诊断（架构图第 6 阶段）：场景化诊断器只用已采事实推根因，不调模型。
	detStart := phaseStart(stageDeterministic, "确定性诊断：场景化诊断器匹配")
	detDiags := deterministic.NewEngine(snap, idx, graph, findingsV).Apply(incs)
	detByIncident := indexByIncident(detDiags)
	phaseDone(stageDeterministic, fmt.Sprintf("%d/%d 个故障链给出确定性结论", len(detDiags), len(incs)), true, detStart)

	result := &model.ScanResult{
		Meta: model.ScanMeta{
			ToolVersion:   version.Version,
			Cluster:       opts.Context,
			ServerVersion: serverVersion,
			ScanStartedAt: started.UTC().Format(time.RFC3339),
			Namespace:     opts.Namespace,
			Pod:           opts.PodTarget,
		},
		Summary:          summaryFromSnapshot(snap, opts),
		Findings:         derefFindings(findings),
		HealthScore:      report.ComputeHealthScoreWithQuality(rootOnlyFindings(findingsV, incs), collection),
		Incidents:        incs,
		LLMSummary:       model.LLMSummary{Enabled: opts.LLM.Enabled},
		Components:       snap.Components,
		CollectionErrors: snap.CollectionErrors,
		SchemaVersion:    model.ReportSchemaVersion,
		Collection:       collection,
	}
	// 1.2：历史对比——读取上一份 latest.json，按指纹输出新增/持续/恢复
	// （人读日报为次要产出；主要消费方是二期 Agent 的记忆契约，ADR-019）。
	if h, err := history.LoadPrevious(opts.ReportDirectory); err != nil {
		slog.Warn("scan: 读取历史报告失败", "error", err)
	} else if h != nil {
		result.History = history.Compare(h, derefFindings(findings))
	}

	// P7：LLM 诊断（预算裁剪 → 校验 → 降级），单个/全部失败都不中断 scan。
	var (
		llmDiags   []model.Diagnosis
		llmSummary = model.LLMSummary{Enabled: opts.LLM.Enabled}
	)
	if opts.LLM.Enabled && len(incs) > 0 {
		llmOpts := opts.LLM // copy，避免把 ScanProgress 塞进 LLMProgress 时改到原 opts
		// 把上层 ScanProgress 桥接到 LLMProgress：Incident 粒度事件走 PhaseSub，
		// LongTask 也走 PhaseSub（detail 里带"仍在执行…"文案）。
		if prog != nil && llmOpts.Progress == nil {
			llmOpts.Progress = &scanProgressLLMBridge{scan: prog}
		}
		llmStart := phaseStart(stageLLM, fmt.Sprintf("LLM 增强诊断：%d 个 Incident（并发 %d）", len(incs), defaultLLMConcurrency))
		slog.Info("scan: LLM 诊断开始", "incidents", len(incs))
		findingByID := map[string]model.Finding{}
		for _, f := range findingsV {
			findingByID[f.ID] = f
		}
		// 增强上下文：模型拿到的是「程序已知事实 + 待解释的部分」，而不是一条 Finding 让它重猜。
		diagCtx := &diagnosis.Context{
			Snapshot:      snap,
			Index:         idx,
			Graph:         graph,
			Deterministic: detByIncident,
		}
		llmDiags, llmSummary, err = diagnosis.NewWithContext(llmOpts, diagCtx).
			DiagnoseIncidents(ctx, incs, findingByID, s.newLLM(llmOpts))
		if err != nil {
			phaseDone(stageLLM, err.Error(), false, llmStart)
			return nil, err
		}
		result.LLMSummary = llmSummary
		ok := llmSummary.Failed == 0
		phaseDone(stageLLM, fmt.Sprintf("calls=%d failed=%d degraded=%d rejected=%d",
			llmSummary.Calls, llmSummary.Failed, llmSummary.Degraded, llmSummary.Rejected), ok, llmStart)
		slog.Info("scan: LLM 诊断完成", "calls", llmSummary.Calls, "failed", llmSummary.Failed, "degraded", llmSummary.Degraded)
	} else {
		switch {
		case !opts.LLM.Enabled:
			rec.skip(stageLLM, "未启用 LLM：本次结论全部来自确定性诊断")
		default:
			rec.skip(stageLLM, "无故障链，无需诊断")
		}
	}

	// 结果校验（架构图第 8 阶段）：合并确定性与 LLM 两条结论线，并做自洽底线检查。
	// 放在报告之前——报告只允许写校验后的结论。
	valStart := phaseStart(stageValidate, "结果校验：结论一致性 + 诊断账")
	merged, diagSummary := mergeDiagnoses(incs, detByIncident, llmDiags, llmSummary, opts.LLM.Enabled)
	applyDiagnosesToIncidents(incs, merged)
	result.Diagnoses = merged
	diagSummary.RuleFindings = len(result.Findings)
	result.DiagnosisSummary = &diagSummary
	result.LLMSummary.Mode = diagSummary.Mode
	phaseDone(stageValidate, diagnosisAccountLine(diagSummary), true, valStart)

	// 诊断完成后记录结束时间：Duration 包含 LLM 耗时（体检 P0-3 修复）。
	result.Meta.ScanEndedAt = time.Now().UTC().Format(time.RFC3339)
	result.Meta.Duration = time.Since(started).String()

	// 报告生成（架构图第 9 阶段）：结论 / 资源健康 / 处置建议全部由程序从既有事实推导。
	repStart := phaseStart(stageReport, "报告生成：结论 + 资源健康 + 处置建议")
	result.Conclusion = report.BuildConclusion(result)
	result.Statistics = report.DomainHealthList(result)
	result.Remediation = report.RemediationItems(result)
	phaseDone(stageReport, fmt.Sprintf("结论=%s 健康域=%d 处置项=%d",
		conclusionLevelText(result), len(result.Statistics), len(result.Remediation)), true, repStart)
	result.Stages = rec.records()

	// 报告落盘：none 不写文件；latest/daily 由 Writer 写出（FR-019）。
	if opts.ReportMode != "" && opts.ReportMode != "none" {
		wrStart := phaseStart(stageWrite, "写入报告文件")
		paths, err := report.NewWriter(opts.ReportDirectory).Write(result, model.ReportOptions{
			Directory: opts.ReportDirectory,
			Format:    opts.ReportFormat,
			Mode:      opts.ReportMode,
		})
		if err != nil {
			phaseDone("write", err.Error(), false, wrStart)
			return nil, fmt.Errorf("write report: %w", err)
		}
		result.ReportPaths = paths
		phaseDone("write", strings.Join(paths, ", "), true, wrStart)
	} else {
		rec.skip(stageWrite, "report-mode=none：不落盘本地报告")
	}
	// 归档到对象存储：与上面的 report.directory 落盘互不相干（本地文件只是兼容副本，
	// 事实源是 objects + reports），失败只记 WARN，不影响本次扫描的返回值。
	s.archiveReportsIfNeeded(ctx, result, opts, phaseStart, phaseDone)
	// 阶段账最后刷一次：把落盘/归档耗时也算进去。
	result.Stages = rec.records()
	return result, nil
}

// scanProgressLLMBridge 把 LLMProgress 事件桥接到 ScanProgress.PhaseSub，
// 这样 Diagnoser 包无需知道上层 ScanProgress 实现（分层依然严格）。
type scanProgressLLMBridge struct {
	scan model.ScanProgress
}

func (b *scanProgressLLMBridge) IncidentStarted(id string, index, total int) {
	b.scan.PhaseSub("llm", index, total, "incident "+shortID(id))
}
func (b *scanProgressLLMBridge) IncidentDone(id string, index, total int, llmUsed bool, err error) {
	tag := "ok"
	switch {
	case err != nil:
		tag = "error"
	case !llmUsed:
		tag = "degraded"
	}
	b.scan.PhaseSub("llm", index, total, "incident "+shortID(id)+" "+tag)
}
func (b *scanProgressLLMBridge) LongTask(id string, index, total int, elapsed time.Duration) {
	b.scan.PhaseSub("llm", index, total, fmt.Sprintf(
		"incident %s 仍在诊断中（已 %s，常见耗时 10-30s，请稍候）", shortID(id), truncateDuration(elapsed)))
}

func shortID(id string) string {
	if len(id) <= 10 {
		return id
	}
	return id[:10]
}

func truncateDuration(d time.Duration) string {
	d = d.Round(time.Second)
	return d.String()
}

// attachLogEvidence 把 Phase2 采集的日志证据按 Pod 挂到对应 Finding（FR-014）。
func attachLogEvidence(findings []*model.Finding, idx *model.CorrelationIndex, reg rule.Registry) {
	for _, f := range findings {
		r := reg.ByName(f.Rule)
		if r == nil || !r.NeedsLogs() {
			continue
		}
		// Pod Finding 直接取自身日志；非 Pod（如 Job）反查下属 Pod 日志。
		var pods []*model.PodInfo
		if f.Resource.Kind == "Pod" {
			if p := idx.Pod(f.Resource.Key()); p != nil {
				pods = append(pods, p)
			}
		} else {
			pods = idx.PodsOfWorkload(f.Resource.Key())
		}
		added := false
		for _, p := range pods {
			for _, cl := range p.Logs {
				f.Evidence = append(f.Evidence, evidence.Log(cl)...)
				added = true
			}
		}
		if added {
			// 重新排序编号，保证证据链稳定（日志不影响指纹签名）。
			f.Evidence = evidence.AssignIDs(f.Evidence)
		}
	}
}

// Validate 仅做配置与连通性校验，不发任何写请求。
func (s *scanService) Validate(ctx context.Context, opts model.ScanOptions) (string, error) {
	client, err := s.newClient(opts)
	if err != nil {
		return "", err
	}
	serverVersion, err := client.ServerVersion(ctx)
	if err != nil {
		return "", fmt.Errorf("connect to cluster: %w", err)
	}
	return serverVersion, nil
}

// summaryFromSnapshot 按扫描作用域汇总资源计数，供控制台摘要与报告"资源汇总"节使用。
// 命名空间/Pod 体检不能把 Collector 顺带采到的集群级对象冒充成报告可见范围。
func summaryFromSnapshot(snap *model.ClusterSnapshot, opts model.ScanOptions) model.ClusterSummary {
	events := 0
	for _, list := range snap.EventsIndex {
		events += len(list)
	}
	summary := model.ClusterSummary{
		Namespaces:          len(snap.Namespaces),
		Pods:                len(snap.Pods),
		Nodes:               len(snap.Nodes),
		Services:            len(snap.Services),
		EndpointSlices:      len(snap.EndpointSlices),
		Workloads:           len(snap.Workloads),
		Storage:             len(snap.Storage),
		Ingresses:           len(snap.Ingresses),
		NetworkPolicies:     len(snap.NetworkPolicies),
		Roles:               len(snap.Roles),
		RoleBindings:        len(snap.RoleBindings),
		ClusterRoles:        len(snap.ClusterRoles),
		ClusterRoleBindings: len(snap.ClusterRoleBindings),
		Components:          len(snap.Components),
		Events:              events,
	}
	if opts.Namespace != "" {
		summary.Namespaces = 1
		summary.Nodes = 0
		summary.Storage = countStorageKind(snap.Storage, "PVC")
		summary.ClusterRoles = 0
		summary.ClusterRoleBindings = 0
		summary.Components = 0
	}
	if opts.PodTarget != "" {
		summary.Pods = 1
	}
	return summary
}

func countStorageKind(items []model.StorageInfo, kind string) int {
	count := 0
	for i := range items {
		if items[i].Ref.Kind == kind {
			count++
		}
	}
	return count
}

// clientOptions 把 ScanOptions 转换为 kubernetes 客户端参数。
func clientOptions(opts model.ScanOptions) kubernetes.Options {
	return kubernetes.Options{
		Kubeconfig: opts.Kubeconfig,
		Context:    opts.Context,
		InCluster:  opts.InCluster,
		Timeout:    opts.RequestTimeout,
		QPS:        opts.QPS,
		Burst:      opts.Burst,
	}
}

// derefFindings 把指针切片转换为值切片（ScanResult 契约）。
func derefFindings(in []*model.Finding) []model.Finding {
	out := make([]model.Finding, 0, len(in))
	for _, f := range in {
		out = append(out, *f)
	}
	return out
}

// filterFindingsForPod 保留目标 Pod 及其直接关联资源的 Finding，
// 让单 Pod 报告聚焦"这个 Pod 为什么异常"，而不是整集群的问题列表。
func filterFindingsForPod(idx *model.CorrelationIndex, findings []*model.Finding, opts model.ScanOptions) []*model.Finding {
	keep := map[string]bool{}
	podRef := model.ResourceRef{Kind: "Pod", Namespace: opts.Namespace, Name: opts.PodTarget}
	keep[podRef.Key()] = true
	if p := idx.Pod(podRef.Key()); p != nil {
		for _, w := range idx.OwnerChain(p) {
			keep[w.Ref.Key()] = true
		}
		if p.NodeName != "" {
			keep[model.ResourceRef{Kind: "Node", Name: p.NodeName}.Key()] = true
		}
		for _, s := range idx.ServicesOfPod(podRef.Key()) {
			keep[s.Ref.Key()] = true
		}
		for _, st := range idx.StorageChain(p) {
			keep[st.Ref.Key()] = true
		}
	}
	var out []*model.Finding
	for _, f := range findings {
		if keep[f.Resource.Key()] {
			out = append(out, f)
		}
	}
	return out
}

// enrichRelatedEvidence 为 Workload/Service/Node 类 Finding 追加关联异常 Pod 的
// 派生证据（状态 + 日志关键行），帮助 LLM 跨资源定位根因。
func enrichRelatedEvidence(findings []*model.Finding, idx *model.CorrelationIndex) {
	podFinding := map[string]model.Finding{}
	for _, f := range findings {
		if f.Resource.Kind == "Pod" {
			podFinding[f.Resource.Key()] = *f
		}
	}
	appendAffected := func(f *model.Finding, pods []*model.PodInfo) {
		for _, p := range pods {
			pf, ok := podFinding[p.Ref.Key()]
			if !ok {
				continue
			}
			keyLine := ""
			for _, e := range pf.Evidence {
				if e.Kind == model.EvLog {
					keyLine = evidence.KeyLogLine(e.Value)
					if keyLine != "" {
						break
					}
				}
			}
			detail := fmt.Sprintf("%s/%s：%s", p.Ref.Namespace, p.Ref.Name, pf.Title)
			if keyLine != "" {
				detail += "；日志关键行：" + keyLine
			}
			if names := configMapNames(p.ConfigMaps); names != "" {
				detail += "；ConfigMaps: " + names
			}
			if names := secretRefNames(p.SecretRefs); names != "" {
				detail += "；SecretRefs: " + names
			}
			f.Evidence = append(f.Evidence, evidence.Derived("related", "affectedPod", detail))
			break // 取第一个异常 Pod 即可
		}
	}
	for _, f := range findings {
		switch f.Resource.Kind {
		case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob":
			appendAffected(f, idx.PodsOfWorkload(f.Resource.Key()))
		case "Service":
			appendAffected(f, idx.PodsOfService(f.Resource.Key()))
		case "Node":
			appendAffected(f, idx.PodsOfNode(f.Resource.Name))
		case "Pod":
			// 把 Pod 引用的 ConfigMap/Secret 名称（仅名称）作为派生证据，
			// 让 LLM 能给出 `kubectl edit configmap` / 检查 Secret 等针对性修复，
			// 而不是只给 restart（通用机制，不针对某个特例）。
			if p := idx.Pod(f.Resource.Key()); p != nil {
				if names := configMapNames(p.ConfigMaps); names != "" {
					f.Evidence = append(f.Evidence, evidence.Derived("related", "configMaps", names))
				}
				if names := secretRefNames(p.SecretRefs); names != "" {
					f.Evidence = append(f.Evidence, evidence.Derived("related", "secretRefs", names))
				}
			}
		}
	}
	// 补充证据后重新排序编号（日志不影响指纹签名）。
	for _, f := range findings {
		f.Evidence = evidence.AssignIDs(f.Evidence)
	}
}

// configMapNames 格式化 ConfigMap 引用（ns/name，逗号分隔）。
func configMapNames(refs []model.ResourceRef) string {
	return referencedNames(refs)
}

// secretRefNames 格式化 Secret 引用（ns/name，逗号分隔；仅名称不读数据）。
func secretRefNames(refs []model.ResourceRef) string {
	return referencedNames(refs)
}

func referencedNames(refs []model.ResourceRef) string {
	if len(refs) == 0 {
		return ""
	}
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, r.Namespace+"/"+r.Name)
	}
	return strings.Join(names, ", ")
}

// rootOnlyFindings 只保留 Incident 根因与独立 Finding（派生症状不参与健康评分）。
func rootOnlyFindings(findings []model.Finding, incs []model.Incident) []model.Finding {
	member := map[string]bool{}
	for _, inc := range incs {
		for _, m := range inc.Members {
			member[m.ID] = true
		}
	}
	var out []model.Finding
	for _, f := range findings {
		if !member[f.ID] {
			out = append(out, f)
		}
	}
	return out
}
