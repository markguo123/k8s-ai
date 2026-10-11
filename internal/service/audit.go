package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/store"
)

// AuditService 是系统事件（登录/登出/配置变更/Skill 运行等）的写入口（P1.11）。
//
// CLI、Server、CronJob 复用同一个服务：调用方只描述"发生了什么"，时间戳、
// 事件类型缺省值与脱敏截断由本服务和 Store 负责（Handler 不直接操作 Store，
// 需求 v1.2 §24）。
type AuditService struct {
	store store.Store
}

// NewAuditService 创建审计服务（st 为空视为未装配，Record 直接报错）。
func NewAuditService(st store.Store) *AuditService {
	return &AuditService{store: st}
}

// Record 写入一条审计事件。Secret/kubeconfig 明文由 Store 侧按键名规则剔除，
// 调用方传入的摘要仍应尽量避免携带凭据。
func (s *AuditService) Record(ctx context.Context, e store.AuditEvent) error {
	if s == nil || s.store == nil {
		return errors.New("audit: store not configured")
	}
	if e.Action == "" {
		return errors.New("audit: action is required")
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	if e.EventType == "" {
		e.EventType = store.EventTypeForAction(e.Action)
	}
	if err := s.store.Audit().Write(ctx, &e); err != nil {
		return fmt.Errorf("write audit event: %w", err)
	}
	return nil
}
