package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/permission"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// 页面发起巡检的三档预设（方案 17 §5）。
//
// 它们是**行为预设**而不是现场调参项，所以锁在代码常量表里并进测试，不进配置中心：
// 一旦变成配置键，「快速体检为什么这次跑了 5 分钟」就没有确定答案了。
const (
	scanPresetQuick    = "quick"
	scanPresetStandard = "standard"
	scanPresetDeep     = "deep"
)

// defaultScanPreset 是缺省档位：标准巡检。
const defaultScanPreset = scanPresetStandard

// scanPresetSpec 是一个档位对 model.ScanOptions 的映射。
type scanPresetSpec struct {
	// timeout 是档位封顶值，最终取它与配置上限的较小者。
	timeout time.Duration
	// llm 表示该档位是否跑 LLM 诊断。
	llm bool
	// events / logs 是采集开关；logSince 是日志时间窗口（仅 logs 为真时有意义）。
	events   bool
	logs     bool
	logSince time.Duration
}

var scanPresets = map[string]scanPresetSpec{
	scanPresetQuick:    {timeout: 60 * time.Second, llm: false, events: true, logs: false},
	scanPresetStandard: {timeout: 5 * time.Minute, llm: true, events: true, logs: false},
	scanPresetDeep:     {timeout: 10 * time.Minute, llm: true, events: true, logs: true, logSince: time.Hour},
}

// scanRequest 是 POST /api/v1/scans 的白名单请求体（方案 17 §8）。
//
// 改造前这里直接 json.Decode 到 model.ScanOptions，等于把 Kubeconfig / ReportDirectory /
// Concurrency / Timeout 全交给调用方：前者能把扫描打到平台之外的集群，后者能让服务往
// 容器内任意可写路径落文件。白名单 + DisallowUnknownFields 是唯一稳的写法——字段集一旦
// 开放，将来给 ScanOptions 加任何新字段都会自动变成一条对外注入面。
type scanRequest struct {
	Scope     string `json:"scope"`
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Preset    string `json:"preset"`
	ClusterID string `json:"clusterId"`
}

// scanGateError 是发起闸门拒绝时的 HTTP 映射（状态码 + 错误码 + 给用户看的文案）。
type scanGateError struct {
	status  int
	code    string
	message string
}

func (e *scanGateError) Error() string { return e.message }

func scanDenied(message string) *scanGateError {
	return &scanGateError{status: http.StatusForbidden, code: codeForbidden, message: message}
}

func scanInvalid(message string) *scanGateError {
	return &scanGateError{status: http.StatusBadRequest, code: codeInvalidArgument, message: message}
}

// scanBaselineFunc 提供连接类与模型类参数的基线（由组合根构造，见 cli/server.go：
// 配置中心优先、静态文件兜底）。每次发起现取，配置中心改完下一次巡检即生效。
type scanBaselineFunc func(ctx context.Context) model.ScanOptions

// WithScanBaseline 注入连接类参数基线（P8.13）。链式可选；未注入时用零值
// （测试装配路径：连接参数由 service.ScanService 的 fake 忽略）。
func (s *Server) WithScanBaseline(fn scanBaselineFunc) *Server {
	s.scanBaseline = fn
	return s
}

func (s *Server) baselineOptions(ctx context.Context) model.ScanOptions {
	if s.scanBaseline == nil {
		return model.ScanOptions{}
	}
	return s.scanBaseline(ctx)
}

// handleCreateScan 发起一次页面巡检：白名单校验 → 授权闸门 → 档位映射 → 异步任务。
//
// 归属（谁触发的、扫的是什么范围）一律由服务端决定，不接受请求体值：请求方若能指定
// actor，就能把报告挂到别人名下；若能指定 cluster_id，就能借「系统报告按集群授权可见」
// 读到别人集群的问题清单（方案 17 §6）。
func (s *Server) handleCreateScan(w http.ResponseWriter, r *http.Request) {
	var req scanRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "invalid body: "+err.Error())
		return
	}

	u := UserFrom(r.Context())
	if u == nil {
		writeError(w, r, http.StatusUnauthorized, codeUnauthorized, "authentication required")
		return
	}
	role := RoleFrom(r.Context())

	opts, gateErr := s.buildScanOptions(r, u, role, req)
	if gateErr != nil {
		writeError(w, r, gateErr.status, gateErr.code, gateErr.message)
		return
	}

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		writeError(w, r, http.StatusConflict, codeConflict, "a scan is already running")
		return
	}
	job := &ScanJob{
		ID:         newID(),
		Status:     StatusPending,
		CreatedAt:  time.Now().UTC(),
		Owner:      u.ID,
		ScopeKind:  opts.Attribution.ScopeKind,
		ScopeValue: opts.Attribution.ScopeValue,
	}
	s.jobs[job.ID] = job
	s.running = true
	s.mu.Unlock()

	// ScanID 依赖任务 ID，只能在任务登记后回填（Attribution 是值类型，直接整体赋值）。
	opts.Attribution.ScanID = job.ID

	started := time.Now().UTC()
	job.Status = StatusRunning
	job.StartedAt = &started
	go s.runJob(job, opts)

	writeOK(w, r, http.StatusAccepted, job)
}

