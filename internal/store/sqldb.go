package store

import (
	"context"
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
	authzGrants *authzGrantStore
	apiTokens   *apiTokenStore
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
	s.authzGrants = &authzGrantStore{s}
	s.apiTokens = &apiTokenStore{s}
	return s
}

func (s *dbStore) Close() error { return s.db.Close() }

// Ping 校验数据库连通（P6.1 /readyz 判据之一）。
func (s *dbStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// MigrationStatus 读 schema_migrations 已应用的最高版本；迁移表还不存在时视为
// "未迁移"（applied=0 且无错误），两驱动共用。
func (s *dbStore) MigrationStatus(ctx context.Context) (int, int, error) {
	var applied sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&applied)
	if err != nil {
		if isMissingTable(err) {
			return 0, schemaVersion, nil
		}
		return 0, schemaVersion, err
	}
	if !applied.Valid {
		return 0, schemaVersion, nil
	}
	return int(applied.Int64), schemaVersion, nil
}

// isMissingTable 识别"表不存在"：SQLite 报 no such table，MySQL 报 1146。
func isMissingTable(err error) bool {
	if err == nil {
		return false
	}
	msg := toLowerASCII(err.Error())
	return containsFold(msg, "no such table") || containsFold(msg, "doesn't exist") ||
		containsFold(msg, "1146")
}

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
func (s *dbStore) AuthzGrants() AuthzGrantStore { return s.authzGrants }
func (s *dbStore) ApiTokens() ApiTokenStore     { return s.apiTokens }

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
