package store

import (
	"context"
	"database/sql"
	"fmt"
)

// schemaVersion 是统一的逻辑 schema 版本。SQLite/MySQL 共用该版本号，
// migrations 按版本逐个应用（P0.3/P1.12）。只允许追加，禁止回跳。
const schemaVersion = 1

// migration 是一条单向升级步骤。
type migration struct {
	version int
	name    string
	up      func(ctx context.Context, tx *sql.Tx) error
}

// migrations 是全部升级步骤（按 version 升序）。
var migrations = []migration{
	{
		version: 1,
		name:    "initial platform schema",
		up:      migrateV1,
	},
}

// migrateV1 建表：users / roles / sessions / audit_events / settings /
// skills / approvals / memories / retention_policies / clusters / credentials。
// 敏感字段约定：password_hash、credential kubeconfig 只进库不回读明文字段；
// audit 与 approval 的 payload/params 在写入前由上层 redactor 脱敏。
func migrateV1(_ context.Context, tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name    TEXT NOT NULL,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,

		`CREATE TABLE IF NOT EXISTS roles (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL DEFAULT '',
			permissions TEXT NOT NULL DEFAULT '[]',
			created_at  TIMESTAMP NOT NULL,
			updated_at  TIMESTAMP NOT NULL
		);`,

		`CREATE TABLE IF NOT EXISTS users (
			id            TEXT PRIMARY KEY,
			username      TEXT NOT NULL UNIQUE,
			display_name  TEXT NOT NULL DEFAULT '',
			password_hash TEXT NOT NULL,
			role_id       TEXT NOT NULL DEFAULT '',
			status        TEXT NOT NULL DEFAULT 'active',
			created_at    TIMESTAMP NOT NULL,
			updated_at    TIMESTAMP NOT NULL
		);`,

		`CREATE TABLE IF NOT EXISTS sessions (
			id            TEXT PRIMARY KEY,
			user_id       TEXT NOT NULL DEFAULT '',
			name          TEXT NOT NULL DEFAULT '',
			cluster_id    TEXT NOT NULL DEFAULT '',
			namespace     TEXT NOT NULL DEFAULT '',
			model         TEXT NOT NULL DEFAULT '',
			state         TEXT NOT NULL DEFAULT '',
			data          TEXT NOT NULL DEFAULT '{}',
			created_at    TIMESTAMP NOT NULL,
			last_modified TIMESTAMP NOT NULL
		);`,

		`CREATE TABLE IF NOT EXISTS audit_events (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp  TIMESTAMP NOT NULL,
			request_id TEXT NOT NULL DEFAULT '',
			session_id TEXT NOT NULL DEFAULT '',
			turn_id    TEXT NOT NULL DEFAULT '',
			step_id    TEXT NOT NULL DEFAULT '',
			user_id    TEXT NOT NULL DEFAULT '',
			cluster_id TEXT NOT NULL DEFAULT '',
			action     TEXT NOT NULL,
			payload    TEXT NOT NULL DEFAULT ''
		);`,

		`CREATE TABLE IF NOT EXISTS settings (
			group_name TEXT NOT NULL,
			key_name   TEXT NOT NULL,
			value      TEXT NOT NULL DEFAULT '',
			sensitive  INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY (group_name, key_name)
		);`,

		`CREATE TABLE IF NOT EXISTS skills (
			name              TEXT PRIMARY KEY,
			spec              TEXT NOT NULL,
			source            TEXT NOT NULL DEFAULT 'builtin',
			enabled           INTEGER NOT NULL DEFAULT 1,
			validation_status TEXT NOT NULL DEFAULT '',
			hit_count         INTEGER NOT NULL DEFAULT 0,
			last_hit          TIMESTAMP,
			last_validation   TIMESTAMP,
			created_at        TIMESTAMP NOT NULL,
			updated_at        TIMESTAMP NOT NULL
		);`,

		`CREATE TABLE IF NOT EXISTS approvals (
			id           TEXT PRIMARY KEY,
			session_id   TEXT NOT NULL DEFAULT '',
			turn_id      TEXT NOT NULL DEFAULT '',
			cluster_id   TEXT NOT NULL DEFAULT '',
			tool         TEXT NOT NULL DEFAULT '',
			resource     TEXT NOT NULL DEFAULT '',
			params       TEXT NOT NULL DEFAULT '',
			risk_level   TEXT NOT NULL DEFAULT '',
			mode         TEXT NOT NULL DEFAULT '',
			status       TEXT NOT NULL DEFAULT 'pending',
			decision     TEXT NOT NULL DEFAULT '',
			match_reason TEXT NOT NULL DEFAULT '',
			grant_id     TEXT NOT NULL DEFAULT '',
			created_at   TIMESTAMP NOT NULL,
			decided_at   TIMESTAMP
		);`,

		`CREATE TABLE IF NOT EXISTS memories (
			id         TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL DEFAULT '',
			scope      TEXT NOT NULL,
			namespace  TEXT NOT NULL DEFAULT '',
			cluster_id TEXT NOT NULL DEFAULT '',
			content    TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);`,

		`CREATE TABLE IF NOT EXISTS retention_policies (
			name           TEXT PRIMARY KEY,
			enabled        INTEGER NOT NULL DEFAULT 0,
			retention_days INTEGER NOT NULL DEFAULT 0,
			max_count      INTEGER NOT NULL DEFAULT 0,
			cleanup_mode   TEXT NOT NULL DEFAULT 'delete',
			updated_at     TIMESTAMP NOT NULL
		);`,

		`CREATE TABLE IF NOT EXISTS clusters (
			id         TEXT PRIMARY KEY,
			name       TEXT NOT NULL,
			context    TEXT NOT NULL DEFAULT '',
			server     TEXT NOT NULL DEFAULT '',
			version    TEXT NOT NULL DEFAULT '',
			status     TEXT NOT NULL DEFAULT 'unknown',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);`,

		`CREATE TABLE IF NOT EXISTS credentials (
			id               TEXT PRIMARY KEY,
			cluster_id       TEXT NOT NULL UNIQUE,
			kubeconfig_secret TEXT NOT NULL,
			created_at       TIMESTAMP NOT NULL,
			updated_at       TIMESTAMP NOT NULL
		);`,

		`CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_events (timestamp);`,
		`CREATE INDEX IF NOT EXISTS idx_audit_session ON audit_events (session_id);`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions (user_id);`,
		`CREATE INDEX IF NOT EXISTS idx_approvals_session ON approvals (session_id);`,
		`CREATE INDEX IF NOT EXISTS idx_memories_scope ON memories (scope);`,
	}

	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("apply v1 migration %q: %w", firstWord(q), err)
		}
	}
	return nil
}

func firstWord(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\n' || s[i] == '\t' {
			return s[:i]
		}
	}
	return s
}

// Migrate 把数据库升级到当前 schemaVersion。幂等：重复调用返回 nil。
func (s *sqliteStore) Migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name    TEXT NOT NULL,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}

	for _, m := range migrations {
		if m.version > schemaVersion {
			continue
		}
		var applied int
		err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&applied)
		if err != nil {
			return fmt.Errorf("check migration %d: %w", m.version, err)
		}
		if applied > 0 {
			continue
		}
		if err := m.up(ctx, tx); err != nil {
			return fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name) VALUES (?, ?)`, m.version, m.name); err != nil {
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}
