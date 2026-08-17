package scanner

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/k8s-ai/k8s-ai/internal/kubernetes"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// benchCluster 构造 1000 Pod / 50 namespace 的假集群（P11 压测用）。
func benchCluster(podsPerNS, nsCount int) []runtime.Object {
	var objs []runtime.Object
	for i := 0; i < nsCount; i++ {
		ns := fmt.Sprintf("ns-%02d", i)
		objs = append(objs, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		for j := 0; j < podsPerNS; j++ {
			objs = append(objs, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("pod-%04d", j), Namespace: ns}})
		}
	}
	objs = append(objs, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}})
	return objs
}

// BenchmarkPhase1LargeCluster 验证 1000 Pod 集群 Phase1 采集的耗时与内存。
func BenchmarkPhase1LargeCluster(b *testing.B) {
	cs := fake.NewSimpleClientset(benchCluster(20, 50)...)
	c := New(kubernetes.NewClientWithClientset(cs))
	opts := model.ScanOptions{Concurrency: 8, CollectEvents: true}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Phase1(context.Background(), opts); err != nil {
			b.Fatal(err)
		}
	}
}
