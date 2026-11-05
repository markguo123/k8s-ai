package store

import (
	"context"
	"testing"
)

// TestMigrateV6ReappliesCleanly 覆盖「迁移跑到一半被杀、运维重跑」这条真实路径：
// migrateV6 在已经建好三表的库上必须整步重放成功——表走 IF NOT EXISTS，补列靠
// isDuplicateColumnErr 容忍（SQLite 报 "duplicate column name"），重复索引走
// IF NOT EXISTS。整步重跑失败的话，现场只会看到一个起不来的 Pod 和一句迁移错误。
//
// 只断言 SQLite：MySQL 的 1060/1061 错误识别由 dialect 用例锁定，整步重放要走
// K8S_AI_TEST_MYSQL_DSN 真机门禁（与既有 MySQL 集成用例同一入口）。
func TestMigrateV6ReappliesCleanly(t *testing.T) {
	s, err := OpenSQLite("file:migrate-v6-" + randID() + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db := s.(*dbStore).db
	for pass := 1; pass <= 2; pass++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin pass %d: %v", pass, err)
		}
		if err := migrateV6(ctx, tx, sqliteDialect{}); err != nil {
			_ = tx.Rollback()
			t.Fatalf("re-apply migrateV6 pass %d: %v", pass, err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("rollback pass %d: %v", pass, err)
		}
	}
	// 重放不许动已登记的行：objects 仍为空即可，真正的「不重置引用」由
	// TestObjectRegisterDoesNotResetReferences 守住。
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM objects`).Scan(&n); err != nil {
		t.Fatalf("count objects: %v", err)
	}
	if n != 0 {
		t.Fatalf("重放 migrateV6 后 objects 应为空，实际 %d 行", n)
	}
}
