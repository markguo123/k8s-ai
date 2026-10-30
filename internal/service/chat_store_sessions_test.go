package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/kubernetes"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/scanner"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/store"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// platformChatFixture 装配一套「平台模式」聊天服务（有平台库）+ 独立的会话/审计目录，
// 用于验证 P8.0 会话事实源收敛：读写全部进平台库，容器卷上的目录不再是事实源。
type platformChatFixture struct {
	cfg *config.Config
	svc ChatService
	st  store.Store
}

func newPlatformChatFixture(t *testing.T, fl *scriptedLLM, tweak func(*config.Config)) *platformChatFixture {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent.SessionDir = t.TempDir()
	cfg.Agent.AuditDir = t.TempDir()
	if tweak != nil {
		tweak(cfg)
	}
	reader := kubernetes.NewClientWithClientset(fake.NewSimpleClientset())
	wr := &fakeWriteRunner{}
	st := persistTestStore(t)
	svc := NewChatWithStore(cfg, func() (scanner.Reader, error) { return reader, nil },
		func(llm.Options) llm.LLMClient { return fl },
		func() tools.ScanRunner { return &fakeScanRunner{} },
		func() (tools.WriteRunner, error) { return wr, nil }, st)
	return &platformChatFixture{cfg: cfg, svc: svc, st: st}
}

func runChatTurn(t *testing.T, svc ChatService, sessionID, query string) {
	t.Helper()
	respond := make(chan string, 1)
	events, err := svc.Chat(context.Background(), ChatRequest{SessionID: sessionID, Query: query}, respond)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	for range events {
	}
}

// plainFiles 列出目录下的普通文件名（目录不存在视为空——平台模式本就不该创建它）。
func plainFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestChatServicePlatformSessionSourceIsStore 验证 P8.0 主结论：平台模式下会话
// 新建/改名/置顶/删除全部即时落平台库，会话目录不产生任何文件，删除是终态。
func TestChatServicePlatformSessionSourceIsStore(t *testing.T) {
	fl := &scriptedLLM{responses: []*llm.ChatResponse{
		{Content: "deployment web 已就绪", Usage: llm.TokenUsage{TotalTokens: 4}},
	}}
	f := newPlatformChatFixture(t, fl, nil)
	ctx := context.Background()

	sess, err := f.svc.GetOrCreateSessionForUser("plat-src", "u1", "default", "qwen-plus")
	if err != nil {
		t.Fatal(err)
	}
	// 建会话即落库（不再依赖「跑一轮对话才补写」）。
	saved, err := f.st.Sessions().Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("session 未落库: %v", err)
	}
	if saved.UserID != "u1" || saved.Namespace != "default" {
		t.Fatalf("saved = %+v", saved)
	}
	if files := plainFiles(t, f.cfg.Agent.SessionDir); len(files) != 0 {
		t.Fatalf("平台模式不应写会话目录: %v", files)
	}

	runChatTurn(t, f.svc, sess.ID, "看看 web")
	saved, err = f.st.Sessions().Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get after turn: %v", err)
	}
	if len(saved.Messages) != 2 || saved.Messages[1].Content != "deployment web 已就绪" {
		t.Fatalf("messages = %+v", saved.Messages)
	}
	// 本轮 Turn/Step 已入库（下面删除会话时要断言它们一并消失）。
	turns, err := f.st.Turns().ListBySession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("turns = %+v", turns)
	}
	turnID := turns[0].ID
	steps, err := f.st.Steps().ListByTurn(ctx, turns[0].ID)
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	if len(steps) == 0 {
		t.Fatalf("本轮没有 Step 入库: %+v", steps)
	}

	// 改名与置顶同步落库（Web 列表读的是库）。
	if err := f.svc.RenameSession(sess.ID, "web 起不来排查"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.PinSession(sess.ID, true); err != nil {
		t.Fatal(err)
	}
	saved, err = f.st.Sessions().Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get after rename: %v", err)
	}
	if saved.Name != "web 起不来排查" || !saved.Pinned {
		t.Fatalf("rename/pin 未落库: %+v", saved)
	}

	// 清空消息同样只改库。
	if err := f.svc.ClearSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	if saved, err = f.st.Sessions().Get(ctx, sess.ID); err != nil {
		t.Fatalf("get after clear: %v", err)
	}
	if len(saved.Messages) != 0 {
		t.Fatalf("clear 后仍有消息: %+v", saved.Messages)
	}

	// 删除是终态：库里读不到、列表为空、目录里也没有残留副本。
	if err := f.svc.DeleteSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Sessions().Get(ctx, sess.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("已删会话仍在库里: %v", err)
	}
	if _, err := f.svc.GetSession(sess.ID); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("已删会话服务层仍可读: %v", err)
	}
	list, err := f.svc.ListSessionsForUser("u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("已删会话仍在列表: %+v", list)
	}
	// Turn/Step 一并清除：它们的 input/output/params/result 同样能拼回已删会话正文，
	// 只删 sessions 行等于「删除」不成立（ADR-046 决策 2）。
	if turns, err = f.st.Turns().ListBySession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if len(turns) != 0 {
		t.Fatalf("已删会话的 Turn 仍在库里: %+v", turns)
	}
	if steps, err = f.st.Steps().ListByTurn(ctx, turnID); err != nil {
		t.Fatal(err)
	}
	if len(steps) != 0 {
		t.Fatalf("已删会话的 Step 仍在库里: %+v", steps)
	}
	// 反向断言：审计事件必须活过会话删除（AGENTS.md 审计留存），不随级联消失。
	events, err := f.st.Audit().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	auditKept := 0
	for _, e := range events {
		if e.SessionID == sess.ID {
			auditKept++
		}
	}
	if auditKept == 0 {
		t.Fatalf("会话删除把审计事件一起删了: events=%d", len(events))
	}
	if files := plainFiles(t, f.cfg.Agent.SessionDir); len(files) != 0 {
		t.Fatalf("平台模式会话目录被写入: %v", files)
	}
}

