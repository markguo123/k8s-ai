package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/session"
)

// newSizedTestStore 开一个指定正文上限的平台库（P8.6 容量闸门用例专用）。上限做成参数
// 是为了能在内存库里造出「刚好预警」「刚好超限」两档体量——按默认 12MiB 造这两档，
// 得真塞十几兆正文进去，测试会慢到没人愿意跑。
func newSizedTestStore(t *testing.T, limit int64) Store {
	t.Helper()
	st, err := OpenSQLite("file:session-size-"+randID()+"?mode=memory&cache=shared", WithSessionMaxBytes(limit))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

// sizedSession 造一个「用户问了一句 + 助手答了一大段」的会话，正文体量由 content 长度决定。
func sizedSession(id, content string) *session.Session {
	sess := &session.Session{ID: id, UserID: "u1", Namespace: "default", Model: "qwen-plus"}
	sess.AddMessage(session.Message{Role: "user", Type: "text", Content: "看看 web"})
	sess.AddMessage(session.Message{Role: "assistant", Type: "text", Content: content})
	return sess
}

func sizedContent(n int) string {
	return strings.Repeat("x", n)
}

// TestSessionSizeWarnsBeforeTheLimit 验证 85% 预警：写入必须成功，只把 Warning 置起来。
//
// 用例从很小的正文起步逐段加大，直到闸门报预警为止。这样断言的是「闸门在预警比例处
// 翻转」这个行为本身，而不是某一次序列化刚好落在多少字节上——多两个字段少一个空格就
// 会把硬编码的字节数打翻，那类失败除了让人改测试数字没有任何价值。
func TestSessionSizeWarnsBeforeTheLimit(t *testing.T) {
	const limit = 64 << 10
	st := newSizedTestStore(t, limit)
	ctx := context.Background()
	sized, ok := st.Sessions().(SessionSizeStore)
	if !ok {
		t.Fatal("Sessions() 必须实现 SessionSizeStore")
	}

	var rep SessionSizeReport
	grew := false
	if err := st.Sessions().Create(ctx, sizedSession("size-warn", "起步正文")); err != nil {
		t.Fatalf("create: %v", err)
	}
	for n := 512; n <= limit; n += 512 {
		var err error
		rep, err = sized.UpdateSized(ctx, sizedSession("size-warn", sizedContent(n)))
		if err != nil {
			t.Fatalf("UpdateSized(content=%d): %v", n, err)
		}
		if rep.Warning {
			grew = true
			break
		}
	}
	if !grew {
		t.Fatalf("正文一路加到上限仍未触发预警: %+v", rep)
	}
	if rep.Size > rep.Limit {
		t.Fatalf("预警不该伴随超限: size=%d limit=%d", rep.Size, rep.Limit)
	}
	if rep.Size*100 < rep.Limit*int64(sessionMemoryWarningPercent) {
		t.Fatalf("预警过早: size=%d limit=%d 尚未到 %d%%", rep.Size, rep.Limit, sessionMemoryWarningPercent)
	}

	// 预警只许是「提醒」：正文必须已落库，读得回来。
	saved, err := st.Sessions().Get(ctx, "size-warn")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(saved.Messages) != 2 {
		t.Fatalf("预警轮的正文必须照常入库: %+v", saved.Messages)
	}
}

// TestSessionSizeRejectsOversizeWithoutTouchingOldBody 验证超限拒写的三条硬要求：
// 报 ErrSessionTooLarge、错误文案说得出上限与配置键、库里那一行还是上一次的好正文。
// 静默截断或写坏旧正文都属 AGENTS.md 禁止的假实现。
func TestSessionSizeRejectsOversizeWithoutTouchingOldBody(t *testing.T) {
	const limit = 4 << 10
	st := newSizedTestStore(t, limit)
	ctx := context.Background()
	sized := st.Sessions().(SessionSizeStore)

	if err := st.Sessions().Create(ctx, sizedSession("size-hard", "上一轮的正常回复")); err != nil {
		t.Fatalf("create: %v", err)
	}

	rep, err := sized.UpdateSized(ctx, sizedSession("size-hard", sizedContent(8<<10)))
	if !errors.Is(err, ErrSessionTooLarge) {
		t.Fatalf("超限写入 = %v, want ErrSessionTooLarge", err)
	}
	if rep.Size <= rep.Limit {
		t.Fatalf("超限回报要给出真实体量: %+v", rep)
	}
	msg := err.Error()
	for _, want := range []string{"agent.session_max_bytes", "上限", "新建会话"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("超限文案缺少 %q: %s", want, msg)
		}
	}

	saved, err := st.Sessions().Get(ctx, "size-hard")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(saved.Messages) != 2 || saved.Messages[1].Content != "上一轮的正常回复" {
		t.Fatalf("被拒的写入动了库里的旧正文: %+v", saved.Messages)
	}
}

