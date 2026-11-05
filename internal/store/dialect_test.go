package store

import (
	"strings"
	"testing"
)

func TestSQLiteDialectRendersSchema(t *testing.T) {
	d := sqliteDialect{}
	users := d.createTable(schemaV1[1])
	if !strings.Contains(users, "`username` TEXT NOT NULL UNIQUE") {
		t.Fatalf("sqlite users should use TEXT UNIQUE: %s", users)
	}
	if strings.Contains(users, "VARCHAR") {
		t.Fatalf("sqlite must not render VARCHAR: %s", users)
	}
	audit := d.createTable(schemaV1[3])
	if !strings.Contains(audit, "`id` INTEGER PRIMARY KEY AUTOINCREMENT") {
		t.Fatalf("sqlite audit id must autoincrement: %s", audit)
	}
	settings := d.createTable(schemaV1[4])
	if !strings.Contains(settings, "PRIMARY KEY (`group_name`, `key_name`)") {
		t.Fatalf("sqlite settings needs composite pk: %s", settings)
	}
	turns := d.createTable(schemaV2[0])
	if !strings.Contains(turns, "`id` TEXT NOT NULL PRIMARY KEY") {
		t.Fatalf("sqlite turns id should be text primary: %s", turns)
	}
	if strings.Contains(turns, "VARCHAR") {
		t.Fatalf("sqlite must not render VARCHAR: %s", turns)
	}
	if !strings.Contains(d.createIndex("idx_x", "t", "c"), "CREATE INDEX IF NOT EXISTS ") {
		t.Fatalf("sqlite index should use IF NOT EXISTS")
	}
	upsert := d.upsert("settings",
		[]string{"a", "b", "c"}, []string{"a"}, []string{"b", "c"})
	if !strings.Contains(upsert, "ON CONFLICT(a) DO UPDATE SET b=excluded.b, c=excluded.c") {
		t.Fatalf("sqlite upsert wrong: %s", upsert)
	}
	if n := strings.Count(upsert, "?"); n != 3 {
		t.Fatalf("sqlite upsert placeholder count = %d, want 3: %s", n, upsert)
	}
}

func TestMySQLDialectRendersSchema(t *testing.T) {
	d := mysqlDialect{}
	users := d.createTable(schemaV1[1])
	if !strings.Contains(users, "`username` VARCHAR(191) NOT NULL UNIQUE") {
		t.Fatalf("mysql users should use VARCHAR unique: %s", users)
	}
	if !strings.Contains(users, "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4") {
		t.Fatalf("mysql table options missing: %s", users)
	}
	for _, tbl := range schemaV1 {
		ddl := d.createTable(tbl)
		if strings.Contains(ddl, "TEXT PRIMARY KEY") || strings.Contains(ddl, "TEXT NOT NULL UNIQUE") {
			t.Fatalf("mysql cannot index TEXT directly in %s: %s", tbl.name, ddl)
		}
	}
	for _, tbl := range schemaV2 {
		ddl := d.createTable(tbl)
		if strings.Contains(ddl, "TEXT PRIMARY KEY") || strings.Contains(ddl, "TEXT NOT NULL UNIQUE") {
			t.Fatalf("mysql cannot index TEXT directly in %s: %s", tbl.name, ddl)
		}
	}
	audit := d.createTable(schemaV1[3])
	if !strings.Contains(audit, "`id` INTEGER AUTO_INCREMENT PRIMARY KEY") {
		t.Fatalf("mysql audit id should auto_increment: %s", audit)
	}
	settings := d.createTable(schemaV1[4])
	if !strings.Contains(settings, "PRIMARY KEY (`group_name`, `key_name`)") {
		t.Fatalf("mysql settings needs composite pk: %s", settings)
	}
	if !strings.Contains(settings, "`sensitive` TINYINT(1) NOT NULL DEFAULT 0") {
		t.Fatalf("mysql bool column should be tinyint(1): %s", settings)
	}
	if strings.Contains(d.createIndex("idx_x", "t", "c"), "IF NOT EXISTS") {
		t.Fatalf("mysql index must not use IF NOT EXISTS")
	}
	if d.isDuplicateIndexErr(nil) || d.isDuplicateIndexErr(sqliteUniqueError{}) {
		t.Fatalf("mysql isDuplicateIndexErr must stay false for unrelated errors")
	}
	upsert := d.upsert("settings",
		[]string{"a", "b", "c"}, []string{"a"}, []string{"b", "c"})
	if !strings.Contains(upsert, "ON DUPLICATE KEY UPDATE b=VALUES(b), c=VALUES(c)") {
		t.Fatalf("mysql upsert wrong: %s", upsert)
	}
	if n := strings.Count(upsert, "?"); n != 3 {
		t.Fatalf("mysql upsert placeholder count = %d, want 3: %s", n, upsert)
	}
}

