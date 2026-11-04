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
