package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/session"
)

// sessionOpTimeout 限制单次会话读写的后台上下文时长：session.Store 是同步接口
// （CLI 与会话管理器共用），拿不到调用方 ctx，因此这里自带超时上限，避免一次
// 数据库抖动把会话读写挂成永久阻塞。
const sessionOpTimeout = 30 * time.Second

func sessionOpContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), sessionOpTimeout)
}

// NewDatabaseSessionStore 把平台库的 SessionStore 适配成 session.Store（无 ctx 的
// 同步接口），让会话事实源落到平台库而不是容器里的会话目录（P8.0 / ADR-046）。
//
// 语义与 session.NewFileStore 逐条对齐，调用点不需要区分两种后端：
//   - 正文与索引列在落库前统一脱敏（redactSessionForStore，比文件副本多覆盖
//     Summary/Arguments/Data）；
//   - 记录不存在返回包装 session.ErrNotFound 的错误，越权与 404 判定才不会退化
//     成 500；
//   - 重复创建返回普通错误（"session %q already exists"），不是哨兵错误。
func NewDatabaseSessionStore(sessions SessionStore) session.Store {
	return &databaseSessionStore{sessions: sessions}
}

type databaseSessionStore struct {
	sessions SessionStore
}

func (s *databaseSessionStore) Create(sess *session.Session) error {
	if sess == nil || sess.ID == "" {
		return fmt.Errorf("session id must not be empty")
	}
	ctx, cancel := sessionOpContext()
	defer cancel()
	if err := s.sessions.Create(ctx, sess); err != nil {
		if errors.Is(err, ErrExists) {
			return fmt.Errorf("session %q already exists", sess.ID)
		}
		return sessionStoreErr(err, sess.ID)
	}
	return nil
}

func (s *databaseSessionStore) Get(id string) (*session.Session, error) {
	ctx, cancel := sessionOpContext()
	defer cancel()
	sess, err := s.sessions.Get(ctx, id)
	if err != nil {
		return nil, sessionStoreErr(err, id)
	}
	return sess, nil
}

func (s *databaseSessionStore) Update(sess *session.Session) error {
	if sess == nil || sess.ID == "" {
		return fmt.Errorf("session id must not be empty")
	}
	ctx, cancel := sessionOpContext()
	defer cancel()
	if err := s.sessions.Update(ctx, sess); err != nil {
		return sessionStoreErr(err, sess.ID)
	}
	return nil
}

func (s *databaseSessionStore) List() ([]*session.Session, error) {
	ctx, cancel := sessionOpContext()
	defer cancel()
	return s.sessions.List(ctx)
}

func (s *databaseSessionStore) Delete(id string) error {
	ctx, cancel := sessionOpContext()
	defer cancel()
	if err := s.sessions.Delete(ctx, id); err != nil {
		return sessionStoreErr(err, id)
	}
	return nil
}

// sessionStoreErr 把持久化层哨兵错误翻译成 session 包的口径：会话不存在必须是
// session.ErrNotFound（会话管理器与 Web 层据此判定新建还是 404）。
func sessionStoreErr(err error, id string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: %s", session.ErrNotFound, id)
	}
	return err
}
