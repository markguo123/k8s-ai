package scanner

import (
	"context"
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/k8s-ai/k8s-ai/internal/kubernetes"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// TestPhase1CollectsCLevel 验证 Phase1 新增采集 HPA / PDB / ClusterRole（P7.5）。
func TestPhase1CollectsCLevel(t *testing.T) {
	minReplicas := int32(2)
	maxReplicas := int32(6)
	objs := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&autoscalingv2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{Name: "web-hpa", Namespace: "default"},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				MinReplicas:     &minReplicas,
				MaxReplicas:     maxReplicas,
				ScaleTargetRef:  autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "web"},
			},
			Status: autoscalingv2.HorizontalPodAutoscalerStatus{
				DesiredReplicas: 4,
				CurrentReplicas: 3,
			},
		},
		&policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Name: "app-pdb", Namespace: "default"},
			Spec: policyv1.PodDisruptionBudgetSpec{
				MinAvailable: &intstr.IntOrString{Type: intstr.Int, IntVal: 1},
			},
			Status: policyv1.PodDisruptionBudgetStatus{
				DisruptionsAllowed: 0,
			},
		},
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: "admin"},
			Rules: []rbacv1.PolicyRule{
				{Verbs: []string{"*"}, Resources: []string{"*"}, APIGroups: []string{"*"}},
			},
		},
	}
	cs := fake.NewSimpleClientset(objs...)
	snap, err := New(kubernetes.NewClientWithClientset(cs)).Phase1(
		context.Background(), model.ScanOptions{Concurrency: 4, CollectEvents: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Hpas) != 1 {
		t.Fatalf("hpa = %d, want 1", len(snap.Hpas))
	}
	h := snap.Hpas[0]
	if h.Ref.Name != "web-hpa" || h.Ref.Namespace != "default" {
		t.Fatalf("hpa ref = %+v", h.Ref)
	}
	if h.MinReplicas == nil || *h.MinReplicas != 2 {
		t.Fatalf("hpa min = %v", h.MinReplicas)
	}
	if h.MaxReplicas != 6 || h.DesiredReplicas != 4 || h.CurrentReplicas != 3 {
		t.Fatalf("hpa spec/status = %+v", h)
	}
	if h.ScaleTarget != "Deployment/web" {
		t.Fatalf("hpa scale target = %q", h.ScaleTarget)
	}
	if len(snap.Pdbs) != 1 {
		t.Fatalf("pdb = %d, want 1", len(snap.Pdbs))
	}
	p := snap.Pdbs[0]
	if p.DisruptionsAllowed != 0 || p.MinAvailable != "1" {
		t.Fatalf("pdb = %+v", p)
	}
	if len(snap.ClusterRoles) != 1 {
		t.Fatalf("cluster roles = %d, want 1", len(snap.ClusterRoles))
	}
	cr := snap.ClusterRoles[0]
	if cr.Ref.Name != "admin" || len(cr.Rules) != 1 || cr.Rules[0].Verbs[0] != "*" {
		t.Fatalf("cluster role = %+v", cr)
	}
}
