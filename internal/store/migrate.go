package store

import (
	"context"
	"database/sql"
	"fmt"
)

// schemaVersion 是统一的逻辑 schema 版本。SQLite/MySQL 共用该版本号，
// migrations 按版本逐个应用（P0.3/P1.12/P1.3）。只允许追加，禁止回跳。
const schemaVersion = 6

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
	{
		version: 5,
		name:    "widen long text columns to mysql mediumtext",
		up:      migrateV5,
	},
	{
		version: 6,
		name:    "object metadata registry attachments and reports",
		up:      migrateV6,
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
// schemaV6 是第 6 版新增表：objects / attachments / reports（方案 15 §6）。
//
// 双存储口径（ADR-043）：MySQL 恒是业务数据与对象元数据的事实源，objects 表与 driver
// 无关——正文可以落 db/dir/s3 任一种后端，元数据行永远在这里。objects.payload 只有
// driver=db 时写，其余后端恒 NULL：它是「兼容/兜底后端的正文载体」，不是元数据模型的
// 一部分，也不参与任何索引。
var schemaV6 = []tableSpec{
	{
		name: "objects",
		cols: []column{
			{name: "blob_key", kind: colText, primary: true},
			{name: "sha256", kind: colText},
			{name: "size", kind: colInt, def: "0"},
			{name: "media_type", kind: colText, def: "''"},
			{name: "category", kind: colText, def: "''"},
			// driver 记录写入时的后端：改配置不清库，旧对象要按原后端读（§10）。
			{name: "driver", kind: colText, def: "''"},
			{name: "ref_count", kind: colInt, def: "0"},
			{name: "state", kind: colText, def: "'ready'"},
			{name: "deleted_at", kind: colTimeOrNull},
			// created_by 是报告归属的唯一依据：巡检 job 是进程内对象，没有 owner 列可继承。
			{name: "created_by", kind: colText, def: "''"},
			{name: "payload", kind: colBytes, nullable: true},
			{name: "created_at", kind: colTime},
			{name: "updated_at", kind: colTime},
		},
	},
	{
		name: "attachments",
		cols: []column{
			{name: "id", kind: colText, primary: true},
			{name: "session_id", kind: colText},
			{name: "turn_id", kind: colText, def: "''"},
			{name: "user_id", kind: colText, def: "''"},
			// name 只存净化后的展示名（去路径、去控制字符、≤191）：真名永不参与对象 key。
			// 它进唯一索引，因此必须是 colText（VARCHAR(191)）而不是更宽的列，否则
			// 三列联合唯一索引超过 InnoDB 3072 字节上限；写入方负责拒绝超长展示名。
			{name: "name", kind: colText},
			{name: "blob_key", kind: colText},
			{name: "media_type", kind: colText, def: "''"},
			{name: "size", kind: colInt, def: "0"},
			{name: "sha256", kind: colText},
			{name: "created_at", kind: colTime},
		},
	},
	{
		name: "reports",
		cols: []column{
			{name: "id", kind: colText, primary: true},
			{name: "cluster_id", kind: colText, def: "''"},
			{name: "scan_id", kind: colText, def: "''"},
			{name: "kind", kind: colText, def: "''"},
			{name: "blob_key", kind: colText},
			{name: "media_type", kind: colText, def: "''"},
			{name: "size", kind: colInt, def: "0"},
			{name: "sha256", kind: colText},
			{name: "findings", kind: colInt, def: "0"},
			{name: "created_by", kind: colText, def: "''"},
			// available 由 verify 维护：false 时列表可见、下载返回 410（§6.2）。
			{name: "available", kind: colBool, def: "1"},
			{name: "created_at", kind: colTime},
		},
	},
}

// schemaV6Indexes 是第 6 版新增索引。
//
// attachments.session_id 不单独建索引：UNIQUE(session_id, sha256, name) 的最左前缀
// 就是它，再建一份只是写放大（方案 15 §6.2 把 session_id 标成 idx，口径以这里为准）。
// 该唯一索引同时是上传接口的幂等锚点（§9.1）。
var schemaV6Indexes = []indexSpec{
	{name: "idx_objects_sha", table: "objects", cols: "sha256"},
	{name: "idx_objects_category", table: "objects", cols: "category"},
	{name: "idx_objects_state_updated", table: "objects", cols: "state, deleted_at"},
	{name: "uk_attachments_session_sha_name", table: "attachments", cols: "session_id, sha256, name", unique: true},
	{name: "idx_reports_cluster", table: "reports", cols: "cluster_id"},
	{name: "idx_reports_created", table: "reports", cols: "created_at"},
}

// schemaV6SessionColumns 是第 6 版给 sessions 追加的两列。四期刻意不读它们：§8.4 决定
// 会话正文不外置，data 列继续做事实源，platform.blobs.session_data=true 被 validate
// 硬拒。列先落下来，是为了将来真开这个开关时不必再动 schema。
var schemaV6SessionColumns = []column{
	{name: "data_blob_key", kind: colText, def: "''"},
	{name: "data_size", kind: colInt, def: "0"},
}


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

// columnRef 是一条需要按 colKind 放宽的列引用（表名 + 列定义）。
type columnRef struct {
	table string
	col   column
}

// longTextColumns 从各版本 schema 派生长文本列（colLong）清单。刻意单源派生而不是
// 手写清单：新增一个存 JSON 的列会自动进入放宽范围，不会出现"加了列忘了放宽类型"。
func longTextColumns() []columnRef {
	var out []columnRef
	for _, tables := range [][]tableSpec{schemaV1, schemaV2, schemaV4} {
		for _, t := range tables {
			for _, c := range t.cols {
				if c.kind == colLong {
					out = append(out, columnRef{table: t.name, col: c})
				}
			}
		}
	}
	return out
}

// migrateV5 把全部长文本列重定义为 dialect 认定的长文本类型（MySQL TEXT →
// MEDIUMTEXT）。
//
// 起因是真机事故：一次 47 命名空间 / 533 Pod 的巡检结果写进会话消息后，
// sessions.data 超过 MySQL TEXT 的 64KB 上限，落库直接 Error 1406，整轮对话记录
// 丢失。SQLite 的 TEXT 没有长度上限，modifyColumn 返回空串，本迁移在 SQLite 上是
// 空操作；MySQL 侧重复 MODIFY 同类型列只是重建，可安全重跑。
func migrateV5(ctx context.Context, tx *sql.Tx, d dialect) error {
	for _, ref := range longTextColumns() {
		q := d.modifyColumn(ref.table, ref.col)
		if q == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("widen column %s.%s: %w", ref.table, ref.col.name, err)
		}
	}
	return nil
}

