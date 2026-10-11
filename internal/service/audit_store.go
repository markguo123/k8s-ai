package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/audit"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/store"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// storeAuditRecorder 在保留三期 JSONL 审计的同时，把同一事件写入四期 AuditStore
// （P1.11：TOOL_CALL / APPROVAL / GRANT / RESOURCE_MUTATION / LLM_CALL ...）。
//
// 落库是观察性质：失败只告警，绝不中断 Agent 主循环（AGENTS.md：单个工具/观察
// 失败不得中止流程）。JSONL 仍照常写，保证四期库不可用时审计链路不丢。
type storeAuditRecorder struct {
	inner audit.Recorder
	st    store.Store
	scope *runScope
	// base 是运行期不变的关联字段（会话/集群）。
	base AuditScope
}

func (r *storeAuditRecorder) Write(ctx context.Context, e audit.Event) error {
	if err := r.inner.Write(ctx, e); err != nil {
		return err
	}
	if err := r.st.Audit().Write(ctx, toStoreAuditEvent(e, r.snapshot())); err != nil {
		slog.Warn("audit: 四期审计落库失败", "action", e.Action, "session", r.base.SessionID, "error", err)
	}
	return nil
}

func (r *storeAuditRecorder) Close() error { return r.inner.Close() }

// snapshot 取当前 turn/step 作用域，组装本条事件的关联字段。
func (r *storeAuditRecorder) snapshot() AuditScope {
	sc := r.base
	sc.TurnID = r.scope.turn()
	sc.StepID = r.scope.step()
	return sc
}

// newStoreAuditRecorder 把 JSONL 记录器包装成"JSONL + Store"双写记录器。
func newStoreAuditRecorder(inner audit.Recorder, st store.Store, scope *runScope, base AuditScope) audit.Recorder {
	return &storeAuditRecorder{inner: inner, st: st, scope: scope, base: base}
}

// skillRunAuditor 返回 create 通道的 Skill 观测回调：每次 Skill 校验写一条
// SKILL_RUN 审计（P1.11）。未装配 Store 时返回 nil，工具层因此不回调。
func (s *chatService) skillRunAuditor(ctx context.Context, sess *session.Session, scope *runScope) func(tools.SkillRunReport) {
	if s.store == nil {
		return nil
	}
	return func(rep tools.SkillRunReport) {
		target := rep.Name
		if rep.Namespace != "" {
			target = rep.Namespace + "/" + rep.Name
		}
		summary, err := json.Marshal(map[string]any{
			"skills":     rep.Skills,
			"findings":   rep.Findings,
			"escalation": rep.Escalation,
		})
		if err != nil {
			summary = []byte("{}")
		}
		event := store.AuditEvent{
			Timestamp:     time.Now().UTC(),
			SessionID:     sess.ID,
			TurnID:        scope.turn(),
			ClusterID:     sess.Cluster,
			Action:        store.ActionSkillRun,
			EventType:     store.AuditEventSkillRun,
			Operation:     "validate",
			ResourceKind:  rep.Kind,
			ResourceName:  target,
			ResultSummary: string(summary),
			Status:        "success",
		}
		if err := s.store.Audit().Write(ctx, &event); err != nil {
			slog.Warn("chat: SKILL_RUN 审计落库失败", "session", sess.ID, "skill_run", target, "error", err)
		}
	}
}
