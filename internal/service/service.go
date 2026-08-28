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
	"github.com/k8s-ai/k8s-ai/internal/diagnosis"
	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/history"
	"github.com/k8s-ai/k8s-ai/internal/incident"
	"github.com/k8s-ai/k8s-ai/internal/kubernetes"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/model"
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
	for _, r := range rule.AllDefault() {
		reg.Register(r)
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
	})
}

// Run 编排：连通性校验 → Phase1 采集 → 关联索引 → 规则引擎 →
// Phase2 深度采集（日志）→ 证据挂载 → 结果组装。
func (s *scanService) Run(ctx context.Context, opts model.ScanOptions) (*model.ScanResult, error) {
	prog := opts.Progress // 允许 nil（静默）
	phaseStart := func(name, title string) time.Time {
		if prog != nil {
			prog.PhaseStarted(name, title)
		}
		return time.Now()
	}
	phaseDone := func(name, detail string, ok bool, started time.Time) {
		if prog != nil {
			prog.PhaseDone(name, detail, ok, time.Since(started))
		}
	}

	// 总预算：scan.timeout 控制整次扫描时长（CONCURRENCY.md §2）。
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	started := time.Now()

	connStart := phaseStart("connect", "连接 Kubernetes 集群并校验连通性")
	client, err := s.newClient(opts)
	if err != nil {
		phaseDone("connect", "", false, connStart)
		return nil, err
	}
	serverVersion, err := client.ServerVersion(ctx)
	if err != nil {
		phaseDone("connect", err.Error(), false, connStart)
		return nil, fmt.Errorf("connect to cluster: %w", err)
	}
	phaseDone("connect", "v"+serverVersion, true, connStart)

	// Phase1：全量轻量 list（无 N+1）。
	p1Start := phaseStart("phase1", "Phase1 集群资源全量采集")
	collector := scanner.New(client)
	snap, err := collector.Phase1(ctx, opts)
	if err != nil {
		phaseDone("phase1", err.Error(), false, p1Start)
		return nil, err
	}
	snap.ServerVersion = serverVersion
	phaseDone("phase1", fmt.Sprintf("namespaces=%d pods=%d nodes=%d",
		len(snap.Namespaces), len(snap.Pods), len(snap.Nodes)), true, p1Start)

	// P3 关联索引 → P4 规则引擎：自动判定异常 Finding。
	corrStart := phaseStart("rules", "关联索引 & 规则引擎评估")
	idx := correlation.Build(snap)
	findings := s.engine.Evaluate(ctx, snap, idx, opts)
	phaseDone("rules", fmt.Sprintf("%d 条 Finding", len(findings)), true, corrStart)
	slog.Info("scan: Phase1 采集完成", "namespaces", len(snap.Namespaces), "pods", len(snap.Pods), "findings", len(findings))

	// 单 Pod 目标扫描：只保留该 Pod 及其直接关联资源（owner/Node/PVC/Service）的 Finding。
	if opts.PodTarget != "" {
		findings = filterFindingsForPod(idx, findings, opts)
	}

	// Phase2：只对需要日志的异常 Pod 深度采集，并把日志挂到 Finding 证据。
	if opts.CollectLogs && len(findings) > 0 {
		targets := rule.LogTargets(s.registry, findings, idx)
		p2Title := fmt.Sprintf("Phase2 深度采集（日志）：%d 个目标 Pod", len(targets))
		p2Start := phaseStart("phase2", p2Title)
		if len(targets) > 0 {
			if err := collector.Phase2(ctx, snap, targets, opts); err != nil {
				// 根 ctx 取消时提前返回；其余失败已记入 collection_errors。
				if ctx.Err() != nil {
					phaseDone("phase2", err.Error(), false, p2Start)
					return nil, ctx.Err()
				}
			}
			attachLogEvidence(findings, idx, s.registry)
		}
		phaseDone("phase2", fmt.Sprintf("log targets=%d errors=%d",
			len(targets), len(snap.CollectionErrors)), true, p2Start)
	}

	// 关联证据补全：必须在 result 拷贝前执行，保证报告证据与 LLM 引用编号一致。
	enrichStart := phaseStart("enrich", "关联证据补全 & 故障链聚合")
	enrichRelatedEvidence(findings, idx)

	// Finding → Correlation → Incident：同一故障链聚合为一个诊断单元；
	// 健康评分只统计根因（派生症状不重复扣分）。
	findingsV := derefFindings(findings)
	incs := incident.Build(findingsV, idx)
	phaseDone("enrich", fmt.Sprintf("incidents=%d", len(incs)), true, enrichStart)

	result := &model.ScanResult{
		Meta: model.ScanMeta{
			ToolVersion:   version.Version,
			Cluster:       opts.Context,
			ServerVersion: serverVersion,
			ScanStartedAt: started.UTC().Format(time.RFC3339),
			Namespace:     opts.Namespace,
			Pod:           opts.PodTarget,
		},
		Summary:          summaryFromSnapshot(snap),
		Findings:         derefFindings(findings),
		HealthScore:      report.ComputeHealthScore(rootOnlyFindings(findingsV, incs)),
		Incidents:        incs,
		LLMSummary:       model.LLMSummary{Enabled: opts.LLM.Enabled},
		Components:       snap.Components,
		CollectionErrors: snap.CollectionErrors,
	}
	// 1.2：历史对比——读取上一份 latest.json，按指纹输出新增/持续/恢复
	// （人读日报为次要产出；主要消费方是二期 Agent 的记忆契约，ADR-019）。
	if h, err := history.LoadPrevious(opts.ReportDirectory); err != nil {
		slog.Warn("scan: 读取历史报告失败", "error", err)
	} else if h != nil {
		result.History = history.Compare(h, derefFindings(findings))
	}

	// P7：LLM 诊断（预算裁剪 → 校验 → 降级），单个/全部失败都不中断 scan。
	if opts.LLM.Enabled && len(incs) > 0 {
		llmOpts := opts.LLM // copy，避免把 ScanProgress 塞进 LLMProgress 时改到原 opts
		// 把上层 ScanProgress 桥接到 LLMProgress：Incident 粒度事件走 PhaseSub，
		// LongTask 也走 PhaseSub（detail 里带"仍在执行…"文案）。
		if prog != nil && llmOpts.Progress == nil {
			llmOpts.Progress = &scanProgressLLMBridge{scan: prog}
		}
		llmStart := phaseStart("llm", fmt.Sprintf("LLM 诊断：%d 个 Incident（并发 %d）", len(incs), defaultLLMConcurrency))
		slog.Info("scan: LLM 诊断开始", "incidents", len(incs))
		findingByID := map[string]model.Finding{}
		for _, f := range findingsV {
			findingByID[f.ID] = f
		}
		diagnoses, llmSummary, err := diagnosis.New(llmOpts).DiagnoseIncidents(ctx, incs, findingByID, s.newLLM(llmOpts))
		if err != nil {
			phaseDone("llm", err.Error(), false, llmStart)
			return nil, err
		}
		result.Diagnoses = diagnoses
		result.LLMSummary = llmSummary
		ok := llmSummary.Failed == 0
		phaseDone("llm", fmt.Sprintf("calls=%d failed=%d degraded=%d",
			llmSummary.Calls, llmSummary.Failed, llmSummary.Degraded), ok, llmStart)
		slog.Info("scan: LLM 诊断完成", "calls", llmSummary.Calls, "failed", llmSummary.Failed, "degraded", llmSummary.Degraded)
	}

	// 诊断完成后记录结束时间：Duration 包含 LLM 耗时（体检 P0-3 修复）。
	result.Meta.ScanEndedAt = time.Now().UTC().Format(time.RFC3339)
	result.Meta.Duration = time.Since(started).String()

	// 报告落盘：none 不写文件；latest/daily 由 Writer 写出（FR-019）。
	if opts.ReportMode != "" && opts.ReportMode != "none" {
		wrStart := phaseStart("report", "写入报告文件")
		paths, err := report.NewWriter(opts.ReportDirectory).Write(result, model.ReportOptions{
			Directory: opts.ReportDirectory,
			Format:    opts.ReportFormat,
			Mode:      opts.ReportMode,
		})
		if err != nil {
			phaseDone("report", err.Error(), false, wrStart)
			return nil, fmt.Errorf("write report: %w", err)
		}
		result.ReportPaths = paths
		phaseDone("report", strings.Join(paths, ", "), true, wrStart)
	}
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

// summaryFromSnapshot 汇总集群资源计数，供控制台摘要与报告"资源汇总"节使用。
func summaryFromSnapshot(snap *model.ClusterSnapshot) model.ClusterSummary {
	events := 0
	for _, list := range snap.EventsIndex {
		events += len(list)
	}
	return model.ClusterSummary{
		Namespaces:      len(snap.Namespaces),
		Pods:            len(snap.Pods),
		Nodes:           len(snap.Nodes),
		Services:        len(snap.Services),
		EndpointSlices:  len(snap.EndpointSlices),
		Workloads:       len(snap.Workloads),
		Storage:         len(snap.Storage),
		Ingresses:       len(snap.Ingresses),
		NetworkPolicies: len(snap.NetworkPolicies),
		Components:      len(snap.Components),
		Events:          events,
	}
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
