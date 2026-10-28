package store

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/session"
)

// mysqlTextLimit 是 MySQL TEXT 的字节上限。真机上会话落库就是撞在这条线上：
// Error 1406 (22001): Data too long for column 'data'。
const mysqlTextLimit = 65535

// TestMySQLLongTextColumnsRenderMediumText 锁住长文本列的类型口径：JSON 承载列一律
// 用 MEDIUMTEXT，且不带 DEFAULT（MySQL 8.0.13 前 TEXT 家族不能有默认值，业务侧
// 始终显式写入）。sessions.data 单独确认在清单内，防止列名被改后悄悄漏出覆盖范围。
func TestMySQLLongTextColumnsRenderMediumText(t *testing.T) {
	refs := longTextColumns()
	if len(refs) == 0 {
		t.Fatal("schema 里应至少有一个长文本列")
	}
	sessionsData := 0
	for _, ref := range refs {
		ddl := renderMySQLColumn(ref.col)
		if !strings.Contains(ddl, "MEDIUMTEXT NOT NULL") {
			t.Fatalf("长文本列 %s.%s 应渲染为 MEDIUMTEXT NOT NULL，实得 %s", ref.table, ref.col.name, ddl)
		}
		if strings.Contains(ddl, "DEFAULT") {
			t.Fatalf("长文本列 %s.%s 不应带 DEFAULT: %s", ref.table, ref.col.name, ddl)
		}
		if ref.table == "sessions" && ref.col.name == "data" {
			sessionsData++
		}
	}
	if sessionsData != 1 {
		t.Fatalf("sessions.data 必须且只能计入长文本列清单一次，实际 %d 次", sessionsData)
	}
}

// TestWidenLongTextColumnDDL 锁放宽语句本身：MySQL 走 MODIFY COLUMN，SQLite 空操作
// （TEXT 无长度上限）。迁移步骤靠"空串即跳过"工作，语义不能变。
func TestWidenLongTextColumnDDL(t *testing.T) {
	data := column{name: "data", kind: colLong, def: "'{}'"}
	got := mysqlDialect{}.modifyColumn("sessions", data)
	want := "ALTER TABLE sessions MODIFY COLUMN `data` MEDIUMTEXT NOT NULL;"
	if got != want {
		t.Fatalf("modifyColumn = %s, want %s", got, want)
	}
	sq := sqliteDialect{}.modifyColumn("sessions", data)
	if sq != "" {
		t.Fatalf("SQLite 无需放宽长文本列，实得 %q", sq)
	}
}

// TestMigrateV5SkippedOnSQLite 确认 SQLite 上迁移可执行且确实不产生任何 DDL 副作用
// （会话列仍是 TEXT）。真正的建表口径由 newTestStore 的完整迁移覆盖。
func TestMigrateV5SkippedOnSQLite(t *testing.T) {
	st := newTestStore(t)
	db := st.(*dbStore)
	var ddl string
	err := db.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='sessions'`).Scan(&ddl)
	if err != nil {
		t.Fatalf("读取 sessions 建表语句: %v", err)
	}
	if !strings.Contains(ddl, "`data` TEXT NOT NULL") {
		t.Fatalf("SQLite 的 sessions.data 应保持 TEXT: %s", ddl)
	}
	if strings.Contains(ddl, "MEDIUMTEXT") {
		t.Fatalf("SQLite 不应出现 MySQL 专有类型: %s", ddl)
	}
}

// TestSessionDataOverMySQLTextLimitRoundTrips 复现真机事故的数据体量：一次全集群巡检
// 把大量工具结果写进会话消息，序列化后远超 MySQL TEXT 的 64KB 上限。Store 必须整条
// 写入、整条读回——既不能保存失败（当时整轮对话记录丢失），也不能静默截断。
func TestSessionDataOverMySQLTextLimitRoundTrips(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	db := st.(*dbStore)

	// 模拟巡检输出：约 200KB 的 Pod 清单，形态接近真实 kubectl/scan 结果。
	var b strings.Builder
	for i := 0; b.Len() < 200*1024; i++ {
		b.WriteString("namespace-" + strconv.Itoa(i%47) + "/pod-" + strconv.Itoa(i) + " 1/1 Running 0 3d\n")
	}
	bulk := b.String()

	sess := &session.Session{ID: "big-scan-1", UserID: "u1", Name: "全集群巡检"}
	sess.AddMessage(session.Message{Role: "user", Type: "text", Content: "巡检整个集群"})
	sess.AddMessage(session.Message{
		Role: "tool", Type: "tool-call-response", Name: "scan_cluster",
		Content: bulk, Data: map[string]any{"report": bulk},
	})
	if err := st.Sessions().Create(ctx, sess); err != nil {
		t.Fatalf("create session: %v", err)
	}

	var raw string
	if err := db.db.QueryRowContext(ctx, `SELECT data FROM sessions WHERE id = ?`, "big-scan-1").Scan(&raw); err != nil {
		t.Fatalf("读原始 data 列: %v", err)
	}
	if len(raw) <= mysqlTextLimit {
		t.Fatalf("用例体量不足：data 列只有 %d 字节，未越过 MySQL TEXT 上限", len(raw))
	}

	got, err := st.Sessions().Get(ctx, "big-scan-1")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("消息条数 = %d, want 2", len(got.Messages))
	}
	if got.Messages[1].Content != bulk {
		t.Fatalf("工具正文被截断：落库 %d 字节，读回 %d 字节", len(bulk), len(got.Messages[1].Content))
	}
	data, ok := got.Messages[1].Data.(map[string]any)
	if !ok || data["report"] != bulk {
		t.Fatalf("工具结构化结果未原样读回")
	}
}
