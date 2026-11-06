package agent

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/approval"
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

// ApprovalRequest 是结构化审批请求。Prompt 保留给 CLI/旧客户端；
// Web 审批卡优先消费 Plan 中的目标、变更、命令和验证条件。
type ApprovalRequest struct {
	Prompt  string
	Options []UserChoiceOption
	Plan    tools.ApprovalPlan
}

// StructuredApprover 是 Approver 的可选能力。未实现时 Agent 回退普通 Confirm，
// 保证 CLI 与既有测试桩不需要跟着改造。
type StructuredApprover interface {
	ConfirmStructured(ctx context.Context, req ApprovalRequest) (string, error)
}

// SecretValueApprover 是 Approver 的可选接口：审批通过后向用户索取单个 Secret 明文值
// （三期 R4.5）。值只在内存与一次提交里存在，绝不写进会话、审计与 LLM 请求。
type SecretValueApprover interface {
	// RequestSecretValue 提问并等待用户输入；返回空串表示用户放弃（该次创建取消）。
	RequestSecretValue(ctx context.Context, prompt string) (string, error)
}

// Options 是 Agent 装配参数。
type Options struct {
	LLM      llm.LLMClient
	Tools    *tools.Registry
	Session  *session.Session
	Approver Approver
	Recorder audit.Recorder
	// Tracer 观察 Agent 运行生命周期（P1.8），service 层用它持久化 Turn/Step；nil 时跳过。
	Tracer Tracer
	// TurnUserID 记录本轮 Turn 归属的产品内用户（P1.5），可为空。
	TurnUserID string
	// AccessScope 是本次运行的 Kubernetes 资源授权范围，工具执行前强制校验。
	AccessScope *tools.AccessScope
	// SkillContext 是命中用户自定义 procedural Skill 的方法论附录（P3.8.4），
	// 由 service 层意图检索后注入 system prompt，正文视为不可信文本只作文本参考。
	SkillContext string
	// ApprovalPolicy 是统一的写操作审批决策；nil 时采用 strict + 要求审批（等价 v1）。
	ApprovalPolicy *approval.Policy

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

	LLM            llm.LLMClient
	Tools          *tools.Registry
	Session        *session.Session
	Approver       Approver
	Recorder       audit.Recorder
	ApprovalPolicy *approval.Policy
	Tracer         Tracer
	TurnUserID     string
	AccessScope    *tools.AccessScope
	SkillContext   string

	MaxIterations  int
	MaxToolCalls   int
	Timeout        time.Duration
	ToolCalling    string
	ResultMaxBytes int
	TurnMaxBytes   int
	HistoryTurns   int

	// turn 是当前聊天轮的运行轨迹（P1.8），Chat 执行期间非 nil。
	turn            *session.Turn
	stepSeq         int
	sensitiveValues map[string]struct{}
}

