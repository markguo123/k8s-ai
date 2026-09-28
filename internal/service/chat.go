package service

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/agent"
	"github.com/k8s-ai/k8s-ai/internal/approval"
	"github.com/k8s-ai/k8s-ai/internal/audit"
	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/executor"
	"github.com/k8s-ai/k8s-ai/internal/kubernetes"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/manifestx"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/scanner"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/tools"
	k8stools "github.com/k8s-ai/k8s-ai/internal/tools/k8s"
)

// ChatEventType 是 ChatService 事件类型（CLI/Web 消费，ADR-007 的 Agent 版）。
type ChatEventType string

const (
	ChatEventState          ChatEventType = "state"
	ChatEventText           ChatEventType = "text"            // 用户输入回显（CLI 交互模式）
	ChatEventDelta          ChatEventType = "delta"           // 兼容旧客户端的 LLM 增量事件
	ChatEventReasoningDelta ChatEventType = "reasoning-delta" // 模型隐藏思考增量（实时推给 Web“分析过程”面板展示）
	ChatEventProgress       ChatEventType = "progress"        // 阶段进度摘要（可见输出的简短说明）
	ChatEventAnswerDelta    ChatEventType = "answer-delta"    // 最终回答增量
	ChatEventToolCall       ChatEventType = "tool-call"
	ChatEventToolResponse   ChatEventType = "tool-response"
	ChatEventAnswer         ChatEventType = "answer" // 最终回答（携带全文，流式气泡以此收尾）
	ChatEventError          ChatEventType = "error"
	ChatEventChoice         ChatEventType = "choice-request"
)

// ChatToolCallView 是工具调用在事件中的展示视图。
type ChatToolCallView struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Risk      string         `json:"risk,omitempty"`
	Result    string         `json:"result,omitempty"`
	Summary   string         `json:"summary,omitempty"`
	Ok        *bool          `json:"ok,omitempty"` // nil=旧客户端/旧会话无此字段，前端回退正则兜底
	Truncated bool           `json:"truncated,omitempty"`
	Data      any            `json:"data,omitempty"` // 结构化结果（仅 UI 回放展示，不参与 LLM 载荷）
}

// ChatChoiceOption 是审批/选择项。
type ChatChoiceOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ChatChoice 是审批请求（写操作确认）。
type ChatChoice struct {
	Prompt  string             `json:"prompt"`
	Options []ChatChoiceOption `json:"options"`
}

// ChatEvent 是一轮对话中的增量事件。
type ChatEvent struct {
	Type   ChatEventType     `json:"type"`
	Text   string            `json:"text,omitempty"`
	Tool   *ChatToolCallView `json:"tool,omitempty"`
	Choice *ChatChoice       `json:"choice,omitempty"`
	State  string            `json:"state,omitempty"`
	Usage  *llm.TokenUsage   `json:"usage,omitempty"`
}

// ChatRequest 是一次对话请求。
type ChatRequest struct {
	SessionID string
	Query     string
	// SkipPermissions 非交互自动化显式放行写操作（等效 kubectl-ai --skip-permissions）。
	SkipPermissions bool
}

// ChatService 是二期 Agent 应用服务（CLI/Web 复用）。
type ChatService interface {
	// Chat 处理一条用户消息，返回事件流；respond 通道回传审批选择。
	Chat(ctx context.Context, req ChatRequest, respond <-chan string) (<-chan ChatEvent, error)
	// GetOrCreateSession 获取或创建会话。
	GetOrCreateSession(id, namespace, model string) (*session.Session, error)
	ListSessions() ([]session.Session, error)
	DeleteSession(id string) error
	ClearSession(id string) error
	RenameSession(id, name string) error
}

type chatService struct {
	cfg            *config.Config
	sessions       *session.Manager
	llmFactory     func(llm.Options) llm.LLMClient
	newReader      func() (scanner.Reader, error)
	scanFactory    func() tools.ScanRunner
	newWriteRunner func() (tools.WriteRunner, error)
	// proposals 跨轮次保存创建提案指纹：每轮 Chat 都会重建工具实例，
	// dry run 与 apply 通常不在同一轮，所以缓存必须挂在服务上。
	proposals *tools.ProposalStore
}

