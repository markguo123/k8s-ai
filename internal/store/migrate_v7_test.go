package store

import (
	"context"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/session"
)

// insertLegacySession 按 v1 的十条列写一行「旧式」会话：镜像列留在建列时的默认值
// （pinned=0 / message_count=0 / title=''），正文里也没有 messageCount/title 字段，
// 用来模拟 P8.6 之前就在库里的那些行。
func insertLegacySession(t *testing.T, st Store, id, data string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	_, err := rawDB(t, st).Exec(`
		INSERT INTO sessions (id, user_id, name, cluster_id, namespace, model, state, data, created_at, last_modified)
		VALUES (?, ?, '', '', 'default', '', '', ?, ?, ?)`, id, "u1", data, now, now)
	if err != nil {
		t.Fatalf("insert legacy session %s: %v", id, err)
	}
}

// TestMigrateV7BackfillsSessionIndexColumns 是 P8.6 的升级路径兜底：列表页改读镜像列之后，
// 老会话如果没被回填，Web 上所有历史会话都会变成「0 条消息 / 新会话」，而库里正文其实
// 完好——这种数据不会报错，只会静默变丑，必须靠用例钉住。
//
// 做法是插一行旧式会话、抹掉 version=7 的迁移记录再跑一次 Migrate，正好复现
// 「列已存在 + 历史行待回填」这条现场升级路径（列已存在还要能整步重放，走的是
// isDuplicateColumnErr 分支）。
func TestMigrateV7BackfillsSessionIndexColumns(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	db := rawDB(t, st)

	insertLegacySession(t, st, "legacy-1", `{"id":"legacy-1","messages":[`+
		`{"role":"user","type":"text","content":"为什么 nginx-ingress 一直 0/1"},`+
		`{"role":"assistant","type":"text","content":"节点 NotReady"}],"pinned":true}`)
	// 坏正文一行：迁移不能被它卡住。
	insertLegacySession(t, st, "legacy-2", `{not json`)

	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = 7`); err != nil {
		t.Fatalf("reset v7 migration record: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("re-run migrate: %v", err)
	}

	var pinned, count, size int
	var title string
	if err := db.QueryRow(`SELECT pinned, message_count, title, data_size FROM sessions WHERE id = 'legacy-1'`).
		Scan(&pinned, &count, &title, &size); err != nil {
		t.Fatalf("scan legacy-1: %v", err)
	}
	if pinned != 1 || count != 2 || title != "为什么 nginx-ingress 一直 0/1" {
		t.Fatalf("回填结果 = pinned=%d count=%d title=%q", pinned, count, title)
	}
	// 按字节量：data_size 存的是序列化字节数，SQLite 的 LENGTH(text) 数的是字符数，
	// 正文里有中文，不转 BLOB 这两个数永远对不上。
	var dataLen int
	if err := db.QueryRow(`SELECT LENGTH(CAST(data AS BLOB)) FROM sessions WHERE id = 'legacy-1'`).Scan(&dataLen); err != nil {
		t.Fatalf("measure data: %v", err)
	}
	if size != dataLen {
		t.Fatalf("data_size = %d, want 正文长度 %d", size, dataLen)
	}

	// 坏行只跳过，不改库、不中断：它仍然在库里，等人工处理。
	if _, err := st.Sessions().Get(ctx, "legacy-2"); err == nil {
		t.Fatal("坏正文应当读不出来，但迁移不许为它失败")
	}

	// 幂等：再跑一次整库迁移，镜像值必须一模一样（重跑把计数翻倍就是数据损坏）。
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var count2, size2 int
	var title2 string
	if err := db.QueryRow(`SELECT message_count, title, data_size FROM sessions WHERE id = 'legacy-1'`).
		Scan(&count2, &title2, &size2); err != nil {
		t.Fatalf("rescan legacy-1: %v", err)
	}
	if count2 != count || title2 != title || size2 != size {
		t.Fatalf("重跑迁移改变了镜像列: %d/%q/%d -> %d/%q/%d", count, title, size, count2, title2, size2)
	}
}

// TestMigrateV7ServesFreshWrites 确认回填只是补历史债，新写入的镜像列由写入路径自己维护：
// 两条路算出来的标题必须一致，否则老会话和新会话在列表里会长得不一样。
func TestMigrateV7ServesFreshWrites(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	sess := &session.Session{ID: "v7-fresh", UserID: "u1"}
	sess.AddMessage(session.Message{Role: "user", Type: "text", Content: "回填后新会话标题"})
	if err := st.Sessions().Create(ctx, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	list, err := st.Sessions().List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, got := range list {
		if got.ID != "v7-fresh" {
			continue
		}
		if got.MessageCount != 1 || got.Title != "回填后新会话标题" {
			t.Fatalf("新写入的镜像列 = count=%d title=%q", got.MessageCount, got.Title)
		}
		return
	}
	t.Fatal("列表里没有新建的会话")
}
