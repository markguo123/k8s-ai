package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/audit"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/skills"
)

func newTestStore(t *testing.T) Store {
	t.Helper()
	s, err := OpenSQLite("file:store-test-" + randID() + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

func randID() string {
	return time.Now().Format("150405.000000000")
}

func TestMigrateIdempotent(t *testing.T) {
	s, err := OpenSQLite("file:migrate-test-" + randID() + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("migrate pass %d: %v", i, err)
		}
	}
	// schema_migrations 只应有一条迁移记录。
	st := s.(*sqliteStore)
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 applied migration, got %d", n)
	}
}

func TestUserCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := &User{ID: "u1", Username: "alice", DisplayName: "Alice", PasswordHash: "hash", RoleID: "r1", Status: "active"}
	if err := s.Users().Create(ctx, u); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.Users().Get(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Username != "alice" || got.DisplayName != "Alice" {
		t.Fatalf("unexpected user: %+v", got)
	}
	if got, err := s.Users().GetByUsername(ctx, "alice"); err != nil || got.ID != "u1" {
		t.Fatalf("get by username: %v %v", got, err)
	}
	u.DisplayName = "Alice Updated"
	if err := s.Users().Update(ctx, u); err != nil {
		t.Fatalf("update: %v", err)
	}
	list, err := s.Users().List(ctx)
	if err != nil || len(list) != 1 || list[0].DisplayName != "Alice Updated" {
		t.Fatalf("list: %v %+v", err, list)
	}
	if err := s.Users().Delete(ctx, "u1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Users().Get(ctx, "u1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRoleCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := &Role{ID: "r1", Name: "admin", Description: "Admin", Permissions: []string{"approve", "clusters.write"}}
	if err := s.Roles().Create(ctx, r); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.Roles().Get(ctx, "r1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Permissions) != 2 || got.Permissions[0] != "approve" {
		t.Fatalf("permissions not round-tripped: %+v", got)
	}
	r.Permissions = []string{"clusters.write"}
	if err := s.Roles().Update(ctx, r); err != nil {
		t.Fatalf("update: %v", err)
	}
	list, err := s.Roles().List(ctx)
	if err != nil || len(list) != 1 || len(list[0].Permissions) != 1 {
		t.Fatalf("list: %v %+v", err, list)
	}
	if err := s.Roles().Delete(ctx, "r1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Roles().Get(ctx, "r1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSessionCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := &session.Session{ID: "s1", Name: "troubleshoot", Namespace: "default", State: "idle"}
	sess.AddMessage(session.Message{Role: "user", Content: "hello"})
	if err := s.Sessions().Create(ctx, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.Sessions().Get(ctx, "s1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "troubleshoot" || len(got.Messages) != 1 || got.Messages[0].Content != "hello" {
		t.Fatalf("session not round-tripped: %+v", got)
	}
	got.State = "running"
	if err := s.Sessions().Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	list, err := s.Sessions().List(ctx)
	if err != nil || len(list) != 1 || list[0].State != "running" {
		t.Fatalf("list: %v %+v", err, list)
	}
	if err := s.Sessions().Delete(ctx, "s1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Sessions().Get(ctx, "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestAuditStoreRedactsSensitive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	e := &AuditEvent{
		RequestID: "rid-1",
		SessionID: "s1",
		TurnID:    "t1",
		Action:    audit.ActionToolCallRequest,
		Payload:   `{"api_key":"sk-abcdefghijklmnopqrstuvwxyz","cmd":"create"}`,
	}
	if err := s.Audit().Write(ctx, e); err != nil {
		t.Fatalf("write: %v", err)
	}
	if e.ID == 0 {
		t.Fatalf("expected generated id")
	}
	got, err := s.Audit().Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if strings.Contains(got.Payload, "sk-abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("sensitive payload not redacted: %s", got.Payload)
	}
	if !strings.Contains(got.Payload, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] marker, got %s", got.Payload)
	}
	list, err := s.Audit().List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %+v", err, list)
	}
}

func TestSettingsStore(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Settings().Set(ctx, &Settings{Group: "model", Key: "endpoint", Value: `"http://x"`}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Settings().Set(ctx, &Settings{Group: "model", Key: "api_key", Value: `"secret"`, Sensitive: true}); err != nil {
		t.Fatalf("set sensitive: %v", err)
	}
	got, err := s.Settings().Get(ctx, "model", "endpoint")
	if err != nil || got.Value != `"http://x"` {
		t.Fatalf("get: %v %+v", err, got)
	}
	key, err := s.Settings().Get(ctx, "model", "api_key")
	if err != nil || !key.Sensitive {
		t.Fatalf("sensitive flag lost: %v %+v", err, key)
	}
	list, err := s.Settings().ListByGroup(ctx, "model")
	if err != nil || len(list) != 2 {
		t.Fatalf("list by group: %v %+v", err, list)
	}
	if err := s.Settings().Delete(ctx, "model", "api_key"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Settings().Get(ctx, "model", "api_key"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSkillStore(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sk := &Skill{
		Name:    "pod-safety",
		Source:  "builtin",
		Enabled: true,
		Spec: skills.Skill{
			Name:   "pod-safety",
			Domain: "safety",
			Kinds:  []string{"Pod"},
		},
	}
	if err := s.Skills().Upsert(ctx, sk); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.Skills().Get(ctx, "pod-safety")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Enabled || got.Spec.Domain != "safety" || len(got.Spec.Kinds) != 1 {
		t.Fatalf("skill not round-tripped: %+v", got)
	}
	list, err := s.Skills().List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %+v", err, list)
	}
	if err := s.Skills().Delete(ctx, "pod-safety"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Skills().Get(ctx, "pod-safety"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestApprovalStoreRedactsSensitive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	a := &Approval{
		ID:        "a1",
		SessionID: "s1",
		Tool:      "create_resource",
		Resource:  "Deployment/app",
		Params:    `{"kubeconfig":"user:pass@host","kind":"Deployment"}`,
		RiskLevel: "HIGH",
		Status:    "pending",
		CreatedAt: now,
	}
	if err := s.Approvals().Create(ctx, a); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.Approvals().Get(ctx, "a1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if strings.Contains(got.Params, "pass@host") {
		t.Fatalf("approval params not redacted: %s", got.Params)
	}
	if !strings.Contains(got.Params, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] marker, got %s", got.Params)
	}
	got.Status = "approved"
	decided := time.Now()
	got.DecidedAt = &decided
	if err := s.Approvals().Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	list, err := s.Approvals().List(ctx)
	if err != nil || len(list) != 1 || list[0].Status != "approved" {
		t.Fatalf("list: %v %+v", err, list)
	}
	if err := s.Approvals().Delete(ctx, "a1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestMemoryStore(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := &Memory{ID: "m1", UserID: "u1", Scope: "namespace", Namespace: "ns1", Content: "prefer 2 replicas"}
	if err := s.Memories().Create(ctx, m); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.Memories().Get(ctx, "m1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Scope != "namespace" || got.Namespace != "ns1" {
		t.Fatalf("memory not round-tripped: %+v", got)
	}
	m.Content = "updated"
	if err := s.Memories().Update(ctx, m); err != nil {
		t.Fatalf("update: %v", err)
	}
	list, err := s.Memories().List(ctx)
	if err != nil || len(list) != 1 || list[0].Content != "updated" {
		t.Fatalf("list: %v %+v", err, list)
	}
	if err := s.Memories().Delete(ctx, "m1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Memories().Get(ctx, "m1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRetentionStore(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Retention().Upsert(ctx, &RetentionPolicy{Name: "audit", Enabled: true, RetentionDays: 30, MaxCount: 5000, CleanupMode: "archive"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Retention().Upsert(ctx, &RetentionPolicy{Name: "audit", Enabled: true, RetentionDays: 60}); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	got, err := s.Retention().Get(ctx, "audit")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Enabled || got.RetentionDays != 60 {
		t.Fatalf("retention not round-tripped: %+v", got)
	}
	list, err := s.Retention().List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %+v", err, list)
	}
	if err := s.Retention().Delete(ctx, "audit"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestClusterCredentialCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := &Cluster{ID: "c1", Name: "prod", Context: "prod-context", Server: "https://x", Status: "reachable"}
	if err := s.Clusters().Create(ctx, c); err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	got, err := s.Clusters().Get(ctx, "c1")
	if err != nil || got.Context != "prod-context" {
		t.Fatalf("get cluster: %v %+v", err, got)
	}

	// 凭据只在 Get/GetByCluster 读取（KubeconfigSecret 字段不会随 JSON 序列化泄漏）。
	cred := &Credential{ID: "cred1", ClusterID: "c1", KubeconfigSecret: "ciphertext"}
	if err := s.Credentials().Create(ctx, cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}
	gotCred, err := s.Credentials().Get(ctx, "cred1")
	if err != nil || gotCred.KubeconfigSecret != "ciphertext" {
		t.Fatalf("get credential: %v %+v", err, gotCred)
	}
	byCluster, err := s.Credentials().GetByCluster(ctx, "c1")
	if err != nil || byCluster.ID != "cred1" {
		t.Fatalf("get by cluster: %v %+v", err, byCluster)
	}

	list, err := s.Clusters().List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list clusters: %v %+v", err, list)
	}
	if err := s.Credentials().Delete(ctx, "cred1"); err != nil {
		t.Fatalf("delete credential: %v", err)
	}
	if err := s.Clusters().Delete(ctx, "c1"); err != nil {
		t.Fatalf("delete cluster: %v", err)
	}
	if _, err := s.Clusters().Get(ctx, "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
