package kubernetes

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/scanner"
)

// Compile-time proof that the read-only facade satisfies the Reader contract.
var _ scanner.Reader = (*Client)(nil)

func TestReadOnlyActions(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-0", Namespace: "default"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		&corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "e1", Namespace: "default"}},
	)
	c := NewClientWithClientset(cs)
	ctx := context.Background()

	if _, err := c.ServerVersion(ctx); err != nil {
		t.Fatalf("ServerVersion: %v", err)
	}
	if _, err := c.ListNamespaces(ctx); err != nil {
		t.Fatalf("ListNamespaces: %v", err)
	}
	if _, err := c.ListPods(ctx, ""); err != nil {
		t.Fatalf("ListPods: %v", err)
	}
	if _, err := c.ListNodes(ctx); err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if _, err := c.ListEvents(ctx, "default"); err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if _, err := c.ListServices(ctx, ""); err != nil {
		t.Fatalf("ListServices: %v", err)
	}
	if _, err := c.ListPersistentVolumes(ctx); err != nil {
		t.Fatalf("ListPersistentVolumes: %v", err)
	}

	for _, a := range cs.Actions() {
		switch a.GetVerb() {
		case "list", "get":
		default:
			t.Fatalf("read-only violation: verb %q on resource %q", a.GetVerb(), a.GetResource().Resource)
		}
		if a.GetResource().Resource == "secrets" {
			t.Fatal("secrets must never be accessed")
		}
	}
}

func TestGetPodLogsOptionsFlow(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-0", Namespace: "default"}},
	)
	c := NewClientWithClientset(cs)
	tail := int64(100)
	_, _ = c.GetPodLogs(context.Background(), "default", "web-0", "web", model.LogOptions{
		TailLines:    &tail,
		MaxBytes:     1024,
		MaxLineBytes: 256,
	})
	// Logs may not be fully simulated by the fake client; the critical
	// guarantee is that no mutating action is ever recorded.
	for _, a := range cs.Actions() {
		if a.GetVerb() != "get" {
			t.Fatalf("GetPodLogs produced unexpected verb %q", a.GetVerb())
		}
	}
}

func TestTruncateLogs(t *testing.T) {
	raw := []byte("line-one\nline-two-is-long\nthree")
	out := truncateLogs(raw, 64*1024, 8)
	lines := strings.Split(string(out), "\n")
	if len(lines) != 3 || lines[0] != "line-one" || lines[1] != "line-two" || lines[2] != "three" {
		t.Fatalf("unexpected truncation result: %q", out)
	}
	capped := truncateLogs(raw, 10, 1024)
	if len(capped) != 10 || string(capped) != "line-one\nl" {
		t.Fatalf("byte cap failed: %q", capped)
	}
}
