package store

import (
	"context"
	"database/sql"
	"fmt"
)

// schemaVersion 是统一的逻辑 schema 版本。SQLite/MySQL 共用该版本号，
// migrations 按版本逐个应用（P0.3/P1.12/P1.3）。只允许追加，禁止回跳。
const schemaVersion = 1

// migration 是一条单向升级步骤，通过 dialect 适配两个驱动。
type migration struct {
	version int
	name    string
	up      func(ctx context.Context, tx *sql.Tx, d dialect) error
}

// migrations 是全部升级步骤（按 version 升序）。
var migrations = []migration{
	{
		version: 1,
		name:    "initial platform schema",
		up:      migrateV1,
	},
}

// schemaV1 描述建表：users / roles / sessions / audit_events / settings /
// skills / approvals / memories / retention_policies / clusters / credentials。
//
// 敏感字段约定：password_hash、credential kubeconfig 只进库不回读明文字段；
// audit 与 approval 的 payload/params 在写入前由上层 redactor 脱敏。长文本列
// （colLong）存放 JSON，业务侧始终显式写入。
var schemaV1 = []tableSpec{
	{
		name: "roles",
		cols: []column{
			{name: "id", kind: colText, primary: true},
			{name: "name", kind: colText, unique: true},
			{name: "description", kind: colLong, def: "''"},
			{name: "permissions", kind: colLong, def: "'[]'"},
			{name: "created_at", kind: colTime},
			{name: "updated_at", kind: colTime},
		},
	},
	{
		name: "users",
		cols: []column{
			{name: "id", kind: colText, primary: true},
			{name: "username", kind: colText, unique: true},
			{name: "display_name", kind: colLong, def: "''"},
			{name: "password_hash", kind: colText},
			{name: "role_id", kind: colText, def: "''"},
			{name: "status", kind: colText, def: "'active'"},
			{name: "created_at", kind: colTime},
			{name: "updated_at", kind: colTime},
		},
	},
	{
		name: "sessions",
		cols: []column{
			{name: "id", kind: colText, primary: true},
			{name: "user_id", kind: colText, def: "''"},
			{name: "name", kind: colText, def: "''"},
			{name: "cluster_id", kind: colText, def: "''"},
			{name: "namespace", kind: colText, def: "''"},
			{name: "model", kind: colText, def: "''"},
			{name: "state", kind: colText, def: "''"},
			{name: "data", kind: colLong, def: "'{}'"},
			{name: "created_at", kind: colTime},
			{name: "last_modified", kind: colTime},
		},
	},
	{
		name: "audit_events",
		cols: []column{
			{name: "id", kind: colInt, autoIncrement: true},
			{name: "timestamp", kind: colTime},
			{name: "request_id", kind: colText, def: "''"},
			{name: "session_id", kind: colText, def: "''"},
			{name: "turn_id", kind: colText, def: "''"},
			{name: "step_id", kind: colText, def: "''"},
			{name: "user_id", kind: colText, def: "''"},
			{name: "cluster_id", kind: colText, def: "''"},
			{name: "action", kind: colText},
			{name: "payload", kind: colLong, def: "''"},
		},
	},
	{
		name: "settings",
		cols: []column{
			{name: "group_name", kind: colText},
			{name: "key_name", kind: colText},
			{name: "value", kind: colLong, def: "''"},
			{name: "sensitive", kind: colBool, def: "0"},
			{name: "created_at", kind: colTime},
			{name: "updated_at", kind: colTime},
		},
		primaryKey: []string{"group_name", "key_name"},
	},
	{
		name: "skills",
		cols: []column{
			{name: "name", kind: colText, primary: true},
			{name: "spec", kind: colLong},
			{name: "source", kind: colText, def: "'builtin'"},
			{name: "enabled", kind: colBool, def: "1"},
			{name: "validation_status", kind: colText, def: "''"},
			{name: "hit_count", kind: colInt, def: "0"},
			{name: "last_hit", kind: colTimeOrNull},
			{name: "last_validation", kind: colTimeOrNull},
			{name: "created_at", kind: colTime},
			{name: "updated_at", kind: colTime},
		},
	},
	{
		name: "approvals",
		cols: []column{
			{name: "id", kind: colText, primary: true},
			{name: "session_id", kind: colText, def: "''"},
			{name: "turn_id", kind: colText, def: "''"},
			{name: "cluster_id", kind: colText, def: "''"},
			{name: "tool", kind: colText, def: "''"},
			{name: "resource", kind: colText, def: "''"},
			{name: "params", kind: colLong, def: "''"},
			{name: "risk_level", kind: colText, def: "''"},
			{name: "mode", kind: colText, def: "''"},
			{name: "status", kind: colText, def: "'pending'"},
			{name: "decision", kind: colText, def: "''"},
			{name: "match_reason", kind: colText, def: "''"},
			{name: "grant_id", kind: colText, def: "''"},
			{name: "created_at", kind: colTime},
			{name: "decided_at", kind: colTimeOrNull},
		},
	},
	{
		name: "memories",
		cols: []column{
			{name: "id", kind: colText, primary: true},
			{name: "user_id", kind: colText, def: "''"},
			{name: "scope", kind: colText},
			{name: "namespace", kind: colText, def: "''"},
			{name: "cluster_id", kind: colText, def: "''"},
			{name: "content", kind: colLong},
			{name: "created_at", kind: colTime},
			{name: "updated_at", kind: colTime},
		},
	},
	{
		name: "retention_policies",
		cols: []column{
			{name: "name", kind: colText, primary: true},
			{name: "enabled", kind: colBool, def: "0"},
			{name: "retention_days", kind: colInt, def: "0"},
			{name: "max_count", kind: colInt, def: "0"},
			{name: "cleanup_mode", kind: colText, def: "'delete'"},
			{name: "updated_at", kind: colTime},
		},
	},
	{
		name: "clusters",
		cols: []column{
			{name: "id", kind: colText, primary: true},
			{name: "name", kind: colText},
			{name: "context", kind: colText, def: "''"},
			{name: "server", kind: colText, def: "''"},
			{name: "version", kind: colText, def: "''"},
			{name: "status", kind: colText, def: "'unknown'"},
			{name: "created_at", kind: colTime},
			{name: "updated_at", kind: colTime},
		},
	},
	{
		name: "credentials",
		cols: []column{
			{name: "id", kind: colText, primary: true},
			{name: "cluster_id", kind: colText, unique: true},
			{name: "kubeconfig_secret", kind: colLong},
			{name: "created_at", kind: colTime},
			{name: "updated_at", kind: colTime},
		},
	},
}

