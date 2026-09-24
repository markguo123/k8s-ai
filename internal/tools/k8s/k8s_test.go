package k8s

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"

	"github.com/k8s-ai/k8s-ai/internal/kubernetes"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

func testReader(objs ...runtime.Object) Reader {
	return kubernetes.NewClientWithClientset(fake.NewSimpleClientset(objs...))
}

func TestGetPod(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "payment-api-abc", Namespace: "production"},
		Spec:       corev1.PodSpec{NodeName: "node-1"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "app", Ready: false, RestartCount: 5, Image: "pay:v1",
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
					LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						Reason: "OOMKilled", ExitCode: 137,
					}},
				},
			},
		},
	}
	r := testReader(pod)
	tr, err := (&podTool{base{reader: r}}).Execute(context.Background(), json.RawMessage(`{"namespace":"production","name":"payment-api-abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := tr.Data.(map[string]any)
	if m["phase"] != corev1.PodRunning || m["restartCount"] != int32(5) {
		t.Fatalf("pod summary = %v", m)
	}
	containers := m["containers"].([]map[string]any)
	if containers[0]["lastState"] != "terminated:OOMKilled(exit=137)" {
		t.Fatalf("container summary = %v", containers[0])
	}
}

func TestGetPodMissingArgs(t *testing.T) {
	r := testReader()
	tr, err := (&podTool{base{reader: r}}).Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Data.(map[string]any)["error"] == nil {
		t.Fatal("缺少 name 应返回错误结果")
	}
}

func TestGetPodNamespaceSearch(t *testing.T) {
	pods := []runtime.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "web-0", Namespace: "agentops"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "web-0", Namespace: "default"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "default"},
			Status:     corev1.PodStatus{Phase: corev1.PodPending},
		},
	}
	r := testReader(pods...)
	// 同名多个 → 候选列表。
	tr, err := (&podTool{base{reader: r}}).Execute(context.Background(), json.RawMessage(`{"name":"web-0"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := tr.Data.(map[string]any)
	if m["kind"] != "PodCandidates" || m["count"] != 2 {
		t.Fatalf("candidates = %v", m)
	}
	// 唯一匹配 → 直接返回 Pod 详情。
	tr, err = (&podTool{base{reader: r}}).Execute(context.Background(), json.RawMessage(`{"name":"other"}`))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Data.(map[string]any)["name"] != "other" {
		t.Fatalf("pod = %v", tr.Data)
	}
	// 找不到 → 错误结果。
	tr, err = (&podTool{base{reader: r}}).Execute(context.Background(), json.RawMessage(`{"name":"missing"}`))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Data.(map[string]any)["error"] == nil {
		t.Fatal("找不到应返回错误")
	}
}

func TestGetPodPrefixMatch(t *testing.T) {
	pods := []runtime.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "naviapp-backend-7597875c9d-jwsqs", Namespace: "agentops"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "naviapp-frontend-abc", Namespace: "agentops"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "other-pod", Namespace: "default"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		},
	}
	r := testReader(pods...)
	// 用 Deployment 名查询 → 前缀命中唯一 Pod，直接返回详情。
	tr, err := (&podTool{base{reader: r}}).Execute(context.Background(), json.RawMessage(`{"name":"naviapp-backend"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := tr.Data.(map[string]any)
	if m["name"] != "naviapp-backend-7597875c9d-jwsqs" || m["namespace"] != "agentops" {
		t.Fatalf("prefix 匹配结果 = %v", m)
	}
	// 前缀命中多个 → 候选列表。
	tr, err = (&podTool{base{reader: r}}).Execute(context.Background(), json.RawMessage(`{"name":"naviapp"}`))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Data.(map[string]any)["kind"] != "PodCandidates" || tr.Data.(map[string]any)["count"] != 2 {
		t.Fatalf("多个前缀命中应返回候选: %v", tr.Data)
	}
}

func TestGetPods(t *testing.T) {
	pods := []runtime.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns1"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns1"}, Status: corev1.PodStatus{Phase: corev1.PodPending}},
	}
	tr, err := (&podsTool{base{reader: testReader(pods...)}}).Execute(context.Background(), json.RawMessage(`{"namespace":"ns1"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := tr.Data.(map[string]any)
	if m["count"] != 2 {
		t.Fatalf("count = %v", m["count"])
	}
}

func TestGetNodeAndNodes(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse, Reason: "KubeletNotReady"}},
		},
	}
	r := testReader(node)
	tr, err := (&nodeTool{base{reader: r}}).Execute(context.Background(), json.RawMessage(`{"name":"node-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := tr.Data.(map[string]any)
	conds := m["conditions"].([]map[string]any)
	if conds[0]["type"] != corev1.NodeReady || conds[0]["status"] != corev1.ConditionFalse {
		t.Fatalf("node summary = %v", m)
	}
	tr2, err := (&nodesTool{base{reader: r}}).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if tr2.Data.(map[string]any)["count"] != 1 {
		t.Fatalf("nodes = %v", tr2.Data)
	}
}

func TestGetDeployment(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "payment-api", Namespace: "production"},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(3),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "pay:v1"}}}},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 2},
	}
	tr, err := (&deploymentTool{base{reader: testReader(dep)}}).Execute(context.Background(), json.RawMessage(`{"namespace":"production","name":"payment-api"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := tr.Data.(map[string]any)
	if m["replicas"] != int32(3) || m["readyReplicas"] != int32(2) {
		t.Fatalf("deployment summary = %v", m)
	}
}

func TestGetServiceEndpointsAndSlices(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.0.0.1", Selector: map[string]string{"app": "web"}},
	}
	eps := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Subsets:    []corev1.EndpointSubset{{Addresses: []corev1.EndpointAddress{{IP: "10.1.0.2"}}}},
	}
	es := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "default", Labels: map[string]string{"kubernetes.io/service-name": "web"}},
		Endpoints:  []discoveryv1.Endpoint{{Addresses: []string{"10.1.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(true)}}},
	}
	r := testReader(svc, eps, es)
	if tr, err := (&serviceTool{base{reader: r}}).Execute(context.Background(), json.RawMessage(`{"namespace":"default","name":"web"}`)); err != nil || tr.Data.(map[string]any)["clusterIP"] != "10.0.0.1" {
		t.Fatalf("service: %v err=%v", tr, err)
	}
	if tr, err := (&endpointsTool{base{reader: r}}).Execute(context.Background(), json.RawMessage(`{"namespace":"default","name":"web"}`)); err != nil || tr.Data.(map[string]any)["endpointCount"] != 1 {
		t.Fatalf("endpoints: %v err=%v", tr, err)
	}
	tr, err := (&endpointSlicesTool{base{reader: r}}).Execute(context.Background(), json.RawMessage(`{"namespace":"default","service":"web"}`))
	if err != nil || tr.Data.(map[string]any)["count"] != 1 {
		t.Fatalf("endpointslices: %v err=%v", tr, err)
	}
}

func TestGetStorage(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "default"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-1"},
		Status:     corev1.PersistentVolumeStatus{Phase: corev1.VolumeAvailable},
	}
	sc := &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: "fast"},
		Provisioner: "kubernetes.io/aws-ebs",
	}
	r := testReader(pvc, pv, sc)
	tr, err := (&pvcTool{base{reader: r}}).Execute(context.Background(), json.RawMessage(`{"namespace":"default","name":"data"}`))
	if err != nil || tr.Data.(map[string]any)["phase"] != "Pending" {
		t.Fatalf("pvc: %v err=%v", tr, err)
	}
	tr, err = (&pvTool{base{reader: r}}).Execute(context.Background(), json.RawMessage(`{"name":"pv-1"}`))
	if err != nil || tr.Data.(map[string]any)["phase"] != "Available" {
		t.Fatalf("pv: %v err=%v", tr, err)
	}
	tr, err = (&storageClassTool{base{reader: r}}).Execute(context.Background(), json.RawMessage(`{"name":"fast"}`))
	if err != nil || tr.Data.(map[string]any)["provisioner"] != "kubernetes.io/aws-ebs" {
		t.Fatalf("sc: %v err=%v", tr, err)
	}
}

