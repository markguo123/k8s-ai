package service

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"

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
	"github.com/k8s-ai/k8s-ai/internal/skills"
	"github.com/k8s-ai/k8s-ai/internal/sse"
	"github.com/k8s-ai/k8s-ai/internal/store"
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
	// Secret 为 true 时该请求要收集 Secret 值：前端用密码框，值不回显、不入会话与审计。
	Secret bool `json:"secret,omitempty"`
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

// SessionTokenizer 是 ChatService 的可选能力（P2.13）：返回会话级 token 用量合计。
// 单独成接口而不是并入 ChatService，是为了让三期实现与其测试桩无需跟着变更；
// 调用方用类型断言探测，未实现时按"未持久化"退化为不展示统计。
type SessionTokenizer interface {
	SessionTokenUsage(ctx context.Context, sessionID string) (session.TokenUsage, error)
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
	// store 是四期 SQLite/MySQL 统一持久化门面：非空时把 Turn/Step 生命周期记录落库（P1.8）。
	store store.Store
	// sink 非空时把统一 SSE 事件（P1.9 事件路由）实时广播给会话客户端；
	// 三期 ChatEvent 事件流原样保留（D-08：迁移期旧事件继续可消费）。
	sink func(sse.Event)
}

// NewChat 创建二期 Agent 服务。
func NewChat(cfg *config.Config, newReader func() (scanner.Reader, error), llmFactory func(llm.Options) llm.LLMClient, scanFactory func() tools.ScanRunner, newWriteRunner func() (tools.WriteRunner, error)) ChatService {
	return newChat(cfg, newReader, llmFactory, scanFactory, newWriteRunner, nil)
}

func newChat(cfg *config.Config, newReader func() (scanner.Reader, error), llmFactory func(llm.Options) llm.LLMClient, scanFactory func() tools.ScanRunner, newWriteRunner func() (tools.WriteRunner, error), st store.Store) ChatService {
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
		store:          st,
	}
}

// NewChatWithStore 用 Store 持久化创建四期 Agent 服务（P1.8）：非空 store 时把每轮
// Session/Turn 及其 Reason/Tool/Observe/Approval/Response Step 记录入库，与三期文件会话并存。
func NewChatWithStore(cfg *config.Config, newReader func() (scanner.Reader, error), llmFactory func(llm.Options) llm.LLMClient, scanFactory func() tools.ScanRunner, newWriteRunner func() (tools.WriteRunner, error), st store.Store) ChatService {
	return newChat(cfg, newReader, llmFactory, scanFactory, newWriteRunner, st)
}

// NewChatWithSink 创建四期 Agent 服务：保留三期事件流，并通过 sink 实时广播统一 SSE 事件（P1.9）。
// 无 Store 参与，仅做事件路由广播。
func NewChatWithSink(cfg *config.Config, newReader func() (scanner.Reader, error), llmFactory func(llm.Options) llm.LLMClient, scanFactory func() tools.ScanRunner, newWriteRunner func() (tools.WriteRunner, error), sink func(sse.Event)) ChatService {
	svc := newChat(cfg, newReader, llmFactory, scanFactory, newWriteRunner, nil).(*chatService)
	svc.sink = sink
	return svc
}

// NewChatWithStoreAndSink 创建四期 Agent 服务：同时做 Store 持久化（P1.8）与统一 SSE 事件广播（P1.9）。
func NewChatWithStoreAndSink(cfg *config.Config, newReader func() (scanner.Reader, error), llmFactory func(llm.Options) llm.LLMClient, scanFactory func() tools.ScanRunner, newWriteRunner func() (tools.WriteRunner, error), st store.Store, sink func(sse.Event)) ChatService {
	svc := newChat(cfg, newReader, llmFactory, scanFactory, newWriteRunner, st).(*chatService)
	svc.sink = sink
	return svc
}

