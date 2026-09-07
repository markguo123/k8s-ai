package tools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// ScanRunner 是一期 ScanService 的最小消费接口（service 包实现，避免 tools→service 依赖环）。
type ScanRunner interface {
	Run(ctx context.Context, opts model.ScanOptions) (*model.ScanResult, error)
}

// NewDiagnosisTools 创建诊断类 Tool（scan_cluster/scan_pod/diagnose_pod/diagnose_node/diagnose_workload）。
// 全部复用一期 ScanService.Run，不重复实现扫描/规则/诊断逻辑（需求规格 §8.2）。
func NewDiagnosisTools(runner ScanRunner, baseOpts model.ScanOptions) []Tool {
	base := scanToolBase{runner: runner, baseOpts: baseOpts}
	return []Tool{
		&scanClusterTool{scanToolBase: base},
		&scanPodTool{scanToolBase: base},
		&diagnosePodTool{scanToolBase: base},
		&diagnoseNodeTool{scanToolBase: base},
		&diagnoseWorkloadTool{scanToolBase: base},
	}
}

type scanToolBase struct {
	runner   ScanRunner
	baseOpts model.ScanOptions
}

func (b *scanToolBase) IsReadOnly(json.RawMessage) bool { return true }
func (b *scanToolBase) Risk(json.RawMessage) model.RiskLevel {
	return model.RiskSafe
}

// scanClusterTool：全集群/命名空间巡检（规则级，快；LLM 诊断由 Agent 后续分析）。
type scanClusterTool struct{ scanToolBase }

func (t *scanClusterTool) Name() string { return "scan_cluster" }
func (t *scanClusterTool) Description() string {
	return "对集群（或指定命名空间）执行一期巡检：复用 Scanner+规则引擎+Incident+历史对比，返回问题清单与健康评分。适合『检查集群有没有异常』类问题。"
}
func (t *scanClusterTool) InputSchema() any {
	return objSchema(map[string]any{"namespace": strProp("命名空间（可选，省略=全集群）")})
}
func (t *scanClusterTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	args, err := ParseArgs(input)
	if err != nil {
		return ResultError(err.Error()), nil
	}
	opts := t.baseOpts
	opts.Namespace = args["namespace"]
	opts.PodTarget = ""
	opts.LLM.Enabled = false // 规则级巡检保持快速，LLM 分析由 Agent 负责
	opts.ReportMode = "none"
	result, err := t.runner.Run(ctx, opts)
	if err != nil {
		return ResultError("scan_cluster: " + err.Error()), nil
	}
	return ToolResult{Data: summarizeScanResult(result)}, nil
}

// scanPodTool：单 Pod 巡检（规则级）。
type scanPodTool struct{ scanToolBase }

func (t *scanPodTool) Name() string { return "scan_pod" }
func (t *scanPodTool) Description() string {
	return "对单个 Pod 执行一期巡检（规则级），返回该 Pod 及其关联资源的 Finding 清单。"
}
func (t *scanPodTool) InputSchema() any {
	return objSchema(map[string]any{
		"namespace": strProp("命名空间"),
		"name":      strProp("Pod 名称"),
	}, "namespace", "name")
}
func (t *scanPodTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	args, err := ParseArgs(input, "namespace", "name")
	if err != nil {
		return ResultError(err.Error()), nil
	}
	opts := t.baseOpts
	opts.Namespace = args["namespace"]
	opts.PodTarget = args["name"]
	opts.LLM.Enabled = false
	opts.ReportMode = "none"
	result, err := t.runner.Run(ctx, opts)
	if err != nil {
		return ResultError("scan_pod: " + err.Error()), nil
	}
	return ToolResult{Data: summarizeScanResult(result)}, nil
}

// diagnosePodTool：单 Pod 深度证据采集（规则级；LLM 分析由 Agent 负责，
// 工具内部不再跑诊断 LLM，避免双重 LLM 拖慢与信息损耗）。
type diagnosePodTool struct{ scanToolBase }