// buildScanOptions 把白名单请求翻译成内部 ScanOptions：范围闸门 + 档位映射 + 连接基线。
//
// 判定顺序是「先问能不能扫，再问怎么扫」：范围不合法时不该先把档位解析结果暴露出去。
func (s *Server) buildScanOptions(r *http.Request, u *store.User, role *store.Role, req scanRequest) (model.ScanOptions, *scanGateError) {
	presetName := strings.TrimSpace(req.Preset)
	if presetName == "" {
		presetName = defaultScanPreset
	}
	preset, ok := scanPresets[presetName]
	if !ok {
		return model.ScanOptions{}, scanInvalid("unknown preset: " + presetName + " (quick|standard|deep)")
	}

	scope := strings.TrimSpace(req.Scope)
	ns := strings.TrimSpace(req.Namespace)
	pod := strings.TrimSpace(req.Pod)
	if req.ClusterID != "" {
		// 多集群本期只留口不实现连通（方案 17 §6）：宁可现在拒绝，也不能让请求体里一个
		// clusterId 把扫描打到平台之外的集群上。
		return model.ScanOptions{}, scanInvalid("该集群尚未接入扫描：本期巡检目标固定为平台自身集群（clusterId 必须为空）")
	}

	scopeKind, scopeValue, targetNS, gateErr := s.resolveScanScope(r.Context(), u, role, scope, ns, pod)
	if gateErr != nil {
		return model.ScanOptions{}, gateErr
	}

	opts := s.baselineOptions(r.Context())
	opts.Namespace = targetNS
	opts.PodTarget = pod
	opts.FailOn = ""
	// 页面巡检不写容器本地副本：页面下载走对象存储，容器里再落一份文件没有消费方，
	// 还会把「PVC 没挂上」这类部署问题变成整次巡检失败（Run 写本地副本失败是硬错误）。
	// CLI 的 report.directory 语义一字未动，见方案 17 §7.3。
	opts.ReportMode = ""
	opts.ReportDirectory = ""
	opts.WithDoc = false
	applyScanPreset(&opts, preset)
	opts.Attribution = model.ReportAttribution{
		Actor:      u.ID,
		ScopeKind:  scopeKind,
		ScopeValue: scopeValue,
	}
	return opts, nil
}

// resolveScanScope 校验巡检范围并给出实际扫描的 namespace（方案 17 §4）。
//
// 返回的 scopeKind/scopeValue 是落 reports 的两列；targetNS 是传给扫描器的命名空间
// （全集群时为空串，扫描器把空串理解为「全部命名空间」）。
func (s *Server) resolveScanScope(ctx context.Context, u *store.User, role *store.Role, scope, ns, pod string) (scopeKind, scopeValue, targetNS string, gateErr *scanGateError) {
	switch scope {
	case model.ScopeCluster:
		if !serverRoleIsAdmin(role) {
			// 全集群巡检会读全命名空间的清单、事件与日志，超出 namespace 授权模型的表达力，
			// 只能由 admin 发起。
			return "", "", "", scanDenied("scope=cluster 仅管理员可发起：全集群巡检超出 namespace 授权范围")
		}
		return model.ScopeCluster, "", "", nil
	case model.ScopeNamespace:
		if ns == "" {
			return "", "", "", scanInvalid("scope=namespace 必须给出 namespace")
		}
		if gateErr := s.requireNamespaceAccess(ctx, u, role, ns); gateErr != nil {
			return "", "", "", gateErr
		}
		return model.ScopeNamespace, ns, ns, nil
	case model.ScopePod:
		if ns == "" || pod == "" {
			return "", "", "", scanInvalid("scope=pod 必须同时给出 namespace 与 pod")
		}
		if gateErr := s.requireNamespaceAccess(ctx, u, role, ns); gateErr != nil {
			return "", "", "", gateErr
		}
		return model.ScopePod, ns + "/" + pod, ns, nil
	default:
		return "", "", "", scanInvalid("scope 必填且只能是 cluster / namespace / pod")
	}
}

