package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/audit"
	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/settings"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// migrateTestCipher 是 P3.9 的 fake 静态加密器（带随机 nonce）。迁移装配
// cipher 后，敏感配置键以密文落库、读视图仍不回明文。
type migrateTestCipher struct{}

func (migrateTestCipher) Encrypt(plain []byte) (string, error) {
	nonce := make([]byte, 4)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	buf := append(nonce, plain...)
	return "test:" + base64.StdEncoding.EncodeToString(buf), nil
}

func (migrateTestCipher) Decrypt(encoded string) ([]byte, error) {
	const prefix = "test:"
	if !strings.HasPrefix(encoded, prefix) {
		return nil, errors.New("test cipher: 未知前缀")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, prefix))
	if err != nil {
		return nil, err
	}
	if len(raw) < 4 {
		return nil, errors.New("test cipher: 载荷过短")
	}
	return raw[4:], nil
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// migrateTestFixture 构造一套三期遗留数据（config/session/audit/report），
// 并返回填好源路径的 config.Config 与各源目录。
func migrateTestFixture(t *testing.T) (*config.Config, string, string, string) {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	sessDir, auditDir, reportDir := t.TempDir(), t.TempDir(), t.TempDir()

	// 三个非默认值：敏感 api_key 只写不读、model、require_approval。
	cfg.LLM.APIKey = "sk-migrate-secret-api-key"
	cfg.LLM.Model = "custom-model"
	cfg.Agent.RequireApproval = false
	cfg.Agent.SessionDir = sessDir
	cfg.Agent.AuditDir = auditDir
	cfg.Report.Directory = reportDir

	// Session 文件：Messages 的 Arguments/Data/Content 内嵌 Secret 值、
	// kubeconfig 与 API Key 明文，验证迁移写库前整体脱敏。
	now := time.Now().UTC()
	oldSession := session.Session{
		ID:      "s1",
		Name:    "三期旧会话",
		Cluster: "prod",
		State:   "done",
		Messages: []session.Message{
			{
				Role: "user", Type: "text",
				Content: "帮我看看 deployment web 为什么起不来 api_key=sk-abcdefghijklmnop",
			},
			{
				Role: "tool", Type: "tool-call-response", Name: "kubectl",
				Arguments: json.RawMessage(`{"secret":"sk-deploy-token","kubeconfigPath":"/tmp/kube.conf","api_key":"sk-config-key"}`),
				Data:      map[string]any{"secret": "sk-data-secret", "kubeconfig": "apiVersion: v1\nkind: Config"},
			},
		},
		CreatedAt:    now,
		LastModified: now,
	}
	raw, err := json.Marshal(oldSession)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(sessDir, "s1.json"), string(raw))
	// 坏 JSON：解析失败应进 Unmigratable，不静默丢弃。
	writeFixtureFile(t, filepath.Join(sessDir, "bad.json"), `{"id": "bad", "messages": [`)

	// 审计文件 s1.jsonl：两行以上，含 user.input 与写工具响应，payload 携带凭据明文。
	lines := strings.Join([]string{
		`{"timestamp":"2026-10-12T10:00:00Z","action":"user.input","payload":{"query":"apply -f web.yaml","secret":"sk-audit-query"}}`,
		`{"timestamp":"2026-10-12T10:00:01Z","action":"tool-call.response","payload":{"readonly":false,"operation":"create","kind":"Deployment","resource_name":"web","api_key":"sk-audit-tool-key","kubeconfig":"ZXhhbXBsZQ=="}}`,
	}, "\n") + "\n"
	writeFixtureFile(t, filepath.Join(auditDir, "s1.jsonl"), lines)
	// 有坏行的审计文件：好行进库，坏行聚合成一条 Unmigratable。
	writeFixtureFile(t, filepath.Join(auditDir, "partial.jsonl"),
		`{"timestamp":"2026-10-12T10:00:02Z","action":"session.created","payload":{}}`+"\n"+
			"not-json-line\n")

	// 报告文件：四期无存储表，应显式保留并报告。
	writeFixtureFile(t, filepath.Join(reportDir, "latest.md"), "# report")
	writeFixtureFile(t, filepath.Join(reportDir, "latest.json"), `{"summary":"x"}`)

	return cfg, sessDir, auditDir, reportDir
}

func assertAbsent(t *testing.T, s string, absent ...string) {
	t.Helper()
	for _, a := range absent {
		if a == "" {
			continue
		}
		if strings.Contains(s, a) {
			t.Fatalf("输出仍含敏感明文 %q", a)
		}
	}
}

