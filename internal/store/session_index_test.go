package store

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/session"
)

// TestSessionListColumnsExcludeBody 是最便宜也最要紧的一条门禁：列表语句一旦被人把正文
// 列加回来，P8.6 省的是一次全表 MEDIUMTEXT 扫描，代价是「测试全绿但优化没了」——列表
// 结果看着完全正常，只有库体积上来之后才慢。这里逐列比对，`data_size` 含 data 子串，
// 所以只能按整列名判定，不能用 strings.Contains。
func TestSessionListColumnsExcludeBody(t *testing.T) {
	for _, col := range strings.Split(sessionListColumns, ",") {
		name := strings.TrimSpace(col)
		if name == "data" {
			t.Fatalf("列表读取列不得包含正文列 data: %s", sessionListColumns)
		}
	}
	for _, want := range []string{"pinned", "message_count", "title"} {
		if !strings.Contains(sessionListColumns, want) {
			t.Fatalf("列表读取列缺少镜像列 %s: %s", want, sessionListColumns)
		}
	}
}

// TestSessionListServesIndexColumnsWithoutBody 验证列表页真正拿到的东西：正文缺席，但
// 条数、标题、置顶三样镜像列照样正确——否则前端列表要么空一格，要么被迫逐条点详情。
func TestSessionListServesIndexColumnsWithoutBody(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	const sentinel = "SENTINEL-BODY-MUST-NOT-LEAK"

	first := &session.Session{ID: "idx-1", UserID: "u1", Namespace: "default"}
	first.AddMessage(session.Message{Role: "user", Type: "text", Content: "为什么 nginx 网关起不来\n补充：事件里有个 ImagePullBackOff"})
	first.AddMessage(session.Message{Role: "assistant", Type: "text", Content: sentinel})
	if err := st.Sessions().Create(ctx, first); err != nil {
		t.Fatalf("create idx-1: %v", err)
	}

	second := &session.Session{ID: "idx-2", UserID: "u1", Name: "手工改过名的会话"}
	second.AddMessage(session.Message{Role: "user", Type: "text", Content: "看看节点"})
	if err := st.Sessions().Create(ctx, second); err != nil {
		t.Fatalf("create idx-2: %v", err)
	}
	pinned, err := st.Sessions().Get(ctx, "idx-2")
	if err != nil {
		t.Fatalf("get idx-2: %v", err)
	}
	pinned.Pinned = true
	if err := st.Sessions().Update(ctx, pinned); err != nil {
		t.Fatalf("pin idx-2: %v", err)
	}

	list, err := st.Sessions().List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list = %+v", list)
	}
	if strings.Contains(fmt.Sprintf("%+v", list), sentinel) {
		t.Fatal("列表结果里出现了会话正文")
	}

	byID := map[string]*session.Session{}
	for _, s := range list {
		byID[s.ID] = s
		if len(s.Messages) != 0 {
			t.Fatalf("列表不该带正文: id=%s messages=%d", s.ID, len(s.Messages))
		}
	}
	if got := byID["idx-1"]; got.Title != "为什么 nginx 网关起不来" || got.MessageCount != 2 || got.Pinned {
		t.Fatalf("idx-1 镜像列 = title=%q count=%d pinned=%v", got.Title, got.MessageCount, got.Pinned)
	}
	if got := byID["idx-2"]; got.Title != "手工改过名的会话" || got.MessageCount != 1 || !got.Pinned {
		t.Fatalf("idx-2 镜像列 = title=%q count=%d pinned=%v", got.Title, got.MessageCount, got.Pinned)
	}
	// 正文缺席时条数仍要报得出来（消费方是 Web 列表与 CLI 列表）。
	if byID["idx-1"].TotalMessages() != 2 {
		t.Fatalf("TotalMessages = %d, want 2", byID["idx-1"].TotalMessages())
	}

	// 详情按 id 读才拿到正文。
	full, err := st.Sessions().Get(ctx, "idx-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(full.Messages) != 2 || full.Messages[1].Content != sentinel {
		t.Fatalf("详情正文缺失: %+v", full.Messages)
	}
}

// TestSessionIndexMirrorSurvivesBodyFreeUpdate 守住镜像列的例外分支：从列表读来的会话
// 正文是空的，这种会话被更新一次（改名、置顶）不许把标题和条数抹平。
func TestSessionIndexMirrorSurvivesBodyFreeUpdate(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	sess := &session.Session{ID: "idx-3", UserID: "u1"}
	sess.AddMessage(session.Message{Role: "user", Type: "text", Content: "查一下 pvc 卡住"})
	sess.AddMessage(session.Message{Role: "assistant", Type: "text", Content: "存储卷已绑定"})
	if err := st.Sessions().Create(ctx, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	list, err := st.Sessions().List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}

	list[0].State = "done"
	if err := st.Sessions().Update(ctx, list[0]); err != nil {
		t.Fatalf("update: %v", err)
	}
	saved, err := st.Sessions().Get(ctx, "idx-3")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if saved.State != "done" {
		t.Fatalf("更新没落库: %+v", saved)
	}
	if saved.Title != "查一下 pvc 卡住" || saved.MessageCount != 2 {
		t.Fatalf("无关更新抹平了镜像列: title=%q count=%d", saved.Title, saved.MessageCount)
	}
}

// TestDisplayTitleFallbacks 锁住标题推导的优先级：显式改名 > 写入时固化的快照 >
// 首条用户消息（含 24 rune 截断）> 「新会话」。列表页看不到正文，全靠这条推导链。
func TestDisplayTitleFallbacks(t *testing.T) {
	long := strings.Repeat("节", 30)
	cases := []struct {
		name string
		sess *session.Session
		want string
	}{
		{"显式改名", &session.Session{ID: "t1", Name: "web 起不来排查"}, "web 起不来排查"},
		{"占位名不算改名", &session.Session{ID: "t2", Name: "Session t2", Title: "快照标题"}, "快照标题"},
		{"首条用户消息", &session.Session{ID: "t3", Messages: []session.Message{
			{Role: "assistant", Type: "text", Content: "先于用户的助手消息"},
			{Role: "user", Type: "text", Content: " 第二行之前\n忽略这行"},
		}}, "第二行之前"},
		{"超长截断", &session.Session{ID: "t4", Messages: []session.Message{
			{Role: "user", Type: "text", Content: long},
		}}, strings.Repeat("节", 24) + "…"},
		{"空会话", &session.Session{ID: "t5"}, "新会话"},
	}
	for _, tc := range cases {
		if got := tc.sess.DisplayTitle(); got != tc.want {
			t.Errorf("%s: DisplayTitle() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
