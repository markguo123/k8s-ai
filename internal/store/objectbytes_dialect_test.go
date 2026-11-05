package store

import (
	"strings"
	"testing"
)

// TestBytesColumnRendersWithoutDefault 钉住二进制列的渲染口径：SQLite 用 BLOB、MySQL 用
// MEDIUMBLOB（BLOB 只有 64KB，装不下 32MiB 的对象正文，方向必须是放大而不是截断），
// 且两方言都不带 DEFAULT、可空列不带 NOT NULL。
//
// MySQL 那条尤其要紧：8.0.13 之前 BLOB 带默认值直接 1101 建表失败；而 NOT NULL 会把
// dir/s3 后端永远写不进来的 payload 变成插入必填项。
func TestBytesColumnRendersWithoutDefault(t *testing.T) {
	var objects *tableSpec
	for i := range schemaV6 {
		if schemaV6[i].name == "objects" {
			objects = &schemaV6[i]
		}
	}
	if objects == nil {
		t.Fatal("schemaV6 里没有 objects 表")
	}

	sqliteDDL := sqliteDialect{}.createTable(*objects)
	mysqlDDL := mysqlDialect{}.createTable(*objects)
	for name, ddl := range map[string]string{"sqlite": sqliteDDL, "mysql": mysqlDDL} {
		line := payloadColumnLine(t, ddl)
		if strings.Contains(line, "DEFAULT") {
			t.Errorf("%s 二进制列带 DEFAULT，低版本 MySQL 会 1101：%s", name, line)
		}
		if strings.Contains(line, "NOT NULL") {
			t.Errorf("%s payload 必须可空（dir/s3 后端恒 NULL）：%s", name, line)
		}
		if strings.Contains(line, "CHARSET") || strings.Contains(line, "CHARACTER SET") {
			t.Errorf("%s 二进制列不得带字符集：%s", name, line)
		}
	}
	if !strings.Contains(sqliteDDL, "`payload` BLOB") {
		t.Errorf("sqlite 应渲染 BLOB：%s", sqliteDDL)
	}
	if !strings.Contains(mysqlDDL, "`payload` MEDIUMBLOB") {
		t.Errorf("mysql 应渲染 MEDIUMBLOB：%s", mysqlDDL)
	}
}

// TestUniqueIndexRendersOnBothDialects 覆盖 attachments 幂等锚点的 DDL：MySQL 不支持
// IF NOT EXISTS，重复执行由 isDuplicateIndexErr 容忍，两方言都不能把 UNIQUE 丢了。
func TestUniqueIndexRendersOnBothDialects(t *testing.T) {
	var uk *indexSpec
	for i := range schemaV6Indexes {
		if schemaV6Indexes[i].unique {
			uk = &schemaV6Indexes[i]
		}
	}
	if uk == nil {
		t.Fatal("schemaV6 应有一个唯一索引（附件幂等锚点）")
	}
	if uk.table != "attachments" || !strings.Contains(uk.cols, "session_id") {
		t.Fatalf("唯一索引应建在 attachments(session_id,...) 上：%+v", uk)
	}
	sqlite := sqliteDialect{}.createUniqueIndex(uk.name, uk.table, uk.cols)
	if !strings.HasPrefix(sqlite, "CREATE UNIQUE INDEX IF NOT EXISTS ") {
		t.Errorf("sqlite 唯一索引缺少 IF NOT EXISTS：%s", sqlite)
	}
	mysql := mysqlDialect{}.createUniqueIndex(uk.name, uk.table, uk.cols)
	if !strings.HasPrefix(mysql, "CREATE UNIQUE INDEX ") || strings.Contains(mysql, "IF NOT EXISTS") {
		t.Errorf("mysql 唯一索引渲染不当：%s", mysql)
	}
	if (mysqlDialect{}).isDuplicateIndexErr(nil) {
		t.Error("nil 错误不该判成重复索引")
	}
	// 普通索引不能顺手变成唯一索引：objects.state 等索引天然有重复值。
	if strings.Contains(sqliteDialect{}.createIndex("idx", "objects", "state, deleted_at"), "UNIQUE") {
		t.Error("createIndex 不得渲染 UNIQUE")
	}
}

// TestBytesColumnsIncludesObjectPayload 守住迁移的「放宽列」清单：colBytes 加入 schema 后
// 如果没进 bytesColumns()，MySQL 上老库升级就不会跑 MODIFY，payload 会停在 BLOB(64KB)
// 上，直到第一次上传大对象才炸——那是最糟的失败时机。
func TestBytesColumnsIncludesObjectPayload(t *testing.T) {
	refs := bytesColumns()
	found := false
	for _, ref := range refs {
		if ref.table == "objects" && ref.col.name == "payload" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("bytesColumns() 缺少 objects.payload：%+v", refs)
	}
	d := mysqlDialect{}
	for _, ref := range refs {
		c := ref.col
		if c.kind != colBytes {
			t.Fatalf("%s.%s 不是二进制列", ref.table, c.name)
		}
		if ddl := d.modifyColumn(ref.table, c); !strings.Contains(ddl, "MEDIUMBLOB") {
			t.Fatalf("放宽语句未渲染 MEDIUMBLOB：%s", ddl)
		}
	}
}

func payloadColumnLine(t *testing.T, ddl string) string {
	t.Helper()
	for _, line := range strings.Split(ddl, "\n") {
		if strings.Contains(line, "`payload`") {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("建表语句里没有 payload 列：%s", ddl)
	return ""
}
