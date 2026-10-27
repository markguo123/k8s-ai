package store

import (
	"fmt"
	"strings"
)

// colKind 描述统一 schema 中一列的逻辑类型。SQLite/MySQL 据此渲染各自 DDL。
type colKind int

const (
	// colText 短文本：可作主键/唯一键/索引列（SQLite TEXT，MySQL VARCHAR(191)）。
	colText colKind = iota
	// colLong 长文本：不可建索引，常存放 JSON（SQLite/MySQL 均为 TEXT）。
	colLong
	// colInt 整数。
	colInt
	// colBool 布尔：以 0/1 存储（SQLite INTEGER，MySQL TINYINT(1)）。
	colBool
	// colTime 必填时间列。
	colTime
	// colTimeOrNull 可空时间列。
	colTimeOrNull
	// colTextMedium 中等长度文本：不建索引，可安全 ALTER 追加（SQLite TEXT，
	// MySQL VARCHAR(1024)）。用于审计摘要这类需要补列且必须带 DEFAULT 的字段。
	colTextMedium
)

// column 描述统一 schema 的一列。
type column struct {
	name string
	kind colKind
	// def 是 DEFAULT 字面量（含引号），空表示不加默认值。
	def string
	// autoIncrement 仅用于单列自增主键（如 audit_events.id）。
	autoIncrement bool
	unique        bool
	primary       bool
}

// indexSpec 描述一张表上的一个二级索引。
type indexSpec struct {
	name  string
	table string
	cols  string
}

// tableSpec 描述一张表的统一结构，由 dialect 渲染为 SQLite/MySQL DDL。
type tableSpec struct {
	name       string
	cols       []column
	primaryKey []string // 复合主键列；非空时在表级声明
}

// dialect 封装 SQLite/MySQL 在 DDL 与 upsert 上的语法差异。业务 CRUD 与
// 统一迁移都只面向该抽象（D-04）。
type dialect interface {
	name() string
	// createTable 渲染"不存在则建表"语句。
	createTable(t tableSpec) string
	// createIndex 渲染建索引语句（SQLite 支持 IF NOT EXISTS，MySQL 不支持）。
	createIndex(name, table, cols string) string
	// upsert 渲染单行插入或更新（SQLite ON CONFLICT / MySQL ON DUPLICATE KEY）。
	upsert(table string, cols, conflictCols, updateCols []string) string
	// isDuplicateIndexErr 判断"索引已存在"错误（MySQL 幂等迁移需忽略）。
	isDuplicateIndexErr(err error) bool
	// addColumn 渲染补列语句。只允许追加带常量默认值的非空列，
	// 保证 SQLite（不支持非常量默认值）与 MySQL 都能幂等执行。
	addColumn(table string, c column) string
	// isDuplicateColumnErr 判断"列已存在"错误（补列幂等需忽略）。
	isDuplicateColumnErr(err error) bool
}

// sqliteDialect 是 SQLite 的 SQL 方言。
type sqliteDialect struct{}

func (sqliteDialect) name() string { return "sqlite" }

func (d sqliteDialect) createTable(t tableSpec) string {
	defs := make([]string, 0, len(t.cols)+1)
	for _, c := range t.cols {
		defs = append(defs, "  "+renderSQLiteColumn(c))
	}
	if len(t.primaryKey) > 0 {
		defs = append(defs, "  PRIMARY KEY ("+strings.Join(quoteIdents(t.primaryKey), ", ")+")")
	}
	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n%s\n);", t.name, strings.Join(defs, ",\n"))
}

func (sqliteDialect) createIndex(name, table, cols string) string {
	return fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (%s);", name, table, cols)
}

// upsert 生成 SQLite 的 ON CONFLICT(cols) DO UPDATE SET col=excluded.col。
func (sqliteDialect) upsert(table string, cols, conflictCols, updateCols []string) string {
	placeholders := make([]string, len(cols))
	for i := range cols {
		placeholders[i] = "?"
	}
	assigns := make([]string, len(updateCols))
	for i, c := range updateCols {
		assigns[i] = c + "=excluded." + c
	}
	return fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) ON CONFLICT(%s) DO UPDATE SET %s",
		table, strings.Join(cols, ", "), strings.Join(placeholders, ", "),
		strings.Join(conflictCols, ", "), strings.Join(assigns, ", "))
}

func (sqliteDialect) isDuplicateIndexErr(error) bool { return false }

func (sqliteDialect) addColumn(table string, c column) string {
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;", table, renderSQLiteColumn(c))
}

// SQLite 的重复补列错误文本为 "duplicate column name: x"。
func (sqliteDialect) isDuplicateColumnErr(err error) bool {
	if err == nil {
		return false
	}
	return containsFold(err.Error(), "duplicate column name")
}

