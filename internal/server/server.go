// Package server 提供一期 1.2 最小化 HTTP 服务：健康检查、版本、异步扫描任务。
// 复用 service.ScanService；任务注册表为内存实现，单次扫描并发限制。
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/version"
)

// ScanStatus 扫描任务状态。
type ScanStatus string

const (
	StatusPending   ScanStatus = "pending"
	StatusRunning   ScanStatus = "running"
	StatusSucceeded ScanStatus = "succeeded"
	StatusFailed    ScanStatus = "failed"
)

// ScanJob 内存任务。
type ScanJob struct {
	ID        string            `json:"id"`
	Status    ScanStatus        `json:"status"`
	Result    *model.ScanResult `json:"result,omitempty"`
	Error     string            `json:"error,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
	StartedAt *time.Time        `json:"startedAt,omitempty"`
	EndedAt   *time.Time        `json:"endedAt,omitempty"`
}

// Server 最小化 HTTP 服务。
type Server struct {
	svc     service.ScanService
	mu      sync.Mutex
	jobs    map[string]*ScanJob
	running bool
}

// New 创建服务（注入 ScanService）。
func New(svc service.ScanService) *Server {
	return &Server{svc: svc, jobs: map[string]*ScanJob{}}
}

// Handler 返回路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.HandleFunc("POST /api/v1/scans", s.handleCreateScan)
	mux.HandleFunc("GET /api/v1/scans/{id}", s.handleGetScan)
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	// 无外部依赖；服务可运行即就绪。
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version": version.Version,
		"commit":  version.Commit,
		"date":    version.Date,
	})
}

// handleCreateScan 创建异步扫描任务；单次扫描并发限制，已有任务运行时返回 409。
func (s *Server) handleCreateScan(w http.ResponseWriter, r *http.Request) {
	var opts model.ScanOptions
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&opts); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
		return
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a scan is already running"})
		return
	}
	s.running = true
	job := &ScanJob{ID: newID(), Status: StatusPending, CreatedAt: time.Now().UTC()}
	s.jobs[job.ID] = job
	s.mu.Unlock()

	started := time.Now().UTC()
	job.Status = StatusRunning
	job.StartedAt = &started
	go s.runJob(job, opts)

	writeJSON(w, http.StatusAccepted, job)
}

// runJob 后台执行扫描（ctx 带 opts.Timeout 预算）。
func (s *Server) runJob(job *ScanJob, opts model.ScanOptions) {
	ctx := context.Background()
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	result, err := s.svc.Run(ctx, opts)
	ended := time.Now().UTC()
	s.mu.Lock()
	job.EndedAt = &ended
	s.running = false
	if err != nil {
		job.Status = StatusFailed
		job.Error = err.Error()
	} else {
		job.Status = StatusSucceeded
		job.Result = result
	}
	s.mu.Unlock()
}

func (s *Server) handleGetScan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	job, ok := s.jobs[id]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "scan not found"})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("scan-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
