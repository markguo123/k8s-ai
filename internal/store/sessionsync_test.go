package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/session"
)

// newSyncSessionStore 返回「平台库适配器」与被包装的 Store：前者是给会话管理器
// 用的同步接口，后者用于绕开适配器直接核验库里的真实内容（P8.0）。
func newSyncSessionStore(t *testing.T) (session.Store, Store) {
	t.Helper()
	st := newTestStore(t)
	return NewDatabaseSessionStore(st.Sessions()), st
}

func syncFixtureSession(id string) *session.Session {
	now := time.Now().UTC().Truncate(time.Second)
	return &session.Session{
		ID:      id,
		Name:    "会话 " + id,
		UserID:  "u1",
		Cluster: "prod",
		State:   "done",
		Messages: []session.Message{
			{Role: "user", Type: "text", Content: "检查 deployment web"},
		},
		CreatedAt:    now,
		LastModified: now,
	}
}

func TestDatabaseSessionStoreCRUD(t *testing.T) {
	sess, st := newSyncSessionStore(t)
	ctx := context.Background()

	if err := sess.Create(syncFixtureSession("sync-1")); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := sess.Get("sync-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UserID != "u1" || got.Name != "会话 sync-1" || len(got.Messages) != 1 {
		t.Fatalf("get = %+v", got)
	}

	got.Name = "改名后的会话"
	got.Pinned = true
	got.AddMessage(session.Message{Role: "assistant", Type: "text", Content: "节点正常"})
	if err := sess.Update(got); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, err := st.Sessions().Get(ctx, "sync-1")
	if err != nil {
		t.Fatalf("store get: %v", err)
	}
	if again.Name != "改名后的会话" || !again.Pinned || len(again.Messages) != 2 {
		t.Fatalf("persisted session = %+v", again)
	}

	list, err := sess.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != "sync-1" {
		t.Fatalf("list = %+v", list)
	}

	if err := sess.Delete("sync-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err = sess.List()
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("list after delete = %+v", list)
	}
}

// TestDatabaseSessionStoreMissingIDMapsSentinel 锁住错误口径：库里的「不存在」必须
// 翻译成 session.ErrNotFound，否则会话管理器的「新建 vs 404」判定和 Web 状态码会退化。
func TestDatabaseSessionStoreMissingIDMapsSentinel(t *testing.T) {
	sess, st := newSyncSessionStore(t)

	if err := sess.Create(syncFixtureSession("sync-2")); err != nil {
		t.Fatalf("create: %v", err)
	}
	// 重复创建：普通错误文案，不是哨兵错误（与 session.NewFileStore 对齐）。
	dup := sess.Create(syncFixtureSession("sync-2"))
	if dup == nil || !strings.Contains(dup.Error(), "already exists") {
		t.Fatalf("duplicate create = %v", dup)
	}

	if err := sess.Delete("sync-2"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// 删除必须是终态：适配器读不回，底层库里也没有残留行。
	if _, err := sess.Get("sync-2"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("get deleted = %v, want session.ErrNotFound", err)
	}
	if _, err := st.Sessions().Get(context.Background(), "sync-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("store get deleted = %v, want ErrNotFound", err)
	}
	// 不存在 id 的读取与更新同样翻成 session.ErrNotFound。
	if _, err := sess.Get("never-existed"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("get missing = %v, want session.ErrNotFound", err)
	}
	if err := sess.Update(syncFixtureSession("never-existed")); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("update missing = %v, want session.ErrNotFound", err)
	}
	if err := sess.Delete("never-existed"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("delete missing = %v, want session.ErrNotFound", err)
	}
}

func TestDatabaseSessionStoreRejectsEmptyID(t *testing.T) {
	sess, _ := newSyncSessionStore(t)
	if err := sess.Create(nil); err == nil {
		t.Fatal("nil session should fail")
	}
	if err := sess.Update(&session.Session{}); err == nil {
		t.Fatal("empty session id should fail")
	}
}

// TestDatabaseSessionStoreRedactsBeforePersist 锁红线：经适配器写入的会话，落库
// 副本不得含 Secret 值 / kubeconfig / API Key 明文（AGENTS.md），且内存里的当轮
// 正文不受影响（LLM 上下文与前端展示仍看原文）。
func TestDatabaseSessionStoreRedactsBeforePersist(t *testing.T) {
	sess, st := newSyncSessionStore(t)
	const plainKey = "sk-sync-secret-value"
	memory := syncFixtureSession("sync-3")
	memory.Name = "线上排障 api_key=" + plainKey
	memory.Messages[0].Content += " 用 api_key=" + plainKey + " 试试"
	memory.Messages[0].Arguments = json.RawMessage(`{"secret":"` + plainKey + `"}`)
	memory.Messages[0].Data = map[string]any{"kubeconfig": "apiVersion: v1\nkind: Config"}

	if err := sess.Create(memory); err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(memory.Messages[0].Content, plainKey) {
		t.Fatal("调用方内存正文不应被就地脱敏")
	}

	saved, err := st.Sessions().Get(context.Background(), "sync-3")
	if err != nil {
		t.Fatalf("store get: %v", err)
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), plainKey) {
		t.Fatalf("落库会话仍含凭据明文: %s", raw)
	}
}
