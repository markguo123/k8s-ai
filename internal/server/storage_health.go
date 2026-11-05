package server

import (
	"context"
	"net/http"

	"github.com/k8s-ai/k8s-ai/internal/service"
)

// storageHealthService 是存储健康端点依赖的最小接口（service.BlobGateway 满足）。
type storageHealthService interface {
	Health(ctx context.Context) service.BlobHealth
}

// handleStorageHealth GET /api/v1/storage/health：对象存储的观测入口（方案 15 §13.1）。
//
// 它是唯一一处能看到对象存储状态的地方：/healthz 与 /readyz 刻意不含对象存储字段——
// 桶不可达时平台该继续接活，把探针打成红等于让一个可降级依赖去决定整个服务是否在线，
// 编排器还会顺手把 Pod 重启掉，故障反而被放大。
//
// 这个 handler 必须待在独立文件里：架构门禁禁止定义 handleReadyz 的那个文件引用
// objectstore（tests/arch/objectstore_test.go），而 Health 的返回体来自 service 包，
// 这里连 objectstore 都不 import，天然满足。
func (s *Server) handleStorageHealth(w http.ResponseWriter, r *http.Request) {
	if s.storageSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "object storage is not configured")
		return
	}
	health := s.storageSvc.Health(r.Context())
	// 状态码恒为 200：这是一个"报健康"的观测端点，不是探针。degraded 时回 503 会让前端的
	// 徽标变成一次请求失败，运维在浏览器里也就看不到那句真正有用的 ProbeError。
	writeOK(w, r, http.StatusOK, health)
}

// 编译期确认 service.BlobGateway 的观测方法签名与本文件假设一致（Health 单返回值）。
var _ storageHealthService = (*service.BlobGateway)(nil)
