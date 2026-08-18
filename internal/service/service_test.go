package service

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"encoding/json"
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/k8s-ai/k8s-ai/internal/correlation"
	"github.com/k8s-ai/k8s-ai/internal/kubernetes"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/model"
	appsv1 "k8s.io/api/apps/v1"
)

// TestRunFindsCrashLoop 端到端验证：fake 集群中的 CrashLoopBackOff Pod
// 应产出 Finding，且 scan 全流程（Phase1→索引→规则→Phase2）无写操作。
func TestRunFindsCrashLoop(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "default"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "nginx"}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "web", Image: "nginx", RestartCount: 5, Ready: false,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off"}},
				}},
			},
		},
	)
	svc := newService(
		func(opts model.ScanOptions) (*kubernetes.Client, error) {
			return kubernetes.NewClientWithClientset(cs), nil
		},
		func(model.LLMOptions) llm.LLMClient { return &fakeLLM{} },
	)
	opts := model.ScanOptions{
		Timeout:         30 * time.Second,
		Concurrency:     4,
		CollectLogs:     true,
		MaxLogLines:     100,
		MaxLogBytes:     1024,
		MaxLogLineBytes: 256,
		ReportMode:      "latest",
		ReportDirectory: t.TempDir(),
	}
	result, err := svc.Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range result.Findings {
		if f.Rule == "CrashLoopBackOff" {
			found = true
			if f.Resource.Name != "web-abc" {
				t.Fatalf("finding 资源 = %+v", f.Resource)
			}
		}
	}
	if !found {
		t.Fatalf("未找到 CrashLoopBackOff Finding: %+v", result.Findings)
	}
	if result.HealthScore.Score >= 100 {
		t.Fatalf("存在 Finding 时评分应低于 100，got %d", result.HealthScore.Score)
	}
	if len(result.ReportPaths) != 2 {
		t.Fatalf("latest 模式应写 2 个文件，got %d", len(result.ReportPaths))
	}
	// 只读保证：全程不允许出现写动词。
	for _, a := range cs.Actions() {
		switch a.GetVerb() {
		case "list", "get":
		default:
			t.Fatalf("只读违规: %s %s", a.GetVerb(), a.GetResource().Resource)
		}
	}
}

// TestRunPodTarget 验证单 Pod 目标扫描只产出该 Pod 的 Finding。
func TestRunPodTarget(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-0", Namespace: "default"},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "web", RestartCount: 9,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				}},
			},
		},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "good-0", Namespace: "default"}},
	)
	svc := newService(
		func(opts model.ScanOptions) (*kubernetes.Client, error) {
			return kubernetes.NewClientWithClientset(cs), nil
		},
		func(model.LLMOptions) llm.LLMClient { return &fakeLLM{} },
	)
	opts := model.ScanOptions{
		Timeout:     30 * time.Second,
		Concurrency: 4,
		CollectLogs: false,
		Namespace:   "default",
		PodTarget:   "bad-0",
		ReportMode:  "none",
	}
	result, err := svc.Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %d, want 1（仅目标 Pod）: %+v", len(result.Findings), result.Findings)
	}
	if result.Findings[0].Resource.Name != "bad-0" {
		t.Fatalf("finding 资源 = %+v, want bad-0", result.Findings[0].Resource)
	}
	if result.Meta.Pod != "bad-0" {
		t.Fatalf("meta.pod = %q, want bad-0", result.Meta.Pod)
	}
}

// fakeLLM 服务层测试用 LLM 桩。
type fakeLLM struct {
	content string
	err     error
	calls   int
}

func (f *fakeLLM) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &llm.ChatResponse{Content: f.content}, nil
}