// requireNamespaceAccess 判定非 admin 调用者能否扫指定命名空间。
//
// 越权一律 403 并点名是哪个 ns，**绝不静默裁剪成授权子集**：静默裁剪会让人以为扫了全量，
// 而巡检结论的价值恰恰在于「我知道我扫了什么」。
func (s *Server) requireNamespaceAccess(ctx context.Context, u *store.User, role *store.Role, ns string) *scanGateError {
	if serverRoleIsAdmin(role) {
		return nil
	}
	if s.authzSvc == nil || role == nil {
		// 判定不了就是不允许：这里默认放行等于把 namespace 授权当成装饰。
		return scanDenied("无法校验 namespace 授权（授权服务未装配），拒绝发起巡检")
	}
	es, err := s.authzSvc.EffectiveScope(ctx, u, role, "")
	if err != nil {
		// 授权查询失败与「没授权」在两回事，但对外都只能拒：把内部错误原文回给调用方
		// 会把 grant 结构泄露出去。
		return &scanGateError{status: http.StatusInternalServerError, code: codeInternal,
			message: "校验 namespace 授权失败，请稍后重试"}
	}
	if es == nil {
		return scanDenied("无法校验 namespace 授权，拒绝发起巡检")
	}
	if es.ClusterScoped || es.IsAdmin {
		return nil
	}
	for _, allowed := range es.Namespaces {
		if allowed == ns {
			return nil
		}
	}
	return scanDenied("命名空间 " + ns + " 不在你的授权范围内，未发起巡检（不会自动改扫已授权的命名空间）")
}

// serverRoleIsAdmin 判定角色是否持全域权限（与报告面 reportScopeFor 同一判据）。
func serverRoleIsAdmin(role *store.Role) bool {
	return role != nil && permission.HasPermission(role.Permissions, "*")
}

// applyScanPreset 把档位映射到 ScanOptions。
//
// 两条刻意的取舍：
//
//  1. 档位只能**关掉** LLM，不能凭空打开。opts.LLM.Enabled 来自部署配置，没有凭据时
//     把它置真是把部署问题变成用户面前的巡检失败；「无 LLM 配置时快速体检仍出结论」
//     这条验收靠的就是这里不无中生有。
//  2. Timeout 取档位封顶与配置上限的较小者。两个方向都不能单独生效：只看档位会让现场
//     的 scan.timeout=30s 形同虚设，只看配置会让「快速体检」跑成十分钟。
func applyScanPreset(opts *model.ScanOptions, p scanPresetSpec) {
	opts.CollectEvents = p.events
	opts.CollectLogs = p.logs
	if p.logs {
		if p.logSince > 0 {
			opts.Since = p.logSince
		}
	} else {
		opts.CollectPreviousLogs = false
		opts.Since = 0
	}
	opts.LLM.Enabled = opts.LLM.Enabled && p.llm

	limit := p.timeout
	if opts.Timeout > 0 && opts.Timeout < limit {
		limit = opts.Timeout
	}
	opts.Timeout = limit
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

// handleGetScan 查询任务状态：只有发起人本人与 admin 可读（方案 17 §4.5）。
//
// 任务结果里含被扫命名空间的问题清单，任何持 cluster.read 的人凭 job id 都能读别人
// 的任务就是越权。非本人的任务返回与「不存在」同形的 404——job id 同样是 8 字节随机串，
// 不该变成一个能探测存在性的枚举面。
func (s *Server) handleGetScan(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	if u == nil {
		writeError(w, r, http.StatusUnauthorized, codeUnauthorized, "authentication required")
		return
	}
	id := r.PathValue("id")
	s.mu.Lock()
	job, ok := s.jobs[id]
	s.mu.Unlock()
	if !ok {
		writeError(w, r, http.StatusNotFound, codeNotFound, "scan not found")
		return
	}
	if job.Owner != "" && job.Owner != u.ID && !serverRoleIsAdmin(RoleFrom(r.Context())) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "scan not found")
		return
	}
	writeOK(w, r, http.StatusOK, job)
}