// SetSink 挂载统一 SSE 事件广播（P1.9 事件路由入口，Web 用可选接口断言注入）。
func (s *chatService) SetSink(sink func(sse.Event)) {
	s.sink = sink
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

// NewChatWithStoreAndConfig 用完整配置 + 四期 Store 创建 Agent 服务（Web/Server 组合根入口）：
// 既保留三期事件流，又把 Session/Turn/Step 与 token 用量落库（P1.8/P2.13）。
func NewChatWithStoreAndConfig(cfg *config.Config, st store.Store) ChatService {
	return NewChatWithStore(cfg, func() (scanner.Reader, error) { return chatReader(cfg) }, nil, nil, nil, st)
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
	// 本轮运行的 turn/step 作用域要在工具装配前创建：Skill 观测回调要用它补关联字段。
	scope := &runScope{sessID: sess.ID}
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
			opts := createToolOptions(s.cfg)
			skillReg, err := skills.NewBuiltin()
			if err != nil {
				return nil, err
			}
			opts.Skills = skillReg
			opts.OnSkillRun = s.skillRunAuditor(ctx, sess, scope)
			if err := reg.Register(tools.NewCreateTool(opts, runner, s.proposals, sess.ID)); err != nil {
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
	// P1.9: 生命周期观察器同时负责 Store 持久化与统一事件广播，approver 复用同一
	// runScope 给 approval.required 补全关联字段。
	tracer := s.chatTracer(ctx, sess, scope)
	approver := &chatApprover{
		events:  events,
		respond: respond,
		sink:    s.sink,
		scope:   scope,
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
	recorder := s.newAuditRecorder(sess, scope)
	// P1.9: 会话创建即广播 session.created；后续 Turn/Step 生命周期由 Tracer 转发。
	if s.sink != nil {
		s.sink(sse.New(sess.ID, "", "", sse.EventSessionCreated, sse.SessionData{State: sess.State}))
	}
	ag := agent.New(agent.Options{
		LLM:            client,
		Tools:          reg,
		Session:        sess,
		Approver:       approver,
		ApprovalPolicy: &pol,
		Recorder:       recorder,
		Tracer:         tracer,
		MaxIterations:  s.cfg.Agent.MaxIterations,
		MaxToolCalls:   s.cfg.Agent.MaxToolCalls,
		Timeout:        s.cfg.Agent.Timeout,
		ToolCalling:    s.cfg.Agent.ToolCalling,
		ResultMaxBytes: s.cfg.Agent.ResultMaxBytes,
		TurnMaxBytes:   s.cfg.Agent.TurnMaxBytes,
		HistoryTurns:   s.cfg.Agent.HistoryTurns,
	})
	go s.runChat(ctx, ag, sess, req, events, recorder, s.sink, scope)
	return events, nil
}

// chatTracer 装配生命周期观察器：统一事件广播器（P1.9）+ Store 持久化（P1.8），
// 两者共用调用方创建的 runScope（当前 turn/step 作用域）。
// 数据库不可用时不阻塞聊天，仅回退为不持久化。
func (s *chatService) chatTracer(ctx context.Context, sess *session.Session, scope *runScope) agent.Tracer {
	var persist agent.Tracer
	if s.store != nil {
		if err := s.store.Sessions().Create(ctx, sess); err != nil && !errors.Is(err, store.ErrExists) {
			slog.Warn("chat: 四期会话初始化失败，本轮跳过 Turn/Step 持久化", "session", sess.ID, "error", err)
		} else {
			persist = &persistTracer{store: s.store}
		}
	}
	if s.sink == nil && persist == nil {
		return nil
	}
	return &fanOutTracer{scope: scope, persist: persist, sink: s.sink}
}

// persistTracer 把 Agent 生命周期回调写入四期 Store（P1.8）。落库前由 Store 侧统一脱敏，
// 回调为观察性质，所有写失败只记日志、不中断 Agent 主循环。
type persistTracer struct {
	store store.Store
}

func (t *persistTracer) TurnStarted(ctx context.Context, turn *session.Turn) {
	if err := t.store.Turns().Create(ctx, turn); err != nil {
		slog.Warn("chat: Turn 写入失败", "turn", turn.ID, "error", err)
	}
}

func (t *persistTracer) TurnUpdated(ctx context.Context, turn *session.Turn) {
	if err := t.store.Turns().Update(ctx, turn); err != nil {
		slog.Warn("chat: Turn 更新失败", "turn", turn.ID, "error", err)
	}
}

func (t *persistTracer) StepStarted(ctx context.Context, step *session.Step) {
	if err := t.store.Steps().Create(ctx, step); err != nil {
		slog.Warn("chat: Step 写入失败", "turn", step.TurnID, "step", step.ID, "error", err)
	}
}

func (t *persistTracer) StepUpdated(ctx context.Context, step *session.Step) {
	if err := t.store.Steps().Update(ctx, step); err != nil {
		slog.Warn("chat: Step 更新失败", "turn", step.TurnID, "step", step.ID, "error", err)
	}
}

// newAuditRecorder 为会话创建审计记录器（目录不存在自动创建；失败回退日志记录器）。
// 装配了四期 Store 时同一事件同时落 AuditStore（P1.11），关联字段取自会话与本轮 turn/step。
func (s *chatService) newAuditRecorder(sess *session.Session, scope *runScope) audit.Recorder {
	path := filepath.Join(s.cfg.Agent.AuditDir, sess.ID+".jsonl")
	var inner audit.Recorder
	fileRecorder, err := audit.NewFileRecorder(path)
	if err != nil {
		slog.Warn("chat: 审计文件不可用，回退日志记录器", "path", path, "error", err)
		inner = &audit.LogRecorder{}
	} else {
		inner = fileRecorder
	}
	if s.store == nil {
		return inner
	}
	return newStoreAuditRecorder(inner, s.store, scope, AuditScope{
		SessionID: sess.ID,
		ClusterID: sess.Cluster,
	})
}

func (s *chatService) runChat(ctx context.Context, ag *agent.Agent, sess *session.Session, req ChatRequest, events chan<- ChatEvent, recorder audit.Recorder, sink func(sse.Event), scope *runScope) {
	defer close(events)
	defer func() { _ = recorder.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var pendingAnswer strings.Builder
		hadToolCall := false
		// 分片序号按 role 各自计数：reasoning 与 assistant 共用一个计数器会让
		// 报告的“第几段”被思考分片推高，消费方无法据此排序。
		reasoningIndex := 0
		answerIndex := 0
		for m := range ag.Output {
			if ev, ok := translateEvent(m); ok {
				switch ev.Type {
				case ChatEventReasoningDelta:
					events <- ev
					emitMessageDelta(sink, scope, "reasoning", ev.Text, reasoningIndex)
					reasoningIndex++
				case ChatEventProgress:
					events <- ev
				case ChatEventAnswerDelta, ChatEventDelta:
					// 先缓存回答增量。若后续发现本轮实际包含工具调用，
					// 这段内容只能是工具前的中间说明，不能提前展示为报告。
					pendingAnswer.WriteString(ev.Text)
					emitMessageDelta(sink, scope, "assistant", ev.Text, answerIndex)
					answerIndex++
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
	}()
	answer, usage, chatErr := ag.Chat(ctx, req.Query)
	sess.State = string(ag.State)
	// 先关通道并等转发协程收尾，再发终态事件。事件顺序必须是
	// message.delta… → message.completed（权威全文）→ session.completed / failed；
	// 否则最后一段正文会落在终态之后，被前端当成新分片并永久停在“正在生成…”。
	close(ag.Output)
	<-done
	if sink != nil {
		if answer != "" {
			sink(sse.New(sess.ID, scope.turn(), "", sse.EventMessageCompleted, sse.MessageData{
				Role: "assistant", Delta: store.RedactJSONPayload(answer),
			}))
		}
		if chatErr != nil {
			sink(sse.New(sess.ID, scope.turn(), "", sse.EventSessionFailed, sse.SessionData{
				State: sess.State, Reason: chatErr.Error(),
			}))
		} else {
			sink(sse.New(sess.ID, scope.turn(), "", sse.EventSessionCompleted, sse.SessionData{State: sess.State}))
		}
	}
	if err := s.sessions.Update(sess); err != nil {
		slog.Warn("chat: 保存会话失败", "session", sess.ID, "error", err)
	}
	if s.store != nil {
		if err := s.store.Sessions().Update(ctx, sess); err != nil {
			slog.Warn("chat: 四期会话保存失败", "session", sess.ID, "error", err)
		}
	}
	events <- ChatEvent{Type: ChatEventAnswer, Text: answer, Usage: &usage}
	if chatErr != nil {
		events <- ChatEvent{Type: ChatEventError, Text: chatErr.Error()}
	}
}

// emitMessageDelta 通过 sink 广播统一 message.delta 事件（携带当前 turn/step 作用域）。
// 增量与终态走同一套脱敏（sse 包约定：写入事件流的内容在封装前必须脱敏），
// 避免同一段正文在流式过程中与 message.completed 里呈现不同内容。
func emitMessageDelta(sink func(sse.Event), scope *runScope, role, delta string, index int) {
	if sink == nil {
		return
	}
	sink(sse.New(scope.session(), scope.turn(), scope.step(), sse.EventMessageDelta, sse.MessageData{
		Role: role, Delta: store.RedactJSONPayload(delta), Index: index,
	}))
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
	// sink 非空时以统一事件顺带广播审批请求（P1.9）。
	sink  func(sse.Event)
	scope *runScope
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
	if a.sink != nil {
		sseOpts := make([]sse.ChoiceOption, 0, len(options))
		for _, o := range options {
			sseOpts = append(sseOpts, sse.ChoiceOption{Label: o.Label, Value: o.Value})
		}
		a.sink(sse.New(a.scope.session(), a.scope.turn(), a.scope.step(), sse.EventApprovalRequired, sse.ApprovalData{
			Prompt: prompt, Options: sseOpts,
		}))
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case c := <-a.respond:
		return c, nil
	}
}

// RequestSecretValue 走同一条 choice 通道索取 Secret 值（secret=true）。
// 返回值只存在于内存与本次工具调用，不进事件流、会话文件与审计日志。
func (a *chatApprover) RequestSecretValue(ctx context.Context, prompt string) (string, error) {
	if a.skip || a.autoApprove {
		return "", errors.New("非交互模式下无法向用户索取 Secret 值，请改用 $GENERATE 由 k8s-ai 随机生成")
	}
	a.events <- ChatEvent{Type: ChatEventChoice, Choice: &ChatChoice{Prompt: prompt, Secret: true}}
	if a.sink != nil {
		// 只下发 secret=true 标记，明文值从不进入事件流（AGENTS.md 红线）。
		a.sink(sse.New(a.scope.session(), a.scope.turn(), a.scope.step(), sse.EventApprovalRequired, sse.ApprovalData{
			Prompt: prompt, Secret: true,
		}))
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case c := <-a.respond:
		value := strings.TrimSpace(c)
		if value == "" || strings.EqualFold(value, "no") {
			return "", nil
		}
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

// SessionTokenUsage 汇总会话内各轮 token 用量（P2.13）。
// 未装配四期 Store（三期运行模式）时返回零值：Web 右下角统计自动隐藏，
// 而不是把"未持久化"误报成 0 tokens。
func (s *chatService) SessionTokenUsage(ctx context.Context, sessionID string) (session.TokenUsage, error) {
	if s.store == nil || sessionID == "" {
		return session.TokenUsage{}, nil
	}
	return s.store.Turns().SumTokensBySession(ctx, sessionID)
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
