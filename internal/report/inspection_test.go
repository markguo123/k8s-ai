package report

import (
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// 需求 §7.7：采集不完整时评分必须封顶，且分数只降不升；结论区在关键数据缺失时
// 只能说「无法判定」，不许说「集群健康」。

func TestHealthScoreCappedByCollectionIntegrity(t *testing.T) {
	cases := []struct {
		name       string
		q          *model.CollectionQuality
		want       int
		wantCapped bool
	}{
		{"完整不封顶", &model.CollectionQuality{Integrity: model.IntegrityComplete}, 100, false},
		{"部分缺失封顶85", &model.CollectionQuality{
			Integrity: model.IntegrityPartial, Failed: 1, FailedKinds: []string{"ingresses"},
		}, 85, true},
		{"关键数据不足封顶60", &model.CollectionQuality{
			Integrity: model.IntegrityInsufficient, Failed: 1, FailedKinds: []string{"pods"},
		}, 60, true},
		{"有跳过项不给满分", &model.CollectionQuality{
			Integrity: model.IntegrityComplete, Skipped: 1, SkippedKinds: []string{"logs"},
		}, 99, true},
	}
	for _, tc := range cases {
		hs := ComputeHealthScoreWithQuality(nil, tc.q)
		if hs.Score != tc.want {
			t.Errorf("%s：评分应为 %d，实际 %d（reason=%s）", tc.name, tc.want, hs.Score, hs.CapReason)
		}
		if hs.Capped != tc.wantCapped {
			t.Errorf("%s：Capped 应为 %v，实际 %v", tc.name, tc.wantCapped, hs.Capped)
		}
		if hs.DataIntegrity != tc.q.Integrity {
			t.Errorf("%s：应带上采集完整性口径，实际 %s", tc.name, hs.DataIntegrity)
		}
		if tc.wantCapped && hs.CapReason == "" {
			t.Errorf("%s：封顶必须说明原因", tc.name)
		}
	}
}

func TestHealthScoreNilQuality(t *testing.T) {
	hs := ComputeHealthScoreWithQuality(nil, nil)
	if hs.Score != 100 || hs.Capped || hs.DataIntegrity != model.IntegrityComplete {
		t.Fatalf("无采集账目时应保持原口径：%+v", hs)
	}
}

// 封顶只做一件事：把过高的分数压下来。已经很低的分不会被改写，更不会反弹。
func TestHealthScoreCapNeverRaisesScore(t *testing.T) {
	findings := []model.Finding{{
		ID:       "f1",
		Rule:     "ImagePullBackOff",
		Severity: model.SeverityCritical,
		Title:    "镜像拉取失败",
		Resource: model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-1"},
	}}
	base := ComputeHealthScore(findings)
	hs := ComputeHealthScoreWithQuality(findings, &model.CollectionQuality{
		Integrity: model.IntegrityInsufficient, Failed: 1, FailedKinds: []string{"pods"},
	})
	if hs.Score > base.Score {
		t.Fatalf("封顶不得抬高分数：%d -> %d", base.Score, hs.Score)
	}
	if want := minInt(base.Score, 60); hs.Score != want {
		t.Fatalf("评分应为 min(%d,60)=%d，实际 %d", base.Score, want, hs.Score)
	}
}

func TestConclusionUnknownWhenKeyDataMissing(t *testing.T) {
	r := &model.ScanResult{Collection: &model.CollectionQuality{
		Integrity:    model.IntegrityInsufficient,
		Failed:       1,
		FailedKinds:  []string{"pods"},
		CriticalGaps: []string{"关键采集项 pods 数据不完整（1/1 次采集失败）"},
	}}
	c := BuildConclusion(r)
	if c == nil {
		t.Fatal("结论不应为空")
	}
	if c.Level != "unknown" {
		t.Fatalf("关键数据缺失时结论必须判 unknown，实际 %s（%s）", c.Level, c.Headline)
	}
	if !strings.Contains(c.Headline, "无法判定") {
		t.Fatalf("关键数据缺失时必须承认无法判定：%s", c.Headline)
	}
	if len(c.DataCaveats) == 0 {
		t.Fatal("数据缺口必须进入结论的注意事项")
	}
}

// 故障链里的 Finding 不能再算「独立问题」，但故障链本身必须体现在结论里：
// 否则一次全部异常都归进了同一条链的体检，会被读成「本次体检未发现异常」。
func TestConclusionReflectsIncidentsEvenWhenAllFindingsCorrelated(t *testing.T) {
	root := model.Finding{
		ID:       "f1",
		Rule:     "CrashLoopBackOff",
		Severity: model.SeverityCritical,
		Title:    "容器持续重启",
		Resource: model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-1"},
	}
	r := &model.ScanResult{
		Findings: []model.Finding{root},
		Incidents: []model.Incident{{
			ID: "i1", Title: "容器持续重启", Severity: model.SeverityCritical,
			Root: model.FindingRef{ID: root.ID}, Members: []model.FindingRef{{ID: root.ID}},
			RootCauseStatus: model.RootCauseProbable,
		}},
		HealthScore: model.HealthScore{Score: 70, Max: 100},
	}
	c := BuildConclusion(r)
	if c.Level == "healthy" {
		t.Fatalf("存在严重级故障链时结论不得判健康：%s / %s", c.Level, c.Headline)
	}
	if !strings.Contains(c.Summary, "故障链") {
		t.Fatalf("结论摘要必须点出故障链数量：%s", c.Summary)
	}
	if len(c.TopRisks) == 0 || !strings.Contains(c.TopRisks[0], "CrashLoopBackOff") {
		t.Fatalf("首要风险必须包含故障链根因：%v", c.TopRisks)
	}
}

func TestConclusionHealthy(t *testing.T) {
	c := BuildConclusion(&model.ScanResult{
		HealthScore: model.HealthScore{Score: 100, Max: 100},
	})
	if c.Level != "healthy" || !strings.Contains(c.Headline, "未发现异常") {
		t.Fatalf("无异常且采集完整时应判健康：%s / %s", c.Level, c.Headline)
	}
}

func TestConclusionSeparatesBusinessFaultFromSuggestionAndUsesDetailedCause(t *testing.T) {
	business := model.Finding{
		ID: "f-business", Rule: "Unhealthy", Severity: model.SeverityMedium,
		Title: "容器探针失败", Resource: model.ResourceRef{Kind: "Pod", Namespace: "app", Name: "api-0"},
	}
	suggestion := model.Finding{
		ID: "f-policy", Rule: "NetworkPolicyNoDefaultDeny", Severity: model.SeverityLow,
		Title: "缺少默认拒绝策略", Resource: model.ResourceRef{Kind: "Namespace", Name: "app"},
		Suggestion: true,
	}
	r := &model.ScanResult{
		Findings: []model.Finding{business, suggestion},
		Incidents: []model.Incident{
			{ID: business.ID, Title: business.Title, Severity: business.Severity, Root: model.FindingRef{ID: business.ID, Rule: business.Rule, Title: business.Title, Resource: business.Resource}},
			{ID: suggestion.ID, Title: suggestion.Title, Severity: suggestion.Severity, Root: model.FindingRef{ID: suggestion.ID, Rule: suggestion.Rule, Title: suggestion.Title, Resource: suggestion.Resource}, Independent: true},
		},
		Diagnoses: []model.Diagnosis{{
			FindingID: business.ID, IncidentID: business.ID,
			RootCause: "探针请求 /ready 返回 404，容器无法进入 Ready 状态",
			Summary:   "探针路径与镜像内容不匹配",
		}},
		HealthScore: model.HealthScore{Score: 94, Max: 100},
	}
	c := BuildConclusion(r)
	if strings.Contains(c.Headline, "2 项") {
		t.Fatalf("治理建议不应与业务故障合并计数：%s", c.Headline)
	}
	if !strings.Contains(c.Summary, "业务故障链 1") || !strings.Contains(c.Summary, "治理建议 1") {
		t.Fatalf("摘要必须分开业务故障与治理建议：%s", c.Summary)
	}
	if len(c.TopRisks) == 0 || !strings.Contains(c.TopRisks[0], "/ready 返回 404") {
		t.Fatalf("首要风险必须使用后端已给出的详细诊断根因：%v", c.TopRisks)
	}
}

func TestDomainHealthIncludesRBACResourcesAndBindingFindings(t *testing.T) {
	r := &model.ScanResult{
		Summary: model.ClusterSummary{
			Namespaces:          2,
			Roles:               3,
			RoleBindings:        4,
			ClusterRoles:        5,
			ClusterRoleBindings: 6,
		},
		Findings: []model.Finding{{
			ID:       "rbac-1",
			Kind:     "ClusterRoleBinding",
			Severity: model.SeverityHigh,
			Title:    "全通配 ClusterRole 被绑定",
		}},
	}

	var permission *model.DomainHealth
	for _, row := range DomainHealthList(r) {
		if row.Domain == "权限与治理" {
			permission = &row
			break
		}
	}
	if permission == nil {
		t.Fatal("RBAC 资源存在时必须输出权限与治理域")
	}
	if permission.Total != 20 {
		t.Fatalf("权限与治理资源总数 = %d, want 20", permission.Total)
	}
	if permission.Unhealthy != 1 || permission.Critical != 1 {
		t.Fatalf("Binding Finding 应计入异常和高风险：%+v", *permission)
	}
}

func TestRemediationItemsKeepNarrativeAndCommandFormat(t *testing.T) {
	r := &model.ScanResult{
		Incidents: []model.Incident{{
			ID: "I-1", Severity: model.SeverityHigh,
			Root: model.FindingRef{ID: "F-1", Resource: model.ResourceRef{Kind: "Pod", Namespace: "app", Name: "api-0"}},
		}},
		Diagnoses: []model.Diagnosis{{
			IncidentID:           "I-1",
			RemediationText:      "**目标**：恢复业务。\n\n1. 确认外部依赖。\n2. 修复后验证 Ready。",
			RemediationDirection: "当前无法确认精确写入参数，不生成写命令。",
			Investigation: []model.Command{{
				Category: model.CmdInvestigation,
				Text:     "kubectl -n app logs api-0 --tail=50",
				Risk:     model.RiskSafe,
			}},
		}},
	}
	items := RemediationItems(r)
	if len(items) < 3 {
		t.Fatalf("文字方案、前置条件、排查命令都不得丢失: %+v", items)
	}
	if items[0].Format != "text" || !strings.Contains(items[0].Text, "恢复业务") {
		t.Fatalf("完整修复方案必须是第一个文字项: %+v", items[0])
	}
	if items[1].Format != "text" || !strings.Contains(items[1].Text, "不生成写命令") {
		t.Fatalf("执行边界必须保留为文字项: %+v", items[1])
	}
	if items[2].Format != "command" || items[2].Risk != "SAFE" {
		t.Fatalf("可复制命令必须显式标记格式与风险: %+v", items[2])
	}
}
