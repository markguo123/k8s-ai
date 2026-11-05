package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/audit"
)

func TestEventTypeForActionExactMappings(t *testing.T) {
	cases := []struct {
		action string
		want   string
	}{
		{"session.created", AuditEventSession},
		{"session.resumed", AuditEventSession},
		{"session.closed", AuditEventSession},
		{"user.input", AuditEventUserInput},
		{"plan.generated", AuditEventLLMCall},
		{"approval.decision", AuditEventApproval},
		{"approval.granted", AuditEventApproval},
		{"approval.denied", AuditEventApproval},
		{"approval.skipped", AuditEventApproval},
		{"execution.started", AuditEventResourceMutation},
		{"execution.succeeded", AuditEventResourceMutation},
		{"execution.failed", AuditEventResourceMutation},
		{"verification.started", AuditEventResourceMutation},
		{"verification.succeeded", AuditEventResourceMutation},
		{"verification.failed", AuditEventResourceMutation},
		{"tool-call.request", AuditEventToolCall},
		{"tool-call.response", AuditEventToolCall},
		{"tool.error", AuditEventToolCall},
		{"config.change", AuditEventConfigChange},
		{"skill.run", AuditEventSkillRun},
		{"cluster.change", AuditEventClusterChange},
		{"auth.login", AuditEventLogin},
		{"auth.logout", AuditEventLogout},
		{"secret.value_requested", AuditEventSecretRead},
		{"secret.touched", AuditEventSecretRead},
		{"stream_fallback", AuditEventLLMCall},
		{"stream_partial", AuditEventLLMCall},
	}
	for _, tc := range cases {
		if got := EventTypeForAction(tc.action); got != tc.want {
			t.Errorf("EventTypeForAction(%q) = %q, want %q", tc.action, got, tc.want)
		}
	}
}

func TestEventTypeForActionPrefixFallback(t *testing.T) {
	if got := EventTypeForAction("llm.request"); got != AuditEventLLMCall {
		t.Fatalf("llm.* 应为 LLM_CALL, got %q", got)
	}
	if got := EventTypeForAction("agent.step"); got != AuditEventSession {
		t.Fatalf("agent.* 应为 SESSION, got %q", got)
	}
	if got := EventTypeForAction("whatever.else"); got != AuditEventOther {
		t.Fatalf("未知应为 OTHER, got %q", got)
	}
}

func TestAuditSummaryRedactsAndTruncates(t *testing.T) {
	long := strings.Repeat("a", maxAuditSummary+50)
	got := auditSummary(`{"kubeconfig":"c2VjcmV0","api_key":"sk-abc","data":"` + long + `"}`)
	if strings.Contains(got, "c2VjcmV0") || strings.Contains(got, "sk-abc") {
		t.Fatalf("敏感键值未脱敏: %s", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("缺 [REDACTED] 标记: %s", got)
	}
	if len([]rune(got)) != maxAuditSummary {
		t.Fatalf("截断长度 = %d, want %d", len([]rune(got)), maxAuditSummary)
	}
}

// TestBackfillAuditEventType 验证 v3 迁移的历史回填：把空 event_type 行按
// EventTypeForAction 单源映射补全，且重复执行不改变结果（幂等）。
func TestBackfillAuditEventType(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite("file:backfill-test-" + randID() + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ds := st.(*dbStore)

	// 模拟 v3 之前写入的历史行：event_type 取默认空值。
	insert := []struct {
		action string
		want   string
	}{
		{audit.ActionToolCallResponse, AuditEventToolCall},
		{ActionAuthLogin, AuditEventLogin},
		{audit.ActionExecutionSucceeded, AuditEventResourceMutation},
		{"legacy.action", AuditEventOther},
	}
	for _, row := range insert {
		if _, err := ds.db.ExecContext(ctx,
			`INSERT INTO audit_events (timestamp, action) VALUES (?, ?)`, time.Now(), row.action); err != nil {
			t.Fatalf("insert legacy row %q: %v", row.action, err)
		}
	}

	tx, err := ds.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := backfillAuditEventType(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatalf("backfill: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	events, err := st.Audit().List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != len(insert) {
		t.Fatalf("events = %d, want %d", len(events), len(insert))
	}
	byAction := map[string]string{}
	for _, e := range events {
		byAction[e.Action] = e.EventType
	}
	for _, row := range insert {
		if byAction[row.action] != row.want {
			t.Errorf("action %q 回填为 %q, want %q", row.action, byAction[row.action], row.want)
		}
	}

	// 幂等：再跑一遍并统计是否仍有空 event_type。
	tx, err = ds.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin second tx: %v", err)
	}
	if err := backfillAuditEventType(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatalf("second backfill: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit second: %v", err)
	}
	var empty int
	if err := ds.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_events WHERE event_type = ''`).Scan(&empty); err != nil {
		t.Fatalf("count empty event_type: %v", err)
	}
	if empty != 0 {
		t.Fatalf("幂等回填后仍有 %d 行 event_type 为空", empty)
	}
}
