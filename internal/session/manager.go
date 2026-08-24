package session

import (
	"crypto/rand"
	"encoding/hex"
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
	if id == "" {
		id = generateID()
	}
	now := time.Now()
	s := &Session{
		ID:           id,
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
