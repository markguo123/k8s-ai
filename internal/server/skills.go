package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/k8s-ai/k8s-ai/internal/skills"
	"github.com/k8s-ai/k8s-ai/internal/store"
	"gopkg.in/yaml.v3"
)

// maxSkillsBody 限制声明式 YAML 导入体大小（1MB 足够且防滥用）。
const maxSkillsBody = 1 << 20

// maxProceduralDocsBytes 限制原始 SKILL.md 文本导入体大小（P3.8.2）。
// 单篇正文上限由 skills 包再按 512KiB 二次收口。
const maxProceduralDocsBytes = 2 << 20

// maxZipUploadBytes 限制技能包 zip 的压缩上传大小（P3.8.3）；解压后总大小
// 由 skills.ParseProceduralZip 内 16MiB 上限兜底，防御 zip 炸弹。
const maxZipUploadBytes = 32 << 20

// skillsService 是 Skill 平台端点依赖的应用服务接口（service.SkillPlatform 满足）。
// Handler 只依赖该接口，不直接触碰 Store（需求 v1.2 §24）。列表读取（P3.5）、
// 导入与单条读取（P3.6）、启停（P3.7）都走这组能力；命中的运行元数据
// （hit_count/last_hit/last_validation）由 create_resource 审计回调写入，不在管理 API 暴露。
type skillsService interface {
	List(ctx context.Context) ([]*store.Skill, error)
	Get(ctx context.Context, name string) (*store.Skill, error)
	Upsert(ctx context.Context, sk *store.Skill) error
	SetEnabled(ctx context.Context, name string, enabled bool) error
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

// handleGetSkill GET /api/v1/skills/{name}：单条读取（P3.6），读不到 404。
func (s *Server) handleGetSkill(w http.ResponseWriter, r *http.Request) {
	if s.skillsSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "skills is not configured")
		return
	}
	rec, err := s.skillsSvc.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, codeNotFound, "skill not found")
			return
		}
		slog.Warn("server: Skill 单条读取失败", "name", r.PathValue("name"), "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "get skill failed")
		return
	}
	writeOK(w, r, http.StatusOK, rec)
}

// handleImportSkills POST /api/v1/skills/import：请求体是原始 YAML 文本，
// 支持单个 Skill 或用 --- 分隔的多文档。解析与规则校验（来源合法、无执行字段、
// 风险只升不降）整批通过后才入库，任一项非法则整批不落库，避免半更新。
func (s *Server) handleImportSkills(w http.ResponseWriter, r *http.Request) {
	if s.skillsSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "skills is not configured")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSkillsBody))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "非法 YAML: "+err.Error())
		return
	}
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	var batch []*store.Skill
	seen := map[string]bool{}
	for {
		var spec skills.Skill
		err := dec.Decode(&spec)
		if err == io.EOF {
			break
		}
		if err != nil {
			writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "非法 YAML: "+err.Error())
			return
		}
		src := spec.Source
		if src == "" {
			src = skills.SourceCustom // 缺省视为本平台自定义 Skill
		}
		if src == skills.SourceBuiltin {
			writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "内置 Skill 不可通过导入覆盖: "+spec.Name)
			return
		}
		spec.Source = src
		// 整批先校验再入库：走 skills 内建校验（third-party 附 author/description/version），
		// 同一批内名称重复也在此拒绝。
		if err := skills.New().Register(spec); err != nil {
			writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "Skill 校验未通过: "+err.Error())
			return
		}
		if seen[spec.Name] {
			writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "Skill 校验未通过: 批量导入中出现重复名称 "+spec.Name)
			return
		}
		seen[spec.Name] = true
		batch = append(batch, &store.Skill{Name: spec.Name, Spec: spec, Source: string(src), Enabled: true})
	}
	if len(batch) == 0 {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "导入内容为空")
		return
	}
	for _, sk := range batch {
		if err := s.skillsSvc.Upsert(r.Context(), sk); err != nil {
			slog.Warn("server: Skill 导入落库失败", "skill", sk.Name, "error", err)
			writeError(w, r, http.StatusInternalServerError, codeInternal, "import skills failed")
			return
		}
	}
	writeOK(w, r, http.StatusCreated, batch)
}

