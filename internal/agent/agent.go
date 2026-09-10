package agent

import (
	"context"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/audit"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// Approver 处理写操作确认（CLI/Web 各自实现；P4 接入真实执行）。
type Approver interface {
	// Confirm 展示计划/命令并等待用户选择，返回 ChoiceYes/ChoiceYesNoAskAgain/ChoiceNo。
	Confirm(ctx context.Context, prompt string, options []UserChoiceOption) (string, error)
}

// Options 是 Agent 装配参数。
type Options struct {
	LLM      llm.LLMClient
	Tools    *tools.Registry
	Session  *session.Session
	Approver Approver
	Recorder audit.Recorder

	MaxIterations  int
	MaxToolCalls   int
	Timeout        time.Duration
	ToolCalling    string // native | shim | auto
	ResultMaxBytes int
	TurnMaxBytes   int
	HistoryTurns   int
}

// Agent 是 Agent 实例：消息总线 + 状态机 + Agentic Loop（P2）。
type Agent struct {
	ID    string
	State AgentState

	// Output 是 Agent → UI 的消息流（CLI/Web 消费）。
	Output chan Message
	// Input 是 UI → Agent 的消息流（用户输入/审批响应）。
	Input chan any

	LLM      llm.LLMClient
	Tools    *tools.Registry
	Session  *session.Session
	Approver Approver
	Recorder audit.Recorder

	MaxIterations  int
	MaxToolCalls   int
	Timeout        time.Duration
	ToolCalling    string
	ResultMaxBytes int
	TurnMaxBytes   int
	HistoryTurns   int
}

// New 创建 Agent 实例并应用默认值。
func New(opts Options) *Agent {
	a := &Agent{
		ID:             newID(),
		State:          AgentStateIdle,
		Output:         make(chan Message, 64),
		Input:          make(chan any, 16),
		LLM:            opts.LLM,
		Tools:          opts.Tools,
		Session:        opts.Session,
		Approver:       opts.Approver,
		Recorder:       opts.Recorder,
		MaxIterations:  opts.MaxIterations,
		MaxToolCalls:   opts.MaxToolCalls,
		Timeout:        opts.Timeout,
		ToolCalling:    opts.ToolCalling,
		ResultMaxBytes: opts.ResultMaxBytes,
		TurnMaxBytes:   opts.TurnMaxBytes,
		HistoryTurns:   opts.HistoryTurns,
	}
	if a.MaxIterations <= 0 {
		a.MaxIterations = 10
	}
	if a.MaxToolCalls <= 0 {
		a.MaxToolCalls = 20
	}
	if a.Timeout <= 0 {
		a.Timeout = 5 * time.Minute
	}
	if a.ToolCalling == "" {
		a.ToolCalling = "native"
	}
	if a.ResultMaxBytes <= 0 {
		a.ResultMaxBytes = 8 * 1024
	}
	if a.TurnMaxBytes <= 0 {
		a.TurnMaxBytes = 32 * 1024
	}
	if a.HistoryTurns <= 0 {
		a.HistoryTurns = 20
	}
	return a
}

// audit 写入一条审计事件（Recorder 为空时静默跳过；实现负责脱敏）。
func (a *Agent) audit(ctx context.Context, action string, payload any) {
	if a.Recorder == nil {
		return
	}
	if err := a.Recorder.Write(ctx, audit.Event{Action: action, Payload: payload}); err != nil {
		// 审计失败不影响主流程（只记日志由调用方处理）。
	}
}

// Emit 向消息总线发送可合并的消息；通道满时丢弃，调用方不应使用它发送控制事件。
func (a *Agent) Emit(m Message) {
	a.prepareMessage(&m)
	select {
	case a.Output <- m:
	default:
	}
}

// EmitCritical 可靠发送工具、错误等控制消息。Agent 运行期间总有转发协程消费 Output。
func (a *Agent) EmitCritical(m Message) {
	a.prepareMessage(&m)
	a.Output <- m
}

func (a *Agent) prepareMessage(m *Message) {
	if m.ID == "" {
		m.ID = newID()
	}
	if m.Timestamp.IsZero() {
		m.Timestamp = time.Now()
	}
}

// SetState 更新状态并向 UI 广播 agent-state 事件。
func (a *Agent) SetState(s AgentState) {
	if a.State == s {
		return
	}
	a.State = s
	a.Emit(Message{Source: SourceAgent, Type: MessageTypeAgentState, Payload: s})
}

// EmitText 发送一条 Agent 文本消息（thinking / 过程 / 回答）。
func (a *Agent) EmitText(text string) {
	a.Emit(Message{Source: SourceAgent, Type: MessageTypeText, Payload: text})
}
