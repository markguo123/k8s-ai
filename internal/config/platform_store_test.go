package config

import (
	"strings"
	"testing"
)

func TestPlatformStoreDefaults(t *testing.T) {
	cfg, err := Load(LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Platform.Server.StaticEnabled {
		t.Error("platform.server.static_enabled 默认应为 true（形态 A 同端口托管前端）")
	}
	if cfg.Platform.Store.Driver != StoreDriverSQLite || cfg.Platform.Store.IsMySQL() {
		t.Errorf("platform.store 默认应为 sqlite, got %+v", cfg.Platform.Store)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("默认配置应合法: %v", err)
	}
}

func TestPlatformStoreDriverValidation(t *testing.T) {
	base := func(t *testing.T) *Config {
		t.Helper()
		cfg, err := Load(LoadOptions{NoEnv: true})
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	t.Run("sqlite 缺路径", func(t *testing.T) {
		cfg := base(t)
		cfg.Platform.Store.SQLitePath = "  "
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "sqlite_path") {
			t.Fatalf("sqlite 驱动必须要求 sqlite_path, got %v", err)
		}
	})

	t.Run("未知驱动", func(t *testing.T) {
		cfg := base(t)
		cfg.Platform.Store.Driver = "postgres"
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "platform.store.driver") {
			t.Fatalf("未支持驱动必须报错, got %v", err)
		}
	})

	mysqlCases := []struct {
		name    string
		dsn     string
		wantErr bool
	}{
		{"空 DSN", "", true},
		{"缺库名", "user:pass@tcp(host:3306)/?parseTime=true", true},
		{"无参数段", "user:pass@tcp(host:3306)/k8s_ai", true},
		{"parseTime 非 true", "user:pass@tcp(host:3306)/k8s_ai?parseTime=false", true},
		{"缺少 parseTime", "user:pass@tcp(host:3306)/k8s_ai?charset=utf8mb4", true},
		{"合法 DSN", "user:pass@tcp(host:3306)/k8s_ai?charset=utf8mb4&parseTime=true", false},
		{"合法 DSN 大写参数", "user:pass@tcp(host:3306)/k8s_ai?ParseTime=TRUE", false},
	}
	for _, tc := range mysqlCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base(t)
			cfg.Platform.Store.Driver = StoreDriverMySQL
			cfg.Platform.Store.DSN = tc.dsn
			err := cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("DSN %q 应被拒绝", tc.dsn)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("DSN %q 应合法, got %v", tc.dsn, err)
			}
		})
	}

	t.Run("mysql 不再要求 sqlite_path", func(t *testing.T) {
		cfg := base(t)
		cfg.Platform.Store.Driver = StoreDriverMySQL
		cfg.Platform.Store.DSN = "user:pass@tcp(host:3306)/k8s_ai?parseTime=true"
		cfg.Platform.Store.SQLitePath = ""
		if err := cfg.Validate(); err != nil {
			t.Fatalf("走 MySQL 时不应再要求 sqlite_path, got %v", err)
		}
	})

	t.Run("IsMySQL 大小写与空白", func(t *testing.T) {
		if !(PlatformStoreConfig{Driver: " MySQL "}).IsMySQL() {
			t.Error("IsMySQL 应忽略大小写与空白")
		}
		if (PlatformStoreConfig{Driver: ""}).IsMySQL() {
			t.Error("空驱动应按 sqlite 处理")
		}
	})
}
