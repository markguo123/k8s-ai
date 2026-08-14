package scanner

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/k8s-ai/k8s-ai/internal/kubernetes"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

const (
	resourceTypes = 17 // Phase1 的资源类型 list 任务数（不含 events）
	nsCount       = 50
	podsPerNS     = 20
)

// buildFakeCluster 构造 1000 Pod / 50 namespace 的假集群，用于请求量回归测试。
func buildFakeCluster() []runtime.Object {
	var objs []runtime.Object
	for i := 0; i < nsCount; i++ {
		ns := fmt.Sprintf("ns-%02d", i)
		objs = append(objs, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		for j := 0; j < podsPerNS; j++ {
			objs = append(objs, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("pod-%03d", j), Namespace: ns}})
		}
	}
	objs = append(objs,
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns-00"}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "pvc", Namespace: "ns-00"}},
	)
	return objs
}

// TestPhase1RequestCounts 验证 Phase1 无 N+1：请求数只与资源类型数和 namespace 数相关。
func TestPhase1RequestCounts(t *testing.T) {
	cs := fake.NewSimpleClientset(buildFakeCluster()...)
	snap, err := New(kubernetes.NewClientWithClientset(cs)).Phase1(
		context.Background(), model.ScanOptions{Concurrency: 8, CollectEvents: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Pods) != nsCount*podsPerNS {
		t.Fatalf("pods = %d, want %d", len(snap.Pods), nsCount*podsPerNS)
	}
	if len(snap.Namespaces) != nsCount {
		t.Fatalf("namespaces = %d, want %d", len(snap.Namespaces), nsCount)
	}
	listActions, eventsLists := 0, 0
	for _, a := range cs.Actions() {
		if a.GetVerb() != "list" {
			t.Fatalf("Phase1 只允许 list，发现 %s %s", a.GetVerb(), a.GetResource().Resource)
		}
		listActions++
		if a.GetResource().Resource == "events" {
			eventsLists++
		}
	}
	if eventsLists != nsCount {
		t.Fatalf("events list 次数 = %d, want %d", eventsLists, nsCount)
	}
	if listActions != resourceTypes+nsCount {
		t.Fatalf("list 总次数 = %d, want %d", listActions, resourceTypes+nsCount)
	}
}

// TestPhase1NamespaceFilter 验证 namespace 过滤：只采集该 ns，且不做系统组件探测。
func TestPhase1NamespaceFilter(t *testing.T) {
	objs := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns-a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns-b"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a-1", Namespace: "ns-a"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "b-1", Namespace: "ns-b"}},
	}
	cs := fake.NewSimpleClientset(objs...)
	snap, err := New(kubernetes.NewClientWithClientset(cs)).Phase1(
		context.Background(), model.ScanOptions{Namespace: "ns-a", Concurrency: 4, CollectEvents: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Pods) != 1 || snap.Pods[0].Ref.Name != "a-1" {
		t.Fatalf("pods = %+v, want only a-1", snap.Pods)
	}
	if len(snap.Components) != 0 {
		t.Fatal("namespace 过滤模式下不应做系统组件探测")
	}
	eventsLists := 0
	for _, a := range cs.Actions() {
		if a.GetVerb() != "list" {
			t.Fatalf("发现非 list 动作: %s", a.GetVerb())
		}
		if a.GetResource().Resource == "events" {
			eventsLists++
		}
	}
	if eventsLists != 1 {
		t.Fatalf("events list 次数 = %d, want 1", eventsLists)
	}
}

// TestPhase1ErrorIsolation 验证单个资源 list 失败不会中断整体扫描（FR-004）。
func TestPhase1ErrorIsolation(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}})
	cs.PrependReactor("list", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("simulated nodes failure")
	})
	snap, err := New(kubernetes.NewClientWithClientset(cs)).Phase1(
		context.Background(), model.ScanOptions{Concurrency: 4, CollectEvents: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.CollectionErrors) == 0 {
		t.Fatal("期望记录 collection error")
	}
	if len(snap.Namespaces) != 1 {
		t.Fatal("namespace 采集不应受影响")
	}
}