// TestRunWithLLM 验证 LLM 诊断接入主流程：诊断结果进入 ScanResult。
func TestRunWithLLM(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "default"},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "web", RestartCount: 5,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				}},
			},
		},
	)
	var fake *fakeLLM
	svc := newService(
		func(opts model.ScanOptions) (*kubernetes.Client, error) {
			return kubernetes.NewClientWithClientset(cs), nil
		},
		func(model.LLMOptions) llm.LLMClient { return fake },
	)
	fake = &fakeLLM{content: `{"summary":"OOM","rootCause":"内存限制过低","confidence":0.9,"evidenceChain":[],"investigation":["kubectl -n default get pod web-abc"],"remediation":[],"verification":[],"risk":"MEDIUM"}`}
	opts := model.ScanOptions{
		Timeout:     30 * time.Second,
		Concurrency: 4,
		CollectLogs: false,
		ReportMode:  "none",
		LLM: model.LLMOptions{
			Enabled:        true,
			Model:          "qwen-plus",
			MaxInputTokens: 8192,
			MaxTotalTokens: 32768,
			MaxFindings:    10,
		},
	}
	result, err := svc.Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnoses) != 1 || !result.Diagnoses[0].LLMUsed {
		t.Fatalf("diagnoses = %+v", result.Diagnoses)
	}
	if result.LLMSummary.Calls != 1 {
		t.Fatalf("llm summary = %+v", result.LLMSummary)
	}
}

// TestEnrichRelatedEvidence 验证 Deployment Finding 会带上关联异常 Pod 的崩溃证据。
func TestEnrichRelatedEvidence(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Pods: []model.PodInfo{{
			Ref:       model.ResourceRef{Kind: "Pod", Namespace: "agentops", Name: "app-abc"},
			OwnerRefs: []model.ResourceRef{{Kind: "ReplicaSet", Namespace: "agentops", Name: "app-rs"}},
		}},
		Workloads: []model.WorkloadInfo{
			{Ref: model.ResourceRef{Kind: "ReplicaSet", Namespace: "agentops", Name: "app-rs"}, OwnerRefs: []model.ResourceRef{{Kind: "Deployment", Namespace: "agentops", Name: "app"}}},
			{Ref: model.ResourceRef{Kind: "Deployment", Namespace: "agentops", Name: "app"}},
		},
	}
	idx := correlation.Build(snap)
	depFinding := &model.Finding{
		ID: "f1", Rule: "DeploymentReplica",
		Resource: model.ResourceRef{Kind: "Deployment", Namespace: "agentops", Name: "app"},
	}
	podFinding := &model.Finding{
		ID: "f2", Rule: "CrashLoopBackOff",
		Resource: model.ResourceRef{Kind: "Pod", Namespace: "agentops", Name: "app-abc"},
		Evidence: []model.Evidence{{ID: "E1", Kind: model.EvLog, Value: "INFO x\npanic: StartConsumer fail, topic not found\n"}},
	}
	enrichRelatedEvidence([]*model.Finding{depFinding, podFinding}, idx)
	got := ""
	for _, e := range depFinding.Evidence {
		if e.Key == "affectedPod" {
			got = e.Value
		}
	}
	if !strings.Contains(got, "app-abc") || !strings.Contains(got, "panic: StartConsumer fail") {
		t.Fatalf("Deployment Finding 缺少关联 Pod 证据: %q", got)
	}
}

