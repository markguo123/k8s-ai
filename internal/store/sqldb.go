package store

import (
	"database/sql"
	"fmt"
	"time"
)

// dbStore 是 Store 的通用数据库实现（D-04）：core CRUD 只依赖 *sql.DB 与
// dialect，SQLite/MySQL 两种驱动共用同一套逻辑与统一迁移，避免双驱动漂移。
type dbStore struct {
	db      *sql.DB
	dialect dialect

	users       *userStore
	roles       *roleStore
	sessions    *sessionStore
	turns       *turnStore
	steps       *stepStore
	audit       *auditStore
	settings    *settingsStore
	skills      *skillStore
	approvals   *approvalStore
	memories    *memoryStore
	retention   *retentionStore
	clusters    *clusterStore
	credentials *credentialStore
}

// newDBStore 组装通用 Store 并装配各子 Store。
func newDBStore(db *sql.DB, d dialect) *dbStore {
	s := &dbStore{db: db, dialect: d}
	s.users = &userStore{s}
	s.roles = &roleStore{s}
	s.sessions = &sessionStore{s}
	s.turns = &turnStore{s}
	s.steps = &stepStore{s}
	s.audit = &auditStore{s}
	s.settings = &settingsStore{s}
	s.skills = &skillStore{s}
	s.approvals = &approvalStore{s}
	s.memories = &memoryStore{s}
	s.retention = &retentionStore{s}
	s.clusters = &clusterStore{s}
	s.credentials = &credentialStore{s}
	return s
}

func (s *dbStore) Close() error { return s.db.Close() }

func (s *dbStore) Users() UserStore             { return s.users }
func (s *dbStore) Roles() RoleStore             { return s.roles }
func (s *dbStore) Sessions() SessionStore       { return s.sessions }
func (s *dbStore) Turns() TurnStore             { return s.turns }
func (s *dbStore) Steps() StepStore             { return s.steps }
func (s *dbStore) Audit() AuditStore            { return s.audit }
func (s *dbStore) Settings() SettingsStore      { return s.settings }
func (s *dbStore) Skills() SkillStore           { return s.skills }
func (s *dbStore) Approvals() ApprovalStore     { return s.approvals }
func (s *dbStore) Memories() MemoryStore        { return s.memories }
func (s *dbStore) Retention() RetentionStore    { return s.retention }
func (s *dbStore) Clusters() ClusterStore       { return s.clusters }
func (s *dbStore) Credentials() CredentialStore { return s.credentials }

// ---- shared helpers（两个驱动通用） ----

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nilTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

func mapErr(err error, op string) error {
	if err == nil {
		return nil
	}
	if err == sql.ErrNoRows {
		return fmt.Errorf("%w: %s", ErrNotFound, op)
	}
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: %s", ErrExists, err)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// isUniqueViolation 识别唯一约束冲突：SQLite 报 "UNIQUE constraint failed"，
// MySQL 报 "Duplicate entry ... for key ..."（NO.1062）。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := toLowerASCII(err.Error())
	return containsFold(msg, "unique") || containsFold(msg, "duplicate entry")
}

func ensureAffected(res sql.Result, op string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, op)
	}
	return nil
}

func containsFold(s, sub string) bool {
	ls := toLowerASCII(s)
	lsub := toLowerASCII(sub)
	return len(ls) >= len(lsub) && indexIn(ls, lsub) >= 0
}

func indexIn(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func toLowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
