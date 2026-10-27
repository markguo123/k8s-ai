package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// openPlatformStore 按 platform.store.driver 打开持久化层（P6.1 / D-31）。
//
// 主路径是 mysql（DSN 来自 platform.store.dsn，口令经 Secret 注入）；sqlite 是
// 单机与离线兜底，此时 sqlite_path 必须落在持久卷上。产品不会 CREATE DATABASE，
// 库与字符集由 DBA 预建。返回的 target 是可直接写日志的安全摘要（不含口令）。
func openPlatformStore(cfg *config.Config) (store.Store, string, error) {
	if cfg == nil {
		return nil, "", fmt.Errorf("config not loaded")
	}
	sc := cfg.Platform.Store
	if sc.IsMySQL() {
		// 先算安全目标再连接：连接失败时错误也要能指名目标库，且不带口令。
		target := mysqlTarget(sc.DSN)
		st, err := store.OpenMySQL(sc.DSN)
		if err != nil {
			return nil, "", fmt.Errorf("open store %s: %w", target, err)
		}
		return st, target, nil
	}
	path, err := expandHomePath(sc.SQLitePath)
	if err != nil {
		return nil, "", err
	}
	if err := ensureSQLiteDir(path); err != nil {
		return nil, "", err
	}
	st, err := store.OpenSQLite(path)
	if err != nil {
		return nil, "", fmt.Errorf("open store: %w", err)
	}
	return st, "sqlite:" + path, nil
}

// mysqlTarget 把 DSN 收敛成可进日志/命令输出的目标摘要（口令已脱敏）。
func mysqlTarget(dsn string) string {
	return "mysql:" + store.RedactDSN(dsn)
}

// ensureSQLiteDir 为普通文件路径预建父目录；URI/内存库形式交给驱动处理。
func ensureSQLiteDir(path string) error {
	if path == "" || strings.HasPrefix(path, ":") || strings.HasPrefix(path, "file:") || strings.Contains(path, "?") {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create store dir: %w", err)
	}
	return nil
}

// expandHomePath 展开 "~/..." 形式的相对路径（sqlite 兜底路径用）。
func expandHomePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("store path must not be empty")
	}
	if path == "~" || !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, path[2:]), nil
}
