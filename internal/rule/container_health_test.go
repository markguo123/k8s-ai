// Package rule 实现规则引擎（FR-013）。
package rule

import (
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// TestContainerHealthTimeoutRule 容器 Running 但长期未 Ready（Pod 存活超阈值）时命中。
func TestContainerHealthTimeoutRule(t *testing.T) {
	mk := func(created time.Time, ready bool) *RuleContext {
		return ctxFor(&model.ClusterSnapshot{
			Pods: []model.PodInfo{
				{
					Ref:               model.ResourceRef{Kind: "Pod", Namespace: "app", Name: "web-1"},
					CreationTimestamp: created.UTC().Format(time.RFC3339),
					Containers: []model.ContainerInfo{
						{Name: "web", State: "Running", Ready: ready},
					},
				},
			},
		})
	}
	// 陈旧 Pod + Running 未 Ready：命中。
	fs := (ContainerHealthTimeoutRule{}).Evaluate(mk(time.Now().Add(-10*time.Minute), false))
	if len(fs) != 1 {
		t.Fatalf("陈旧 NotReady 应命中: %d", len(fs))
	}
	// 陈旧 Pod 但容器已 Ready：跳过。
	if fs := (ContainerHealthTimeoutRule{}).Evaluate(mk(time.Now().Add(-10*time.Minute), true)); len(fs) != 0 {
		t.Fatalf("Ready 应跳过: %d", len(fs))
	}
	// 新建 Pod（未达阈值）+ NotReady：跳过（启动暂态）。
	if fs := (ContainerHealthTimeoutRule{}).Evaluate(mk(time.Now().Add(-time.Minute), false)); len(fs) != 0 {
		t.Fatalf("新建 NotReady 应跳过: %d", len(fs))
	}
}
