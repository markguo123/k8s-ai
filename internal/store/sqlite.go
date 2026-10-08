package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/security"
	"github.com/k8s-ai/k8s-ai/internal/session"

	// pure-Go SQLite 驱动（无需 CGO，Windows 交叉编译 Linux amd64 可用）。
	_ "modernc.org/sqlite"
)

// sqliteStore 是 Store 的 SQLite 实现（D-04 默认驱动）。
type sqliteStore struct {
	db *sql.DB

	users       *userStore
	roles       *roleStore
	sessions    *sessionStore
	audit       *auditStore
	settings    *settingsStore
	skills      *skillStore
	approvals   *approvalStore
	memories    *memoryStore
	retention   *retentionStore
	clusters    *clusterStore
	credentials *credentialStore
}

// OpenSQLite 打开或创建 path 处的 SQLite 库并返回 Store。
// URI 形式（如 ":memory:"、"file:...?...&=shared"）由底层驱动支持。
func OpenSQLite(path string) (Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	// 单连接：串行化写事务，避免 in-memory/文件锁竞争。
	db.SetMaxOpenConns(1)
	s := &sqliteStore{db: db}
	s.users = &userStore{s}
	s.roles = &roleStore{s}
	s.sessions = &sessionStore{s}
	s.audit = &auditStore{s}
	s.settings = &settingsStore{s}
	s.skills = &skillStore{s}
	s.approvals = &approvalStore{s}
	s.memories = &memoryStore{s}
	s.retention = &retentionStore{s}
	s.clusters = &clusterStore{s}
	s.credentials = &credentialStore{s}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite %q: %w", path, err)
	}
	return s, nil
}

func (s *sqliteStore) Close() error { return s.db.Close() }

func (s *sqliteStore) Users() UserStore             { return s.users }
func (s *sqliteStore) Roles() RoleStore             { return s.roles }
func (s *sqliteStore) Sessions() SessionStore       { return s.sessions }
func (s *sqliteStore) Audit() AuditStore            { return s.audit }
func (s *sqliteStore) Settings() SettingsStore      { return s.settings }
func (s *sqliteStore) Skills() SkillStore           { return s.skills }
func (s *sqliteStore) Approvals() ApprovalStore     { return s.approvals }
func (s *sqliteStore) Memories() MemoryStore        { return s.memories }
func (s *sqliteStore) Retention() RetentionStore    { return s.retention }
func (s *sqliteStore) Clusters() ClusterStore       { return s.clusters }
func (s *sqliteStore) Credentials() CredentialStore { return s.credentials }

// redactor 对审计/审批敏感字段统一脱敏（与 audit/session 同一策略）。
var auditRedactor = security.NewRedactor()

// sensitiveKeyName 覆盖配置/审批/审计 JSON 负载中的敏感键名（含 kubeconfig）。
var sensitiveKeyName = regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|credential|kubeconfig|client[_-]?secret)`)

// redactJSONPayload 对 JSON 负载做 JSON 感知脱敏：
//   - 敏感键名的值整体替换为 [REDACTED]（含 kubeconfig，保证审计/审批零明文）；
//   - 非敏感键名的字符串再走 security.Redactor 兜底（JWT/bearer/API Key 等）。
//
// 若负载不是合法 JSON，退回纯文本脱敏。
func redactJSONPayload(payload string) string {
	var v any
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		return auditRedactor.Redact(payload)
	}
	redactInPlace(&v)
	if out, err := json.Marshal(v); err == nil {
		return string(out)
	}
	return auditRedactor.Redact(payload)
}

func redactInPlace(v *any) {
	switch val := (*v).(type) {
	case map[string]any:
		for k, item := range val {
			if sensitiveKeyName.MatchString(k) {
				val[k] = "[REDACTED]"
				continue
			}
			redactInPlace(&item)
			val[k] = item
		}
	case []any:
		for i := range val {
			redactInPlace(&val[i])
		}
	case string:
		*v = auditRedactor.Redact(val)
	}
}

// ---- helpers ----

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
	if containsFold(err.Error(), "unique") {
		return fmt.Errorf("%w: %s", ErrExists, err)
	}
	return fmt.Errorf("%s: %w", op, err)
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

// ---- Users ----

type userStore struct{ s *sqliteStore }

func (st *userStore) Create(ctx context.Context, u *User) error {
	now := time.Now()
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	if u.UpdatedAt.IsZero() {
		u.UpdatedAt = now
	}
	_, err := st.s.db.ExecContext(ctx, `
		INSERT INTO users (id, username, display_name, password_hash, role_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.DisplayName, u.PasswordHash, u.RoleID, u.Status, u.CreatedAt, u.UpdatedAt)
	return mapErr(err, "create user")
}