// schemaV1Indexes 是初始 Schema 的二级索引。
var schemaV1Indexes = []indexSpec{
	{name: "idx_audit_timestamp", table: "audit_events", cols: "timestamp"},
	{name: "idx_audit_session", table: "audit_events", cols: "session_id"},
	{name: "idx_sessions_user", table: "sessions", cols: "user_id"},
	{name: "idx_approvals_session", table: "approvals", cols: "session_id"},
	{name: "idx_memories_scope", table: "memories", cols: "scope"},
}

// migrateV1 建全部表与索引，SQL 由 dialect 按驱动渲染。
func migrateV1(ctx context.Context, tx *sql.Tx, d dialect) error {
	for _, t := range schemaV1 {
		if _, err := tx.ExecContext(ctx, d.createTable(t)); err != nil {
			return fmt.Errorf("create table %s: %w", t.name, err)
		}
	}
	for _, idx := range schemaV1Indexes {
		q := d.createIndex(idx.name, idx.table, idx.cols)
		if _, err := tx.ExecContext(ctx, q); err != nil {
			// MySQL 无 CREATE INDEX IF NOT EXISTS；重复执行时容忍已存在索引。
			if d.isDuplicateIndexErr(err) {
				continue
			}
			return fmt.Errorf("create index %s: %w", idx.name, err)
		}
	}
	return nil
}

// Migrate 把数据库升级到当前 schemaVersion（幂等），两驱动共用。
func (s *dbStore) Migrate(ctx context.Context) error {
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
		if err := m.up(ctx, tx, s.dialect); err != nil {
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
