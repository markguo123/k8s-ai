package executor

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func testDeployment(ns, name string, replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"app": name}},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(replicas),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "img:v1"}}},
			},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
}

func int32Ptr(v int32) *int32 { return &v }

func TestScaleDeployment(t *testing.T) {
	cs := fake.NewSimpleClientset(testDeployment("default", "web", 2))
	r := NewRunner(Options{Clientset: cs})
	res, err := r.ScaleDeployment(context.Background(), "default", "web", 5)
	if err != nil {
		t.Fatal(err)
	}
	dep, _ := cs.AppsV1().Deployments("default").Get(context.Background(), "web", metav1.GetOptions{})
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 5 {
		t.Fatalf("replicas = %v", dep.Spec.Replicas)
	}
	if res.Verification == nil || res.Verification["readyReplicas"] != int32(1) {
		t.Fatalf("verification = %v", res.Verification)
	}
}

func TestPatchDeployment(t *testing.T) {
	cs := fake.NewSimpleClientset(testDeployment("default", "web", 2))
	r := NewRunner(Options{Clientset: cs})
	if _, err := r.PatchDeployment(context.Background(), "default", "web", `{"spec":{"replicas":7}}`); err != nil {
		t.Fatal(err)
	}
	dep, _ := cs.AppsV1().Deployments("default").Get(context.Background(), "web", metav1.GetOptions{})
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 7 {
		t.Fatalf("replicas = %v", dep.Spec.Replicas)
	}
}

func TestRestartRollout(t *testing.T) {
	cs := fake.NewSimpleClientset(testDeployment("default", "web", 2))
	r := NewRunner(Options{Clientset: cs})
	if _, err := r.RestartRollout(context.Background(), "default", "web"); err != nil {
		t.Fatal(err)
	}
	dep, _ := cs.AppsV1().Deployments("default").Get(context.Background(), "web", metav1.GetOptions{})
	if dep.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] == "" {
		t.Fatal("缺少 restartedAt 注解")
	}
}

func TestLabelResource(t *testing.T) {
	cs := fake.NewSimpleClientset(testDeployment("default", "web", 1))
	r := NewRunner(Options{Clientset: cs})
	if _, err := r.LabelResource(context.Background(), "deployment", "default", "web", "version", "v2"); err != nil {
		t.Fatal(err)
	}
	dep, _ := cs.AppsV1().Deployments("default").Get(context.Background(), "web", metav1.GetOptions{})
	if dep.Labels["version"] != "v2" {
		t.Fatalf("labels = %v", dep.Labels)
	}
	if _, err := r.LabelResource(context.Background(), "bogus", "default", "web", "a", "b"); err == nil {
		t.Fatal("不支持的 kind 应报错")
	}
}

func TestPatchConfigMap(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "nginx-config"},
		Data:       map[string]string{"default.conf": "listen 8x0;"},
	})
	r := NewRunner(Options{Clientset: cs})
	if _, err := r.PatchConfigMap(context.Background(), "default", "nginx-config", `{"data":{"default.conf":"listen 80;"}}`); err != nil {
		t.Fatal(err)
	}
	cm, _ := cs.CoreV1().ConfigMaps("default").Get(context.Background(), "nginx-config", metav1.GetOptions{})
	if cm.Data["default.conf"] != "listen 80;" {
		t.Fatalf("configmap data = %v", cm.Data)
	}
}

func TestDeletePod(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web-0"}})
	r := NewRunner(Options{Clientset: cs})
	grace := int64(5)
	if _, err := r.DeletePod(context.Background(), "default", "web-0", &grace); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().Pods("default").Get(context.Background(), "web-0", metav1.GetOptions{}); err == nil {
		t.Fatal("Pod 应已被删除")
	}
}
