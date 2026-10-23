// Package session 实现二期会话：可插拔 Store（memory/file）与 Manager。
// 设计逻辑借鉴 kubectl-ai 的 Store/Manager 模式，类型按 k8s-ai 重新实现。
package session

import (
	"encoding/json"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/approval"
)

// Message 是会话中持久化的一条消息（落盘前统一脱敏）。
type Message struct {
	Role       string          `json:"role"`                 // user | assistant | tool
	Type       string          `json:"type"`                 // text | reasoning | tool-call-response | error | ...
	Name       string          `json:"name,omitempty"`       // tool 消息的工具名
	ToolCallID string          `json:"toolCallId,omitempty"` // native Tool Calling 的调用 ID（恢复会话回传必需）
	Arguments  json.RawMessage `json:"arguments,omitempty"`  // 工具调用参数（恢复会话展示命令用，仅读回放不喂 LLM）
	Data       any             `json:"data,omitempty"`       // 工具结构化结果（恢复会话渲染表格用，仅读回放不喂 LLM）
	Ok         *bool           `json:"ok,omitempty"`         // 工具执行是否成功（前端权威判定，回放兜底用）
	// P7.4 工具结果同源契约（ADR-034）：摘要/渲染形态/耗时/风险在本次执行时一并落库，
	// 刷新后与 chat 流式结果共用同一份头部信息，避免回放时丢失 "320ms" 与风险标签。
	Summary     string    `json:"summary,omitempty"`     // 头部概览摘要（后端计算）
	DisplayType string    `json:"displayType,omitempty"` // table | kv | events | yaml | logs | text
	DurationMs  int64     `json:"durationMs,omitempty"`  // 工具执行耗时（毫秒）
	Risk        string    `json:"risk,omitempty"`        // 风险等级（SAFE/LOW/MEDIUM/HIGH/CRITICAL）
	Content     string    `json:"content"`
	Timestamp   time.Time `json:"timestamp"`
}

// Session 是一个对话会话。
type Session struct {
	ID string `json:"id"`
	// UserID 是会话所有者。空值表示三期历史会话或 CLI 会话；管理员可读取
	// 这类会话，普通用户/操作员不得认领或访问。
	UserID         string           `json:"userId,omitempty"`
	Name           string           `json:"name"`
	Cluster        string           `json:"cluster,omitempty"`
	Namespace      string           `json:"namespace,omitempty"`
	Model          string           `json:"model,omitempty"`
	State          string           `json:"state,omitempty"` // AgentState 快照（避免包依赖环）
	Messages       []Message        `json:"messages"`
	ApprovedWrites []string         `json:"approvedWrites,omitempty"` // 本次会话已授权的写操作
	ApprovalGrants []approval.Grant `json:"approvalGrants,omitempty"` // Phase D 结构化授权（旧 key 仅兼容迁移）
	DeclinedWrites map[string]int   `json:"declinedWrites,omitempty"` // 本次会话拒绝过的写操作 key→次数（用于≥2 次硬拦截）
	CreatedAt      time.Time        `json:"createdAt"`
	LastModified   time.Time        `json:"lastModified"`
	// P7.5 会话置顶：置顶会话始终排最前（置顶集合内部按最近活跃排序）。
	Pinned bool `json:"pinned,omitempty"`
}

// AddMessage 追加消息并刷新 LastModified。
func (s *Session) AddMessage(m Message) {
	if m.Timestamp.IsZero() {
		m.Timestamp = time.Now()
	}
	s.Messages = append(s.Messages, m)
	s.LastModified = time.Now()
}

// ClearMessages 清空会话消息（元命令 clear）。
func (s *Session) ClearMessages() {
	s.Messages = nil
	s.LastModified = time.Now()
}

// Approved 判断"操作类型+资源"是否已在本次会话授权。
func (s *Session) Approved(key string) bool {
	for _, k := range s.ApprovedWrites {
		if k == key {
			return true
		}
	}
	return false
}

// AddApproved 记录"操作类型+资源"授权（选项 a：本次会话不再询问）。
func (s *Session) AddApproved(key string) {
	if s.Approved(key) {
		return
	}
	s.ApprovedWrites = append(s.ApprovedWrites, key)
	s.LastModified = time.Now()
}

// AddGrant 保存一条结构化授权记录（按 ID 去重/覆盖）。
func (s *Session) AddGrant(g approval.Grant) {
	if g.ID == "" {
		return
	}
	for i := range s.ApprovalGrants {
		if s.ApprovalGrants[i].ID == g.ID {
			s.ApprovalGrants[i] = g
			s.LastModified = time.Now()
			return
		}
	}
	s.ApprovalGrants = append(s.ApprovalGrants, g)
	s.LastModified = time.Now()
}

// FindGrant 返回第一条匹配请求的结构化授权记录，没有则返回 nil。
func (s *Session) FindGrant(req approval.GrantRequest) *approval.Grant {
	if len(s.ApprovalGrants) == 0 {
		return nil
	}
	return approval.MatchGrant(s.ApprovalGrants, req)
}

// RemoveGrantMatching 删除所有匹配请求的结构化授权记录。
// 用户否定某个写操作时调用，避免旧授权继续自动放行同类操作。
func (s *Session) RemoveGrantMatching(req approval.GrantRequest) {
	if len(s.ApprovalGrants) == 0 {
		return
	}
	var kept []approval.Grant
	for i := range s.ApprovalGrants {
		if !s.ApprovalGrants[i].Matches(req) {
			kept = append(kept, s.ApprovalGrants[i])
		}
	}
	if len(kept) == len(s.ApprovalGrants) {
		return
	}
	s.ApprovalGrants = kept
	s.LastModified = time.Now()
}

// Declined 返回"操作类型+资源"在本次会话被拒绝的次数。
func (s *Session) Declined(key string) int {
	if s.DeclinedWrites == nil {
		return 0
	}
	return s.DeclinedWrites[key]
}

// AddDeclined 累计一次用户拒绝（返回累计次数供上层判断硬拦截阈值）。
// 同 key 在同一拒绝窗口的重复计入，用于触发"被拒≥2 次必须换方案"的强闸门。
// 授权与拒绝互斥：若该 key 已在 ApprovedWrites，说明用户后来否定之前的选择，移除已授权记录。
func (s *Session) AddDeclined(key string) int {
	for i, k := range s.ApprovedWrites {
		if k == key {
			s.ApprovedWrites = append(s.ApprovedWrites[:i], s.ApprovedWrites[i+1:]...)
			break
		}
	}
	if s.DeclinedWrites == nil {
		s.DeclinedWrites = map[string]int{key: 1}
		s.LastModified = time.Now()
		return 1
	}
	s.DeclinedWrites[key]++
	s.LastModified = time.Now()
	return s.DeclinedWrites[key]
}