// NewChat 创建二期 Agent 服务。
func NewChat(cfg *config.Config, newReader func() (scanner.Reader, error), llmFactory func(llm.Options) llm.LLMClient, scanFactory func() tools.ScanRunner, newWriteRunner func() (tools.WriteRunner, error)) ChatService {
	var store session.Store
	store, err := session.NewFileStore(cfg.Agent.SessionDir)
	if err != nil {
		slog.Warn("chat: 会话目录不可用，回退内存存储", "dir", cfg.Agent.SessionDir, "error", err)
		store = session.NewMemoryStore()
	}
	if llmFactory == nil {
		llmFactory = func(o llm.Options) llm.LLMClient { return llm.New(o) }
	}
	if scanFactory == nil {
		scanFactory = func() tools.ScanRunner { return New() }
	}
	if newWriteRunner == nil {
		newWriteRunner = func() (tools.WriteRunner, error) { return chatWriteRunner(cfg) }
	}
	return &chatService{
		cfg:            cfg,
		sessions:       session.NewManager(store),
		llmFactory:     llmFactory,
		newReader:      newReader,
		scanFactory:    scanFactory,
		newWriteRunner: newWriteRunner,
		proposals:      tools.NewProposalStore(),
	}
}

// chatReader 从配置构建只读 Kubernetes 门面（chat 用）。
func chatReader(cfg *config.Config) (scanner.Reader, error) {
	return kubernetes.NewClient(kubernetes.Options{
		Kubeconfig: cfg.Kubernetes.Kubeconfig,
		Context:    cfg.Kubernetes.Context,
		Timeout:    cfg.Kubernetes.Timeout,
		QPS:        cfg.Kubernetes.QPS,
		Burst:      cfg.Kubernetes.Burst,
	})
}

// NewChatWithConfig 用完整配置创建 Agent 服务（main/CLI 组合根入口）。
func NewChatWithConfig(cfg *config.Config) ChatService {
	return NewChat(cfg, func() (scanner.Reader, error) { return chatReader(cfg) }, nil, nil, nil)
}

// chatWriteRunner 从配置构建写执行器（kubectl argv + client-go，仅 executor 包内）。
func chatWriteRunner(cfg *config.Config) (tools.WriteRunner, error) {
	cs, err := kubernetes.NewClientset(kubernetes.Options{
		Kubeconfig: cfg.Kubernetes.Kubeconfig,
		Context:    cfg.Kubernetes.Context,
		Timeout:    cfg.Kubernetes.Timeout,
		QPS:        cfg.Kubernetes.QPS,
		Burst:      cfg.Kubernetes.Burst,
	})
	if err != nil {
		return nil, err
	}
	bin := cfg.Kubernetes.KubectlPath
	if bin == "" {
		bin = "kubectl"
	}
	// 三期创建通道（R3）需要 dynamic + discovery：RESTConfig 与只读门面同源，
	// 缺它时 executor 会报「写通道未装配」而不是静默降级。
	restConfig, err := kubernetes.NewRESTConfig(kubernetes.Options{
		Kubeconfig: cfg.Kubernetes.Kubeconfig,
		Context:    cfg.Kubernetes.Context,
		Timeout:    cfg.Kubernetes.Timeout,
		QPS:        cfg.Kubernetes.QPS,
		Burst:      cfg.Kubernetes.Burst,
	})
	if err != nil {
		return nil, err
	}
	return executor.NewRunner(executor.Options{
		KubectlBin: bin,
		Kubeconfig: cfg.Kubernetes.Kubeconfig,
		MaxBytes:   cfg.Agent.CmdMaxBytes,
		DenyOps:    cfg.Agent.DenyOps,
		Clientset:  cs,
		Namespace:  cfg.Kubernetes.Namespace,
		RESTConfig: restConfig,
	}), nil
}