func (st *userStore) Get(ctx context.Context, id string) (*User, error) {
	u := &User{}
	err := st.s.db.QueryRowContext(ctx, `
		SELECT id, username, display_name, password_hash, role_id, status, created_at, updated_at
		FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.RoleID, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, mapErr(err, "get user")
	}
	return u, nil
}

func (st *userStore) GetByUsername(ctx context.Context, username string) (*User, error) {
	u := &User{}
	err := st.s.db.QueryRowContext(ctx, `
		SELECT id, username, display_name, password_hash, role_id, status, created_at, updated_at
		FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.RoleID, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, mapErr(err, "get user by username")
	}
	return u, nil
}

func (st *userStore) Update(ctx context.Context, u *User) error {
	u.UpdatedAt = time.Now()
	res, err := st.s.db.ExecContext(ctx, `
		UPDATE users SET username=?, display_name=?, password_hash=?, role_id=?, status=?, updated_at=?
		WHERE id=?`,
		u.Username, u.DisplayName, u.PasswordHash, u.RoleID, u.Status, u.UpdatedAt, u.ID)
	if err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	return ensureAffected(res, "update user")
}

func (st *userStore) List(ctx context.Context) ([]*User, error) {
	rows, err := st.s.db.QueryContext(ctx, `
		SELECT id, username, display_name, password_hash, role_id, status, created_at, updated_at
		FROM users ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.RoleID, &u.Status, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (st *userStore) Delete(ctx context.Context, id string) error {
	res, err := st.s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return ensureAffected(res, "delete user")
}

// ---- Roles ----

type roleStore struct{ s *sqliteStore }

func (st *roleStore) Create(ctx context.Context, r *Role) error {
	now := time.Now()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now
	}
	perms, err := json.Marshal(r.Permissions)
	if err != nil {
		return fmt.Errorf("marshal role permissions: %w", err)
	}
	_, err = st.s.db.ExecContext(ctx, `
		INSERT INTO roles (id, name, description, permissions, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		r.ID, r.Name, r.Description, string(perms), r.CreatedAt, r.UpdatedAt)
	return mapErr(err, "create role")
}

func (st *roleStore) Get(ctx context.Context, id string) (*Role, error) {
	r := &Role{}
	var perms string
	err := st.s.db.QueryRowContext(ctx, `
		SELECT id, name, description, permissions, created_at, updated_at
		FROM roles WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &r.Description, &perms, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, mapErr(err, "get role")
	}
	if err := json.Unmarshal([]byte(perms), &r.Permissions); err != nil {
		return nil, fmt.Errorf("parse role permissions: %w", err)
	}
	return r, nil
}

func (st *roleStore) Update(ctx context.Context, r *Role) error {
	perms, err := json.Marshal(r.Permissions)
	if err != nil {
		return fmt.Errorf("marshal role permissions: %w", err)
	}
	r.UpdatedAt = time.Now()
	res, err := st.s.db.ExecContext(ctx, `
		UPDATE roles SET name=?, description=?, permissions=?, updated_at=? WHERE id=?`,
		r.Name, r.Description, string(perms), r.UpdatedAt, r.ID)
	if err != nil {
		return fmt.Errorf("update role: %w", err)
	}
	return ensureAffected(res, "update role")
}

func (st *roleStore) List(ctx context.Context) ([]*Role, error) {
	rows, err := st.s.db.QueryContext(ctx, `
		SELECT id, name, description, permissions, created_at, updated_at FROM roles ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	defer rows.Close()
	var out []*Role
	for rows.Next() {
		r := &Role{}
		var perms string
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &perms, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan role: %w", err)
		}
		if err := json.Unmarshal([]byte(perms), &r.Permissions); err != nil {
			return nil, fmt.Errorf("parse role permissions: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (st *roleStore) Delete(ctx context.Context, id string) error {
	res, err := st.s.db.ExecContext(ctx, `DELETE FROM roles WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete role: %w", err)
	}
	return ensureAffected(res, "delete role")
}

// ---- Sessions ----

type sessionStore struct{ s *sqliteStore }

func (st *sessionStore) marshal(sess *session.Session) (string, error) {
	data, err := json.Marshal(sess)
	if err != nil {
		return "", fmt.Errorf("marshal session: %w", err)
	}
	return string(data), nil
}

func (st *sessionStore) Create(ctx context.Context, sess *session.Session) error {
	now := time.Now()
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}
	if sess.LastModified.IsZero() {
		sess.LastModified = now
	}
	data, err := st.marshal(sess)
	if err != nil {
		return err
	}
	_, err = st.s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, name, cluster_id, namespace, model, state, data, created_at, last_modified)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.Cluster, sess.Name, sess.Cluster, sess.Namespace, sess.Model, sess.State,
		data, sess.CreatedAt, sess.LastModified)
	return mapErr(err, "create session")
}

func (st *sessionStore) Get(ctx context.Context, id string) (*session.Session, error) {
	var data string
	err := st.s.db.QueryRowContext(ctx, `SELECT data FROM sessions WHERE id = ?`, id).Scan(&data)
	if err != nil {
		return nil, mapErr(err, "get session")
	}
	var sess session.Session
	if err := json.Unmarshal([]byte(data), &sess); err != nil {
		return nil, fmt.Errorf("parse session: %w", err)
	}
	return &sess, nil
}

func (st *sessionStore) Update(ctx context.Context, sess *session.Session) error {
	sess.LastModified = time.Now()
	data, err := st.marshal(sess)
	if err != nil {
		return err
	}
	res, err := st.s.db.ExecContext(ctx, `
		UPDATE sessions SET user_id=?, name=?, cluster_id=?, namespace=?, model=?, state=?, data=?, last_modified=?
		WHERE id=?`,
		sess.Cluster, sess.Name, sess.Cluster, sess.Namespace, sess.Model, sess.State, data, sess.LastModified, sess.ID)
	if err != nil {
		return fmt.Errorf("update session: %w", err)
	}
	return ensureAffected(res, "update session")
}

func (st *sessionStore) List(ctx context.Context) ([]*session.Session, error) {
	rows, err := st.s.db.QueryContext(ctx, `SELECT data FROM sessions ORDER BY last_modified DESC`)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	var out []*session.Session
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		var sess session.Session
		if err := json.Unmarshal([]byte(data), &sess); err != nil {
			return nil, fmt.Errorf("parse session: %w", err)
		}
		out = append(out, &sess)
	}
	return out, rows.Err()
}

func (st *sessionStore) Delete(ctx context.Context, id string) error {
	res, err := st.s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return ensureAffected(res, "delete session")
}

// ---- Audit ----

type auditStore struct{ s *sqliteStore }

func (st *auditStore) Write(ctx context.Context, e *AuditEvent) error {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	payload := redactJSONPayload(e.Payload)
	res, err := st.s.db.ExecContext(ctx, `
		INSERT INTO audit_events (timestamp, request_id, session_id, turn_id, step_id, user_id, cluster_id, action, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Timestamp, e.RequestID, e.SessionID, e.TurnID, e.StepID, e.UserID, e.ClusterID, e.Action, payload)
	if err != nil {
		return fmt.Errorf("write audit event: %w", err)
	}
	if id, err := res.LastInsertId(); err == nil {
		e.ID = id
	}
	return nil
}

func (st *auditStore) Get(ctx context.Context, id int64) (*AuditEvent, error) {
	e := &AuditEvent{}
	err := st.s.db.QueryRowContext(ctx, `
		SELECT id, timestamp, request_id, session_id, turn_id, step_id, user_id, cluster_id, action, payload
		FROM audit_events WHERE id = ?`, id).
		Scan(&e.ID, &e.Timestamp, &e.RequestID, &e.SessionID, &e.TurnID, &e.StepID,
			&e.UserID, &e.ClusterID, &e.Action, &e.Payload)
	if err != nil {
		return nil, mapErr(err, "get audit event")
	}
	return e, nil
}

func (st *auditStore) List(ctx context.Context) ([]*AuditEvent, error) {
	rows, err := st.s.db.QueryContext(ctx, `
		SELECT id, timestamp, request_id, session_id, turn_id, step_id, user_id, cluster_id, action, payload
		FROM audit_events ORDER BY timestamp DESC`)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	var out []*AuditEvent
	for rows.Next() {
		e := &AuditEvent{}
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.RequestID, &e.SessionID, &e.TurnID, &e.StepID,
			&e.UserID, &e.ClusterID, &e.Action, &e.Payload); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- Settings ----

type settingsStore struct{ s *sqliteStore }

func (st *settingsStore) Set(ctx context.Context, set *Settings) error {
	now := time.Now()
	if set.CreatedAt.IsZero() {
		set.CreatedAt = now
	}
	set.UpdatedAt = now
	_, err := st.s.db.ExecContext(ctx, `
		INSERT INTO settings (group_name, key_name, value, sensitive, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(group_name, key_name) DO UPDATE SET
			value=excluded.value, sensitive=excluded.sensitive, updated_at=excluded.updated_at`,
		set.Group, set.Key, set.Value, boolInt(set.Sensitive), set.CreatedAt, set.UpdatedAt)
	return mapErr(err, "set setting")
}

func (st *settingsStore) Get(ctx context.Context, group, key string) (*Settings, error) {
	set := &Settings{}
	var sensitive int
	err := st.s.db.QueryRowContext(ctx, `
		SELECT group_name, key_name, value, sensitive, created_at, updated_at
		FROM settings WHERE group_name=? AND key_name=?`, group, key).
		Scan(&set.Group, &set.Key, &set.Value, &sensitive, &set.CreatedAt, &set.UpdatedAt)
	if err != nil {
		return nil, mapErr(err, "get setting")
	}
	set.Sensitive = sensitive != 0
	return set, nil
}

func (st *settingsStore) ListByGroup(ctx context.Context, group string) ([]*Settings, error) {
	rows, err := st.s.db.QueryContext(ctx, `
		SELECT group_name, key_name, value, sensitive, created_at, updated_at
		FROM settings WHERE group_name=? ORDER BY key_name`, group)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	defer rows.Close()
	var out []*Settings
	for rows.Next() {
		set := &Settings{}
		var sensitive int
		if err := rows.Scan(&set.Group, &set.Key, &set.Value, &sensitive, &set.CreatedAt, &set.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan setting: %w", err)
		}
		set.Sensitive = sensitive != 0
		out = append(out, set)
	}
	return out, rows.Err()
}

func (st *settingsStore) Delete(ctx context.Context, group, key string) error {
	res, err := st.s.db.ExecContext(ctx, `DELETE FROM settings WHERE group_name=? AND key_name=?`, group, key)
	if err != nil {
		return fmt.Errorf("delete setting: %w", err)
	}
	return ensureAffected(res, "delete setting")
}

// ---- Skills ----

type skillStore struct{ s *sqliteStore }

func (st *skillStore) Upsert(ctx context.Context, sk *Skill) error {
	now := time.Now()
	if sk.CreatedAt.IsZero() {
		sk.CreatedAt = now
	}
	sk.UpdatedAt = now
	spec, err := json.Marshal(sk.Spec)
	if err != nil {
		return fmt.Errorf("marshal skill spec: %w", err)
	}
	_, err = st.s.db.ExecContext(ctx, `
		INSERT INTO skills (name, spec, source, enabled, validation_status, hit_count, last_hit, last_validation, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			spec=excluded.spec, source=excluded.source, enabled=excluded.enabled,
			validation_status=excluded.validation_status, hit_count=excluded.hit_count,
			last_hit=excluded.last_hit, last_validation=excluded.last_validation, updated_at=excluded.updated_at`,
		sk.Name, string(spec), sk.Source, boolInt(sk.Enabled), sk.ValidationStatus,
		sk.HitCount, nilTime(sk.LastHit), nilTime(sk.LastValidation), sk.CreatedAt, sk.UpdatedAt)
	return mapErr(err, "upsert skill")
}

func scanSkill(scanner interface{ Scan(...any) error }) (*Skill, error) {
	sk := &Skill{}
	var spec string
	var enabled int
	if err := scanner.Scan(&sk.Name, &spec, &sk.Source, &enabled, &sk.ValidationStatus,
		&sk.HitCount, &sk.LastHit, &sk.LastValidation, &sk.CreatedAt, &sk.UpdatedAt); err != nil {
		return nil, err
	}
	sk.Enabled = enabled != 0
	if err := json.Unmarshal([]byte(spec), &sk.Spec); err != nil {
		return nil, fmt.Errorf("parse skill spec: %w", err)
	}
	return sk, nil
}

func (st *skillStore) Get(ctx context.Context, name string) (*Skill, error) {
	row := st.s.db.QueryRowContext(ctx, `
		SELECT name, spec, source, enabled, validation_status, hit_count, last_hit, last_validation, created_at, updated_at
		FROM skills WHERE name = ?`, name)
	sk, err := scanSkill(row)
	if err != nil {
		return nil, mapErr(err, "get skill")
	}
	return sk, nil
}

func (st *skillStore) List(ctx context.Context) ([]*Skill, error) {
	rows, err := st.s.db.QueryContext(ctx, `
		SELECT name, spec, source, enabled, validation_status, hit_count, last_hit, last_validation, created_at, updated_at
		FROM skills ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	defer rows.Close()
	var out []*Skill
	for rows.Next() {
		if sk, err := scanSkill(rows); err != nil {
			return nil, fmt.Errorf("scan skill: %w", err)
		} else {
			out = append(out, sk)
		}
	}
	return out, rows.Err()
}

func (st *skillStore) Delete(ctx context.Context, name string) error {
	res, err := st.s.db.ExecContext(ctx, `DELETE FROM skills WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete skill: %w", err)
	}
	return ensureAffected(res, "delete skill")
}

// ---- Approvals ----

type approvalStore struct{ s *sqliteStore }

func scanApproval(scanner interface{ Scan(...any) error }) (*Approval, error) {
	a := &Approval{}
	if err := scanner.Scan(&a.ID, &a.SessionID, &a.TurnID, &a.ClusterID, &a.Tool, &a.Resource, &a.Params,
		&a.RiskLevel, &a.Mode, &a.Status, &a.Decision, &a.MatchReason, &a.GrantID, &a.CreatedAt, &a.DecidedAt); err != nil {
		return nil, err
	}
	return a, nil
}

func (st *approvalStore) Create(ctx context.Context, a *Approval) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	params := redactJSONPayload(a.Params)
	_, err := st.s.db.ExecContext(ctx, `
		INSERT INTO approvals (id, session_id, turn_id, cluster_id, tool, resource, params,
			risk_level, mode, status, decision, match_reason, grant_id, created_at, decided_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.SessionID, a.TurnID, a.ClusterID, a.Tool, a.Resource, params,
		a.RiskLevel, a.Mode, a.Status, a.Decision, a.MatchReason, a.GrantID, a.CreatedAt, nilTime(a.DecidedAt))
	return mapErr(err, "create approval")
}

func (st *approvalStore) Get(ctx context.Context, id string) (*Approval, error) {
	row := st.s.db.QueryRowContext(ctx, `
		SELECT id, session_id, turn_id, cluster_id, tool, resource, params,
			risk_level, mode, status, decision, match_reason, grant_id, created_at, decided_at
		FROM approvals WHERE id = ?`, id)
	a, err := scanApproval(row)
	if err != nil {
		return nil, mapErr(err, "get approval")
	}
	return a, nil
}

func (st *approvalStore) Update(ctx context.Context, a *Approval) error {
	params := redactJSONPayload(a.Params)
	res, err := st.s.db.ExecContext(ctx, `
		UPDATE approvals SET session_id=?, turn_id=?, cluster_id=?, tool=?, resource=?, params=?,
			risk_level=?, mode=?, status=?, decision=?, match_reason=?, grant_id=?, decided_at=?
		WHERE id=?`,
		a.SessionID, a.TurnID, a.ClusterID, a.Tool, a.Resource, params,
		a.RiskLevel, a.Mode, a.Status, a.Decision, a.MatchReason, a.GrantID, nilTime(a.DecidedAt), a.ID)
	if err != nil {
		return fmt.Errorf("update approval: %w", err)
	}
	return ensureAffected(res, "update approval")
}

func (st *approvalStore) List(ctx context.Context) ([]*Approval, error) {
	rows, err := st.s.db.QueryContext(ctx, `
		SELECT id, session_id, turn_id, cluster_id, tool, resource, params,
			risk_level, mode, status, decision, match_reason, grant_id, created_at, decided_at
		FROM approvals ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list approvals: %w", err)
	}
	defer rows.Close()
	var out []*Approval
	for rows.Next() {
		if a, err := scanApproval(rows); err != nil {
			return nil, fmt.Errorf("scan approval: %w", err)
		} else {
			out = append(out, a)
		}
	}
	return out, rows.Err()
}

func (st *approvalStore) Delete(ctx context.Context, id string) error {
	res, err := st.s.db.ExecContext(ctx, `DELETE FROM approvals WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete approval: %w", err)
	}
	return ensureAffected(res, "delete approval")
}

// ---- Memories ----

type memoryStore struct{ s *sqliteStore }

func (st *memoryStore) Create(ctx context.Context, m *Memory) error {
	now := time.Now()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = now
	}
	_, err := st.s.db.ExecContext(ctx, `
		INSERT INTO memories (id, user_id, scope, namespace, cluster_id, content, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.UserID, m.Scope, m.Namespace, m.ClusterID, m.Content, m.CreatedAt, m.UpdatedAt)
	return mapErr(err, "create memory")
}

func scanMemory(scanner interface{ Scan(...any) error }) (*Memory, error) {
	m := &Memory{}
	if err := scanner.Scan(&m.ID, &m.UserID, &m.Scope, &m.Namespace, &m.ClusterID, &m.Content,
		&m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	return m, nil
}

func (st *memoryStore) Get(ctx context.Context, id string) (*Memory, error) {
	row := st.s.db.QueryRowContext(ctx, `
		SELECT id, user_id, scope, namespace, cluster_id, content, created_at, updated_at
		FROM memories WHERE id = ?`, id)
	m, err := scanMemory(row)
	if err != nil {
		return nil, mapErr(err, "get memory")
	}
	return m, nil
}

func (st *memoryStore) Update(ctx context.Context, m *Memory) error {
	m.UpdatedAt = time.Now()
	res, err := st.s.db.ExecContext(ctx, `
		UPDATE memories SET user_id=?, scope=?, namespace=?, cluster_id=?, content=?, updated_at=?
		WHERE id=?`,
		m.UserID, m.Scope, m.Namespace, m.ClusterID, m.Content, m.UpdatedAt, m.ID)
	if err != nil {
		return fmt.Errorf("update memory: %w", err)
	}
	return ensureAffected(res, "update memory")
}

func (st *memoryStore) List(ctx context.Context) ([]*Memory, error) {
	rows, err := st.s.db.QueryContext(ctx, `
		SELECT id, user_id, scope, namespace, cluster_id, content, created_at, updated_at
		FROM memories ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()
	var out []*Memory
	for rows.Next() {
		if m, err := scanMemory(rows); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		} else {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

func (st *memoryStore) Delete(ctx context.Context, id string) error {
	res, err := st.s.db.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	return ensureAffected(res, "delete memory")
}

// ---- Retention ----

type retentionStore struct{ s *sqliteStore }

func (st *retentionStore) Upsert(ctx context.Context, p *RetentionPolicy) error {
	p.UpdatedAt = time.Now()
	_, err := st.s.db.ExecContext(ctx, `
		INSERT INTO retention_policies (name, enabled, retention_days, max_count, cleanup_mode, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			enabled=excluded.enabled, retention_days=excluded.retention_days,
			max_count=excluded.max_count, cleanup_mode=excluded.cleanup_mode, updated_at=excluded.updated_at`,
		p.Name, boolInt(p.Enabled), p.RetentionDays, p.MaxCount, p.CleanupMode, p.UpdatedAt)
	return mapErr(err, "upsert retention policy")
}

func scanRetention(scanner interface{ Scan(...any) error }) (*RetentionPolicy, error) {
	p := &RetentionPolicy{}
	var enabled int
	if err := scanner.Scan(&p.Name, &enabled, &p.RetentionDays, &p.MaxCount, &p.CleanupMode, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Enabled = enabled != 0
	return p, nil
}

func (st *retentionStore) Get(ctx context.Context, name string) (*RetentionPolicy, error) {
	row := st.s.db.QueryRowContext(ctx, `
		SELECT name, enabled, retention_days, max_count, cleanup_mode, updated_at
		FROM retention_policies WHERE name = ?`, name)
	p, err := scanRetention(row)
	if err != nil {
		return nil, mapErr(err, "get retention policy")
	}
	return p, nil
}

func (st *retentionStore) List(ctx context.Context) ([]*RetentionPolicy, error) {
	rows, err := st.s.db.QueryContext(ctx, `
		SELECT name, enabled, retention_days, max_count, cleanup_mode, updated_at
		FROM retention_policies ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list retention policies: %w", err)
	}
	defer rows.Close()
	var out []*RetentionPolicy
	for rows.Next() {
		if p, err := scanRetention(rows); err != nil {
			return nil, fmt.Errorf("scan retention policy: %w", err)
		} else {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

func (st *retentionStore) Delete(ctx context.Context, name string) error {
	res, err := st.s.db.ExecContext(ctx, `DELETE FROM retention_policies WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete retention policy: %w", err)
	}
	return ensureAffected(res, "delete retention policy")
}

// ---- Clusters ----

type clusterStore struct{ s *sqliteStore }

func (st *clusterStore) Create(ctx context.Context, c *Cluster) error {
	now := time.Now()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = now
	}
	_, err := st.s.db.ExecContext(ctx, `
		INSERT INTO clusters (id, name, context, server, version, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.Context, c.Server, c.Version, c.Status, c.CreatedAt, c.UpdatedAt)
	return mapErr(err, "create cluster")
}

func scanCluster(scanner interface{ Scan(...any) error }) (*Cluster, error) {
	c := &Cluster{}
	if err := scanner.Scan(&c.ID, &c.Name, &c.Context, &c.Server, &c.Version, &c.Status,
		&c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return c, nil
}

func (st *clusterStore) Get(ctx context.Context, id string) (*Cluster, error) {
	row := st.s.db.QueryRowContext(ctx, `
		SELECT id, name, context, server, version, status, created_at, updated_at
		FROM clusters WHERE id = ?`, id)
	c, err := scanCluster(row)
	if err != nil {
		return nil, mapErr(err, "get cluster")
	}
	return c, nil
}

func (st *clusterStore) Update(ctx context.Context, c *Cluster) error {
	c.UpdatedAt = time.Now()
	res, err := st.s.db.ExecContext(ctx, `
		UPDATE clusters SET name=?, context=?, server=?, version=?, status=?, updated_at=? WHERE id=?`,
		c.Name, c.Context, c.Server, c.Version, c.Status, c.UpdatedAt, c.ID)
	if err != nil {
		return fmt.Errorf("update cluster: %w", err)
	}
	return ensureAffected(res, "update cluster")
}

func (st *clusterStore) List(ctx context.Context) ([]*Cluster, error) {
	rows, err := st.s.db.QueryContext(ctx, `
		SELECT id, name, context, server, version, status, created_at, updated_at FROM clusters ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list clusters: %w", err)
	}
	defer rows.Close()
	var out []*Cluster
	for rows.Next() {
		if c, err := scanCluster(rows); err != nil {
			return nil, fmt.Errorf("scan cluster: %w", err)
		} else {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

func (st *clusterStore) Delete(ctx context.Context, id string) error {
	res, err := st.s.db.ExecContext(ctx, `DELETE FROM clusters WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete cluster: %w", err)
	}
	return ensureAffected(res, "delete cluster")
}

// ---- Credentials ----

type credentialStore struct{ s *sqliteStore }

func scanCredential(scanner interface{ Scan(...any) error }) (*Credential, error) {
	c := &Credential{}
	if err := scanner.Scan(&c.ID, &c.ClusterID, &c.KubeconfigSecret, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return c, nil
}

func (st *credentialStore) Create(ctx context.Context, c *Credential) error {
	now := time.Now()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = now
	}
	_, err := st.s.db.ExecContext(ctx, `
		INSERT INTO credentials (id, cluster_id, kubeconfig_secret, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`,
		c.ID, c.ClusterID, c.KubeconfigSecret, c.CreatedAt, c.UpdatedAt)
	return mapErr(err, "create credential")
}

func (st *credentialStore) Get(ctx context.Context, id string) (*Credential, error) {
	row := st.s.db.QueryRowContext(ctx, `
		SELECT id, cluster_id, kubeconfig_secret, created_at, updated_at FROM credentials WHERE id = ?`, id)
	c, err := scanCredential(row)
	if err != nil {
		return nil, mapErr(err, "get credential")
	}
	return c, nil
}

func (st *credentialStore) GetByCluster(ctx context.Context, clusterID string) (*Credential, error) {
	row := st.s.db.QueryRowContext(ctx, `
		SELECT id, cluster_id, kubeconfig_secret, created_at, updated_at FROM credentials WHERE cluster_id = ?`, clusterID)
	c, err := scanCredential(row)
	if err != nil {
		return nil, mapErr(err, "get credential by cluster")
	}
	return c, nil
}

func (st *credentialStore) Update(ctx context.Context, c *Credential) error {
	c.UpdatedAt = time.Now()
	res, err := st.s.db.ExecContext(ctx, `
		UPDATE credentials SET cluster_id=?, kubeconfig_secret=?, updated_at=? WHERE id=?`,
		c.ClusterID, c.KubeconfigSecret, c.UpdatedAt, c.ID)
	if err != nil {
		return fmt.Errorf("update credential: %w", err)
	}
	return ensureAffected(res, "update credential")
}

func (st *credentialStore) Delete(ctx context.Context, id string) error {
	res, err := st.s.db.ExecContext(ctx, `DELETE FROM credentials WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete credential: %w", err)
	}
	return ensureAffected(res, "delete credential")
}
