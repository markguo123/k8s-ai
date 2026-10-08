package store

import (
	"context"
	"os"
	"testing"
)

// TestStoresMySQL 是对 MySQL 驱动的集成验证：通过环境变量
// K8S_AI_TEST_MYSQL_DSN 传入 DSN（需 parseTime=true），未设置时跳过。
//
// 示例：
//
//	$env:K8S_AI_TEST_MYSQL_DSN="root:pass@tcp(127.0.0.1:3306)/k8s_ai_test?parseTime=true"
//	go test ./internal/store/ -run TestStoresMySQL -v
func TestStoresMySQL(t *testing.T) {
	dsn := os.Getenv("K8S_AI_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("K8S_AI_TEST_MYSQL_DSN not set; skipping MySQL integration test")
	}
	s, err := OpenMySQL(dsn)
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	// 迁移幂等：连续执行两次，仅记录一条迁移。
	for i := 0; i < 2; i++ {
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("migrate mysql pass %d: %v", i, err)
		}
	}
	runAllStoreCRUD(t, s)
}
