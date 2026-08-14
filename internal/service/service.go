// Package service 是唯一的应用编排层：CLI/Server/CronJob 都复用
// ScanService.Run（ADR-007）。业务逻辑不依赖 CLI（AGENTS.md）。
package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/correlation"
	"github.com/k8s-ai/k8s-ai/internal/diagnosis"
	"github.com/k8s-ai/k8s-ai/internal/evidence"
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
	// 总预算：scan.timeout 控制整次扫描时长（CONCURRENCY.md §2）。
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	started := time.Now()
	client, err := s.newClient(opts)
	if err != nil {
		return nil, err
	}
	serverVersion, err := client.ServerVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to cluster: %w", err)
	}

	// Phase1：全量轻量 list（无 N+1）。
	collector := scanner.New(client)
	snap, err := collector.Phase1(ctx, opts)
	if err != nil {
		return nil, err
	}
	snap.ServerVersion = serverVersion

	// P3 关联索引 → P4 规则引擎：自动判定异常 Finding。
	idx := correlation.Build(snap)
	findings := s.engine.Evaluate(ctx, snap, idx, opts)
	slog.Info("scan: Phase1 采集完成", "namespaces", len(snap.Namespaces), "pods", len(snap.Pods), "findings", len(findings))

	// 单 Pod 目标扫描：只保留该 Pod 及其直接关联资源（owner/Node/PVC/Service）的 Finding。
	if opts.PodTarget != "" {
		findings = filterFindingsForPod(idx, findings, opts)
	}

	// Phase2：只对需要日志的异常 Pod 深度采集，并把日志挂到 Finding 证据。
	if opts.CollectLogs && len(findings) > 0 {
		targets := rule.LogTargets(s.registry, findings)
		if len(targets) > 0 {
			if err := collector.Phase2(ctx, snap, targets, opts); err != nil {
				// 根 ctx 取消时提前返回；其余失败已记入 collection_errors。
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
			}
			attachLogEvidence(findings, idx)
		}
	}

	result := &model.ScanResult{
		Meta: model.ScanMeta{
			ToolVersion:   version.Version,
			Cluster:       opts.Context,
			ServerVersion: serverVersion,
			ScanStartedAt: started.UTC().Format(time.RFC3339),
			ScanEndedAt:   time.Now().UTC().Format(time.RFC3339),
			Duration:      time.Since(started).String(),
			Namespace:     opts.Namespace,
			Pod:           opts.PodTarget,
		},
		Summary:          summaryFromSnapshot(snap),
		Findings:         derefFindings(findings),
		HealthScore:      report.ComputeHealthScore(derefFindings(findings)),
		LLMSummary:       model.LLMSummary{Enabled: opts.LLM.Enabled},
		Components:       snap.Components,
		CollectionErrors: snap.CollectionErrors,
	}
	// P7：LLM 诊断（预算裁剪 → 校验 → 降级），单个/全部失败都不中断 scan。
	// 关联证据补全：给 Deployment/Service/Node 等 Finding 附上关联异常 Pod 的
	// 崩溃状态与日志关键行，让 LLM 能跨资源定位真正根因。
	enrichRelatedEvidence(findings, idx)

	if opts.LLM.Enabled && len(findings) > 0 {
		slog.Info("scan: LLM 诊断开始", "findings", len(findings))
		diagnoses, llmSummary, err := diagnosis.New(opts.LLM).Diagnose(ctx, derefFindings(findings), s.newLLM(opts.LLM))
		if err != nil {
			return nil, err
		}
		result.Diagnoses = diagnoses
		result.LLMSummary = llmSummary
		slog.Info("scan: LLM 诊断完成", "calls", llmSummary.Calls, "failed", llmSummary.Failed, "degraded", llmSummary.Degraded)
	}

	// 报告落盘：none 不写文件；latest/daily 由 Writer 写出（FR-019）。
	if opts.ReportMode != "" && opts.ReportMode != "none" {
		paths, err := report.NewWriter(opts.ReportDirectory).Write(result, model.ReportOptions{
			Directory: opts.ReportDirectory,
			Format:    opts.ReportFormat,
			Mode:      opts.ReportMode,
		})
		if err != nil {
			return nil, fmt.Errorf("write report: %w", err)
		}
		result.ReportPaths = paths
	}
	return result, nil
}

// attachLogEvidence 把 Phase2 采集的日志证据按 Pod 挂到对应 Finding（FR-014）。
func attachLogEvidence(findings []*model.Finding, idx *model.CorrelationIndex) {
	for _, f := range findings {
		if f.Resource.Kind != "Pod" {
			continue
		}
		p := idx.Pod(f.Resource.Key())
		if p == nil {
			continue
		}
		for _, cl := range p.Logs {
			f.Evidence = append(f.Evidence, evidence.Log(cl)...)
		}
		if len(p.Logs) > 0 {
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
		}
	}
	// 补充证据后重新排序编号（日志不影响指纹签名）。
	for _, f := range findings {
		f.Evidence = evidence.AssignIDs(f.Evidence)
	}
}