func TestMySQLDialectDetectsDuplicateKey(t *testing.T) {
	d := mysqlDialect{}
	if !d.isDuplicateIndexErr(errInexact("Error 1061 (42000): Duplicate key name 'idx_x'")) {
		t.Fatalf("should detect mysql duplicate key name")
	}
	if d.isDuplicateIndexErr(errInexact("no such index")) {
		t.Fatalf("should not match unrelated error")
	}
	if !isUniqueViolation(errInexact("Error 1062 (23000): Duplicate entry 'a' for key 'users.username'")) {
		t.Fatalf("should map mysql duplicate entry to ErrExists")
	}
	if !isUniqueViolation(errInexact("UNIQUE constraint failed: users.username")) {
		t.Fatalf("should map sqlite unique violation to ErrExists")
	}
}

// TestDialectQuotesColumnIdentifiers 锁住真机踩过的坑：MySQL 8.0.36 把 sensitive 当
// 保留字，裸列名建表直接 1064，整套 MySQL 迁移在第一步就断。DDL 因此一律用反引号
// 包裹标识符（SQLite 为兼容 MySQL 也接受反引号），CRUD SQL 里的 sensitive 走带引号的
// 列清单常量。这里全量扫一遍 schema，新增列忘引号会立刻红。
func TestDialectQuotesColumnIdentifiers(t *testing.T) {
	versions := [][]tableSpec{schemaV1, schemaV2, schemaV4}
	for _, d := range []dialect{sqliteDialect{}, mysqlDialect{}} {
		for _, tables := range versions {
			for _, tbl := range tables {
				ddl := d.createTable(tbl)
				for _, c := range tbl.cols {
					want := quoteIdent(c.name)
					if !strings.Contains(ddl, want) {
						t.Fatalf("%s 表 %s 的列 %s 未加引号（应含 %s）: %s", d.name(), tbl.name, c.name, want, ddl)
					}
				}
			}
		}
	}
	// CRUD 侧：settings 读写必须用带引号的 sensitive。
	if !strings.Contains(settingsColumns, settingsSensitiveCol) {
		t.Fatalf("settings 列清单未引用带引号的 sensitive: %s", settingsColumns)
	}
}

// TestMySQLTimeColumnsKeepMillisecondPrecision 锁 MySQL 时间列精度：TIMESTAMP 默认
// 精度 0 会截掉 SQLite 保留的毫秒，同一份 CRUD 契约在两个驱动上行为就不一致了
// （真机上 expires_at 毫秒丢失就是这么暴露的）。
func TestMySQLTimeColumnsKeepMillisecondPrecision(t *testing.T) {
	d := mysqlDialect{}
	for _, tables := range [][]tableSpec{schemaV1, schemaV2, schemaV4} {
		for _, tbl := range tables {
			ddl := d.createTable(tbl)
			for _, c := range tbl.cols {
				switch c.kind {
				case colTime:
					want := quoteIdent(c.name) + " TIMESTAMP(3) NOT NULL"
					if !strings.Contains(ddl, want) {
						t.Fatalf("表 %s 时间列应保留毫秒（期望含 %s）: %s", tbl.name, want, ddl)
					}
				case colTimeOrNull:
					want := quoteIdent(c.name) + " TIMESTAMP(3) NULL"
					if !strings.Contains(ddl, want) {
						t.Fatalf("表 %s 可空时间列应保留毫秒（期望含 %s）: %s", tbl.name, want, ddl)
					}
				}
			}
		}
	}
}

type errInexact string

func (e errInexact) Error() string { return string(e) }

var _ error = errInexact("")

// sqliteUniqueError 仅用于确保 isDuplicateIndexErr 对非 MySQL 错误不误判。
type sqliteUniqueError struct{}

func (sqliteUniqueError) Error() string { return "UNIQUE constraint failed: users.username" }
