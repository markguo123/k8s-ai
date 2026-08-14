package service

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/k8s-ai/k8s-ai/internal/correlation"
	"github.com/k8s-ai/k8s-ai/internal/kubernetes"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/model"
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
