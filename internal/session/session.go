// Package session 实现二期会话：可插拔 Store（memory/file）与 Manager。
// 设计逻辑借鉴 kubectl-ai 的 Store/Manager 模式，类型按 k8s-ai 重新实现。
package session

import "time"

// Message 是会话中持久化的一条消息（落盘前统一脱敏）。
type Message struct {
	Role       string    `json:"role"`                 // user | assistant | tool
	Type       string    `json:"type"`                 // text | tool-call-response | error | ...
	Name       string    `json:"name,omitempty"`       // tool 消息的工具名
	ToolCallID string    `json:"toolCallId,omitempty"` // native Tool Calling 的调用 ID（恢复会话回传必需）
	Content    string    `json:"content"`
	Timestamp  time.Time `json:"timestamp"`
}

// Session 是一个对话会话。
type Session struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Cluster        string    `json:"cluster,omitempty"`
	Namespace      string    `json:"namespace,omitempty"`
	Model          string    `json:"model,omitempty"`
	State          string    `json:"state,omitempty"` // AgentState 快照（避免包依赖环）
	Messages       []Message `json:"messages"`
	ApprovedWrites []string  `json:"approvedWrites,omitempty"` // 本次会话已授权的写操作
	CreatedAt      time.Time `json:"createdAt"`
	LastModified   time.Time `json:"lastModified"`
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
