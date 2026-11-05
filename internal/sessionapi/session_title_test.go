package sessionapi

import (
	"sort"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/session"
)

func TestSessionTitle(t *testing.T) {
	sess := session.Session{
		ID:   "20260823-ac89",
		Name: "Session 20260823-ac89",
		Messages: []session.Message{
			{Role: "user", Type: "text", Content: "查看 default 命名空间异常工作负载\n第二行"},
		},
	}
	if got := sessionTitle(sess); got != "查看 default 命名空间异常工作负载" {
		t.Fatalf("sessionTitle() = %q, want first user intent", got)
	}

	sess.Name = "我改过的名字"
	if got := sessionTitle(sess); got != "我改过的名字" {
		t.Fatalf("sessionTitle() = %q, want explicit name", got)
	}

	sess.Name = "Session 20260823-ac89"
	sess.Messages = nil
	if got := sessionTitle(sess); got != "新会话" {
		t.Fatalf("sessionTitle() = %q, want 新会话 fallback", got)
	}
}

func TestPinSortsFirst(t *testing.T) {
	now := time.Now()
	list := []session.Session{
		{ID: "a", LastModified: now.Add(-2 * time.Minute)},
		{ID: "b", LastModified: now.Add(-time.Minute)},
		{ID: "c", Pinned: true, LastModified: now.Add(-10 * time.Minute)},
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Pinned != list[j].Pinned {
			return list[i].Pinned
		}
		return list[i].LastModified.After(list[j].LastModified)
	})
	if list[0].ID != "c" {
		t.Fatalf("pinned session should sort first, got %q", list[0].ID)
	}
	if list[1].ID != "b" || list[2].ID != "a" {
		t.Fatalf("unpinned sessions should keep recency order, got %q,%q", list[1].ID, list[2].ID)
	}
}
