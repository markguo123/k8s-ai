package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrationStatusWithoutMigrations 锁"迁移表还不存在"的口径：applied=0 且不报错，
// 让 /readyz 把它读成"迁移 Job 还没跑完"而不是 500。
func TestMigrationStatusWithoutMigrations(t *testing.T) {
	s, err := OpenSQLite("file:ready-test-" + randID() + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	applied, expected, err := s.MigrationStatus(ctx)
	if err != nil {
		t.Fatalf("未迁移时不应报错: %v", err)
	}
	if applied != 0 || expected != schemaVersion {
		t.Fatalf("版本读数异常: applied=%d expected=%d", applied, expected)
	}
	if err := IsReady(ctx, s); err == nil {
		t.Fatal("未迁移应判未就绪")
	} else {
		if !errors.Is(err, ErrNotReady) {
			t.Fatalf("应包装 ErrNotReady, got %v", err)
		}
		if !strings.Contains(err.Error(), "db migrate") {
			t.Fatalf("未就绪原因应指向迁移入口, got %v", err)
		}
	}

	// 迁移完成后就绪。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := IsReady(ctx, s); err != nil {
		t.Fatalf("迁移后应就绪: %v", err)
	}
}

// TestIsReadyFailures 锁另两条未就绪判据：连接断开与 Store 未装配。
func TestIsReadyFailures(t *testing.T) {
	ctx := context.Background()

	if err := IsReady(ctx, nil); err == nil || !errors.Is(err, ErrNotReady) {
		t.Fatalf("Store 未装配应判未就绪, got %v", err)
	}

	s := newTestStore(t)
	if err := IsReady(ctx, s); err != nil {
		t.Fatalf("基线应就绪: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := IsReady(ctx, s); err == nil {
		t.Fatal("连接断开应判未就绪")
	} else if !errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), "ping") {
		t.Fatalf("未就绪原因应指向连通性, got %v", err)
	}
}

// TestRedactDSN 锁日志/探针口径：连接串只保留 user@net(addr)/db，口令永不外泄。
func TestRedactDSN(t *testing.T) {
	got := RedactDSN("appuser:sup3rs3cret@tcp(db.internal:3306)/k8s_ai?charset=utf8mb4&parseTime=true")
	want := "appuser@tcp(db.internal:3306)/k8s_ai"
	if got != want {
		t.Fatalf("脱敏结果异常: got %q want %q", got, want)
	}
	if strings.Contains(got, "sup3rs3cret") {
		t.Fatal("DSN 脱敏后仍含口令")
	}
	if bad := RedactDSN("not-a-dsn"); !strings.Contains(bad, "unparseable") {
		t.Fatalf("非法 DSN 也应安全占位, got %q", bad)
	}
}

// TestOpenSQLiteUnusableDataPath 锁 PVC 挂错/不可写的负向口径（P6.1）：数据目录不可用时
// 必须在打开阶段失败，最迟也要被 IsReady 判未就绪，不能"连上了就接活"。
// 覆盖两种现场常见形态：挂载路径拼错导致父目录不存在、挂上的路径本身是目录。
func TestOpenSQLiteUnusableDataPath(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"父目录不存在", filepath.Join(t.TempDir(), "pvc-missing", "k8s-ai.db")},
		{"数据路径是目录", t.TempDir()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := OpenSQLite(tc.path)
			if err != nil {
				// 打开即失败：启动阶段拒绝装配，符合预期。
				return
			}
			defer s.Close()
			if err := IsReady(context.Background(), s); err == nil {
				t.Fatalf("数据路径不可用却判就绪: %s", tc.path)
			} else if !errors.Is(err, ErrNotReady) {
				t.Fatalf("应包装 ErrNotReady, got %v", err)
			}
		})
	}
}