// chatScanOptions 把配置映射为一期 ScanOptions（诊断 Tool 的 base opts）。
func chatScanOptions(cfg *config.Config) model.ScanOptions {
	return model.ScanOptions{
		Kubeconfig:          cfg.Kubernetes.Kubeconfig,
		Context:             cfg.Kubernetes.Context,
		Namespace:           cfg.Kubernetes.Namespace,
		Timeout:             cfg.Scan.Timeout,
		QPS:                 cfg.Kubernetes.QPS,
		Burst:               cfg.Kubernetes.Burst,
		Concurrency:         cfg.Scan.Concurrency,
		Phase2Concurrency:   cfg.Scan.Phase2Concurrency,
		CollectLogs:         cfg.Scan.CollectLogs,
		CollectPreviousLogs: cfg.Scan.CollectPreviousLogs,
		CollectEvents:       cfg.Scan.CollectEvents,
		MaxLogLines:         cfg.Scan.MaxLogLines,
		MaxLogBytes:         cfg.Scan.MaxLogBytes,
		MaxLogLineBytes:     cfg.Scan.MaxLogLineBytes,
		PodLogsTimeout:      cfg.Scan.PodLogsTimeout,
		ReportDirectory:     cfg.Report.Directory,
		ReportFormat:        cfg.Report.Format,
		ReportMode:          "none",
		LLM: model.LLMOptions{
			Enabled:        cfg.LLM.Enabled,
			Endpoint:       cfg.LLM.Endpoint,
			APIKey:         cfg.LLM.APIKey,
			Model:          cfg.LLM.Model,
			Temperature:    cfg.LLM.Temperature,
			MaxTokens:      cfg.LLM.MaxTokens,
			MaxInputTokens: cfg.LLM.MaxInputTokens,
			MaxTotalTokens: cfg.LLM.MaxTotalTokens,
			MaxFindings:    cfg.LLM.MaxFindings,
			Timeout:        cfg.LLM.Timeout,
			Thinking:       cfg.Scan.Thinking,
		},
	}
}

// createToolOptions 把 agent.create.* / agent.secret.* 映射为创建工具的门禁与上限（R3.2）。
func createToolOptions(cfg *config.Config) tools.CreateOptions {
	limits := manifestx.Limits{
		MaxObjects: cfg.Agent.Create.MaxObjectsPerCall,
		MaxBytes:   cfg.Agent.Create.MaxManifestBytes,
	}
	return tools.CreateOptions{
		Policy: manifestx.Policy{
			DenyKinds:    cfg.Agent.Create.DenyKinds,
			AllowKinds:   cfg.Agent.Create.AllowKinds,
			ClusterKinds: cfg.Agent.Create.ClusterKinds,
		},
		Limits:           limits,
		DefaultNamespace: cfg.Kubernetes.Namespace,
		SecretAccess:     cfg.Agent.Secret.Access,
		VerifyTimeout:    executor.DefaultVerifyTimeout,
	}
}

// Chat 编排一轮对话：装配 Agent → 转发事件 → 持久化会话。
func (s *chatService) Chat(ctx context.Context, req ChatRequest, respond <-chan string) (<-chan ChatEvent, error) {
	sess, err := s.sessions.Get(req.SessionID)
	if err != nil {
		return nil, err
	}
	reader, err := s.newReader()
	if err != nil {
		return nil, err
	}
	reg := tools.NewRegistry()
	if err := k8stools.RegisterAll(reg, reader); err != nil {
		return nil, err
	}
	// 诊断 Tool 复用一期 ScanService（P3）。
	for _, dt := range tools.NewDiagnosisTools(s.scanFactory(), chatScanOptions(s.cfg)) {
		if err := reg.Register(dt); err != nil {
			return nil, err
		}
	}
	// 写 Tool（P4）：kubectl + 结构化写，全部走审批。
	writeRunner, err := s.newWriteRunner()
	if err != nil {
		return nil, err
	}
	for _, wt := range tools.NewWriteTools(writeRunner) {
		if err := reg.Register(wt); err != nil {
			return nil, err
		}
	}
	// 三期创建通道（R3）：结构化 create_resource，注册在 disabled_tools 之前，
	// 这样 agent.disabled_tools 里写 create_resource 就能摘掉它。
	if s.cfg.Agent.Create.Enabled {
		if runner, ok := writeRunner.(tools.CreateRunner); ok {
			if err := reg.Register(tools.NewCreateTool(createToolOptions(s.cfg), runner, s.proposals, sess.ID)); err != nil {
				return nil, err
			}
		}
	}
	for _, name := range s.cfg.Agent.DisabledTools {
		reg.Remove(strings.TrimSpace(name))
	}
	events := make(chan ChatEvent, 64)
	pol := approval.Policy{
		Mode:            approval.Mode(s.cfg.Agent.ApprovalMode),
		RequireApproval: s.cfg.Agent.RequireApproval,
		SkipPermissions: req.SkipPermissions || s.cfg.Agent.SkipPermissions,
		DenyOps:         s.cfg.Agent.DenyOps,
	}
	approver := &chatApprover{
		events:      events,
		respond:     respond,
		skip:        false,
		autoApprove: false,
	}
	client := s.llmFactory(llm.Options{
		Endpoint:        s.cfg.LLM.Endpoint,
		APIKey:          s.cfg.LLM.APIKey,
		Model:           s.cfg.LLM.Model,
		Temperature:     s.cfg.LLM.Temperature,
		MaxTokens:       s.cfg.LLM.MaxTokens,
		Timeout:         s.cfg.LLM.Timeout,
		Thinking:        s.cfg.LLM.Thinking,
		ReasoningFields: s.cfg.LLM.ReasoningFields,
		DisableStream:   !s.cfg.LLM.Stream, // llm.stream=false：ChatStream 退化为非流式
	})
	recorder := s.newAuditRecorder(sess.ID)
	ag := agent.New(agent.Options{
		LLM:            client,
		Tools:          reg,
		Session:        sess,
		Approver:       approver,
		ApprovalPolicy: &pol,
		Recorder:       recorder,
		MaxIterations:  s.cfg.Agent.MaxIterations,
		MaxToolCalls:   s.cfg.Agent.MaxToolCalls,
		Timeout:        s.cfg.Agent.Timeout,
		ToolCalling:    s.cfg.Agent.ToolCalling,
		ResultMaxBytes: s.cfg.Agent.ResultMaxBytes,
		TurnMaxBytes:   s.cfg.Agent.TurnMaxBytes,
		HistoryTurns:   s.cfg.Agent.HistoryTurns,
	})
	go s.runChat(ctx, ag, sess, req, events, recorder)
	return events, nil
}

