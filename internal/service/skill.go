package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/skills"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// SkillPlatform 是四期 Skill 平台应用服务（P3.5）：把持久化在 SkillStore 的
// Skill 记录（builtin / custom / third-party）装配成只读 skills.Registry，并
// 读写运行元数据（enabled / validation_status / hit 统计）。
//
// 分层约定：Skill 侧始终是声明式方法论与校验规则（无执行动作字段，架构测试
// 已锁定），本服务只做持久化装配与运行元数据管理；所有写操作仍走
// create_resource -> internal/executor（P3.8 接入）。
//
// 一致性策略：任何变更都先落库（store.Upsert），再 Reload 重建内存 registry，
// 保证管理 API 读到的记录与工具路径命中的 Skill 永远一致。P3.6 导入、
// P3.7 启停/命中记录、P3.8 风险升档都在此底座上扩展。
type SkillPlatform struct {
	store store.SkillStore
	reg   skills.Registry
	now   func() time.Time
}

// NewSkillPlatform 创建 Skill 平台服务。调用方装配时应先 SeedBuiltins + Reload。
func NewSkillPlatform(st store.SkillStore) *SkillPlatform {
	return &SkillPlatform{store: st, now: time.Now}
}

// SeedBuiltins 把内置 Skill 种子幂等写入 SkillStore（builtin + enabled），
// 供管理 API / 工具路径在无 migrate 命令的环境下仍可读取。
func (s *SkillPlatform) SeedBuiltins(ctx context.Context) error {
	reg, err := skills.NewBuiltin()
	if err != nil {
		return err
	}
	now := s.now().UTC()
	for _, sk := range reg.List() {
		if err := s.store.Upsert(ctx, &store.Skill{
			Name: sk.Name, Spec: sk, Source: string(sk.Source),
			Enabled: true, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return fmt.Errorf("skills: seed %s: %w", sk.Name, err)
		}
	}
	return nil
}

// Reload 从 SkillStore 重建内存 registry：仅 enabled 的 Skill 进入命中表；
// disabled 记录仍保留在 Store 供管理读取。
func (s *SkillPlatform) Reload(ctx context.Context) error {
	recs, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("skills: list store: %w", err)
	}
	reg := skills.New()
	for _, rec := range recs {
		if !rec.Enabled {
			continue
		}
		if err := reg.Register(rec.Spec); err != nil {
			return fmt.Errorf("skills: load %s: %w", rec.Name, err)
		}
	}
	s.reg = reg
	return nil
}

// Registry 返回当前只读 Skill registry（供 create_resource / 工具路径消费）。
// 未 Reload 时退回内置 registry，保证工具路径不因平台未初始化而空转。
func (s *SkillPlatform) Registry() skills.Registry {
	if s.reg == nil {
		if b, err := skills.NewBuiltin(); err == nil {
			return b
		}
		return skills.New()
	}
	return s.reg
}

// List 返回全部 Skill 记录（含运行元数据，管理 API 读取用），按名称排序。
func (s *SkillPlatform) List(ctx context.Context) ([]*store.Skill, error) {
	recs, err := s.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("skills: list store: %w", err)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Name < recs[j].Name })
	return recs, nil
}

// Get 返回单条 Skill 记录（P3.6 管理 API 单条读取，含运行元数据）。
func (s *SkillPlatform) Get(ctx context.Context, name string) (*store.Skill, error) {
	rec, err := s.store.Get(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("skills: get %s: %w", name, err)
	}
	return rec, nil
}

// Upsert 保存一条 Skill 记录（custom / third-party 导入路径，P3.6 复用）。
// spec 需通过 skills 内建校验（来源合法、无执行动作字段、风险只升不降）。
func (s *SkillPlatform) Upsert(ctx context.Context, sk *store.Skill) error {
	if sk == nil {
		return fmt.Errorf("skills: skill is required")
	}
	// 用 Registry.Register 走 skills 包内建校验（含来源与风险约束）。
	if err := skills.New().Register(sk.Spec); err != nil {
		return fmt.Errorf("skills: invalid spec: %w", err)
	}
	if sk.Source == "" {
		sk.Source = string(sk.Spec.Source)
	}
	now := s.now().UTC()
	if sk.CreatedAt.IsZero() {
		sk.CreatedAt = now
	}
	sk.UpdatedAt = now
	if err := s.store.Upsert(ctx, sk); err != nil {
		return fmt.Errorf("skills: upsert: %w", err)
	}
	return s.Reload(ctx)
}

// SetEnabled 启停 Skill（P3.7 后端接口的基础），落库后重建内存 registry。
func (s *SkillPlatform) SetEnabled(ctx context.Context, name string, enabled bool) error {
	rec, err := s.store.Get(ctx, name)
	if err != nil {
		return err
	}
	rec.Enabled = enabled
	rec.UpdatedAt = s.now().UTC()
	if err := s.store.Upsert(ctx, rec); err != nil {
		return fmt.Errorf("skills: update enabled: %w", err)
	}
	return s.Reload(ctx)
}

// RecordHit 记录一次命中：hit_count+1 并刷新 last_hit（P3.7/P3.8 命中记录）。
func (s *SkillPlatform) RecordHit(ctx context.Context, name string) error {
	rec, err := s.store.Get(ctx, name)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	rec.HitCount++
	rec.LastHit = &now
	rec.UpdatedAt = now
	if err := s.store.Upsert(ctx, rec); err != nil {
		return fmt.Errorf("skills: record hit: %w", err)
	}
	return nil
}

// SetValidation 写校验结果：validation_status + last_validation（P3.7）。
func (s *SkillPlatform) SetValidation(ctx context.Context, name, status string) error {
	rec, err := s.store.Get(ctx, name)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	rec.ValidationStatus = status
	rec.LastValidation = &now
	rec.UpdatedAt = now
	if err := s.store.Upsert(ctx, rec); err != nil {
		return fmt.Errorf("skills: update validation: %w", err)
	}
	return nil
}
