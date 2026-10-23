package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/approval"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

func TestMemoryStoreCRUD(t *testing.T) {
	s := NewMemoryStore()
	sess := &Session{ID: "incident-001", Model: "qwen-plus"}
	if err := s.Create(sess); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(&Session{ID: "incident-001"}); err == nil {
		t.Fatal("重复创建应报错")
	}
	got, err := s.Get("incident-001")
	if err != nil || got.ID != "incident-001" {
		t.Fatalf("get = %+v err=%v", got, err)
	}
	sess.AddMessage(Message{Role: "user", Content: "hi"})
	if err := s.Update(sess); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List()
	if len(list) != 1 {
		t.Fatalf("list = %d", len(list))
	}
	if err := s.Delete("incident-001"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("incident-001"); err == nil {
		t.Fatal("删除后应 NotFound")
	}
}

func TestFileStoreRedactsOnWrite(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: "s1"}
	sess.AddMessage(Message{Role: "user", Content: "token=sk-test-secret-key"})
	if err := s.Create(sess); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "s1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-test-secret-key") {
		t.Fatal("文件落盘未脱敏")
	}
	if !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatal("文件缺少脱敏标记")
	}
}

func TestFileStoreCRUDAndListOrder(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &Session{ID: "a"}
	b := &Session{ID: "b"}
	_ = s.Create(a)
	time.Sleep(10 * time.Millisecond)
	_ = s.Create(b)
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "b" {
		t.Fatalf("列表顺序应为最近优先: %+v", list)
	}
	if err := s.Delete("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("missing"); err == nil {
		t.Fatal("missing 应 NotFound")
	}
}

func TestSessionApprovedWrites(t *testing.T) {
	s := &Session{ID: "s"}
	if s.Approved("scale:apps/v1/deployments:production/payment-api") {
		t.Fatal("未授权时不应通过")
	}
	s.AddApproved("scale:apps/v1/deployments:production/payment-api")
	if !s.Approved("scale:apps/v1/deployments:production/payment-api") {
		t.Fatal("授权后应通过")
	}
	s.ClearMessages()
	if len(s.Messages) != 0 {
		t.Fatal("clear 未清空消息")
	}
}

func TestManagerNewSessionAutoID(t *testing.T) {
	m := NewManager(NewMemoryStore())
	s, err := m.NewSession("", "prod", "", "qwen-plus")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID == "" || s.Cluster != "prod" || s.Model != "qwen-plus" {
		t.Fatalf("session = %+v", s)
	}
}

func TestManagerSessionOwnership(t *testing.T) {
	m := NewManager(NewMemoryStore())
	alice, err := m.GetOrCreateSessionForUser("s1", "u-alice", "", "qwen-plus")
	if err != nil {
		t.Fatal(err)
	}
	if alice.UserID != "u-alice" {
		t.Fatalf("owner = %q", alice.UserID)
	}
	if _, err := m.GetOrCreateSessionForUser("s1", "u-bob", "", "qwen-plus"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other user should be forbidden, got %v", err)
	}
	bob, err := m.GetOrCreateSessionForUser("s2", "u-bob", "", "qwen-plus")
	if err != nil {
		t.Fatal(err)
	}
	bob.LastModified = bob.LastModified.Add(time.Second)
	if err := m.Update(bob); err != nil {
		t.Fatal(err)
	}

	list, err := m.ListForUser("u-alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "s1" || list[0].UserID != "u-alice" {
		t.Fatalf("alice sessions = %+v", list)
	}
}

func TestSessionApprovalGrants(t *testing.T) {
	s := &Session{ID: "task-1", Cluster: "prod"}
	g, ok := approval.NewGrant(approval.GrantRequest{
		TaskID:    "task-1",
		Operation: "scale",
		Kind:      "deployments",
		Name:      "web",
		Namespace: "prod",
		Cluster:   "prod",
		Risk:      model.RiskMedium,
	})
	if !ok {
		t.Fatal("medium write should produce a grant")
	}
	s.AddGrant(g)
	if len(s.ApprovalGrants) != 1 {
		t.Fatalf("ApprovalGrants len = %d, want 1", len(s.ApprovalGrants))
	}
	req := approval.GrantRequest{
		TaskID:    "task-1",
		Operation: "scale",
		Kind:      "deployments",
		Name:      "web",
		Namespace: "prod",
		Cluster:   "prod",
		Risk:      model.RiskMedium,
	}
	if got := s.FindGrant(req); got == nil || got.ID != g.ID {
		t.Fatalf("FindGrant = %+v, want %s", got, g.ID)
	}
	other := req
	other.Namespace = "dev"
	if got := s.FindGrant(other); got != nil {
		t.Fatalf("grant must not cross namespace, got %+v", got)
	}
	s.RemoveGrantMatching(req)
	if got := s.FindGrant(req); got != nil {
		t.Fatalf("RemoveGrantMatching should remove matching grants, got %+v", got)
	}
}

func TestSessionAddGrantIgnoresEmptyID(t *testing.T) {
	s := &Session{ID: "task-1"}
	s.AddGrant(approval.Grant{})
	if len(s.ApprovalGrants) != 0 {
		t.Fatalf("empty-id grant should not be persisted: %+v", s.ApprovalGrants)
	}
}