// newAuditRecorder 为会话创建 JSONL 审计记录器（目录不存在自动创建；失败回退日志记录器）。
func (s *chatService) newAuditRecorder(sessionID string) audit.Recorder {
	path := filepath.Join(s.cfg.Agent.AuditDir, sessionID+".jsonl")
	r, err := audit.NewFileRecorder(path)
	if err != nil {
		slog.Warn("chat: 审计文件不可用，回退日志记录器", "path", path, "error", err)
		return &audit.LogRecorder{}
	}
	return r
}

func (s *chatService) runChat(ctx context.Context, ag *agent.Agent, sess *session.Session, req ChatRequest, events chan<- ChatEvent, recorder audit.Recorder) {
	defer close(events)
	defer func() { _ = recorder.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var pendingAnswer strings.Builder
		var reasoning strings.Builder
		hadToolCall := false
		for m := range ag.Output {
			if ev, ok := translateEvent(m); ok {
				switch ev.Type {
				case ChatEventReasoningDelta:
					reasoning.WriteString(ev.Text)
					events <- ev
				case ChatEventProgress:
					events <- ev
				case ChatEventAnswerDelta, ChatEventDelta:
					// 先缓存回答增量。若后续发现本轮实际包含工具调用，
					// 这段内容只能是工具前的中间说明，不能提前展示为报告。
					pendingAnswer.WriteString(ev.Text)
				case ChatEventToolCall:
					if progress := summarizeToolPrelude(pendingAnswer.String()); progress != "" {
						events <- ChatEvent{Type: ChatEventProgress, Text: progress}
					}
					hadToolCall = true
					pendingAnswer.Reset()
					events <- ev
				case ChatEventToolResponse:
					events <- ev
					// 工具结果之后进入新的 LLM 回答轮次，允许最终报告增量通过。
					hadToolCall = false
				default:
					events <- ev
				}
			}
		}
		if !hadToolCall && pendingAnswer.Len() > 0 {
			events <- ChatEvent{Type: ChatEventAnswerDelta, Text: pendingAnswer.String()}
		}
		if reasoning.Len() > 0 {
			persistReasoning(sess, reasoning.String())
		}
	}()
	answer, usage, chatErr := ag.Chat(ctx, req.Query)
	sess.State = string(ag.State)
	// 关闭 Agent 输出通道，让事件转发协程结束。
	close(ag.Output)
	<-done
	if err := s.sessions.Update(sess); err != nil {
		slog.Warn("chat: 保存会话失败", "session", sess.ID, "error", err)
	}
	events <- ChatEvent{Type: ChatEventAnswer, Text: answer, Usage: &usage}
	if chatErr != nil {
		events <- ChatEvent{Type: ChatEventError, Text: chatErr.Error()}
	}
}

func persistReasoning(sess *session.Session, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	message := session.Message{Role: "assistant", Type: "reasoning", Content: text}
	insertAt := -1
	for i, existing := range sess.Messages {
		if existing.Role == "assistant" && existing.Type == "text" {
			insertAt = i
		}
	}
	if insertAt >= 0 {
		sess.Messages = append(sess.Messages, session.Message{})
		copy(sess.Messages[insertAt+1:], sess.Messages[insertAt:])
		sess.Messages[insertAt] = message
		sess.LastModified = time.Now()
		return
	}
	sess.AddMessage(message)
}