// TestRunEvidenceIDConsistent 回归（P0-2）：报告中的 Finding 证据编号必须与
// LLM 诊断引用的 E-ID 一致（关联证据补全必须在 result 拷贝前执行）。
func TestRunEvidenceIDConsistent(t *testing.T) {
	replicas := int32(1)
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
			Status:     appsv1.DeploymentStatus{ReadyReplicas: 0, AvailableReplicas: 0, UpdatedReplicas: 0},
		},
		&appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "web-rs", Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web", UID: "dep-uid"}},
			},
			Spec: appsv1.ReplicaSetSpec{Replicas: &replicas},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "web-abc", Namespace: "default",
				Labels:          map[string]string{"app": "web"},
				OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-rs", UID: "rs-uid"}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "web", RestartCount: 9,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				}},
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "web-svc", Namespace: "default"},
			Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "web"}},
		},
	)
	fake := &fakeLLM{content: `{"summary":"s","rootCause":"r","confidence":0.8,"confidenceLevel":"CONFIRMED","causalChain":"c","evidenceChain":["E2"],"investigation":["kubectl -n default get pod web-abc"],"remediation":[],"verification":[],"risk":"MEDIUM","uncertainty":"u"}`}
	svc := newService(
		func(opts model.ScanOptions) (*kubernetes.Client, error) {
			return kubernetes.NewClientWithClientset(cs), nil
		},
		func(model.LLMOptions) llm.LLMClient { return fake },
	)
	opts := model.ScanOptions{
		Timeout:     30 * time.Second,
		Concurrency: 4,
		Namespace:   "default",
		ReportMode:  "none",
		LLM: model.LLMOptions{
			Enabled: true, MaxInputTokens: 8192, MaxTotalTokens: 32768, MaxFindings: 10,
		},
	}
	result, err := svc.Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	var depFinding, podFinding *model.Finding
	for i := range result.Findings {
		switch result.Findings[i].Rule {
		case "DeploymentReplica":
			depFinding = &result.Findings[i]
		case "CrashLoopBackOff":
			podFinding = &result.Findings[i]
		}
	}
	if depFinding == nil || podFinding == nil {
		t.Fatal("未找到 Deployment/Pod Finding")
	}
	// Incident 聚合：根因为 Pod，Deployment/Service 为派生成员。
	if len(result.Incidents) != 1 || result.Incidents[0].Root.ID != podFinding.ID {
		t.Fatalf("incidents = %+v, 根因应为 Pod", result.Incidents)
	}
	members := map[string]bool{}
	for _, m := range result.Incidents[0].Members {
		members[m.ID] = true
	}
	if !members[depFinding.ID] {
		t.Fatalf("Deployment 应为派生成员: %+v", result.Incidents[0].Members)
	}
	// 派生成员仍带 affectedPod/E4 证据（报告规则层可见）。
	hasAffected := false
	hasE4 := false
	for _, e := range depFinding.Evidence {
		if e.Key == "affectedPod" {
			hasAffected = true
		}
		if e.ID == "E4" {
			hasE4 = true
		}
	}
	if !hasAffected || !hasE4 {
		t.Fatalf("Deployment Finding 证据缺失 affectedPod/E4: %+v", depFinding.Evidence)
	}
	// 诊断只针对 Incident 根因（Pod），证据链与 Pod 证据一致。
	var rootDiag *model.Diagnosis
	for _, d := range result.Diagnoses {
		if d.FindingID == podFinding.ID {
			rootDiag = &d
		}
	}
	if rootDiag == nil || !rootDiag.LLMUsed {
		t.Fatalf("缺少根因诊断: %+v", result.Diagnoses)
	}
	found := false
	for _, ref := range rootDiag.EvidenceChain {
		if ref == "E2" {
			found = true
		}
	}
	if !found || rootDiag.ConfidenceLevel != "CONFIRMED" {
		t.Fatalf("诊断证据链/置信度异常: %+v", rootDiag)
	}
}

