// Package rule 实现规则引擎（FR-013）。
package rule

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func ptr32(v int32) *int32 { return &v }

// TestCronJobSuspendedRule 挂起 CronJob 命中且等级 LOW，未挂起的不报。
func TestCronJobSuspendedRule(t *testing.T) {
	mk := func(suspend bool) *RuleContext {
		snap := &model.ClusterSnapshot{
			Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "default"}}},
			Workloads: []model.WorkloadInfo{
				{Ref: model.ResourceRef{Kind: "CronJob", Namespace: "default", Name: "backup"}, Suspend: suspend},
			},
		}
		return ctxFor(snap)
	}
	fs := (CronJobSuspendedRule{}).Evaluate(mk(true))
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1", len(fs))
	}
	if fs[0].Severity != model.SeverityLow {
		t.Fatalf("severity = %s, want LOW", fs[0].Severity)
	}
	if len(fs[0].Evidence) == 0 {
		t.Fatal("应包含 suspend 证据")
	}
	if fs := (CronJobSuspendedRule{}).Evaluate(mk(false)); len(fs) != 0 {
		t.Fatalf("未挂起应不报: %d", len(fs))
	}
}
