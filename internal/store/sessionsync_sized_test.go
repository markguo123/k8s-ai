package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/session"
)

// plainSessionStore 是「没有容量闸门的 SessionStore」：只实现 Update，其余方法一律用不上
// （嵌入接口本身，调用到就是 nil panic，用例也因此不会误走别的路径）。
type plainSessionStore struct {
	SessionStore
	updates int
}

func (p *plainSessionStore) Update(context.Context, *session.Session) error {
	p.updates++
	return nil
}

// TestDatabaseSessionStoreUpdateSizedPassesReportThrough 锁适配器两端：容量回报要原样
// 透出给会话管理器（Chat 的 85% 预警文案全靠它），超限错误更要原样上抛——一旦被
// sessionStoreErr 翻成 session.ErrNotFound，Web 会把「会话太大」当成「会话不存在」，
// 前端最省事的自动处置就是重建一个会话，用户的历史就这么没了。
func TestDatabaseSessionStoreUpdateSizedPassesReportThrough(t *testing.T) {
	const limit = 4 << 10
	st := newSizedTestStore(t, limit)
	adapter := NewDatabaseSessionStore(st.Sessions())
	sized, ok := adapter.(session.SizedStore)
	if !ok {
		t.Fatal("平台库适配器必须实现 session.SizedStore")
	}

	first := sizedSession("sync-sized", "正常正文")
	if err := adapter.Create(first); err != nil {
		t.Fatalf("create: %v", err)
	}
	rep, err := sized.UpdateSized(sizedSession("sync-sized", "正常正文"))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if rep.Limit != limit || rep.Size <= 0 {
		t.Fatalf("回报没透出来: %+v", rep)
	}

	_, err = sized.UpdateSized(sizedSession("sync-sized", strings.Repeat("x", 8<<10)))
	if !errors.Is(err, ErrSessionTooLarge) {
		t.Fatalf("超限错误 = %v, want ErrSessionTooLarge 原样上抛", err)
	}
	if errors.Is(err, session.ErrNotFound) {
		t.Fatalf("超限错误被翻成了会话不存在: %v", err)
	}
}

// TestDatabaseSessionStoreUpdateSizedWithoutGate 锁退化路径：底层实现没有可选能力时，
// UpdateSized 语义就是普通 Update 加零回报，不许因为没有容量信息就报错。
func TestDatabaseSessionStoreUpdateSizedWithoutGate(t *testing.T) {
	plain := &plainSessionStore{}
	sized := NewDatabaseSessionStore(plain).(session.SizedStore)

	rep, err := sized.UpdateSized(&session.Session{ID: "plain-1"})
	if err != nil {
		t.Fatalf("无闸门后端不该报错: %v", err)
	}
	if rep != (session.SizeReport{}) {
		t.Fatalf("无闸门后端应回零回报: %+v", rep)
	}
	if plain.updates != 1 {
		t.Fatalf("应退回普通 Update，实际调用 %d 次", plain.updates)
	}
}