func TestGetEvents(t *testing.T) {
	ev1 := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web-0", Namespace: "default"},
		Type:           "Warning", Reason: "FailedScheduling", Message: "0/1 nodes are available",
		Count: 3,
	}
	ev2 := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "e2", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "other", Namespace: "default"},
		Type:           "Normal", Reason: "Pulled",
	}
	tr, err := (&eventsTool{base{reader: testReader(ev1, ev2)}}).Execute(context.Background(), json.RawMessage(`{"namespace":"default","kind":"Pod","name":"web-0"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := tr.Data.(map[string]any)
	if m["count"] != 1 {
		t.Fatalf("events = %v", m)
	}
	items := m["items"].([]map[string]any)
	if items[0]["reason"] != "FailedScheduling" {
		t.Fatalf("event = %v", items[0])
	}
}

func TestGetLogs(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-0", Namespace: "default"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}},
	}
	cs := fake.NewSimpleClientset(pod)
	cs.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() == "log" {
			return true, &runtime.Unknown{Raw: []byte("fake log line\nsecond line\n"), ContentType: runtime.ContentTypeJSON}, nil
		}
		return false, nil, nil
	})
	r := kubernetes.NewClientWithClientset(cs)
	tr, err := (&logsTool{base: base{reader: r}}).Execute(context.Background(), json.RawMessage(`{"namespace":"default","pod":"web-0","container":"web","tailLines":50}`))
	if err != nil {
		t.Fatal(err)
	}
	m := tr.Data.(map[string]any)
	if m["pod"] != "web-0" || m["previous"] != false {
		t.Fatalf("日志元数据 = %v", m)
	}
	if !strings.Contains(tr.Text, "fake log line") {
		t.Fatalf("日志正文应在 Text: %q", tr.Text)
	}
	tr2, err := (&logsTool{base: base{reader: r}, previous: true}).Execute(context.Background(), json.RawMessage(`{"namespace":"default","pod":"web-0"}`))
	if err != nil {
		t.Fatal(err)
	}
	if tr2.Data.(map[string]any)["previous"] != true {
		t.Fatalf("previous logs = %v", tr2.Data)
	}
}

func TestGetNamespaceAndResources(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "production"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}}
	r := testReader(ns)
	tr, err := (&namespaceTool{base{reader: r}}).Execute(context.Background(), json.RawMessage(`{"name":"production"}`))
	if err != nil || tr.Data.(map[string]any)["phase"] != "Active" {
		t.Fatalf("namespace: %v err=%v", tr, err)
	}
	tr, err = (&resourcesTool{base{reader: r}}).Execute(context.Background(), json.RawMessage(`{"kind":"namespaces"}`))
	if err != nil || tr.Data.(map[string]any)["count"] != 1 {
		t.Fatalf("resources: %v err=%v", tr, err)
	}
	tr, err = (&resourcesTool{base{reader: r}}).Execute(context.Background(), json.RawMessage(`{"kind":"bogus"}`))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Data.(map[string]any)["error"] == nil {
		t.Fatal("不支持的 kind 应返回错误结果")
	}
}

func TestRegisterAllCount(t *testing.T) {
	reg := tools.NewRegistry()
	if err := RegisterAll(reg, testReader()); err != nil {
		t.Fatal(err)
	}
	if len(reg.All()) != 22 {
		t.Fatalf("工具数 = %d, want 22", len(reg.All()))
	}
	for _, want := range []string{"get_pod", "get_logs", "get_previous_logs", "get_resources", "get_events", "get_node_usage", "get_configmap", "get_configmaps"} {
		if _, ok := reg.Lookup(want); !ok {
			t.Fatalf("缺少工具 %s", want)
		}
	}
}

func TestSchemaMetadataForReadOnlyTools(t *testing.T) {
	reg := tools.NewRegistry()
	if err := RegisterAll(reg, testReader()); err != nil {
		t.Fatal(err)
	}
	specs := reg.Schemas()
	byName := map[string]string{}
	for _, spec := range specs {
		byName[spec.Name] = spec.Description
	}
	for _, name := range []string{"get_pod", "get_deployment", "get_nodes"} {
		desc, ok := byName[name]
		if !ok {
			t.Fatalf("缺少工具 %s", name)
		}
		for _, want := range []string{"[READ ONLY]", "[RISK: SAFE]", "[APPROVAL REQUIRED: NO]"} {
			if !strings.Contains(desc, want) {
				t.Fatalf("%s description 缺少 %q: %q", name, want, desc)
			}
		}
	}
}

func int32Ptr(v int32) *int32 { return &v }
func boolPtr(v bool) *bool    { return &v }

// fakeMetricsReader 是节点指标读取桩。
type fakeMetricsReader struct {
	items []metricsv1beta1.NodeMetrics
}

func (f *fakeMetricsReader) ListNodeMetrics(context.Context) ([]metricsv1beta1.NodeMetrics, error) {
	return f.items, nil
}

func TestGetNodeUsage(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("8"),
			corev1.ResourceMemory: resource.MustParse("16Gi"),
		}},
	}
	metric := metricsv1beta1.NodeMetrics{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Timestamp:  metav1.Time{Time: time.Now()},
		Usage: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		},
	}
	reader := testReader(node)
	tr, err := (&nodeUsageTool{base: base{reader: reader}, metrics: &fakeMetricsReader{items: []metricsv1beta1.NodeMetrics{metric}}}).
		Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := tr.Data.(map[string]any)
	items := m["items"].([]map[string]any)
	if len(items) != 1 || items[0]["name"] != "node-1" {
		t.Fatalf("items = %v", items)
	}
	if items[0]["cpuPercent"] != 25.0 || items[0]["memoryPercent"] != 25.0 {
		t.Fatalf("percent = %v", items[0])
	}
	if items[0]["cpuMillicores"] != int64(2000) || items[0]["memoryMiB"] != int64(4096) {
		t.Fatalf("usage = %v", items[0])
	}
}

func TestGetNodeUsageUnavailable(t *testing.T) {
	tr, err := (&nodeUsageTool{base: base{reader: testReader()}}).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Data.(map[string]any)["error"] == nil {
		t.Fatal("metrics 不可用应返回错误")
	}
}
