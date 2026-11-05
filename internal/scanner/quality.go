package scanner

import (
	"fmt"
	"sort"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// 采集质量账（需求 §7.7 / 方案 18 §6）。
//
// 这一层存在的唯一理由：采集失败绝不能被下游解释成「资源正常」。规则引擎看不到 Pod
// 和「集群里没有 Pod」是两件事，健康评分、结论、报告都必须能把这两件事分开说。
//
// 口径：
//  1. planned 由采集器实际调度的 list 任务推导（与本文件 operation 表一一对应），不事后猜；
//  2. 按「采集项(kind)」聚合，events 按命名空间逐次计，logs 按目标 Pod 容器计；
//  3. 任一采集项全部调用失败 = failed，部分失败 = partial（partial 也计入不完整）；
//  4. 关键采集项（model.CriticalCollectionKinds）失败 → integrity=insufficient。

// collectionKindGroups 把 collector 的 operation 归入采集项。
// 顺序即报告展示顺序；workloads 合并六类工作负载，storage 合并四类存储对象。
var collectionKindGroups = []struct {
	kind       string
	label      string
	operations []string
}{
	{"namespaces", "命名空间", []string{"list_namespaces"}},
	{"pods", "Pod", []string{"list_pods"}},
	{"nodes", "Node", []string{"list_nodes"}},
	{"workloads", "工作负载", []string{
		"list_deployments", "list_replica_sets", "list_stateful_sets",
		"list_daemon_sets", "list_jobs", "list_cron_jobs",
	}},
	{"services", "Service", []string{"list_services"}},
	{"endpointslices", "EndpointSlice", []string{"list_endpoint_slices"}},
	{"storage", "存储", []string{
		"list_pvcs", "list_pvs", "list_storage_classes", "list_volume_attachments",
	}},
	{"ingresses", "Ingress", []string{"list_ingresses"}},
	{"networkpolicies", "NetworkPolicy", []string{"list_network_policies"}},
	{"autoscaling", "HPA", []string{"list_hpa"}},
	{"poddisruption", "PDB", []string{"list_pdb"}},
	{"rbac", "RBAC 权限", []string{
		"list_roles", "list_role_bindings", "list_cluster_roles", "list_cluster_role_bindings",
	}},
	{"config", "ConfigMap", []string{"list_config_maps"}},
	{"secrets", "Secret 元数据", []string{"list_secret_meta"}},
	{"events", "Events", []string{"list_events"}},
	{"logs", "容器日志", []string{"get_logs"}},
}

// operationKind 反查 operation 属于哪个采集项（Phase1 任务表与此保持一致）。
var operationKind = func() map[string]string {
	m := make(map[string]string, 32)
	for _, g := range collectionKindGroups {
		for _, op := range g.operations {
			m[op] = g.kind
		}
	}
	return m
}()

// alwaysPlannedKinds 是任何档位都会调度的采集项（events/logs 受 opts 开关控制）。
var alwaysPlannedKinds = func() []string {
	out := make([]string, 0, len(collectionKindGroups))
	for _, g := range collectionKindGroups {
		if g.kind == "events" || g.kind == "logs" {
			continue
		}
		out = append(out, g.kind)
	}
	return out
}()

// plannedCalls 是每个采集项一次体检应当发生的 API 调用次数：Phase1 每类资源一次
// list（workloads / storage 这类合并采集项按子类型各一次），events 按命名空间数、
// logs 按目标 Pod 数单独计。planned 只有按「调用次数」计量，failed/planned 才是
// 诚实的成功率：把整组工作负载算成 1 次调用，六次里失败一次就会被读成「全组失败」。
var plannedCalls = func() map[string]int {
	m := make(map[string]int, len(collectionKindGroups))
	for _, g := range collectionKindGroups {
		m[g.kind] = len(g.operations)
	}
	m["events"] = 0 // 由计划采集的命名空间数决定
	m["logs"] = 0   // 由日志目标数决定
	return m
}()

type kindAccount struct {
	planned int
	failed  int
	status  string // ok | partial | failed
	detail  string
}

// CollectionQuality 汇总本次体检的采集质量。
//
// logTargets 由调用方（service 层）传入：Phase2 的日志目标由规则引擎筛出，采集器自己不
// 知道计划数。计量单位与 collector 保持一致——一个目标 Pod 一次采集尝试。
func CollectionQuality(snap *model.ClusterSnapshot, opts model.ScanOptions, logTargets []model.ResourceRef) *model.CollectionQuality {
	q := &model.CollectionQuality{Integrity: model.IntegrityComplete}
	if snap == nil {
		q.Integrity = model.IntegrityInsufficient
		q.CriticalGaps = append(q.CriticalGaps, "snapshot 缺失：无法判定采集范围")
		return q
	}

	accts := map[string]*kindAccount{}
	ensure := func(kind string) *kindAccount {
		if a, ok := accts[kind]; ok {
			return a
		}
		a := &kindAccount{status: "ok"}
		accts[kind] = a
		return a
	}
	for _, k := range alwaysPlannedKinds {
		ensure(k).planned += plannedCalls[k]
	}

	// events 按计划采集的命名空间数计（与 collector 的 eventNamespaces 口径一致）。
	eventTargets := 0
	if opts.CollectEvents {
		eventTargets = len(eventNamespaces(snap.Namespaces, opts.Namespace))
		if eventTargets == 0 {
			// 命名空间一个都没采到（list_namespaces 失败）：events 直接算整项失败。
			eventTargets = 1
		}
		ensure("events").planned = eventTargets
	} else {
		q.Skipped++
		q.SkippedKinds = append(q.SkippedKinds, "events")
		q.MissingData = append(q.MissingData, "未采集 Events（本次档位关闭事件采集）：调度、挂载、拉取镜像类根因无法定位")
	}

	// logs 按目标 Pod 计（collector 每个目标 Pod 最多记一条 get_logs 错误）。
	if opts.CollectLogs && len(logTargets) > 0 {
		ensure("logs").planned = len(logTargets)
	} else {
		q.Skipped++
		q.SkippedKinds = append(q.SkippedKinds, "logs")
		if opts.CollectLogs {
			q.MissingData = append(q.MissingData, "本次未发现需要日志的异常 Pod，未采集容器日志")
		} else {
			q.MissingData = append(q.MissingData, "未采集容器日志（本次档位未开启日志采集）")
		}
	}

	// 归集失败明细。
	scopeFailed := map[string]map[string]bool{} // kind -> 失败目标 scope
	for _, ce := range snap.CollectionErrors {
		kind, ok := operationKind[ce.Operation]
		if !ok {
			kind = "other:" + ce.Operation
		}
		a := ensure(kind)
		if !ok {
			// 只有未知 operation 需要补计一次，不能凭空消失；已知采集项的
			// planned 已由调度计划计过，重复累加会把整项失败摊薄成「部分失败」。
			a.planned++
		}
		a.failed++
		scope := ce.Resource.Key()
		if scope == "/" || scope == "" {
			scope = ce.Operation
		}
		if scopeFailed[kind] == nil {
			scopeFailed[kind] = map[string]bool{}
		}
		scopeFailed[kind][scope] = true
		q.Errors = append(q.Errors, fmt.Sprintf("%s %s：%s", ce.Operation, orNone(scope), clipMsg(ce.Message)))
	}

	critical := map[string]bool{}
	for _, k := range model.CriticalCollectionKinds {
		critical[k] = true
	}

	var totalPlanned, totalFailed int
	failedSet := map[string]bool{}
	partialSet := map[string]bool{}
	for _, kind := range orderedKinds(accts) {
		a := accts[kind]
		if a.planned == 0 {
			continue
		}
		totalPlanned += a.planned
		totalFailed += a.failed
		switch {
		case a.failed == 0:
			a.status = "ok"
			q.Collected++
		case a.failed >= a.planned:
			a.status = "failed"
			a.detail = fmt.Sprintf("%d/%d 次采集失败", a.failed, a.planned)
			q.Failed++
			q.FailedKinds = append(q.FailedKinds, kind)
			failedSet[kind] = true
		default:
			a.status = "partial"
			a.detail = fmt.Sprintf("%d/%d 次采集失败（部分数据缺失）", a.failed, a.planned)
			q.Failed++
			q.FailedKinds = append(q.FailedKinds, kind)
			partialSet[kind] = true
		}
	}
	q.Planned = q.Collected + q.Failed
	if totalPlanned > 0 {
		q.Coverage = float64(totalPlanned-totalFailed) / float64(totalPlanned)
		if q.Coverage < 0 {
			q.Coverage = 0
		}
		if q.Coverage > 1 {
			q.Coverage = 1
		}
	}

	// 完整性判级：关键项整项失败 → insufficient；任何失败/跳过 → 至少 partial。
	for k := range failedSet {
		if critical[k] {
			q.CriticalGaps = append(q.CriticalGaps, fmt.Sprintf("关键采集项 %s 数据不完整（%s）", k, accts[k].detail))
		}
	}
	for k := range partialSet {
		if critical[k] {
			q.CriticalGaps = append(q.CriticalGaps, fmt.Sprintf("关键采集项 %s 部分数据缺失（%s）", k, accts[k].detail))
		}
	}
	sort.Strings(q.CriticalGaps)
	sort.Strings(q.FailedKinds)
	sort.Strings(q.SkippedKinds)
	q.Errors = dedupeStrings(q.Errors)

	// 把「哪个范围没采到」写进数据缺口：报告必须能指出缺的是哪块数据，
	// 只说「采集不完整」等于没说。
	for _, kind := range orderedKinds(accts) {
		scopes := scopeFailed[kind]
		if len(scopes) == 0 {
			continue
		}
		names := make([]string, 0, len(scopes))
		for s := range scopes {
			names = append(names, s)
		}
		sort.Strings(names)
		if len(names) > 8 {
			names = append(names[:8:8], fmt.Sprintf("…共 %d 个范围", len(scopes)))
		}
		q.MissingData = append(q.MissingData,
			fmt.Sprintf("%s 缺失范围：%s", kindLabel(kind), strings.Join(names, ", ")))
	}

	switch {
	case anyCriticalFullFailure(failedSet, critical):
		q.Integrity = model.IntegrityInsufficient
	case q.Failed > 0:
		q.Integrity = model.IntegrityPartial
	case q.Skipped > 0:
		q.Integrity = model.IntegrityComplete // 主动跳过不等于采失败，报告用 SkippedKinds 说明
	}
	if q.Integrity == model.IntegrityInsufficient {
		q.MissingData = append([]string{"关键数据采集不完整：本报告不得解读为「集群正常」"}, q.MissingData...)
	}
	return q
}

func anyCriticalFullFailure(failed, critical map[string]bool) bool {
	for k := range failed {
		if critical[k] {
			return true
		}
	}
	return false
}

// orderedKinds 给出稳定的入账顺序：先按报告展示顺序排已知采集项，再补上未登记
// 但确实报过错的采集项——后者同样要进账，否则一条没登记过的采集错误就能让报告
// 看起来「完整」。
func orderedKinds(accts map[string]*kindAccount) []string {
	out := make([]string, 0, len(accts))
	known := make(map[string]bool, len(collectionKindGroups))
	for _, g := range collectionKindGroups {
		if _, ok := accts[g.kind]; ok {
			out = append(out, g.kind)
			known[g.kind] = true
		}
	}
	var extra []string
	for k := range accts {
		if !known[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

// kindLabel 返回采集项的展示名；未登记的采集项直接用其原始标识。
func kindLabel(kind string) string {
	for _, g := range collectionKindGroups {
		if g.kind == kind {
			return g.label
		}
	}
	return kind
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func clipMsg(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) > 40 {
		out = append(out[:40:40], fmt.Sprintf("…另有 %d 条采集错误已合并", len(in)-40))
	}
	return out
}
