package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/k8s-ai/k8s-ai/internal/store"
)

// skillsService 是 Skill 平台只读端点依赖的应用服务接口（service.SkillPlatform 满足）。
// Handler 只依赖该接口，不直接触碰 Store（需求 v1.2 §24）。P3.6 导入 / P3.7 启停
// 在后续小批统一以 PATCH/POST 挂到本组路由。
type skillsService interface {
	List(ctx context.Context) ([]*store.Skill, error)
	Reload(ctx context.Context) error
}

// handleListSkills GET /api/v1/skills：Skill 记录列表（含运行元数据，仅内置方法论
// 与校验规则，不含任何执行动作字段或敏感内容）。
func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	if s.skillsSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "skills is not configured")
		return
	}
	list, err := s.skillsSvc.List(r.Context())
	if err != nil {
		slog.Warn("server: Skill 平台列表读取失败", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "list skills failed")
		return
	}
	writeOK(w, r, http.StatusOK, list)
}