// TestMigrateImportsAllSourcesIdempotent 验证 P1.12 全量导入与幂等：
// config/session/audit/skill/report 各自计数正确，二次运行不重复写库，
// 且迁移后的会话/审计零 Secret/kubeconfig/API Key 明文（AGENTS.md 红线）。
func TestMigrateImportsAllSourcesIdempotent(t *testing.T) {
	cfg, sessDir, auditDir, reportDir := migrateTestFixture(t)
	ctx := context.Background()
	st := persistTestStore(t)
	opts := MigrateOptions{
		Store: st, Config: cfg,
		SessionDir: sessDir, AuditDir: auditDir, ReportsDir: reportDir,
		Cipher: migrateTestCipher{},
	}

	rep1, err := Migrate(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	// 除 api_key/model/require_approval 外，临时源目录本身也是非默认值，
	// 因此只断言自定义项全部入库、存在默认值计数即可。
	if rep1.Config.Imported < 3 || rep1.Config.Defaults == 0 {
		t.Fatalf("config report 异常: %+v", rep1.Config)
	}
	if rep1.Sessions.Found != 2 || rep1.Sessions.Imported != 1 || len(rep1.Sessions.Unmigratable) != 1 {
		t.Fatalf("sessions report = %+v", rep1.Sessions)
	}
	if rep1.Audit.Found != 2 || rep1.Audit.Imported != 3 || len(rep1.Audit.Unmigratable) != 1 {
		t.Fatalf("audit report = %+v", rep1.Audit)
	}
	if rep1.Skills.Found != 5 || rep1.Skills.Imported != 5 {
		t.Fatalf("skills report = %+v", rep1.Skills)
	}
	if rep1.Reports.Found != 2 || rep1.Reports.Kept != 2 || len(rep1.Reports.Unmigratable) != 2 {
		t.Fatalf("reports report = %+v", rep1.Reports)
	}

	// 幂等：同一 Store 二次迁移不重复写 session/audit/skills。
	rep2, err := Migrate(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Sessions.Imported != 0 || rep2.Sessions.Skipped != 1 {
		t.Fatalf("second sessions report = %+v", rep2.Sessions)
	}
	if rep2.Audit.Imported != 0 || rep2.Audit.Skipped != 2 || len(rep2.Audit.Unmigratable) != 0 {
		t.Fatalf("second audit report = %+v", rep2.Audit)
	}
	if rep2.Skills.Imported != 0 || rep2.Skills.Skipped != 1 {
		t.Fatalf("second skills report = %+v", rep2.Skills)
	}

	// 落库数量：1 会话 / 3 审计 / 5 内置技能。
	sessions, err := st.Sessions().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("stored sessions = %d, want 1", len(sessions))
	}
	events, err := st.Audit().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("stored audit events = %d, want 3", len(events))
	}
	skills, err := st.Skills().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 5 {
		t.Fatalf("stored skills = %d, want 5", len(skills))
	}
	// 配置中心应已登记敏感键：密文落库（P3.9），读视图永不回显。
	storedKey, err := st.Settings().Get(ctx, settings.GroupModel, "api_key")
	if err != nil || !storedKey.Sensitive {
		t.Fatalf("api_key 应作为敏感键导入, got %+v err=%v", storedKey, err)
	}
	if strings.Contains(storedKey.Value, "sk-migrate-secret-api-key") {
		t.Fatalf("api_key 不得以明文落库, got %q", storedKey.Value)
	}
	if got, err := st.Settings().Get(ctx, settings.GroupModel, "model"); err != nil || got.Value != `"custom-model"` {
		t.Fatalf("model 应导入为 custom-model, got %+v err=%v", got, err)
	}
	view, err := settings.New(st, settings.WithCipher(migrateTestCipher{})).GetGroup(ctx, settings.GroupModel)
	if err != nil {
		t.Fatalf("GetGroup(model): %v", err)
	}
	raw, _ := json.Marshal(view)
	assertAbsent(t, string(raw), "sk-migrate-secret-api-key")

	// 红线：迁移后的会话输出不含任何 Secret/kubeconfig/API Key 明文。
	gotSession, err := st.Sessions().Get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	sessRaw, _ := json.Marshal(gotSession)
	assertAbsent(t, string(sessRaw),
		"sk-migrate-secret-api-key", "sk-abcdefghijklmnop", "sk-deploy-token",
		"sk-config-key", "sk-data-secret")

	// 红线：迁移后的审计输出不含 payload 明文，且写工具响应按 P1.11 分类。
	var auditRaw strings.Builder
	hasMutation := false
	for _, e := range events {
		raw, _ := json.Marshal(e)
		auditRaw.Write(raw)
		auditRaw.WriteByte('\n')
		if e.Action == audit.ActionToolCallResponse && e.EventType == store.AuditEventResourceMutation {
			hasMutation = true
		}
	}
	assertAbsent(t, auditRaw.String(), "sk-audit-query", "sk-audit-tool-key", "ZXhhbXBsZQ==")
	if !hasMutation {
		t.Fatalf("写工具响应未落 RESOURCE_MUTATION 分类: %+v", events)
	}
}