// TestEventsIndex 验证事件按 involvedObject.UID 本地索引。
func TestEventsIndex(t *testing.T) {
	s := &snapCollector{snap: &model.ClusterSnapshot{EventsIndex: map[string][]model.EventInfo{}}}
	s.addEvents([]corev1.Event{{
		ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "ns-a"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "ns-a", Name: "p1", UID: "uid-1"},
		Reason:         "BackOff",
		Type:           "Warning",
		Count:          3,
		FirstTimestamp: metav1.Time{Time: time.Now()},
		LastTimestamp:  metav1.Time{Time: time.Now()},
	}})
	if got := len(s.snap.EventsIndex["uid-1"]); got != 1 {
		t.Fatalf("EventsIndex[uid-1] = %d, want 1", got)
	}
}

// TestNormalizePod 验证 Pod 归一化的关键字段。
func TestNormalizePod(t *testing.T) {
	p := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "web-0",
			Namespace:       "default",
			UID:             "pod-uid",
			Labels:          map[string]string{"app": "web"},
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-rs", UID: "rs-uid"}},
		},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
			Containers: []corev1.Container{{
				Name:  "web",
				Image: "nginx:1.25",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
				},
			}},
		},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			StartTime: &metav1.Time{Time: time.Now()},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:                 "web",
				Image:                "nginx:1.25",
				RestartCount:         3,
				Ready:                false,
				State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
			}},
		},
	}
	info := normalizePod(p)
	if info.Phase != "Running" || info.NodeName != "node-1" {
		t.Fatalf("unexpected pod info: %+v", info)
	}
	c := info.Containers[0]
	if c.State != "Waiting" || c.Reason != "CrashLoopBackOff" {
		t.Fatalf("state = %s/%s", c.State, c.Reason)
	}
	if c.RestartCount != 3 || c.LastReason != "OOMKilled" || c.LastExitCode != 137 {
		t.Fatalf("restart/last state = %d/%s/%d", c.RestartCount, c.LastReason, c.LastExitCode)
	}
	if c.Requests["cpu"] != "100m" || c.Limits["memory"] != "256Mi" {
		t.Fatalf("resources = %v/%v", c.Requests, c.Limits)
	}
	if len(info.OwnerRefs) != 1 || info.OwnerRefs[0].Name != "web-rs" {
		t.Fatalf("owner refs = %+v", info.OwnerRefs)
	}
}

// TestComponentDetection 验证系统组件动态发现（FR-010）。
func TestComponentDetection(t *testing.T) {
	pods := []model.PodInfo{
		{
			Ref:        model.ResourceRef{Name: "coredns-0", Namespace: "kube-system"},
			Labels:     map[string]string{"k8s-app": "kube-dns"},
			Phase:      "Running",
			Containers: []model.ContainerInfo{{Name: "coredns", Ready: true}},
		},
		{
			Ref:        model.ResourceRef{Name: "calico-node-0", Namespace: "kube-system"},
			Labels:     map[string]string{"k8s-app": "calico-node"},
			Phase:      "Running",
			Containers: []model.ContainerInfo{{Name: "calico", Ready: true}},
		},
	}
	comps := detectComponents(pods)
	byName := map[string]model.ComponentInfo{}
	for _, c := range comps {
		byName[c.Name] = c
	}
	if !byName["CoreDNS"].Present || !byName["CoreDNS"].Ready {
		t.Fatalf("CoreDNS 应被检测为 present+ready: %+v", byName["CoreDNS"])
	}
	if !byName["CNI"].Present {
		t.Fatalf("CNI 应被检测为 present: %+v", byName["CNI"])
	}
	if byName["metrics-server"].Present {
		t.Fatal("metrics-server 未部署时不应被检测为 present")
	}
}
