package service

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/audit"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/store"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

func testAuditScope() AuditScope {
	return AuditScope{
		RequestID: "req-1",
		UserID:    "u1",
		ClusterID: "c1",
		SessionID: "s1",
		TurnID:    "t1",
		StepID:    "st1",
	}
}

func TestToStoreAuditEventResourceMutation(t *testing.T) {
	ev := audit.Event{
		Timestamp: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC),
		Action:    audit.ActionToolCallResponse,
		Payload: map[string]any{
			"readonly":      false,
			"operation":     "create",
			"kind":          "Deployment",
			"resource_name": "web",
			"risk":          "HIGH",
			"status":        "success",
		},
	}
	got := toStoreAuditEvent(ev, testAuditScope())
	if got.EventType != store.AuditEventResourceMutation {
		t.Fatalf("event_type = %q, want RESOURCE_MUTATION", got.EventType)
	}
	if got.Operation != "create" || got.ResourceKind != "Deployment" || got.ResourceName != "web" {
		t.Fatalf("资源字段提取异常: %+v", got)
	}
	if got.Risk != "HIGH" || got.Status != "success" {
		t.Fatalf("风险/状态提取异常: %+v", got)
	}
	if got.ResultSummary == "" || got.ParameterSummary != "" {
		t.Fatalf("tool-call.response 应写 result_summary 而非 parameter_summary: %+v", got)
	}
	if strings.Contains(got.Payload, "secret") {
		t.Fatalf("payload 不应含敏感内容: %s", got.Payload)
	}
}

func TestToStoreAuditEventKindAndNameAliases(t *testing.T) {
	ev := audit.Event{
		Timestamp: time.Now(),
		Action:    audit.ActionExecutionSucceeded,
		Payload: map[string]any{
			"readonly": false,
			"tool":     "kubectl",
			"kind":     "ConfigMap",
			"name":     "app-config",
		},
	}
	got := toStoreAuditEvent(ev, testAuditScope())
	if got.EventType != store.AuditEventResourceMutation {
		t.Fatalf("execution.succeeded 应为 RESOURCE_MUTATION, got %q", got.EventType)
	}
	// kubectl 类事件只有 name 字段，应回退取 name 作为资源名。
	if got.Operation != "kubectl" || got.ResourceKind != "ConfigMap" || got.ResourceName != "app-config" {
		t.Fatalf("别名/回退提取异常: %+v", got)
	}
}

func TestToStoreAuditEventReadonlyStaysToolCall(t *testing.T) {
	ev := audit.Event{
		Timestamp: time.Now(),
		Action:    audit.ActionToolCallResponse,
		Payload: map[string]any{
			"readonly":  true,
			"operation": "get",
		},
	}
	if got := toStoreAuditEvent(ev, testAuditScope()); got.EventType != store.AuditEventToolCall {
		t.Fatalf("readonly 工具响应应为 TOOL_CALL, got %q", got.EventType)
	}
	// 未声明 readonly 时视为未知，不升档为写操作。
	ev.Payload = map[string]any{"operation": "get"}
	if got := toStoreAuditEvent(ev, testAuditScope()); got.EventType != store.AuditEventToolCall {
		t.Fatalf("缺少 readonly 字段不应升级为 RESOURCE_MUTATION, got %q", got.EventType)
	}
}

func TestToStoreAuditEventGrant(t *testing.T) {
	ev := audit.Event{
		Timestamp: time.Now(),
		Action:    audit.ActionApprovalGranted,
		Payload: map[string]any{
			"decision": "approved",
			"grant_id": "g-1",
		},
	}
	got := toStoreAuditEvent(ev, testAuditScope())
	if got.EventType != store.AuditEventGrant {
		t.Fatalf("带 grant_id 的审批应记为 GRANT, got %q", got.EventType)
	}
	if got.GrantID != "g-1" || got.ResultSummary == "" {
		t.Fatalf("grant 事件字段异常: %+v", got)
	}

	// 无 grant_id 的审批维持 APPROVAL 分类。
	ev.Payload = map[string]any{"decision": "approved"}
	if got := toStoreAuditEvent(ev, testAuditScope()); got.EventType != store.AuditEventApproval {
		t.Fatalf("无 grant_id 的审批应为 APPROVAL, got %q", got.EventType)
	}
}

func TestToStoreAuditEventSystemAndFallback(t *testing.T) {
	sc := testAuditScope()
	if got := toStoreAuditEvent(audit.Event{Timestamp: time.Now(), Action: store.ActionAuthLogin}, sc); got.EventType != store.AuditEventLogin {
		t.Fatalf("auth.login 应为 LOGIN, got %q", got.EventType)
	}
	if got := toStoreAuditEvent(audit.Event{Timestamp: time.Now(), Action: store.ActionAuthLogout}, sc); got.EventType != store.AuditEventLogout {
		t.Fatalf("auth.logout 应为 LOGOUT, got %q", got.EventType)
	}
	if got := toStoreAuditEvent(audit.Event{Timestamp: time.Now(), Action: "llm.request"}, sc); got.EventType != store.AuditEventLLMCall {
		t.Fatalf("llm.* 前缀应为 LLM_CALL, got %q", got.EventType)
	}
	if got := toStoreAuditEvent(audit.Event{Timestamp: time.Now(), Action: "agent.event"}, sc); got.EventType != store.AuditEventSession {
		t.Fatalf("agent.* 前缀应为 SESSION, got %q", got.EventType)
	}
	if got := toStoreAuditEvent(audit.Event{Timestamp: time.Now(), Action: "unknown.action"}, sc); got.EventType != store.AuditEventOther {
		t.Fatalf("未知动作应为 OTHER, got %q", got.EventType)
	}
}