// TestMigrateRequiresStoreAndToleratesMissingSources 验证 Store 必填，以及
// 空/不存在的源目录整类跳过而不报错。
func TestMigrateRequiresStoreAndToleratesMissingSources(t *testing.T) {
	ctx := context.Background()
	if _, err := Migrate(ctx, MigrateOptions{}); err == nil {
		t.Fatal("未配置 Store 应报错")
	}
	st := persistTestStore(t)
	rep, err := Migrate(ctx, MigrateOptions{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	if rep.UnmigratableCount() != 0 || rep.Sessions.Found != 0 || rep.Audit.Found != 0 || rep.Reports.Found != 0 {
		t.Fatalf("空源报告异常: %+v", rep)
	}

	rep, err = Migrate(ctx, MigrateOptions{
		Store:      st,
		SessionDir: filepath.Join(t.TempDir(), "nope"),
		AuditDir:   filepath.Join(t.TempDir(), "nope"),
		ReportsDir: filepath.Join(t.TempDir(), "nope"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.UnmigratableCount() != 0 || rep.Sessions.Found != 0 || rep.Audit.Found != 0 || rep.Reports.Found != 0 {
		t.Fatalf("不存在目录报告异常: %+v", rep)
	}
}

// TestMigrateReportUnmigratableCount 覆盖 UnmigratableCount 汇总口径：
// 报告文件按"显式保留"计入，CLI 因此会以非零退出码提示人工确认。
func TestMigrateReportUnmigratableCount(t *testing.T) {
	cfg, sessDir, auditDir, reportDir := migrateTestFixture(t)
	st := persistTestStore(t)
	rep, err := Migrate(context.Background(), MigrateOptions{
		Store: st, Config: cfg,
		SessionDir: sessDir, AuditDir: auditDir, ReportsDir: reportDir,
		Cipher: migrateTestCipher{},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 坏会话 1 + 坏审计行文件 1 + 报告文件 2。
	if got := rep.UnmigratableCount(); got != 4 {
		t.Fatalf("UnmigratableCount = %d, want 4", got)
	}
}

// TestMigrateSessionsImportOnceAndForce 锁住 P8.0 的会话迁移口径：三期会话目录是
// 一次性兜底导入，整轮成功后打标记、后续 migrate 整类跳过，平台上已删除的旧会话
// 不能被下一次导入复活；确需重导入用 Force。
func TestMigrateSessionsImportOnceAndForce(t *testing.T) {
	sessDir := t.TempDir()
	for _, id := range []string{"old-a", "old-b"} {
		now := time.Now().UTC()
		raw, err := json.Marshal(session.Session{
			ID: id, Name: "三期会话 " + id, State: "done",
			CreatedAt: now, LastModified: now,
			Messages: []session.Message{{Role: "user", Type: "text", Content: "历史提问"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		writeFixtureFile(t, filepath.Join(sessDir, id+".json"), string(raw))
	}

	ctx := context.Background()
	st := persistTestStore(t)
	opts := MigrateOptions{Store: st, SessionDir: sessDir}

	rep1, err := Migrate(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep1.Sessions.Found != 2 || rep1.Sessions.Imported != 2 || rep1.Sessions.Skipped != 0 {
		t.Fatalf("首次 sessions report = %+v", rep1.Sessions)
	}
	if rep1.Sessions.Note == "" || len(rep1.Sessions.Unmigratable) != 0 {
		t.Fatalf("首次导入应打完成标记且无不可迁移项: %+v", rep1.Sessions)
	}

	// 模拟运维在平台上删掉一条导入后的历史会话（删除必须是终态）。
	if err := st.Sessions().Delete(ctx, "old-a"); err != nil {
		t.Fatal(err)
	}

	rep2, err := Migrate(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Sessions.Found != 2 || rep2.Sessions.Imported != 0 || rep2.Sessions.Skipped != 2 {
		t.Fatalf("二次 sessions report = %+v", rep2.Sessions)
	}
	if !strings.Contains(rep2.Sessions.Note, "--force") {
		t.Fatalf("整类跳过必须说明重导入方式: %+v", rep2.Sessions)
	}
	sessions, err := st.Sessions().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "old-b" {
		t.Fatalf("已删除的旧会话被 migrate 复活: %+v", sessions)
	}

	// Force：显式重导入（会找回库中已删除的 old-a）。
	rep3, err := Migrate(ctx, MigrateOptions{Store: st, SessionDir: sessDir, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep3.Sessions.Imported != 1 || rep3.Sessions.Skipped != 1 {
		t.Fatalf("force sessions report = %+v", rep3.Sessions)
	}
	if sessions, err = st.Sessions().List(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("force 重导入后 sessions = %+v", sessions)
	}
}
