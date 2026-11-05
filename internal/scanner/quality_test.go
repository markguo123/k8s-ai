package scanner

import (
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// 这一组测试守的是需求 §7.7 的红线：采集失败绝不能被下游读成「资源正常」。
// 判级只允许 complete / partial / insufficient 三态，且关键采集项整项失败必须
// 落到 insufficient，健康评分才有封顶的依据。

func qualSnapshot(nsNames ...string) *model.ClusterSnapshot {
	s := &model.ClusterSnapshot{EventsIndex: map[string][]model.EventInfo{}}
	for _, n := range nsNames {
		s.Namespaces = append(s.Namespaces, model.NamespaceInfo{
			Ref:   model.ResourceRef{Kind: "Namespace", Name: n},
			Phase: "Active",
		})
	}
	return s
}

func qualOpts() model.ScanOptions {
	return model.ScanOptions{CollectEvents: true, CollectLogs: true}
}

func collectErr(op, kind, ns string) model.CollectionError {
	return model.CollectionError{
		Resource:  model.ResourceRef{Kind: kind, Namespace: ns},
		Operation: op,
		Message:   "the server could not find the requested resource",
		Time:      "2026-01-01T00:00:00Z",
	}
}

func hasKind(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func joined(parts []string) string { return strings.Join(parts, "\n") }

func TestCollectionQualityComplete(t *testing.T) {
	snap := qualSnapshot("prod", "kube-system")
	targets := []model.ResourceRef{{Kind: "Pod", Namespace: "prod", Name: "web-1"}}
	q := CollectionQuality(snap, qualOpts(), targets)

	if q.Integrity != model.IntegrityComplete {
		t.Fatalf("无采集错误时应为 complete，实际 %s（gaps=%v）", q.Integrity, q.CriticalGaps)
	}
	if q.Failed != 0 || len(q.FailedKinds) != 0 || len(q.Errors) != 0 {
		t.Fatalf("不该有失败项：failed=%d kinds=%v errors=%v", q.Failed, q.FailedKinds, q.Errors)
	}
	if q.Coverage != 1 {
		t.Fatalf("全部采集成功时覆盖率应为 1，实际 %v", q.Coverage)
	}
	// events 按命名空间计 2 次、logs 按目标 Pod 计 1 次，都不得算成「跳过」。
	if q.Skipped != 0 || hasKind(q.SkippedKinds, "events") || hasKind(q.SkippedKinds, "logs") {
		t.Fatalf("开启的采集项不得记为跳过：skipped=%d kinds=%v", q.Skipped, q.SkippedKinds)
	}
	if q.Planned != q.Collected+q.Failed || q.Planned < 14 {
		t.Fatalf("账目不平：planned=%d collected=%d failed=%d", q.Planned, q.Collected, q.Failed)
	}
}

func TestCollectionQualityCriticalFullFailureIsInsufficient(t *testing.T) {
	snap := qualSnapshot("prod")
	snap.CollectionErrors = []model.CollectionError{collectErr("list_pods", "Pod", "")}
	q := CollectionQuality(snap, qualOpts(), nil)

	if q.Integrity != model.IntegrityInsufficient {
		t.Fatalf("Pod 整项采集失败必须判 insufficient，实际 %s", q.Integrity)
	}
	if !hasKind(q.FailedKinds, "pods") {
		t.Fatalf("失败采集项应包含 pods，实际 %v", q.FailedKinds)
	}
	if len(q.CriticalGaps) == 0 || !strings.Contains(joined(q.CriticalGaps), "pods") {
		t.Fatalf("关键缺口必须点名 pods，实际 %v", q.CriticalGaps)
	}
	// 报告顶部必须先看到「不能解读为集群正常」这句话。
	if len(q.MissingData) == 0 || !strings.Contains(q.MissingData[0], "不得解读为") {
		t.Fatalf("insufficient 时缺口清单首条必须声明结论不成立，实际 %v", q.MissingData)
	}
	if q.Coverage >= 1 {
		t.Fatalf("有采集失败时覆盖率必须低于 1，实际 %v", q.Coverage)
	}
}

func TestCollectionQualityNonCriticalFailureIsPartial(t *testing.T) {
	snap := qualSnapshot("prod")
	snap.CollectionErrors = []model.CollectionError{collectErr("list_ingresses", "Ingress", "")}
	q := CollectionQuality(snap, qualOpts(), nil)

	if q.Integrity != model.IntegrityPartial {
		t.Fatalf("非关键类型失败应为 partial，实际 %s", q.Integrity)
	}
	if !hasKind(q.FailedKinds, "ingresses") {
		t.Fatalf("失败采集项应包含 ingresses，实际 %v", q.FailedKinds)
	}
	if len(q.CriticalGaps) != 0 {
		t.Fatalf("Ingress 不是关键采集项，不该产生关键缺口：%v", q.CriticalGaps)
	}
	if !strings.Contains(joined(q.MissingData), "Ingress 缺失范围") {
		t.Fatalf("缺口清单必须指出缺的是哪块数据，实际 %v", q.MissingData)
	}
}

// 合并采集项（六类工作负载）里只有一类失败时只能算部分缺失，且必须按「一次调用」
// 计量：把六次 list 折成一次，会把部分缺失说成整组失败，或者反过来摊薄成功率。
func TestCollectionQualityGroupedKindPartial(t *testing.T) {
	snap := qualSnapshot("prod")
	snap.CollectionErrors = []model.CollectionError{collectErr("list_stateful_sets", "StatefulSet", "")}
	q := CollectionQuality(snap, qualOpts(), nil)

	if q.Integrity != model.IntegrityPartial {
		t.Fatalf("工作负载部分失败应为 partial，实际 %s", q.Integrity)
	}
	if !hasKind(q.FailedKinds, "workloads") {
		t.Fatalf("失败采集项应包含 workloads，实际 %v", q.FailedKinds)
	}
	if !strings.Contains(joined(q.CriticalGaps), "1/6") {
		t.Fatalf("工作负载应按 6 次 list 调用计量并写入关键缺口，实际 %v", q.CriticalGaps)
	}
	if q.Coverage <= 0 || q.Coverage >= 1 {
		t.Fatalf("覆盖率应落在 0 与 1 之间，实际 %v", q.Coverage)
	}
}

// RBAC 采集由 Role/RoleBinding/ClusterRole/ClusterRoleBinding 四次 list 组成；
// 任意一次失败只能说明 RBAC 数据部分缺失，不能把整组误判为完全失败。
func TestCollectionQualityRBACPartial(t *testing.T) {
	snap := qualSnapshot("prod")
	snap.CollectionErrors = []model.CollectionError{
		collectErr("list_role_bindings", "RoleBinding", ""),
	}
	q := CollectionQuality(snap, qualOpts(), nil)

	if plannedCalls["rbac"] != 4 {
		t.Fatalf("RBAC 应按 4 次 list 调用计量，实际 %d", plannedCalls["rbac"])
	}
	if q.Integrity != model.IntegrityPartial {
		t.Fatalf("单次 RBAC list 失败应为 partial，实际 %s", q.Integrity)
	}
	if !hasKind(q.FailedKinds, "rbac") {
		t.Fatalf("失败采集项应包含 rbac，实际 %v", q.FailedKinds)
	}
	if len(q.CriticalGaps) != 0 {
		t.Fatalf("RBAC 不是关键采集项，不应产生关键缺口：%v", q.CriticalGaps)
	}
	if !strings.Contains(joined(q.MissingData), "RBAC 权限") {
		t.Fatalf("缺口清单必须指出 RBAC 权限数据缺失，实际 %v", q.MissingData)
	}
}

// 主动跳过（档位关闭）不等于采集失败：判级仍是 complete，但必须在清单里说清楚缺了什么。
func TestCollectionQualitySkippedIsNotFailed(t *testing.T) {
	q := CollectionQuality(qualSnapshot("prod"), model.ScanOptions{}, nil)

	if q.Integrity != model.IntegrityComplete {
		t.Fatalf("主动跳过不该降级完整性，实际 %s", q.Integrity)
	}
	if q.Failed != 0 || len(q.FailedKinds) != 0 {
		t.Fatalf("跳过不该计入失败：failed=%d kinds=%v", q.Failed, q.FailedKinds)
	}
	if !hasKind(q.SkippedKinds, "events") || !hasKind(q.SkippedKinds, "logs") {
		t.Fatalf("未采集的 events/logs 必须记入跳过清单，实际 %v", q.SkippedKinds)
	}
	if !strings.Contains(joined(q.MissingData), "Events") {
		t.Fatalf("证据缺口必须说明事件未采集，实际 %v", q.MissingData)
	}
}

func TestCollectionQualityEventsPerNamespace(t *testing.T) {
	snap := qualSnapshot("prod", "stage", "kube-system")
	snap.CollectionErrors = []model.CollectionError{collectErr("list_events", "Event", "stage")}
	q := CollectionQuality(snap, qualOpts(), nil)

	if q.Integrity != model.IntegrityPartial {
		t.Fatalf("单个命名空间事件失败应为 partial，实际 %s", q.Integrity)
	}
	if !strings.Contains(joined(q.Errors), "list_events") {
		t.Fatalf("错误明细要带上 operation，实际 %v", q.Errors)
	}
	if !strings.Contains(joined(q.MissingData), "stage") {
		t.Fatalf("必须指出缺的是哪个范围，实际 %v", q.MissingData)
	}
}

func TestCollectionQualityLogsPerTarget(t *testing.T) {
	snap := qualSnapshot("prod")
	targets := []model.ResourceRef{
		{Kind: "Pod", Namespace: "prod", Name: "web-1"},
		{Kind: "Pod", Namespace: "prod", Name: "web-2"},
	}
	snap.CollectionErrors = []model.CollectionError{collectErr("get_logs", "Pod", "prod")}
	q := CollectionQuality(snap, qualOpts(), targets)

	if !hasKind(q.FailedKinds, "logs") {
		t.Fatalf("一个日志目标失败应记入 logs 失败，实际 %v", q.FailedKinds)
	}
	if q.Integrity != model.IntegrityPartial {
		t.Fatalf("日志部分失败应为 partial，实际 %s", q.Integrity)
	}
	if !strings.Contains(joined(q.MissingData), "容器日志") {
		t.Fatalf("缺日志必须作为证据缺口写出，实际 %v", q.MissingData)
	}
}

func TestCollectionQualityNilSnapshot(t *testing.T) {
	q := CollectionQuality(nil, qualOpts(), nil)
	if q.Integrity != model.IntegrityInsufficient || len(q.CriticalGaps) == 0 {
		t.Fatalf("空快照必须判 insufficient 并给出原因：%+v", q)
	}
}

// 未知 operation 的错误也要进账：不能因为不在采集项表里就凭空消失。
func TestCollectionQualityUnknownOperationStillCounted(t *testing.T) {
	snap := qualSnapshot("prod")
	snap.CollectionErrors = []model.CollectionError{
		collectErr("list_weird_things", "Weird", "prod"),
		collectErr("list_weird_things", "Weird", "prod"), // 同一条错误只报一次
	}
	q := CollectionQuality(snap, qualOpts(), nil)

	if q.Integrity == model.IntegrityComplete {
		t.Fatal("未登记的采集错误不能让报告看起来完整")
	}
	if len(q.Errors) != 1 || !strings.Contains(q.Errors[0], "list_weird_things") {
		t.Fatalf("未知 operation 应去重后保留一条明细，实际 %v", q.Errors)
	}
}