// New 创建 Agent 实例并应用默认值。
func New(opts Options) *Agent {
	a := &Agent{
		ID:              newID(),
		State:           AgentStateIdle,
		Output:          make(chan Message, 64),
		Input:           make(chan any, 16),
		LLM:             opts.LLM,
		Tools:           opts.Tools,
		Session:         opts.Session,
		Approver:        opts.Approver,
		Recorder:        opts.Recorder,
		ApprovalPolicy:  opts.ApprovalPolicy,
		Tracer:          opts.Tracer,
		TurnUserID:      opts.TurnUserID,
		AccessScope:     opts.AccessScope,
		SkillContext:    opts.SkillContext,
		MaxIterations:   opts.MaxIterations,
		MaxToolCalls:    opts.MaxToolCalls,
		Timeout:         opts.Timeout,
		ToolCalling:     opts.ToolCalling,
		ResultMaxBytes:  opts.ResultMaxBytes,
		TurnMaxBytes:    opts.TurnMaxBytes,
		HistoryTurns:    opts.HistoryTurns,
		sensitiveValues: map[string]struct{}{},
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
	if a.ApprovalPolicy == nil {
		// 未显式指定时默认 strict + 要求审批，与 v1 行为一致。
		a.ApprovalPolicy = &approval.Policy{
			Mode:            approval.ModeStrict,
			RequireApproval: true,
		}
	}
	return a
}

// recordSensitiveValues 登记当前轮不得进入 UI/持久化/最终回答的 Secret 原值。
func (a *Agent) recordSensitiveValues(values []string) {
	if a.sensitiveValues == nil {
		a.sensitiveValues = map[string]struct{}{}
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		a.sensitiveValues[value] = struct{}{}
		quoted := strconv.Quote(value)
		if len(quoted) >= 2 {
			a.sensitiveValues[quoted[1:len(quoted)-1]] = struct{}{}
		}
	}
}

func (a *Agent) hasSensitiveValues() bool {
	return len(a.sensitiveValues) > 0
}

func (a *Agent) clearSensitiveValues() {
	clear(a.sensitiveValues)
}

// redactSensitive 对完整字符串执行精确值脱敏；只能用于完整文本，不能用于可能被
// 拆成多个分片的流式增量。敏感模式下流式输出被抑制，最终完整文本再经过这里。
func (a *Agent) redactSensitive(text string) string {
	if text == "" || len(a.sensitiveValues) == 0 {
		return text
	}
	values := make([]string, 0, len(a.sensitiveValues))
	for value := range a.sensitiveValues {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		text = strings.ReplaceAll(text, value, "[REDACTED]")
	}
	return text
}

func (a *Agent) safeArguments(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || len(a.sensitiveValues) == 0 {
		return raw
	}
	return json.RawMessage(a.redactSensitive(string(raw)))
}

func (a *Agent) redactValue(v any) any {
	switch value := v.(type) {
	case string:
		return a.redactSensitive(value)
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, item := range value {
			out[k] = a.redactValue(item)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = a.redactValue(item)
		}
		return out
	default:
		return v
	}
}

func (a *Agent) safeStructured(v any) any {
	if v == nil || len(a.sensitiveValues) == 0 {
		return v
	}
	switch value := v.(type) {
	case string, map[string]any, []any:
		return a.redactValue(value)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var decoded any
	if json.Unmarshal([]byte(a.redactSensitive(string(raw))), &decoded) != nil {
		return v
	}
	return decoded
}

func (a *Agent) safeApprovalPlan(plan tools.ApprovalPlan) tools.ApprovalPlan {
	plan.Title = a.redactSensitive(plan.Title)
	plan.Operation = a.redactSensitive(plan.Operation)
	plan.Summary = a.redactSensitive(plan.Summary)
	plan.Command = a.redactSensitive(plan.Command)
	plan.Target.Cluster = a.redactSensitive(plan.Target.Cluster)
	plan.Target.Namespace = a.redactSensitive(plan.Target.Namespace)
	plan.Target.Group = a.redactSensitive(plan.Target.Group)
	plan.Target.Version = a.redactSensitive(plan.Target.Version)
	plan.Target.Kind = a.redactSensitive(plan.Target.Kind)
	plan.Target.Name = a.redactSensitive(plan.Target.Name)
	for i := range plan.Changes {
		plan.Changes[i].Path = a.redactSensitive(plan.Changes[i].Path)
		plan.Changes[i].Operation = a.redactSensitive(plan.Changes[i].Operation)
		plan.Changes[i].Description = a.redactSensitive(plan.Changes[i].Description)
		plan.Changes[i].Before = a.redactValue(plan.Changes[i].Before)
		plan.Changes[i].After = a.redactValue(plan.Changes[i].After)
	}
	for i := range plan.Checks {
		plan.Checks[i].Name = a.redactSensitive(plan.Checks[i].Name)
		plan.Checks[i].Message = a.redactSensitive(plan.Checks[i].Message)
	}
	for i := range plan.Verification {
		plan.Verification[i] = a.redactSensitive(plan.Verification[i])
	}
	return plan
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
