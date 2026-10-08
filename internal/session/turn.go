package session

import (
	"errors"
	"fmt"
	"time"
)

// TurnStatus 是单轮 Agent 运行的生命周期状态（P1.7，多轮可回放）。
type TurnStatus string

const (
	// TurnPending 排队待开始（用户消息已入队）。
	TurnPending TurnStatus = "pending"
	// TurnRunning Agent Loop 执行中。
	TurnRunning TurnStatus = "running"
	// TurnWaiting 暂停等待审批/用户输入（可恢复续跑）。
	TurnWaiting TurnStatus = "waiting"
	// TurnDone 正常完成，含最终回答。
	TurnDone TurnStatus = "done"
	// TurnFailed 出错终止。
	TurnFailed TurnStatus = "failed"
	// TurnCanceled 被用户取消。
	TurnCanceled TurnStatus = "canceled"
)

// StepType 是 Turn 内一个 Agent Step 的语义类型（P4.9 全链路关联）。
type StepType string

const (
	// StepReason 模型思考/推理。
	StepReason StepType = "reason"
	// StepTool 工具调用。
	StepTool StepType = "tool"
	// StepObserve 工具结果观察。
	StepObserve StepType = "observe"
	// StepApproval 审批决策（写操作门禁）。
	StepApproval StepType = "approval"
	// StepResponse 最终回答输出。
	StepResponse StepType = "response"
)

// StepStatus 是 Step 的状态。
type StepStatus string

const (
	StepRunning StepStatus = "running"
	StepWaiting StepStatus = "waiting"
	StepDone    StepStatus = "done"
	StepFailed  StepStatus = "failed"
)

// TokenUsage 是单轮 token 用量（与 llm.TokenUsage 同构，避免本包反向依赖 llm）。
type TokenUsage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

// Turn 是一条用户消息触发的完整 Agent 运行轨迹（P1.7）。
// Input/Output/Error 在落库前统一脱敏（AGENTS.md：会话文件零敏感值）。
type Turn struct {
	ID        string     `json:"id"`
	SessionID string     `json:"sessionId"`
	UserID    string     `json:"userId,omitempty"`
	Status    TurnStatus `json:"status"`
	Input     string     `json:"input"`
	Output    string     `json:"output,omitempty"`
	Error     string     `json:"error,omitempty"`
	Token     TokenUsage `json:"token,omitempty"`

	StartedAt  time.Time  `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// Step 是 Turn 内的一个可回放执行步（P1.7）。
// Params/Result 为落库前脱敏后的 JSON/文本摘要。
type Step struct {
	ID        string     `json:"id"`
	TurnID    string     `json:"turnId"`
	SessionID string     `json:"sessionId,omitempty"`
	Seq       int        `json:"seq"`
	Type      StepType   `json:"type"`
	Status    StepStatus `json:"status"`
	Tool      string     `json:"tool,omitempty"`
	Params    string     `json:"params,omitempty"` // JSON，落库前脱敏
	Result    string     `json:"result,omitempty"` // 结果摘要，落库前脱敏
	Error     string     `json:"error,omitempty"`

	StartedAt  time.Time  `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// ErrInvalidTransition 表示一次非法的状态推进（状态机单测覆盖）。
var ErrInvalidTurnState = errors.New("invalid turn state")

func (t *Turn) touch() {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	t.UpdatedAt = time.Now()
}

// Start 由 pending 转为 running 并记录开始时间。
func (t *Turn) Start() error {
	if t.Status != TurnPending {
		return fmt.Errorf("%w: Start from %q", ErrInvalidTurnState, t.Status)
	}
	t.Status = TurnRunning
	if t.StartedAt.IsZero() {
		t.StartedAt = time.Now()
	}
	t.touch()
	return nil
}

// Wait 由 running 转入 waiting（等待审批/用户输入，可 Resume 续跑）。
func (t *Turn) Wait() error {
	if t.Status != TurnRunning {
		return fmt.Errorf("%w: Wait from %q", ErrInvalidTurnState, t.Status)
	}
	t.Status = TurnWaiting
	t.touch()
	return nil
}

// Resume 由 waiting 恢复为 running。
func (t *Turn) Resume() error {
	if t.Status != TurnWaiting {
		return fmt.Errorf("%w: Resume from %q", ErrInvalidTurnState, t.Status)
	}
	t.Status = TurnRunning
	t.touch()
	return nil
}

// Complete 正常结束：running/waiting → done，记录最终回答。
func (t *Turn) Complete(output string) error {
	if t.Status != TurnRunning && t.Status != TurnWaiting {
		return fmt.Errorf("%w: Complete from %q", ErrInvalidTurnState, t.Status)
	}
	t.Output = output
	t.Status = TurnDone
	now := time.Now()
	t.FinishedAt = &now
	t.touch()
	return nil
}

// Fail 出错终止：running/waiting → failed。
func (t *Turn) Fail(msg string) error {
	if t.Status != TurnRunning && t.Status != TurnWaiting {
		return fmt.Errorf("%w: Fail from %q", ErrInvalidTurnState, t.Status)
	}
	t.Error = msg
	t.Status = TurnFailed
	now := time.Now()
	t.FinishedAt = &now
	t.touch()
	return nil
}

// Cancel 被用户取消：pending/running/waiting → canceled。
func (t *Turn) Cancel() error {
	switch t.Status {
	case TurnPending, TurnRunning, TurnWaiting:
	default:
		return fmt.Errorf("%w: Cancel from %q", ErrInvalidTurnState, t.Status)
	}
	t.Status = TurnCanceled
	now := time.Now()
	t.FinishedAt = &now
	t.touch()
	return nil
}

func (s *Step) touch() {
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	s.UpdatedAt = time.Now()
}

// Begin 开始执行一个 Step。
func (s *Step) Begin() error {
	if s.Status != "" && s.Status != StepWaiting {
		return fmt.Errorf("%w: Begin from %q", ErrInvalidTurnState, s.Status)
	}
	s.Status = StepRunning
	if s.StartedAt.IsZero() {
		s.StartedAt = time.Now()
	}
	s.touch()
	return nil
}

// Wait 让 Step 暂停（等待审批输入）。
func (s *Step) Wait() error {
	if s.Status != StepRunning {
		return fmt.Errorf("%w: Wait from %q", ErrInvalidTurnState, s.Status)
	}
	s.Status = StepWaiting
	s.touch()
	return nil
}

// Succeed 记录 Step 结果并置为 done。
func (s *Step) Succeed(result string) error {
	if s.Status != StepRunning && s.Status != StepWaiting {
		return fmt.Errorf("%w: Succeed from %q", ErrInvalidTurnState, s.Status)
	}
	s.Result = result
	s.Status = StepDone
	now := time.Now()
	s.FinishedAt = &now
	s.touch()
	return nil
}

// Fail 记录 Step 错误并置为 failed。
func (s *Step) Fail(msg string) error {
	if s.Status != StepRunning && s.Status != StepWaiting {
		return fmt.Errorf("%w: Fail from %q", ErrInvalidTurnState, s.Status)
	}
	s.Error = msg
	s.Status = StepFailed
	now := time.Now()
	s.FinishedAt = &now
	s.touch()
	return nil
}