func (t *diagnosePodTool) Name() string { return "diagnose_pod" }
func (t *diagnosePodTool) Description() string {
	return "对单个 Pod 深度诊断：复用一期 Scanner+规则+Evidence 采集该 Pod 的问题清单、证据链与关联日志，直接返回给 Agent 分析。适合『为什么这个 Pod 起不来/一直重启』类问题。"
}
func (t *diagnosePodTool) InputSchema() any {
	return objSchema(map[string]any{
		"namespace": strProp("命名空间"),
		"name":      strProp("Pod 名称"),
	}, "namespace", "name")
}
func (t *diagnosePodTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	args, err := ParseArgs(input, "namespace", "name")
	if err != nil {
		return ResultError(err.Error()), nil
	}
	opts := t.baseOpts
	opts.Namespace = args["namespace"]
	opts.PodTarget = args["name"]
	opts.LLM.Enabled = false // Agent 是唯一大脑：证据采集秒级返回，分析由 Agent LLM 完成
	opts.ReportMode = "none"
	result, err := t.runner.Run(ctx, opts)
	if err != nil {
		return ResultError("diagnose_pod: " + err.Error()), nil
	}
	return ToolResult{Data: summarizeScanResult(result)}, nil
}

// diagnoseNodeTool：Node 深度证据采集（按 Node 过滤一期结果；LLM 分析由 Agent 负责）。
type diagnoseNodeTool struct{ scanToolBase }

func (t *diagnoseNodeTool) Name() string { return "diagnose_node" }
func (t *diagnoseNodeTool) Description() string {
	return "对单个 Node 深度诊断：巡检后只保留该 Node 及其相关 Pod 的 Finding 与证据，供 Agent 分析。"
}
func (t *diagnoseNodeTool) InputSchema() any {
	return objSchema(map[string]any{
		"name":      strProp("Node 名称"),
		"namespace": strProp("命名空间（可选，省略=全集群巡检后过滤）"),
	}, "name")
}
func (t *diagnoseNodeTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	args, err := ParseArgs(input, "name")
	if err != nil {
		return ResultError(err.Error()), nil
	}
	opts := t.baseOpts
	opts.Namespace = args["namespace"]
	opts.LLM.Enabled = false // Agent 是唯一大脑：证据采集秒级返回，分析由 Agent LLM 完成
	opts.ReportMode = "none"
	result, err := t.runner.Run(ctx, opts)
	if err != nil {
		return ResultError("diagnose_node: " + err.Error()), nil
	}
	filtered := filterFindings(result, func(r model.ResourceRef) bool {
		return (r.Kind == "Node" || r.Kind == "node") && r.Name == args["name"]
	})
	return ToolResult{Data: summarizeScanResult(filtered)}, nil
}

// diagnoseWorkloadTool：Workload 深度证据采集（Deployment/StatefulSet/DaemonSet
// 及其 Pod 链；LLM 分析由 Agent 负责）。
type diagnoseWorkloadTool struct{ scanToolBase }

func (t *diagnoseWorkloadTool) Name() string { return "diagnose_workload" }
func (t *diagnoseWorkloadTool) Description() string {
	return "对 Deployment/StatefulSet/DaemonSet 深度诊断：巡检后保留该 Workload 及其 Pod 链的 Finding 与证据（含故障链聚合 Incident），供 Agent 分析。"
}
func (t *diagnoseWorkloadTool) InputSchema() any {
	return objSchema(map[string]any{
		"kind":      strProp("Workload 类型：deployment | statefulset | daemonset"),
		"namespace": strProp("命名空间"),
		"name":      strProp("Workload 名称"),
	}, "kind", "namespace", "name")
}
func (t *diagnoseWorkloadTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	args, err := ParseArgs(input, "kind", "namespace", "name")
	if err != nil {
		return ResultError(err.Error()), nil
	}
	kind := strings.ToLower(args["kind"])
	var wantKind string
	switch kind {
	case "deployment":
		wantKind = "Deployment"
	case "statefulset":
		wantKind = "StatefulSet"
	case "daemonset":
		wantKind = "DaemonSet"
	default:
		return ResultError("unsupported workload kind: " + kind), nil
	}
	opts := t.baseOpts
	opts.Namespace = args["namespace"]
	opts.LLM.Enabled = false // Agent 是唯一大脑：证据采集秒级返回，分析由 Agent LLM 完成
	opts.ReportMode = "none"
	result, err := t.runner.Run(ctx, opts)
	if err != nil {
		return ResultError("diagnose_workload: " + err.Error()), nil
	}
	filtered := filterFindings(result, func(r model.ResourceRef) bool {
		return r.Kind == wantKind && r.Name == args["name"]
	})
	return ToolResult{Data: summarizeScanResult(filtered)}, nil
}