func TestToStoreAuditEventCopiesScopeAndSummaries(t *testing.T) {
	sc := testAuditScope()
	ev := audit.Event{
		Timestamp: time.Now(),
		Action:    audit.ActionToolCallRequest,
		Payload:   map[string]any{"cmd": "apply -f x.yaml"},
	}
	got := toStoreAuditEvent(ev, sc)
	if got.RequestID != sc.RequestID || got.UserID != sc.UserID || got.ClusterID != sc.ClusterID ||
		got.SessionID != sc.SessionID || got.TurnID != sc.TurnID || got.StepID != sc.StepID {
		t.Fatalf("关联字段未透传: %+v", got)
	}
	if got.EventType != store.AuditEventToolCall || got.ParameterSummary == "" || got.ResultSummary != "" {
		t.Fatalf("tool-call.request 应写 parameter_summary: %+v", got)
	}
}

func TestSkillRunAuditorNilWithoutStore(t *testing.T) {
	s := &chatService{store: nil}
	if fn := s.skillRunAuditor(context.Background(), &session.Session{ID: "s1"}, &runScope{}); fn != nil {
		t.Fatalf("未装配 Store 时应返回 nil 回调，got %p", fn)
	}
}

func TestSkillRunAuditorWritesSkillRun(t *testing.T) {
	st := persistTestStore(t)
	s := &chatService{store: st}
	ctx := context.Background()
	sess := &session.Session{ID: "s1", Cluster: "c1"}
	scope := &runScope{sessID: "s1", turnID: "t1", stepID: "st1"}
	fn := s.skillRunAuditor(ctx, sess, scope)
	if fn == nil {
		t.Fatal("装配 Store 后应返回回调")
	}
	fn(tools.SkillRunReport{
		Kind:      "Deployment",
		Name:      "web",
		Namespace: "default",
		Skills:    []string{"workload"},
		Findings:  2,
		Escalation: 1,
	})

	events, err := st.Audit().List(ctx)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("期望 1 条 SKILL_RUN, got %d", len(events))
	}
	e := events[0]
	if e.EventType != store.AuditEventSkillRun || e.Action != store.ActionSkillRun {
		t.Fatalf("事件分类异常: %+v", e)
	}
	if e.SessionID != "s1" || e.TurnID != "t1" || e.ClusterID != "c1" {
		t.Fatalf("关联字段异常: %+v", e)
	}
	if e.ResourceKind != "Deployment" || e.ResourceName != "default/web" || e.Operation != "validate" || e.Status != "success" {
		t.Fatalf("资源/状态字段异常: %+v", e)
	}
	if !strings.Contains(e.ResultSummary, `"skills":["workload"]`) ||
		!strings.Contains(e.ResultSummary, `"findings":2`) {
		t.Fatalf("result_summary 缺少 skill 观测数据: %s", e.ResultSummary)
	}
}

func TestSkillRunAuditorSummarizesNameOnly(t *testing.T) {
	st := persistTestStore(t)
	s := &chatService{store: st}
	ctx := context.Background()
	fn := s.skillRunAuditor(ctx, &session.Session{ID: "s2", Cluster: "c2"}, &runScope{sessID: "s2", turnID: "t2", stepID: "st2"})
	fn(tools.SkillRunReport{Kind: "ClusterRole", Name: "editor", Skills: []string{"rbac"}})
	events, err := st.Audit().List(ctx)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(events) != 1 || events[0].ResourceName != "editor" {
		t.Fatalf("无 namespace 时应直接用 name: %+v", events)
	}
}

func TestAuditServiceRecordDefaults(t *testing.T) {
	st := persistTestStore(t)
	svc := NewAuditService(st)
	ctx := context.Background()
	if err := svc.Record(ctx, store.AuditEvent{Action: store.ActionConfigChange}); err != nil {
		t.Fatalf("record: %v", err)
	}
	events, err := st.Audit().List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	e := events[0]
	if e.EventType != store.AuditEventConfigChange {
		t.Fatalf("缺省 EventType 应按 Action 推导, got %q", e.EventType)
	}
	if e.Timestamp.IsZero() {
		t.Fatal("缺省时间戳应自动填充")
	}
}

func TestAuditServiceRecordRequiresAction(t *testing.T) {
	st := persistTestStore(t)
	if err := NewAuditService(st).Record(context.Background(), store.AuditEvent{}); err == nil {
		t.Fatal("缺少 action 应报错")
	}
	if err := NewAuditService(nil).Record(context.Background(), store.AuditEvent{Action: "x"}); err == nil {
		t.Fatal("未装配 Store 应报错")
	}
}

func TestStoreRecorderWritesBothChannels(t *testing.T) {
	inner, err := audit.NewFileRecorder(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatalf("open recorder: %v", err)
	}
	st := persistTestStore(t)
	scope := &runScope{sessID: "s1", turnID: "t1", stepID: "st1"}
	rec := newStoreAuditRecorder(inner, st, scope, AuditScope{RequestID: "req-1", ClusterID: "c1", SessionID: "s1"})
	t.Cleanup(func() { _ = rec.Close() })
	if err := rec.Write(context.Background(), audit.Event{
		Timestamp: time.Now(),
		Action:    audit.ActionApprovalGranted,
		Payload:   map[string]any{"decision": "approved", "grant_id": "g-9"},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	events, err := st.Audit().List(context.Background())
	if err != nil {
		t.Fatalf("list store: %v", err)
	}
	if len(events) != 1 || events[0].GrantID != "g-9" || events[0].EventType != store.AuditEventGrant {
		t.Fatalf("store 侧应落地 GRANT 事件: %+v", events)
	}
}