// summarizeToolPrelude 保留工具调用前的短进度说明，避免把真正的诊断文本直接丢掉。
// 关键点：只过滤明显的状态噪音（空文本、超长文本、Markdown/代码块），不要因为
// “修复/建议/结论”等词语出现在思考过程中而误伤实际诊断内容。
func summarizeToolPrelude(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || len([]rune(text)) > 160 {
		return ""
	}
	if strings.Contains(text, "```") || strings.Contains(text, "##") {
		return ""
	}
	return text
}

// translateEvent 把 Agent 消息总线事件转成服务层事件。
func translateEvent(m agent.Message) (ChatEvent, bool) {
	switch m.Type {
	case agent.MessageTypeText:
		// SourceModel = LLM 流式增量；SourceUser = 用户输入回显（CLI 交互模式）。
		if m.Source == agent.SourceModel {
			return ChatEvent{Type: ChatEventAnswerDelta, Text: toString(m.Payload)}, true
		}
		return ChatEvent{Type: ChatEventText, Text: toString(m.Payload)}, true
	case agent.MessageTypeReasoningDelta:
		return ChatEvent{Type: ChatEventReasoningDelta, Text: toString(m.Payload)}, true
	case agent.MessageTypeError:
		return ChatEvent{Type: ChatEventError, Text: toString(m.Payload)}, true
	case agent.MessageTypeAgentState:
		return ChatEvent{Type: ChatEventState, State: toString(m.Payload)}, true
	case agent.MessageTypeToolCallRequest:
		if ev, ok := m.Payload.(agent.ToolCallEvent); ok {
			return ChatEvent{Type: ChatEventToolCall, Tool: &ChatToolCallView{Name: ev.Name, Arguments: ev.Arguments, Risk: ev.Risk}}, true
		}
	case agent.MessageTypeToolCallResponse:
		if mm, ok := m.Payload.(map[string]any); ok {
			return ChatEvent{
				Type: ChatEventToolResponse,
				Tool: &ChatToolCallView{
					Name:      toString(mm["name"]),
					Result:    toString(mm["text"]),
					Summary:   toString(mm["summary"]),
					Ok:        boolPtr(mm["ok"]),
					Truncated: toBool(mm["truncated"]),
					Data:      mm["data"],
				},
			}, true
		}
	}
	return ChatEvent{}, false
}

// chatApprover 把审批请求转成 choice-request 事件，并等待 respond 回传选择。
type chatApprover struct {
	events      chan<- ChatEvent
	respond     <-chan string
	skip        bool
	autoApprove bool
}

func (a *chatApprover) Confirm(ctx context.Context, prompt string, options []agent.UserChoiceOption) (string, error) {
	if a.skip || a.autoApprove {
		return agent.ChoiceYes, nil
	}
	opts := make([]ChatChoiceOption, 0, len(options))
	for _, o := range options {
		opts = append(opts, ChatChoiceOption{Label: o.Label, Value: o.Value})
	}
	a.events <- ChatEvent{Type: ChatEventChoice, Choice: &ChatChoice{Prompt: prompt, Options: opts}}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case c := <-a.respond:
		return c, nil
	}
}

// GetOrCreateSession 获取已有会话，不存在则创建。
func (s *chatService) GetOrCreateSession(id, namespace, model string) (*session.Session, error) {
	if id != "" {
		if sess, err := s.sessions.Get(id); err == nil {
			return sess, nil
		}
	}
	return s.sessions.NewSession(id, s.cfg.Kubernetes.Context, namespace, model)
}

func (s *chatService) ListSessions() ([]session.Session, error) {
	list, err := s.sessions.List()
	if err != nil {
		return nil, err
	}
	out := make([]session.Session, 0, len(list))
	for _, sess := range list {
		out = append(out, *sess)
	}
	return out, nil
}

func (s *chatService) DeleteSession(id string) error {
	return s.sessions.Delete(id)
}

func (s *chatService) ClearSession(id string) error {
	sess, err := s.sessions.Get(id)
	if err != nil {
		return err
	}
	sess.ClearMessages()
	return s.sessions.Update(sess)
}

func (s *chatService) RenameSession(id, name string) error {
	sess, err := s.sessions.Get(id)
	if err != nil {
		return err
	}
	sess.Name = name
	return s.sessions.Update(sess)
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// boolPtr 返回 *bool：字段缺失（旧客户端/旧会话无 ok 键）时为 nil，前端据此回退正则兜底。
func boolPtr(v any) *bool {
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}
