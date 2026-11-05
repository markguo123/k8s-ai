// Package server 提供一期 1.2 最小化 HTTP 服务：健康检查、版本、异步扫描任务。
// 复用 service.ScanService；任务注册表为内存实现，单次扫描并发限制。
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"reflect"
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
	// StatusCancelled 表示发起人主动停止（P9.11）。它不复用 failed：中断与失败在报告口径上
	// 是两件事——失败要说「为什么没跑完」，中断只说明「有人决定不跑了」。
	StatusCancelled ScanStatus = "cancelled"
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
	// Owner 是发起人的用户 ID（P8.13）。任务结果含被扫命名空间的问题清单，
	// 没有 owner 就没法判「谁的任务」，GET 只能对全体登录用户开放。
	Owner string `json:"owner,omitempty"`
	// ScopeKind / ScopeValue 是本次体检范围，与 reports 的 scope 两列同源。
	ScopeKind string `json:"scopeKind,omitempty"`
	// Stages 是本次体检的阶段账（P9）：扫描过程中由进度回调实时刷新，结束后与报告
	//「诊断信息」区块同源，页面进度条读的就是它。
	Stages     []model.StageRecord `json:"stages,omitempty"`
	ScopeValue string              `json:"scopeValue,omitempty"`
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
	// reportSvc 是报告读取面（P8.5），nil 时报告端点返回 501。
	reportSvc reportService
	// storageSvc 是对象存储观测面（P8.5），nil 时 storage/health 返回 501。
	// 注意这里只准依赖 service 包的窄接口：本文件定义 handleReadyz，架构门禁禁止
	// 该文件引用 objectstore（方案 15 §11.1）。
	storageSvc storageHealthService
	perms      *permission.Enforcer
	// ready 是就绪探针回调（P6.1）：由组合根注入 store 连通 + 迁移完成判定。
	// nil 时按"进程活着即可用"处理，与一期行为一致。
	ready ReadinessCheck
	// scanBaseline 提供体检的连接类参数基线（P8.13）：请求体不再携带 kubeconfig /
	// reportDirectory 一类的内部参数，这些只能来自服务端配置。
	scanBaseline scanBaselineFunc
	mu           sync.Mutex
	jobs         map[string]*ScanJob
	// scanCancels 存放每个在跑任务的 context 取消函数（P9.11 停止体检）。
	// 它刻意不放进 ScanJob：ScanJob 会被整体复制成 HTTP 响应体，函数值混进去只会让
	// 「快照里有什么」变得难以推理。
	scanCancels map[string]context.CancelFunc
	running     bool
}

// ReadinessCheck 报告服务是否可以接活（P6.1 /readyz 判据）。返回的 error 文本
// 会出现在探针响应里，用于直接从 kubectl 看出未就绪原因（迁移未完成、存储不可写等）。
type ReadinessCheck func(context.Context) error

// New 创建服务（注入 ScanService）。
func New(svc service.ScanService) *Server {
	return &Server{
		svc:         svc,
		perms:       permission.NewEnforcer(DefaultAPIPolicy()),
		jobs:        map[string]*ScanJob{},
		scanCancels: map[string]context.CancelFunc{},
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

// WithReadiness 注入就绪探针判定（P6.1）。链式可选，不破坏既有构造器签名。
func (s *Server) WithReadiness(fn ReadinessCheck) *Server {
	s.ready = fn
	return s
}

// WithClusters 装配集群管理应用服务（P3.1-P3.3）。链式可选，不破坏既有构造器签名。
func (s *Server) WithClusters(svc clusterService) *Server {
	// 可选依赖常以具体指针（例如未配置加密密钥时的 *cluster.Service(nil)）
	// 传入；若直接存入接口，接口值本身非 nil，handler 会误判为已装配并
	// 调用 nil receiver。这里统一把 typed nil 视为未装配。
	if isNilService(svc) {
		s.clusterSvc = nil
		return s
	}
	s.clusterSvc = svc
	return s
}

func isNilService(svc any) bool {
	if svc == nil {
		return true
	}
	v := reflect.ValueOf(svc)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
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

// WithReports 装配报告读取面（P8.5）。链式可选，不破坏既有构造器签名。
func (s *Server) WithReports(svc reportService) *Server {
	// 与 WithClusters 同理：未接对象存储时调用方可能传进一个 typed nil 指针，
	// 直接存进接口会得到一个"非 nil 但一调就 panic"的值。
	if isNilService(svc) {
		s.reportSvc = nil
		return s
	}
	s.reportSvc = svc
	return s
}

// WithStorage 装配对象存储观测面（P8.5）。链式可选。
func (s *Server) WithStorage(svc storageHealthService) *Server {
	if isNilService(svc) {
		s.storageSvc = nil
		return s
	}
	s.storageSvc = svc
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
	mux.HandleFunc("GET /api/v1/auth/scope", s.handleMyScope)
	mux.HandleFunc("POST /api/v1/auth/password", s.handleChangePassword)
	mux.HandleFunc("POST /api/v1/scans", s.handleCreateScan)
	mux.HandleFunc("GET /api/v1/scans/{id}", s.handleGetScan)
	mux.HandleFunc("POST /api/v1/scans/{id}/cancel", s.handleCancelScan)
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
	mux.HandleFunc("GET /api/v1/reports", s.handleListReports)
	// 总览必须在 {id} 之前登记：Go 的 mux 让静态段优先于通配段，登记顺序只是把
	// 「summary 不是一个报告 ID」这件事写给读代码的人看。
	mux.HandleFunc("GET /api/v1/reports/summary", s.handleReportSummary)
	// 按需导出一份 Markdown：体检只归档一种产物时，另一个格式在下载这一刻渲染。
	mux.HandleFunc("GET /api/v1/reports/{id}/export", s.handleExportReportMarkdown)
	mux.HandleFunc("GET /api/v1/reports/{id}/content", s.handleGetReportContent)
	mux.HandleFunc("GET /api/v1/reports/{id}/report", s.handleGetReportStructured)
	mux.HandleFunc("DELETE /api/v1/reports/{id}", s.handleDeleteReport)
	mux.HandleFunc("POST /api/v1/reports/cleanup", s.handleCleanupReports)
	mux.HandleFunc("GET /api/v1/storage/health", s.handleStorageHealth)
	// 未登记的 /api/v1/* 一律回统一 JSON 错误体，不能被 SPA fallback 吞成 index.html
	// （P6.11：前端拿到 HTML 会比 404 更难排查）。
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		mux.HandleFunc(method+" /api/v1/{path...}", s.handleAPINotFound)
	}
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
	if s.ready == nil {
		// 未注入判据（例如纯 API 测试装配）：进程可运行即就绪。
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.ready(ctx); err != nil {
		// 只回原因摘要，不外泄连接串等细节；细节在启动日志里。
		writeError(w, r, http.StatusServiceUnavailable, codeUnavailable, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleAPINotFound 处理未登记的 API 路径（P6.11）。
func (s *Server) handleAPINotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotFound, codeNotFound, "unknown API path: "+r.Method+" "+r.URL.Path)
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeOK(w, r, http.StatusOK, map[string]string{
		"version": version.Version,
		"commit":  version.Commit,
		"date":    version.Date,
	})
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("scan-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