// backfillAuditEventType 给历史审计行补 event_type（只处理空值，可重复执行）。
// migrateV6 建对象元数据三表与索引、给 sessions 补两列，并把二进制列放宽到
// dialect 认定的类型（MySQL BLOB → MEDIUMBLOB）。
//
// 补列与唯一索引都必须幂等：MySQL 不支持 IF NOT EXISTS，靠 isDuplicate*Err 容忍。
// 这里刻意不做「把已有正文从 dir 目录搬进库」之类的数据搬迁——v6 只加结构，混合后端
// 的读取路由由 objects.driver 承担（§10）。
func migrateV6(ctx context.Context, tx *sql.Tx, d dialect) error {
	for _, t := range schemaV6 {
		if _, err := tx.ExecContext(ctx, d.createTable(t)); err != nil {
			return fmt.Errorf("create table %s: %w", t.name, err)
		}
	}
	for _, idx := range schemaV6Indexes {
		q := d.createIndex(idx.name, idx.table, idx.cols)
		if idx.unique {
			q = d.createUniqueIndex(idx.name, idx.table, idx.cols)
		}
		if _, err := tx.ExecContext(ctx, q); err != nil {
			if d.isDuplicateIndexErr(err) {
				continue
			}
			return fmt.Errorf("create index %s: %w", idx.name, err)
		}
	}
	for _, c := range schemaV6SessionColumns {
		if _, err := tx.ExecContext(ctx, d.addColumn("sessions", c)); err != nil {
			if d.isDuplicateColumnErr(err) {
				continue
			}
			return fmt.Errorf("add column sessions.%s: %w", c.name, err)
		}
	}
	for _, ref := range bytesColumns() {
		q := d.modifyColumn(ref.table, ref.col)
		if q == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("widen column %s.%s: %w", ref.table, ref.col.name, err)
		}
	}
	return nil
}

// bytesColumns 从各版本 schema 派生二进制列（colBytes）清单，与 longTextColumns 同
// 一套单源派生思路：新增二进制列自动进入放宽范围，不会「加了列忘了放宽」。MySQL 的
// BLOB 只有 64KB，正文一旦按 BLOB 建出来就会在写入时报 Data too long。
func bytesColumns() []columnRef {
	var out []columnRef
	for _, tables := range [][]tableSpec{schemaV1, schemaV2, schemaV4, schemaV6} {
		for _, t := range tables {
			for _, c := range t.cols {
				if c.kind == colBytes {
					out = append(out, columnRef{table: t.name, col: c})
				}
			}
		}
	}
	return out
}


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