// TestRunHistoryDiff 验证两次扫描的历史对比：问题消失后标记为"恢复"。
func TestRunHistoryDiff(t *testing.T) {
	dir := t.TempDir()
	mkCluster := func(withPod bool) *fake.Clientset {
		objs := []runtime.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}}
		if withPod {
			objs = append(objs, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "bad-0", Namespace: "default"},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{{
						Name: "web", RestartCount: 9,
						State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
					}},
				},
			})
		}
		return fake.NewSimpleClientset(objs...)
	}
	run := func(cs *fake.Clientset) *model.ScanResult {
		svc := newService(
			func(opts model.ScanOptions) (*kubernetes.Client, error) {
				return kubernetes.NewClientWithClientset(cs), nil
			},
			func(model.LLMOptions) llm.LLMClient { return &fakeLLM{} },
		)
		res, err := svc.Run(context.Background(), model.ScanOptions{
			Timeout: 30 * time.Second, Concurrency: 4,
			ReportDirectory: dir, ReportMode: "none",
		})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	first := run(mkCluster(true))
	if len(first.Findings) == 0 {
		t.Fatal("首轮应发现异常")
	}
	// 把首轮结果写为上一份 latest.json
	raw, _ := json.Marshal(first)
	if err := os.WriteFile(filepath.Join(dir, "latest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	second := run(mkCluster(false))
	if second.History == nil {
		t.Fatal("第二轮应有历史对比")
	}
	if len(second.History.Recovered) != 1 {
		t.Fatalf("recovered = %+v", second.History.Recovered)
	}
}

// TestEnrichConfigMaps 验证 Pod 引用的 ConfigMap 名称进入证据，且关联 Pod 详情携带名称。
func TestEnrichConfigMaps(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Pods: []model.PodInfo{{
			Ref:        model.ResourceRef{Kind: "Pod", Namespace: "yanshou-nginx", Name: "nginx-abc"},
			ConfigMaps: []model.ResourceRef{{Kind: "ConfigMap", Namespace: "yanshou-nginx", Name: "nginx-config"}},
			SecretRefs: []model.ResourceRef{{Kind: "Secret", Namespace: "yanshou-nginx", Name: "tls-secret"}},
			OwnerRefs:  []model.ResourceRef{{Kind: "ReplicaSet", Namespace: "yanshou-nginx", Name: "nginx-rs"}},
		}},
		Workloads: []model.WorkloadInfo{
			{Ref: model.ResourceRef{Kind: "ReplicaSet", Namespace: "yanshou-nginx", Name: "nginx-rs"}, OwnerRefs: []model.ResourceRef{{Kind: "Deployment", Namespace: "yanshou-nginx", Name: "nginx"}}},
			{Ref: model.ResourceRef{Kind: "Deployment", Namespace: "yanshou-nginx", Name: "nginx"}},
		},
	}
	idx := correlation.Build(snap)
	podF := &model.Finding{ID: "p", Rule: "CrashLoopBackOff", Resource: model.ResourceRef{Kind: "Pod", Namespace: "yanshou-nginx", Name: "nginx-abc"}}
	depF := &model.Finding{ID: "d", Rule: "DeploymentReplica", Resource: model.ResourceRef{Kind: "Deployment", Namespace: "yanshou-nginx", Name: "nginx"}}
	enrichRelatedEvidence([]*model.Finding{podF, depF}, idx)
	got := ""
	for _, e := range podF.Evidence {
		if e.Key == "configMaps" {
			got = e.Value
		}
	}
	if !strings.Contains(got, "yanshou-nginx/nginx-config") {
		t.Fatalf("Pod Finding 缺少 ConfigMaps 证据: %q", got)
	}
	secGot := ""
	for _, e := range podF.Evidence {
		if e.Key == "secretRefs" {
			secGot = e.Value
		}
	}
	if !strings.Contains(secGot, "yanshou-nginx/tls-secret") {
		t.Fatalf("Pod Finding 缺少 secretRefs 证据: %q", secGot)
	}
	found := false
	for _, e := range depF.Evidence {
		if strings.Contains(e.Value, "nginx-config") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Deployment Finding 的关联 Pod 详情应包含 ConfigMaps 名称: %+v", depF.Evidence)
	}
}

// TestRunIncidentGrouping 端到端：Pod CrashLoop + Deployment + Service 聚合为 1 个 Incident。
func TestRunIncidentGrouping(t *testing.T) {
	replicas := int32(1)
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
			Status:     appsv1.DeploymentStatus{ReadyReplicas: 0},
		},
		&appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Name: "web-rs", Namespace: "default", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web", UID: "d1"}}},
			Spec:       appsv1.ReplicaSetSpec{Replicas: &replicas},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "default", Labels: map[string]string{"app": "web"}, OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-rs", UID: "r1"}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "web", RestartCount: 5,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				}},
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "web-svc", Namespace: "default"},
			Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "web"}},
		},
	)
	svc := newService(
		func(opts model.ScanOptions) (*kubernetes.Client, error) {
			return kubernetes.NewClientWithClientset(cs), nil
		},
		func(model.LLMOptions) llm.LLMClient {
			return &fakeLLM{content: `{"summary":"s","rootCause":"r","confidence":0.8,"evidenceChain":["E1"],"investigation":[],"remediation":[],"verification":[],"risk":"LOW"}`}
		},
	)
	result, err := svc.Run(context.Background(), model.ScanOptions{
		Timeout: 30 * time.Second, Concurrency: 4, ReportMode: "none",
		LLM: model.LLMOptions{Enabled: true, MaxInputTokens: 8192, MaxTotalTokens: 32768, MaxFindings: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Incidents) != 1 {
		t.Fatalf("incidents = %d, want 1: %+v", len(result.Incidents), result.Incidents)
	}
	if result.Incidents[0].Root.Rule != "CrashLoopBackOff" {
		t.Fatalf("根因规则 = %s, want CrashLoopBackOff", result.Incidents[0].Root.Rule)
	}
	// 根因只扣一次分（派生成员不重复扣分）。
	if len(result.HealthScore.Penalties) != 1 {
		t.Fatalf("penalties = %d, want 1（仅根因）: %+v", len(result.HealthScore.Penalties), result.HealthScore.Penalties)
	}
}