// handleImportProceduralDocs POST /api/v1/skills/import-procedural：请求体是
// 原始 SKILL.md 文本，支持单篇或用 --- 分隔的多篇（P3.8.2）。每篇由
// skills.ParseProceduralDocs 解析（要求 frontmatter 声明 name/description，
// 正文只作文本入库、不执行），再逐条 Register 校验整批通过后才落库。
func (s *Server) handleImportProceduralDocs(w http.ResponseWriter, r *http.Request) {
	if s.skillsSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "skills is not configured")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxProceduralDocsBytes))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "非法 SKILL.md 文本: "+err.Error())
		return
	}
	parsed, err := skills.ParseProceduralDocs(body)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "Skill 校验未通过: "+err.Error())
		return
	}
	// 整批先校验再落库；解析层已强制 name/description/body。
	var batch []*store.Skill
	for _, sk := range parsed {
		if err := skills.New().Register(sk); err != nil {
			writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "Skill 校验未通过: "+err.Error())
			return
		}
		batch = append(batch, &store.Skill{Name: sk.Name, Spec: sk, Source: string(sk.Source), Enabled: true})
	}
	for _, sk := range batch {
		if err := s.skillsSvc.Upsert(r.Context(), sk); err != nil {
			slog.Warn("server: procedural Skill 导入落库失败", "skill", sk.Name, "error", err)
			writeError(w, r, http.StatusInternalServerError, codeInternal, "import skills failed")
			return
		}
	}
	writeOK(w, r, http.StatusCreated, batch)
}

// handleImportProceduralZip POST /api/v1/skills/import-zip：multipart 上传
// 单个技能包 zip（P3.8.3），仅支持根目录唯一 SKILL.md 主文 + 白名单文本资产；
// scripts/ 一律按惰性资产（inert）入库，任何内容都不进入 Agent 工具路径。
func (s *Server) handleImportProceduralZip(w http.ResponseWriter, r *http.Request) {
	if s.skillsSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "skills is not configured")
		return
	}
	if err := r.ParseMultipartForm(maxZipUploadBytes); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "非法技能包上传: "+err.Error())
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "缺少上传文件字段 file")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxZipUploadBytes+1))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "读取技能包失败: "+err.Error())
		return
	}
	if int64(len(data)) > maxZipUploadBytes {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "技能包超过上传大小上限")
		return
	}
	parsed, err := skills.ParseProceduralZip(data)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "Skill 校验未通过: "+err.Error())
		return
	}
	var batch []*store.Skill
	for _, sk := range parsed {
		if err := skills.New().Register(sk); err != nil {
			writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "Skill 校验未通过: "+err.Error())
			return
		}
		batch = append(batch, &store.Skill{Name: sk.Name, Spec: sk, Source: string(sk.Source), Enabled: true})
	}
	for _, sk := range batch {
		if err := s.skillsSvc.Upsert(r.Context(), sk); err != nil {
			slog.Warn("server: procedural Skill 导入落库失败", "skill", sk.Name, "error", err)
			writeError(w, r, http.StatusInternalServerError, codeInternal, "import skills failed")
			return
		}
	}
	writeOK(w, r, http.StatusCreated, batch)
}

// handlePatchSkillEnabled PATCH /api/v1/skills/{name}/enabled：body {"enabled": true|false}。
// 停用后内存 registry 立即移除该 Skill（P3.7：启停生效，create_resource 不再命中）。
func (s *Server) handlePatchSkillEnabled(w http.ResponseWriter, r *http.Request) {
	if s.skillsSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "skills is not configured")
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "invalid body: "+err.Error())
		return
	}
	if req.Enabled == nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "enabled is required")
		return
	}
	if err := s.skillsSvc.SetEnabled(r.Context(), r.PathValue("name"), *req.Enabled); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, codeNotFound, "skill not found")
			return
		}
		slog.Warn("server: Skill 启停更新失败", "name", r.PathValue("name"), "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "update skill failed")
		return
	}
	rec, err := s.skillsSvc.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		slog.Warn("server: Skill 启停后回读失败", "name", r.PathValue("name"), "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "update skill failed")
		return
	}
	writeOK(w, r, http.StatusOK, rec)
}
