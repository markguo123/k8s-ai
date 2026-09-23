// Package session 实现二期会话：可插拔 Store（memory/file）与 Manager。
// 设计逻辑借鉴 kubectl-ai 的 Store/Manager 模式，类型按 k8s-ai 重新实现。
package session

import (
	"encoding/json"
	"time"
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
	Content    string          `json:"content"`
	Timestamp  time.Time       `json:"timestamp"`
}

// Session 是一个对话会话。
type Session struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Cluster        string         `json:"cluster,omitempty"`
	Namespace      string         `json:"namespace,omitempty"`
	Model          string         `json:"model,omitempty"`
	State          string         `json:"state,omitempty"` // AgentState 快照（避免包依赖环）
	Messages       []Message      `json:"messages"`
	ApprovedWrites []string       `json:"approvedWrites,omitempty"` // 本次会话已授权的写操作
	DeclinedWrites map[string]int `json:"declinedWrites,omitempty"` // 本次会话拒绝过的写操作 key→次数（用于≥2 次硬拦截）
	CreatedAt      time.Time      `json:"createdAt"`
	LastModified   time.Time      `json:"lastModified"`
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
