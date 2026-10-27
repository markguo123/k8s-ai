package store

import (
	"database/sql"
	"fmt"

	// 同时注册驱动与供 RedactDSN 解析连接串。
	"github.com/go-sql-driver/mysql"
)

// OpenMySQL 打开 MySQL 数据库并返回 Store（D-04 可选驱动）。业务与迁移逻辑
// 与 SQLite 完全复用，仅 DDL/upsert 由 mysqlDialect 渲染。
//
// DSN 示例（必须带 parseTime=true 才能把 TIMESTAMP 扫描为 time.Time）：
//
//	user:pass@tcp(127.0.0.1:3306)/k8s_ai?parseTime=true&charset=utf8mb4&collation=utf8mb4_unicode_ci
func OpenMySQL(dsn string) (Store, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(0)
	s := newDBStore(db, mysqlDialect{})
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	return s, nil
}

// RedactDSN 返回可以安全写进日志的 MySQL 连接串（P6.1）：只保留用户、网络地址
// 与库名，口令一律不落日志。解析失败时返回固定占位，绝不回显原始串。
func RedactDSN(dsn string) string {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "<unparseable mysql dsn>"
	}
	net := cfg.Net
	if net == "" {
		net = "tcp"
	}
	return fmt.Sprintf("%s@%s(%s)/%s", cfg.User, net, cfg.Addr, cfg.DBName)
}
