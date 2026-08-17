package rule

import (
	"context"
	"fmt"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/correlation"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// benchSnapshot 构造 1000 Pod（5% CrashLoopBackOff）快照。
func benchSnapshot(pods int) *model.ClusterSnapshot {
	snap := &model.ClusterSnapshot{
		Namespaces:  []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "default"}}},
		EventsIndex: map[string][]model.EventInfo{},
	}
	for i := 0; i < pods; i++ {
		p := model.PodInfo{
			Ref:        model.ResourceRef{Kind: "Pod", Namespace: "default", Name: fmt.Sprintf("p-%05d", i)},
			Phase:      "Running",
			Containers: []model.ContainerInfo{{Name: "c", State: "Running", Ready: true}},
		}
		if i%20 == 0 {
			p.Containers[0] = model.ContainerInfo{Name: "c", State: "Waiting", Reason: "CrashLoopBackOff", RestartCount: 10}
		}
		snap.Pods = append(snap.Pods, p)
	}
	return snap
}

// BenchmarkRulesLargeCluster 验证 1000 Pod 的 关联索引+规则判定 链路耗时与内存。
func BenchmarkRulesLargeCluster(b *testing.B) {
	snap := benchSnapshot(1000)
	reg := NewRegistry()
	for _, r := range AllDefault() {
		reg.Register(r)
	}
	eng := NewEngine(reg)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx := correlation.Build(snap)
		fs := eng.Evaluate(context.Background(), snap, idx, model.ScanOptions{})
		if len(fs) == 0 {
			b.Fatal("1000 Pod 中应发现异常")
		}
	}
}
