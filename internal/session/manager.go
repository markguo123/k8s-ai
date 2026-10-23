package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Manager 是会话门面：CLI/Web 共用一个实例。
type Manager struct {
	store Store
}

// NewManager 创建 Manager。
func NewManager(store Store) *Manager {
	return &Manager{store: store}
}

// NewSession 创建会话；id 为空时自动生成。
func (m *Manager) NewSession(id, cluster, namespace, model string) (*Session, error) {
	return m.NewSessionForUser(id, "", cluster, namespace, model)
}

// NewSessionForUser 创建带所有者的会话；userID 为空时保持 CLI/三期兼容行为。
func (m *Manager) NewSessionForUser(id, userID, cluster, namespace, model string) (*Session, error) {
	if id == "" {
		id = generateID()
	}
	now := time.Now()
	s := &Session{
		ID:           id,
		UserID:       userID,
		Name:         "Session " + id,
		Cluster:      cluster,
		Namespace:    namespace,
		Model:        model,
		State:        "idle",
		CreatedAt:    now,
		LastModified: now,
	}
	if err := m.store.Create(s); err != nil {
		return nil, err
	}
	return s, nil
}

// GetOrCreateSessionForUser 仅允许所有者获取或创建会话。空所有者、
// 他人会话均返回 ErrForbidden，避免按猜测的会话 ID 越权读取。
func (m *Manager) GetOrCreateSessionForUser(id, userID, namespace, model string) (*Session, error) {
	if id != "" {
		if sess, err := m.store.Get(id); err == nil {
			if userID == "" || sess.UserID != userID {
				return nil, fmt.Errorf("%w: session %s", ErrForbidden, id)
			}
			return sess, nil
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return m.NewSessionForUser(id, userID, "", namespace, model)
}

// Get 获取会话。
func (m *Manager) Get(id string) (*Session, error) {
	return m.store.Get(id)
}

// Update 更新会话。
func (m *Manager) Update(s *Session) error {
	return m.store.Update(s)
}

// List 列出会话（最近优先）。
func (m *Manager) List() ([]*Session, error) {
	return m.store.List()
}

// ListForUser 仅返回指定用户的会话，排序保持不变。
func (m *Manager) ListForUser(userID string) ([]*Session, error) {
	list, err := m.store.List()
	if err != nil {
		return nil, err
	}
	out := make([]*Session, 0, len(list))
	for _, sess := range list {
		if userID != "" && sess.UserID == userID {
			out = append(out, sess)
		}
	}
	return out, nil
}

// Delete 删除会话。
func (m *Manager) Delete(id string) error {
	return m.store.Delete(id)
}

// generateID 生成形如 20260823-XXXX 的可读会话 ID。
func generateID() string {
	b := make([]byte, 2)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s-%04d", time.Now().Format("20060102"), time.Now().Nanosecond()%10000)
	}
	return fmt.Sprintf("%s-%s", time.Now().Format("20060102"), hex.EncodeToString(b))
}