// filterFindings 只保留 Resource 或 Related 命中匹配器的 Finding（及其诊断）。
func filterFindings(result *model.ScanResult, match func(model.ResourceRef) bool) *model.ScanResult {
	if result == nil {
		return &model.ScanResult{}
	}
	keep := make([]model.Finding, 0, len(result.Findings))
	keepIDs := map[string]bool{}
	for _, f := range result.Findings {
		if match(f.Resource) {
			keep = append(keep, f)
			keepIDs[f.ID] = true
			continue
		}
		for _, r := range f.Related {
			if match(r) {
				keep = append(keep, f)
				keepIDs[f.ID] = true
				break
			}
		}
	}
	diags := make([]model.Diagnosis, 0, len(result.Diagnoses))
	for _, d := range result.Diagnoses {
		if keepIDs[d.FindingID] {
			diags = append(diags, d)
		}
	}
	out := *result
	out.Findings = keep
	out.Diagnoses = diags
	return &out
}

// summarizeScanResult 把一期 ScanResult 转成紧凑视图（LLM 上下文友好）。
func summarizeScanResult(result *model.ScanResult) map[string]any {
	if result == nil {
		return map[string]any{"kind": "ScanResult", "error": "no result"}
	}
	findings := make([]map[string]any, 0, len(result.Findings))
	for _, f := range result.Findings {
		findings = append(findings, map[string]any{
			"id":         f.ID,
			"rule":       f.Rule,
			"severity":   f.Severity,
			"title":      f.Title,
			"summary":    f.Summary,
			"resource":   map[string]any{"kind": f.Resource.Kind, "namespace": f.Resource.Namespace, "name": f.Resource.Name},
			"correlated": f.Correlated,
		})
	}
	diagnoses := make([]map[string]any, 0, len(result.Diagnoses))
	for _, d := range result.Diagnoses {
		diagnoses = append(diagnoses, map[string]any{
			"findingId":       d.FindingID,
			"summary":         d.Summary,
			"rootCause":       d.RootCause,
			"confidence":      d.Confidence,
			"confidenceLevel": d.ConfidenceLevel,
			"evidenceChain":   d.EvidenceChain,
			"remediation":     d.Remediation,
			"llmUsed":         d.LLMUsed,
		})
	}
	history := map[string]any{"added": 0, "continued": 0, "recovered": 0}
	if result.History != nil {
		history["added"] = len(result.History.Added)
		history["continued"] = len(result.History.Continued)
		history["recovered"] = len(result.History.Recovered)
	}
	components := make([]map[string]any, 0, len(result.Components))
	for _, c := range result.Components {
		components = append(components, map[string]any{
			"name":    c.Name,
			"present": c.Present,
			"ready":   c.Ready,
			"detail":  c.Detail,
		})
	}
	return map[string]any{
		"kind":             "ScanResult",
		"cluster":          result.Meta.Cluster,
		"namespace":        result.Meta.Namespace,
		"summary":          result.Summary,
		"healthScore":      result.HealthScore.Score,
		"findings":         findings,
		"diagnoses":        diagnoses,
		"history":          history,
		"components":       components,
		"collectionErrors": len(result.CollectionErrors),
	}
}