// quoteIdent 用反引号包裹标识符。SQLite 和 MySQL 都接受反引号形式的标识符引用，
// 统一加引号后列名不再受 MySQL 保留字表影响：settings.sensitive 在 MySQL 8.0 是
// 保留字，裸写会让建表直接 1064（真机集成测试 TestStoresMySQL 覆盖）。
func quoteIdent(name string) string { return "`" + name + "`" }

// quoteIdents 批量包裹标识符，用于表级主键声明。
func quoteIdents(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = quoteIdent(n)
	}
	return out
}

func renderSQLiteColumn(c column) string {
	var typ string
	switch c.kind {
	case colText, colLong:
		typ = "TEXT"
	case colTextMedium:
		typ = "TEXT"
	case colInt, colBool:
		typ = "INTEGER"
	case colTime, colTimeOrNull:
		typ = "TIMESTAMP"
	}
	s := quoteIdent(c.name) + " " + typ
	if c.autoIncrement {
		return s + " PRIMARY KEY AUTOINCREMENT"
	}
	if c.kind != colTimeOrNull {
		s += " NOT NULL"
	}
	if c.unique {
		s += " UNIQUE"
	}
	if c.primary {
		s += " PRIMARY KEY"
	}
	if c.def != "" {
		s += " DEFAULT " + c.def
	}
	return s
}

// mysqlDialect 是 MySQL 的 SQL 方言。
type mysqlDialect struct{}

func (mysqlDialect) name() string { return "mysql" }

func (d mysqlDialect) createTable(t tableSpec) string {
	defs := make([]string, 0, len(t.cols)+1)
	for _, c := range t.cols {
		defs = append(defs, "  "+renderMySQLColumn(c))
	}
	if len(t.primaryKey) > 0 {
		defs = append(defs, "  PRIMARY KEY ("+strings.Join(quoteIdents(t.primaryKey), ", ")+")")
	}
	return fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (\n%s\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;",
		t.name, strings.Join(defs, ",\n"))
}

// MySQL 不支持 CREATE INDEX IF NOT EXISTS，依赖 caller 用
// isDuplicateIndexErr 容忍重复索引（幂等迁移）。
func (mysqlDialect) createIndex(name, table, cols string) string {
	return fmt.Sprintf("CREATE INDEX %s ON %s (%s);", name, table, cols)
}

// upsert 生成 MySQL 的 ON DUPLICATE KEY UPDATE col=VALUES(col)。
func (mysqlDialect) upsert(table string, cols, conflictCols, updateCols []string) string {
	placeholders := make([]string, len(cols))
	for i := range cols {
		placeholders[i] = "?"
	}
	assigns := make([]string, len(updateCols))
	for i, c := range updateCols {
		assigns[i] = c + "=VALUES(" + c + ")"
	}
	return fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) ON DUPLICATE KEY UPDATE %s",
		table, strings.Join(cols, ", "), strings.Join(placeholders, ", "),
		strings.Join(assigns, ", "))
}

func (mysqlDialect) isDuplicateIndexErr(err error) bool {
	if err == nil {
		return false
	}
	return containsFold(err.Error(), "duplicate key name")
}

func (mysqlDialect) addColumn(table string, c column) string {
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;", table, renderMySQLColumn(c))
}

// MySQL 的重复补列错误文本为 "Duplicate column name 'x'"（NO.1060）。
func (mysqlDialect) isDuplicateColumnErr(err error) bool {
	if err == nil {
		return false
	}
	return containsFold(err.Error(), "duplicate column name")
}

func renderMySQLColumn(c column) string {
	var typ string
	switch c.kind {
	case colText:
		typ = "VARCHAR(191)"
	case colTextMedium:
		typ = "VARCHAR(1024)"
	case colLong:
		typ = "TEXT"
	case colInt:
		typ = "INTEGER"
	case colBool:
		typ = "TINYINT(1)"
	case colTime:
		// 带 3 位小数秒：MySQL 的 TIMESTAMP 默认精度是 0，会把 SQLite 能保留的
		// 毫秒直接截掉，同一秒内的 audit/turn/step 顺序就会退化（真机 MySQL 8.0.36
		// 集成测试里 expires_at 往返失败暴露）。两个引擎统一到毫秒精度。
		typ = "TIMESTAMP(3)"
	case colTimeOrNull:
		typ = "TIMESTAMP(3) NULL"
	}
	s := quoteIdent(c.name) + " " + typ
	if c.autoIncrement {
		return s + " AUTO_INCREMENT PRIMARY KEY"
	}
	if c.kind != colTimeOrNull {
		s += " NOT NULL"
	}
	if c.unique {
		s += " UNIQUE"
	}
	if c.primary {
		s += " PRIMARY KEY"
	}
	// MySQL 8.0.13 才支持 TEXT 默认值；长文本列业务侧始终显式写入，故略去默认值。
	if c.def != "" && c.kind != colLong {
		s += " DEFAULT " + c.def
	}
	return s
}
