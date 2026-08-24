package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/security"
)

// ErrNotFound 表示会话不存在。
var ErrNotFound = errors.New("session not found")

// Store 是可插拔会话存储（memory/file，未来可扩展 SQLite/PostgreSQL/Redis）。
type Store interface {
	Create(s *Session) error
	Get(id string) (*Session, error)
	Update(s *Session) error
	List() ([]*Session, error)
	Delete(id string) error
}

// redactor 用于文件落盘前脱敏（AGENTS.md：会话文件零敏感值）。
var redactor = security.NewRedactor()

// NewMemoryStore 创建内存 Store。
func NewMemoryStore() Store {
	return &memoryStore{sessions: map[string]*Session{}}
}

type memoryStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func (m *memoryStore) Create(s *Session) error {
	if s.ID == "" {
		return fmt.Errorf("session id must not be empty")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[s.ID]; ok {
		return fmt.Errorf("session %q already exists", s.ID)
	}
	now := time.Now()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	if s.LastModified.IsZero() {
		s.LastModified = now
	}
	m.sessions[s.ID] = s
	return nil
}

func (m *memoryStore) Get(id string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return s, nil
}

func (m *memoryStore) Update(s *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[s.ID]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, s.ID)
	}
	s.LastModified = time.Now()
	m.sessions[s.ID] = s
	return nil
}

func (m *memoryStore) List() ([]*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	sortSessions(out)
	return out, nil
}

func (m *memoryStore) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[id]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	delete(m.sessions, id)
	return nil
}

// NewFileStore 创建文件 Store（目录不存在则创建；文件写前脱敏）。
func NewFileStore(dir string) (Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("session dir must not be empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	return &fileStore{dir: dir}, nil
}

type fileStore struct {
	dir string
}

func (f *fileStore) path(id string) string {
	return filepath.Join(f.dir, id+".json")
}

func (f *fileStore) Create(s *Session) error {
	if s.ID == "" {
		return fmt.Errorf("session id must not be empty")
	}
	if _, err := os.Stat(f.path(s.ID)); err == nil {
		return fmt.Errorf("session %q already exists", s.ID)
	}
	now := time.Now()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	if s.LastModified.IsZero() {
		s.LastModified = now
	}
	return f.write(s)
}

func (f *fileStore) Get(id string) (*Session, error) {
	raw, err := os.ReadFile(f.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil, fmt.Errorf("read session %s: %w", id, err)
	}
	var s Session
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse session %s: %w", id, err)
	}
	return &s, nil
}

func (f *fileStore) Update(s *Session) error {
	if _, err := os.Stat(f.path(s.ID)); err != nil {
		return fmt.Errorf("%w: %s", ErrNotFound, s.ID)
	}
	s.LastModified = time.Now()
	return f.write(s)
}

func (f *fileStore) List() ([]*Session, error) {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	out := make([]*Session, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-len(".json")]
		if s, err := f.Get(id); err == nil {
			out = append(out, s)
		}
	}
	sortSessions(out)
	return out, nil
}

func (f *fileStore) Delete(id string) error {
	if err := os.Remove(f.path(id)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return fmt.Errorf("delete session %s: %w", id, err)
	}
	return nil
}

// write 序列化并落盘；消息内容在写入前脱敏（副本操作，不污染内存中的会话）。
func (f *fileStore) write(s *Session) error {
	copy := *s
	copy.Messages = make([]Message, len(s.Messages))
	for i, m := range s.Messages {
		m.Content = redactor.Redact(m.Content)
		copy.Messages[i] = m
	}
	raw, err := json.MarshalIndent(&copy, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	tmp := f.path(s.ID) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("write session %s: %w", s.ID, err)
	}
	return os.Rename(tmp, f.path(s.ID))
}

// sortSessions 按 LastModified 倒序（最近会话在前）。
func sortSessions(list []*Session) {
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].LastModified.After(list[j].LastModified)
	})
}
