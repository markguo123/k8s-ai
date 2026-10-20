package store

import (
	"context"
	"database/sql"
	"fmt"
)

// schemaVersion 是统一的逻辑 schema 版本。SQLite/MySQL 共用该版本号，
// migrations 按版本逐个应用（P0.3/P1.12/P1.3）。只允许追加，禁止回跳。
const schemaVersion = 4

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
	{
		version: 2,
		name:    "agent session turn and step model",
		up:      migrateV2,
	},
	{
		version: 3,
		name:    "audit event v4 classification fields",
		up:      migrateV3,
	},
	{
		version: 4,
		name:    "authz grants and api tokens platform model",
		up:      migrateV4,
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

// schemaV2 是第 2 版新增表：turns / steps（P1.7 三模型入库）。
// 敏感字段约定：turn input/output/error 与 step params/result/error 在写入前
// 由上层 redactor 脱敏；长文本列（colLong）存放 JSON/文本摘要，业务侧显式写入。
var schemaV2 = []tableSpec{
	{
		name: "turns",
		cols: []column{
			{name: "id", kind: colText, primary: true},
			{name: "session_id", kind: colText},
			{name: "user_id", kind: colText, def: "''"},
			{name: "status", kind: colText},
			{name: "input", kind: colLong, def: "''"},
			{name: "output", kind: colLong, def: "''"},
			{name: "error", kind: colLong, def: "''"},
			{name: "prompt_tokens", kind: colInt, def: "0"},
			{name: "completion_tokens", kind: colInt, def: "0"},
			{name: "total_tokens", kind: colInt, def: "0"},
			{name: "started_at", kind: colTimeOrNull},
			{name: "finished_at", kind: colTimeOrNull},
			{name: "created_at", kind: colTime},
			{name: "updated_at", kind: colTime},
		},
	},
	{
		name: "steps",
		cols: []column{
			{name: "id", kind: colText, primary: true},
			{name: "turn_id", kind: colText},
			{name: "session_id", kind: colText, def: "''"},
			{name: "seq", kind: colInt, def: "0"},
			{name: "type", kind: colText},
			{name: "status", kind: colText},
			{name: "tool", kind: colText, def: "''"},
			{name: "params", kind: colLong, def: "''"},
			{name: "result", kind: colLong, def: "''"},
			{name: "error", kind: colLong, def: "''"},
			{name: "started_at", kind: colTimeOrNull},
			{name: "finished_at", kind: colTimeOrNull},
			{name: "created_at", kind: colTime},
			{name: "updated_at", kind: colTime},
		},
	},
}

// schemaV2Indexes 是第 2 版新增表的二级索引。
var schemaV2Indexes = []indexSpec{
	{name: "idx_turns_session", table: "turns", cols: "session_id"},
	{name: "idx_turns_status", table: "turns", cols: "status"},
	{name: "idx_steps_turn", table: "steps", cols: "turn_id"},
	{name: "idx_steps_session", table: "steps", cols: "session_id"},
}

// auditV4Columns 是第 3 版给 audit_events 追加的 §19 分类列。
//
// 约束：补列必须带常量默认值且非空（SQLite 不接受非常量默认值），因此摘要列
// 用 colTextMedium（MySQL VARCHAR(1024)）而不是 colLong；写入侧负责截断。
var auditV4Columns = []column{
	{name: "event_type", kind: colText, def: "''"},
	{name: "resource_kind", kind: colText, def: "''"},
	{name: "resource_name", kind: colText, def: "''"},
	{name: "operation", kind: colText, def: "''"},
	{name: "risk", kind: colText, def: "''"},
	{name: "decision", kind: colText, def: "''"},
	{name: "approval_id", kind: colText, def: "''"},
	{name: "grant_id", kind: colText, def: "''"},
	{name: "match_reason", kind: colText, def: "''"},
	{name: "parameter_summary", kind: colTextMedium, def: "''"},
	{name: "result_summary", kind: colTextMedium, def: "''"},
	{name: "status", kind: colText, def: "''"},
}

// schemaV3Indexes 是第 3 版为审计查询新增的二级索引（P4.7 过滤条件用）。
var schemaV3Indexes = []indexSpec{
	{name: "idx_audit_event_type", table: "audit_events", cols: "event_type"},
	{name: "idx_audit_user", table: "audit_events", cols: "user_id"},
}

// schemaV4 是第 4 版新增表：authz_grants / api_tokens（P3.11 身份与授权底座）。
// 敏感字段约定：api_tokens.token_hash 只存 SHA-256 哈希，明文仅在创建时返回一次，
// 永不回读。namespaces / permissions 为 JSON 数组字符串，长文本列（colLong）
// 存放，业务侧显式写入；expires_at / last_used_at 可空。
var schemaV4 = []tableSpec{
	{
		name: "authz_grants",
		cols: []column{
			{name: "id", kind: colText, primary: true},
			{name: "principal_type", kind: colText},
			{name: "principal_id", kind: colText},
			{name: "cluster_id", kind: colText, def: "''"},
			{name: "namespaces", kind: colLong, def: "'[]'"},
			{name: "permissions", kind: colLong, def: "'[]'"},
			{name: "expires_at", kind: colTimeOrNull},
			{name: "created_by", kind: colText, def: "''"},
			{name: "created_at", kind: colTime},
			{name: "updated_at", kind: colTime},
		},
	},
	{
		name: "api_tokens",
		cols: []column{
			{name: "id", kind: colText, primary: true},
			{name: "user_id", kind: colText},
			{name: "name", kind: colText, def: "''"},
			{name: "token_hash", kind: colText},
			{name: "expires_at", kind: colTimeOrNull},
			{name: "status", kind: colText, def: "'active'"},
			{name: "last_used_at", kind: colTimeOrNull},
			{name: "created_at", kind: colTime},
			{name: "updated_at", kind: colTime},
		},
	},
}

// schemaV4Indexes 是第 4 版新增表的二级索引（按 principal / user 查询用）。
var schemaV4Indexes = []indexSpec{
	{name: "idx_authz_grant_principal", table: "authz_grants", cols: "principal_type, principal_id"},
	{name: "idx_api_token_user", table: "api_tokens", cols: "user_id"},
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

// migrateV2 建 turns / steps 表与索引（SQL 由 dialect 按驱动渲染）。
func migrateV2(ctx context.Context, tx *sql.Tx, d dialect) error {
	for _, t := range schemaV2 {
		if _, err := tx.ExecContext(ctx, d.createTable(t)); err != nil {
			return fmt.Errorf("create table %s: %w", t.name, err)
		}
	}
	for _, idx := range schemaV2Indexes {
		q := d.createIndex(idx.name, idx.table, idx.cols)
		if _, err := tx.ExecContext(ctx, q); err != nil {
			if d.isDuplicateIndexErr(err) {
				continue
			}
			return fmt.Errorf("create index %s: %w", idx.name, err)
		}
	}
	return nil
}

// migrateV3 给 audit_events 追加 §19 分类列、建查询索引，并回填历史行的
// event_type（用 EventTypeForAction 在 Go 侧单源映射，避免 SQL 里再写一份 CASE）。
func migrateV3(ctx context.Context, tx *sql.Tx, d dialect) error {
	for _, c := range auditV4Columns {
		if _, err := tx.ExecContext(ctx, d.addColumn("audit_events", c)); err != nil {
			if d.isDuplicateColumnErr(err) {
				continue
			}
			return fmt.Errorf("add column audit_events.%s: %w", c.name, err)
		}
	}
	for _, idx := range schemaV3Indexes {
		q := d.createIndex(idx.name, idx.table, idx.cols)
		if _, err := tx.ExecContext(ctx, q); err != nil {
			if d.isDuplicateIndexErr(err) {
				continue
			}
			return fmt.Errorf("create index %s: %w", idx.name, err)
		}
	}
	return backfillAuditEventType(ctx, tx)
}

// migrateV4 建 authz_grants / api_tokens 表与索引（SQL 由 dialect 按驱动渲染）。
func migrateV4(ctx context.Context, tx *sql.Tx, d dialect) error {
	for _, t := range schemaV4 {
		if _, err := tx.ExecContext(ctx, d.createTable(t)); err != nil {
			return fmt.Errorf("create table %s: %w", t.name, err)
		}
	}
	for _, idx := range schemaV4Indexes {
		q := d.createIndex(idx.name, idx.table, idx.cols)
		if _, err := tx.ExecContext(ctx, q); err != nil {
			if d.isDuplicateIndexErr(err) {
				continue
			}
			return fmt.Errorf("create index %s: %w", idx.name, err)
		}
	}
	return nil
}

// backfillAuditEventType 给历史审计行补 event_type（只处理空值，可重复执行）。
func backfillAuditEventType(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, action FROM audit_events WHERE event_type = ''`)
	if err != nil {
		return fmt.Errorf("scan audit actions: %w", err)
	}
	type pair struct {
		id     int64
		action string
	}
	var pending []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.action); err != nil {
			rows.Close()
			return fmt.Errorf("scan audit action: %w", err)
		}
		pending = append(pending, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, p := range pending {
		if _, err := tx.ExecContext(ctx,
			`UPDATE audit_events SET event_type = ? WHERE id = ?`,
			EventTypeForAction(p.action), p.id); err != nil {
			return fmt.Errorf("backfill audit event_type: %w", err)
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