// TestSessionSizeGateCoversTxAndCreate 守住「闸门只有一条漏斗」这件事：Chat 的一轮落库走
// WithTx，超限必须同样过不去；Create 走 INSERT，也不能绕过闸门。
func TestSessionSizeGateCoversTxAndCreate(t *testing.T) {
	const limit = 4 << 10
	st := newSizedTestStore(t, limit)
	ctx := context.Background()
	if err := st.Sessions().Create(ctx, sizedSession("size-tx", "小正文")); err != nil {
		t.Fatalf("create: %v", err)
	}

	err := st.(*dbStore).WithTx(ctx, func(tx *Tx) error {
		return tx.UpdateSession(ctx, sizedSession("size-tx", sizedContent(8<<10)))
	})
	if !errors.Is(err, ErrSessionTooLarge) {
		t.Fatalf("事务内超限写入 = %v, want ErrSessionTooLarge", err)
	}
	saved, err := st.Sessions().Get(ctx, "size-tx")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if saved.Messages[1].Content != "小正文" {
		t.Fatalf("事务被拒后旧正文应保持不变: %+v", saved.Messages)
	}

	// Create 同样过不去闸门，且不许留下半行。
	if err := st.Sessions().Create(ctx, sizedSession("size-create", sizedContent(8<<10))); !errors.Is(err, ErrSessionTooLarge) {
		t.Fatalf("超限 Create = %v, want ErrSessionTooLarge", err)
	}
	if _, err := st.Sessions().Get(ctx, "size-create"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("超限 Create 不该留下会话行: %v", err)
	}
}

// TestSessionSizeLimitFallsBackToDefault 锁住「绕过配置装配直接用 store」的兜底：
// 没传 WithSessionMaxBytes 时闸门按 DefaultSessionMaxBytes 判定，而不是没有上限。
func TestSessionSizeLimitFallsBackToDefault(t *testing.T) {
	st := newTestStore(t)
	sized := st.Sessions().(SessionSizeStore)
	ctx := context.Background()
	if err := st.Sessions().Create(ctx, sizedSession("size-default", "起步正文")); err != nil {
		t.Fatalf("create: %v", err)
	}
	rep, err := sized.UpdateSized(context.Background(), sizedSession("size-default", "正文"))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if rep.Limit != DefaultSessionMaxBytes {
		t.Fatalf("默认上限 = %d, want %d", rep.Limit, DefaultSessionMaxBytes)
	}
	if rep.Warning {
		t.Fatal("几个字节的正文不该报预警")
	}
}

// TestEvaluateSessionSizeBoundaries 直接锁判定函数本身的边界：等于上限算通过、超过才拒，
// 恰好 85% 报预警。这三条在 UpdateSized 上很难用构造正文精确命中，交给纯函数用例守。
func TestEvaluateSessionSizeBoundaries(t *testing.T) {
	cases := []struct {
		size, limit int64
		wantErr     bool
		wantWarning bool
	}{
		{size: 100, limit: 1000},
		{size: 849, limit: 1000, wantWarning: false},
		{size: 850, limit: 1000, wantWarning: true},
		{size: 1000, limit: 1000, wantWarning: true},
		{size: 1001, limit: 1000, wantErr: true},
		{size: 1 << 20, limit: 0, wantWarning: false}, // limit<=0 回落默认值
	}
	for _, tc := range cases {
		rep, err := evaluateSessionSize(tc.size, tc.limit)
		if tc.wantErr {
			if !errors.Is(err, ErrSessionTooLarge) {
				t.Fatalf("evaluateSessionSize(%d,%d) = %v, want ErrSessionTooLarge", tc.size, tc.limit, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("evaluateSessionSize(%d,%d) 不该报错: %v", tc.size, tc.limit, err)
		}
		if rep.Warning != tc.wantWarning {
			t.Fatalf("evaluateSessionSize(%d,%d).Warning = %v, want %v", tc.size, tc.limit, rep.Warning, tc.wantWarning)
		}
		if tc.limit <= 0 && rep.Limit != DefaultSessionMaxBytes {
			t.Fatalf("limit<=0 应回落默认值, got %d", rep.Limit)
		}
	}
}

// TestFormatBytesReadable 锁住给用户看的体积写法：超限与预警文案都靠它，写成
// "12582912 字节"那种，运维照样得自己按计算器。
func TestFormatBytesReadable(t *testing.T) {
	cases := map[int64]string{
		0:              "0 B",
		1024:           "1024 B",
		1 << 20:        "1 MiB",
		12 << 20:       "12 MiB",
		(12 << 20) + 1: "12.0 MiB",
	}
	for in, want := range cases {
		if got := FormatBytes(in); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
