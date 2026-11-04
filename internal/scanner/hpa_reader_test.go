package scanner

import (
	"context"
	"reflect"
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

// TestPhase1CollectsCLevel 验证 Phase1 新增采集 HPA / PDB / RBAC（P7.5）。
func TestPhase1CollectsCLevel(t *testing.T) {
	minReplicas := int32(2)
	maxReplicas := int32(6)
	objs := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&autoscalingv2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{Name: "web-hpa", Namespace: "default"},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				MinReplicas:    &minReplicas,
				MaxReplicas:    maxReplicas,
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "web"},
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
				{
					Verbs:           []string{"*"},
					Resources:       []string{"*"},
					APIGroups:       []string{"*"},
					NonResourceURLs: []string{"/metrics", "/healthz"},
				},
			},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "admin-binding"},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "admin",
			},
			Subjects: []rbacv1.Subject{
				{Kind: "ServiceAccount", Name: "k8s-ai", Namespace: "k8s-ai"},
			},
		},
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-reader", Namespace: "default"},
			Rules: []rbacv1.PolicyRule{
				{
					Verbs:           []string{"watch", "get", "list"},
					Resources:       []string{"pods", "deployments"},
					ResourceNames:   []string{"web", "api"},
					APIGroups:       []string{"apps", ""},
					NonResourceURLs: []string{"/version", "/healthz"},
				},
			},
		},
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "app-binding", Namespace: "default"},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "pod-reader",
			},
			Subjects: []rbacv1.Subject{
				{Kind: "User", APIGroup: "rbac.authorization.k8s.io", Name: "alice"},
				{Kind: "ServiceAccount", Name: "builder", Namespace: "default"},
				{Kind: "Group", APIGroup: "rbac.authorization.k8s.io", Name: "devs"},
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
	if !reflect.DeepEqual(cr.Rules[0].NonResourceURLs, []string{"/healthz", "/metrics"}) {
		t.Fatalf("cluster role nonResourceURLs = %v", cr.Rules[0].NonResourceURLs)
	}
	if len(snap.ClusterRoleBindings) != 1 {
		t.Fatalf("cluster role bindings = %d, want 1", len(snap.ClusterRoleBindings))
	}
	crb := snap.ClusterRoleBindings[0]
	if crb.Ref.Kind != "ClusterRoleBinding" || crb.Ref.Name != "admin-binding" || crb.Ref.Namespace != "" {
		t.Fatalf("cluster role binding ref = %+v", crb.Ref)
	}
	if crb.RoleRef.Kind != "ClusterRole" || crb.RoleRef.Name != "admin" {
		t.Fatalf("cluster role binding roleRef = %+v", crb.RoleRef)
	}
	if len(crb.Subjects) != 1 || crb.Subjects[0].Kind != "ServiceAccount" ||
		crb.Subjects[0].Name != "k8s-ai" || crb.Subjects[0].Namespace != "k8s-ai" {
		t.Fatalf("cluster role binding subjects = %+v", crb.Subjects)
	}

	if len(snap.Roles) != 1 {
		t.Fatalf("roles = %d, want 1", len(snap.Roles))
	}
	role := snap.Roles[0]
	if role.Ref.Kind != "Role" || role.Ref.Namespace != "default" || role.Ref.Name != "pod-reader" {
		t.Fatalf("role ref = %+v", role.Ref)
	}
	if len(role.Rules) != 1 {
		t.Fatalf("role rules = %d, want 1", len(role.Rules))
	}
	if !reflect.DeepEqual(role.Rules[0].APIGroups, []string{"", "apps"}) ||
		!reflect.DeepEqual(role.Rules[0].Resources, []string{"deployments", "pods"}) ||
		!reflect.DeepEqual(role.Rules[0].ResourceNames, []string{"api", "web"}) ||
		!reflect.DeepEqual(role.Rules[0].Verbs, []string{"get", "list", "watch"}) ||
		!reflect.DeepEqual(role.Rules[0].NonResourceURLs, []string{"/healthz", "/version"}) {
		t.Fatalf("role rule not normalized/sorted: %+v", role.Rules[0])
	}
	if len(snap.RoleBindings) != 1 {
		t.Fatalf("role bindings = %d, want 1", len(snap.RoleBindings))
	}
	rb := snap.RoleBindings[0]
	if rb.Ref.Kind != "RoleBinding" || rb.Ref.Namespace != "default" || rb.Ref.Name != "app-binding" {
		t.Fatalf("role binding ref = %+v", rb.Ref)
	}
	if rb.RoleRef.Kind != "Role" || rb.RoleRef.Name != "pod-reader" {
		t.Fatalf("role binding roleRef = %+v", rb.RoleRef)
	}
	gotSubjects := []string{}
	for _, subject := range rb.Subjects {
		gotSubjects = append(gotSubjects, subject.Kind)
	}
	if !reflect.DeepEqual(gotSubjects, []string{"Group", "ServiceAccount", "User"}) {
		t.Fatalf("role binding subjects not sorted: %+v", rb.Subjects)
	}
}