// TestChatServiceAuditFileCopyToggle 验证审计副本开关（P8.0）：默认保留会话 jsonl
// 副本（现场排障依赖它），关掉后目录不产文件、但 audit_events 一条不少。
func TestChatServiceAuditFileCopyToggle(t *testing.T) {
	cases := []struct {
		name      string
		tweak     func(*config.Config)
		wantFiles int
	}{
		{name: "默认开启副本", wantFiles: 1},
		{name: "显式关闭副本", wantFiles: 0, tweak: func(cfg *config.Config) { cfg.Agent.AuditFileCopy = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fl := &scriptedLLM{responses: []*llm.ChatResponse{{Content: "节点正常"}}}
			f := newPlatformChatFixture(t, fl, tc.tweak)
			sess, err := f.svc.GetOrCreateSessionForUser("plat-audit", "u1", "default", "qwen-plus")
			if err != nil {
				t.Fatal(err)
			}
			runChatTurn(t, f.svc, sess.ID, "节点状态")

			jsonl := 0
			for _, name := range plainFiles(t, f.cfg.Agent.AuditDir) {
				if strings.HasSuffix(name, ".jsonl") {
					jsonl++
				}
			}
			if jsonl != tc.wantFiles {
				t.Fatalf("审计副本文件数 = %d, want %d", jsonl, tc.wantFiles)
			}
			events, err := f.st.Audit().List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(events) == 0 {
				t.Fatal("audit_events 应有本轮事件：关副本不得减少审计事实源")
			}
		})
	}
}

// TestChatServiceTerminalModeKeepsFileStore 反向锁住降级路径：终端直用（无平台库）
// 行为与文件布局不变——会话仍写进 SessionDir，审计仍写 jsonl。
func TestChatServiceTerminalModeKeepsFileStore(t *testing.T) {
	fl := &scriptedLLM{responses: []*llm.ChatResponse{{Content: "直接回答"}}}
	svc, cfg, _ := testChatService(t, fl)
	sess, err := svc.GetOrCreateSession("term-s1", "", "qwen-plus")
	if err != nil {
		t.Fatal(err)
	}
	runChatTurn(t, svc, sess.ID, "你好")
	if files := plainFiles(t, cfg.Agent.SessionDir); len(files) != 1 {
		t.Fatalf("终端直用模式会话文件 = %v, want 1 个", files)
	}
	if files := plainFiles(t, cfg.Agent.AuditDir); len(files) != 1 {
		t.Fatalf("终端直用模式审计文件 = %v, want 1 个", files)
	}
}
