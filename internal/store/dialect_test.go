package store

import (
	"strings"
	"testing"
)

func TestSQLiteDialectRendersSchema(t *testing.T) {
	d := sqliteDialect{}
	users := d.createTable(schemaV1[1])
	if !strings.Contains(users, "username TEXT NOT NULL UNIQUE") {
		t.Fatalf("sqlite users should use TEXT UNIQUE: %s", users)
	}
	if strings.Contains(users, "VARCHAR") {
		t.Fatalf("sqlite must not render VARCHAR: %s", users)
	}
	audit := d.createTable(schemaV1[3])
	if !strings.Contains(audit, "id INTEGER PRIMARY KEY AUTOINCREMENT") {
		t.Fatalf("sqlite audit id must autoincrement: %s", audit)
	}
	settings := d.createTable(schemaV1[4])
	if !strings.Contains(settings, "PRIMARY KEY (group_name, key_name)") {
		t.Fatalf("sqlite settings needs composite pk: %s", settings)
	}
	turns := d.createTable(schemaV2[0])
	if !strings.Contains(turns, "id TEXT NOT NULL PRIMARY KEY") {
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
	if !strings.Contains(users, "username VARCHAR(191) NOT NULL UNIQUE") {
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
	if !strings.Contains(audit, "id INTEGER AUTO_INCREMENT PRIMARY KEY") {
		t.Fatalf("mysql audit id should auto_increment: %s", audit)
	}
	settings := d.createTable(schemaV1[4])
	if !strings.Contains(settings, "PRIMARY KEY (group_name, key_name)") {
		t.Fatalf("mysql settings needs composite pk: %s", settings)
	}
	if !strings.Contains(settings, "sensitive TINYINT(1) NOT NULL DEFAULT 0") {
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

type errInexact string

func (e errInexact) Error() string { return string(e) }

var _ error = errInexact("")

// sqliteUniqueError 仅用于确保 isDuplicateIndexErr 对非 MySQL 错误不误判。
type sqliteUniqueError struct{}

func (sqliteUniqueError) Error() string { return "UNIQUE constraint failed: users.username" }
