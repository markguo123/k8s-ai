package store

import (
	"database/sql"
	"fmt"

	_ "github.com/go-sql-driver/mysql"
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
