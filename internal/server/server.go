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

	"github.com/k8s-ai/k8s-ai/internal/auth"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/permission"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/store"
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
	svc  service.ScanService
	auth authService
	// settings 是配置中心应用服务（P1.10），nil 时配置端点返回 501。
	settings settingsService
	// auditlog 写系统事件（LOGIN/LOGOUT 等，P1.11），nil 时跳过。
	auditlog auditWriter
	// clusterSvc 是集群管理应用服务（P3.1-P3.3），nil 时集群端点返回 501。
	clusterSvc clusterService
	// skillsSvc 是 Skill 平台应用服务（P3.5），nil 时 Skill 端点返回 501。
	skillsSvc skillsService
	// authzSvc 是授权面应用服务（P3.11），nil 时 grants/tokens 端点返回 501。
	authzSvc authzService
	perms     *permission.Enforcer
	mu        sync.Mutex
	jobs      map[string]*ScanJob
	running   bool
}

// New 创建服务（注入 ScanService）。
func New(svc service.ScanService) *Server {
	return &Server{
		svc:   svc,
		perms: permission.NewEnforcer(DefaultAPIPolicy()),
		jobs:  map[string]*ScanJob{},
	}
}

// NewWithAuth 创建服务并启用产品内认证端点（P1.5）。
func NewWithAuth(svc service.ScanService, auth authService) *Server {
	s := New(svc)
	s.auth = auth
	return s
}

// NewWithPlatform 创建服务并装配四期平台底座依赖（P1.10/P1.11）：
// 配置中心应用服务与系统事件审计写入。auth 仍由调用方按需注入。
func NewWithPlatform(svc service.ScanService, auth authService, settingsSvc settingsService, audit auditWriter) *Server {
	s := NewWithAuth(svc, auth)
	s.settings = settingsSvc
	s.auditlog = audit
	return s
}

// WithClusters 装配集群管理应用服务（P3.1-P3.3）。链式可选，不破坏既有构造器签名。
func (s *Server) WithClusters(svc clusterService) *Server {
	s.clusterSvc = svc
	return s
}

// WithSkills 装配 Skill 平台应用服务（P3.5）。链式可选，不破坏既有构造器签名。
func (s *Server) WithSkills(svc skillsService) *Server {
	s.skillsSvc = svc
	return s
}

// WithAuthz 装配授权面应用服务（P3.11）。链式可选，不破坏既有构造器签名。
func (s *Server) WithAuthz(svc authzService) *Server {
	s.authzSvc = svc
	return s
}

// Handler 返回路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/auth/me", s.handleMe)
	mux.HandleFunc("POST /api/v1/scans", s.handleCreateScan)
	mux.HandleFunc("GET /api/v1/scans/{id}", s.handleGetScan)
	mux.HandleFunc("POST /api/v1/clusters", s.handleCreateCluster)
	mux.HandleFunc("POST /api/v1/clusters/test", s.handleTestCluster)
	mux.HandleFunc("GET /api/v1/clusters", s.handleListClusters)
	mux.HandleFunc("GET /api/v1/clusters/current", s.handleCurrentCluster)
	mux.HandleFunc("GET /api/v1/clusters/{id}", s.handleGetCluster)
	mux.HandleFunc("DELETE /api/v1/clusters/{id}", s.handleDeleteCluster)
	mux.HandleFunc("GET /api/v1/settings/{group}", s.handleGetSettings)
	mux.HandleFunc("PATCH /api/v1/settings/{group}", s.handlePatchSettings)
	mux.HandleFunc("GET /api/v1/users", s.handleListUsers)
	mux.HandleFunc("POST /api/v1/users", s.handleCreateUser)
	mux.HandleFunc("PATCH /api/v1/users/{id}", s.handleUpdateUser)
	mux.HandleFunc("DELETE /api/v1/users/{id}", s.handleDeleteUser)
	mux.HandleFunc("GET /api/v1/roles", s.handleListRoles)
	mux.HandleFunc("GET /api/v1/grants", s.handleListGrants)
	mux.HandleFunc("GET /api/v1/grants/{id}", s.handleGetGrant)
	mux.HandleFunc("POST /api/v1/grants", s.handleCreateGrant)
	mux.HandleFunc("PATCH /api/v1/grants/{id}", s.handleUpdateGrant)
	mux.HandleFunc("DELETE /api/v1/grants/{id}", s.handleDeleteGrant)
	mux.HandleFunc("GET /api/v1/users/{userID}/tokens", s.handleListTokens)
	mux.HandleFunc("POST /api/v1/users/{userID}/tokens", s.handleCreateToken)
	mux.HandleFunc("PATCH /api/v1/users/{userID}/tokens/{id}", s.handlePatchToken)
	mux.HandleFunc("DELETE /api/v1/users/{userID}/tokens/{id}", s.handleDeleteToken)
	mux.HandleFunc("GET /api/v1/skills", s.handleListSkills)
	mux.HandleFunc("GET /api/v1/skills/{name}", s.handleGetSkill)
	mux.HandleFunc("POST /api/v1/skills/import", s.handleImportSkills)
	mux.HandleFunc("POST /api/v1/skills/import-procedural", s.handleImportProceduralDocs)
	mux.HandleFunc("POST /api/v1/skills/import-zip", s.handleImportProceduralZip)
	mux.HandleFunc("PATCH /api/v1/skills/{name}/enabled", s.handlePatchSkillEnabled)
	return WithMiddleware(mux, DefaultCORS(), s.perms, s.principal)
}

// principal 由 Token 解析当前用户与角色；未配置认证服务时视为未认证。
func (s *Server) principal(ctx context.Context, token string) (*store.User, *store.Role, error) {
	if s.auth == nil {
		return nil, nil, auth.ErrInvalidToken
	}
	return s.auth.UserByToken(ctx, token)
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
	writeOK(w, r, http.StatusOK, map[string]string{
		"version": version.Version,
		"commit":  version.Commit,
		"date":    version.Date,
	})
}

// handleCreateScan 创建异步扫描任务；单次扫描并发限制，已有任务运行时返回 409。
func (s *Server) handleCreateScan(w http.ResponseWriter, r *http.Request) {
	var opts model.ScanOptions
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&opts); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "invalid body: "+err.Error())
		return
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		writeError(w, r, http.StatusConflict, codeConflict, "a scan is already running")
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

	writeOK(w, r, http.StatusAccepted, job)
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
		writeError(w, r, http.StatusNotFound, codeNotFound, "scan not found")
		return
	}
	writeOK(w, r, http.StatusOK, job)
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("scan-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
