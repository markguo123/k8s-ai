package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/store"
)

// 停止体检（P9.11）。三条口径值得写下来：
//
//  1. 只能停自己发起的那次。他人任务与不存在的任务同形 404，与 GET 任务详情一致：
//     job id 不该变成一个能探测存在性、更不该变成一条别人可以按下的按钮。
//  2. 停止靠 context 向上传播，不强杀 goroutine。采集、规则、关系、诊断每一步都在用
//     同一个 ctx 调 API Server，取消后它会在下一个调用点自己退出，正在飞的那一次请求
//     由 client-go 收尾——这也是取消不需要扫描器配合实现的原因。
//  3. s.running 要等 runJob 真正退出才放开。否则「停止」当场放行第二次体检，
//     等于两个采集器同时打同一个 API Server，比不允许多跑更糟。

// registerScanCancelLocked 登记任务的取消函数。**调用方必须已持有 s.mu**（发起体检时
// 正是在那把锁里登记任务），所以它自己不再取锁——sync.Mutex 不可重入，这里再 Lock 一次
// 就是把服务挂死。
func (s *Server) registerScanCancelLocked(id string, cancel context.CancelFunc) {
	if s.scanCancels == nil {
		s.scanCancels = map[string]context.CancelFunc{}
	}
	s.scanCancels[id] = cancel
}

// clearScanCancel 收回取消函数（任务退出时调用，锁可重入安全：调用方不持锁）。
func (s *Server) clearScanCancel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.scanCancels, id)
}

// handleCancelScan POST /api/v1/scans/{id}/cancel：发起人主动停止一次体检。
//
// 状态在返回前就写成 cancelled，页面下一次刷就停下：等 goroutine 真正退出可能要几秒，
// 让用户的按钮等那几秒是把「我已经按了」这件事说清楚的成本。
func (s *Server) handleCancelScan(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	if u == nil {
		writeError(w, r, http.StatusUnauthorized, codeUnauthorized, "authentication required")
		return
	}
	id := r.PathValue("id")

	s.mu.Lock()
	job, ok := s.jobs[id]
	if !ok || (job.Owner != "" && job.Owner != u.ID && !serverRoleIsAdmin(RoleFrom(r.Context()))) {
		s.mu.Unlock()
		writeError(w, r, http.StatusNotFound, codeNotFound, "scan not found")
		return
	}
	if job.Status != StatusPending && job.Status != StatusRunning {
		s.mu.Unlock()
		writeError(w, r, http.StatusConflict, codeConflict, "该次体检已经结束，无需停止")
		return
	}
	ended := time.Now().UTC()
	job.Status = StatusCancelled
	job.EndedAt = &ended
	job.Error = "已由发起人停止"
	freezeRunningStages(job)
	snapshot := *job
	cancel := s.scanCancels[id]
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	s.recordScanCancel(r, u, snapshot)
	writeOK(w, r, http.StatusOK, snapshot)
}

// freezeRunningStages 把还挂在 running 的阶段落成 failed 并写明是停止造成的。
//
// 不做这件事的话，页面上会留一个永远转圈的步骤：一个不会结束的进度条比没有进度条更糟，
// 它会让人以为集群还在被采集，而实际上什么都没了。
func freezeRunningStages(job *ScanJob) {
	for i := range job.Stages {
		if job.Stages[i].Status != "running" {
			continue
		}
		job.Stages[i].Status = "failed"
		job.Stages[i].Detail = "已停止"
		job.Stages[i].Progress = ""
	}
}

// recordScanCancel 记一次审计：谁在什么时候中断了对哪个范围的一次采集。
func (s *Server) recordScanCancel(r *http.Request, u *store.User, job ScanJob) {
	if s.auditlog == nil || u == nil {
		return
	}
	summary, err := json.Marshal(map[string]any{
		"scanId":     job.ID,
		"scopeKind":  job.ScopeKind,
		"scopeValue": job.ScopeValue,
	})
	if err != nil {
		summary = []byte("{}")
	}
	_ = s.auditlog.Record(r.Context(), store.AuditEvent{
		Action:        store.ActionScanCancel,
		EventType:     store.EventTypeForAction(store.ActionScanCancel),
		RequestID:     RequestIDFrom(r.Context()),
		UserID:        u.ID,
		Operation:     "cancel",
		ResourceKind:  "scan",
		ResourceName:  job.ID,
		Status:        "success",
		ResultSummary: string(summary),
	})
}
