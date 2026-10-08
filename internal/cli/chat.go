package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/chzyer/readline"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/k8s-ai/k8s-ai/internal/service"
)

type chatFlags struct {
	format          string
	session         string
	newSession      bool
	listSessions    bool
	deleteSession   string
	skipPermissions bool
	namespace       string
}

// newChatCmd 二期 Agent：自然语言 Kubernetes 对话（交互/单发/stdin/JSON）。
func newChatCmd(deps Dependencies) *cobra.Command {
	var f chatFlags
	cmd := &cobra.Command{
		Use:   "chat [query]",
		Short: "Natural-language Kubernetes agent",
		Long:  "Chat with a Kubernetes AI agent. Reads from args, stdin, or interactively.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChat(deps, cmd, args, &f)
		},
	}
	cmd.Flags().StringVar(&f.format, "format", "text", "output format (text|json)")
	cmd.Flags().StringVar(&f.session, "session", "", "session id (resume or create)")
	cmd.Flags().BoolVar(&f.newSession, "new-session", false, "force a new session")
	cmd.Flags().BoolVar(&f.listSessions, "list-sessions", false, "list sessions")
	cmd.Flags().StringVar(&f.deleteSession, "delete-session", "", "delete a session by id")
	cmd.Flags().BoolVar(&f.skipPermissions, "skip-permissions", false, "skip write confirmation (automation only, dangerous)")
	cmd.Flags().String("kubeconfig", "", "path to kubeconfig")
	cmd.Flags().String("context", "", "kubeconfig context")
	cmd.Flags().StringVarP(&f.namespace, "namespace", "n", "", "default namespace for the session")
	return cmd
}

func runChat(deps Dependencies, cmd *cobra.Command, args []string, f *chatFlags) error {
	switch f.format {
	case "text", "json":
	default:
		return fmt.Errorf("invalid --format %q (text|json)", f.format)
	}
	cfg, err := deps.LoadConfig(loadOptionsFromFlags(cmd))
	if err != nil {
		return err
	}
	chat := deps.NewChat(cfg)

	if f.listSessions {
		return listSessions(cmd.OutOrStdout(), chat)
	}
	if f.deleteSession != "" {
		if err := chat.DeleteSession(f.deleteSession); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "session %s deleted\n", f.deleteSession)
		return nil
	}

	var wantID string
	switch {
	case f.newSession:
		wantID = ""
	case f.session != "":
		wantID = f.session
	default:
		wantID = ""
	}
	ns := f.namespace
	if ns == "" {
		ns = cfg.Kubernetes.Namespace
	}
	sess, err := chat.GetOrCreateSession(wantID, ns, cfg.LLM.Model)
	if err != nil {
		return err
	}

	query := ""
	if len(args) > 0 {
		query = strings.TrimSpace(args[0])
	}
	in := cmd.InOrStdin()
	if stdinHasData(in) {
		data, readErr := io.ReadAll(in)
		if readErr != nil {
			return readErr
		}
		if q := strings.TrimSpace(string(data)); q != "" {
			if query != "" {
				query = query + "\n" + q
			} else {
				query = q
			}
		}
	}
	if query == "" {
		return runChatInteractive(cmd, chat, sess.ID, f, cfg.Kubernetes.Context)
	}
	return runChatOnce(cmd, chat, sess.ID, query, f)
}

func listSessions(w io.Writer, chat service.ChatService) error {
	list, err := chat.ListSessions()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(w, "no sessions")
		return nil
	}
	fmt.Fprintf(w, "%-24s %-12s %-8s %s\n", "ID", "STATE", "MSGS", "MODIFIED")
	for _, s := range list {
		fmt.Fprintf(w, "%-24s %-12s %-8d %s\n", s.ID, s.State, len(s.Messages), s.LastModified.Format("2006-01-02 15:04"))
	}
	return nil
}

// runChatOnce 非交互单发：输出最终结果（text/json）；审批默认拒绝。
func runChatOnce(cmd *cobra.Command, chat service.ChatService, sessionID, query string, f *chatFlags) error {
	respond := make(chan string, 1)
	events, err := chat.Chat(cmd.Context(), service.ChatRequest{
		SessionID:       sessionID,
		Query:           query,
		SkipPermissions: f.skipPermissions,
	}, respond)
	if err != nil {
		return err
	}
	var answer string
	var usage *serviceUsage
	var toolCalls []service.ChatToolCallView
	var chatErr string
	w := cmd.OutOrStdout()
	md := newMarkdownRenderer()
	for ev := range events {
		switch ev.Type {
		case service.ChatEventDelta:
			// 非交互模式：流式增量直接追加到 answer（最终一起输出）。
			if ev.Text != "" {
				if ev.Text != "" {
					answer += ev.Text
				}
			}
		case service.ChatEventAnswer:
			answer = ev.Text
			if ev.Usage != nil {
				usage = &serviceUsage{
					PromptTokens:     ev.Usage.PromptTokens,
					CompletionTokens: ev.Usage.CompletionTokens,
					TotalTokens:      ev.Usage.TotalTokens,
				}
			}
		case service.ChatEventToolCall:
			if ev.Tool != nil {
				toolCalls = append(toolCalls, *ev.Tool)
			}
		case service.ChatEventError:
			chatErr = ev.Text
		case service.ChatEventChoice:
			// 非交互默认拒绝写操作（spec §12.3）。
			respond <- "no"
		}
	}
	if f.format == "json" {
		out := chatJSONOutput{
			SessionID: sessionID,
			Answer:    answer,
			ToolCalls: toolCalls,
			Usage:     usage,
		}
		raw, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(w, string(raw))
	} else if answer != "" {
		fmt.Fprintln(w, renderMarkdown(md, answer))
	}
	if chatErr != "" {
		return errors.New(chatErr)
	}
	return nil
}

// runChatInteractive 交互模式：提示符循环 + 事件渲染 + 审批输入。
// 终端输入走 readline（历史记录 / Ctrl+C/Ctrl+D 优雅退出），管道/测试回退 bufio。
func runChatInteractive(cmd *cobra.Command, chat service.ChatService, sessionID string, f *chatFlags, cluster string) error {
	w := cmd.OutOrStdout()
	errW := cmd.ErrOrStderr()
	md := newMarkdownRenderer()
	printChatBanner(w, cluster)
	in := newChatInput(cmd.InOrStdin(), w)
	defer in.Close()
	for {
		in.SetPrompt("> ")
		line, err := in.Readline()
		if err != nil {
			// EOF / Ctrl+C / Ctrl+D → 退出；不让单次读错误中断会话。
			return nil
		}
		query := strings.TrimSpace(line)
		switch query {
		case "":
			continue
		case "exit", "quit":
			return nil
		case "clear":
			if err := chat.ClearSession(sessionID); err != nil {
				fmt.Fprintln(errW, "error:", err)
			} else {
				fmt.Fprintln(w, "会话已清空。")
			}
			continue
		}
		respond := make(chan string, 1)
		events, err := chat.Chat(cmd.Context(), service.ChatRequest{
			SessionID:       sessionID,
			Query:           query,
			SkipPermissions: f.skipPermissions,
		}, respond)
		if err != nil {
			fmt.Fprintln(errW, "error:", err)
			continue
		}
		renderEvents(events, respond, in, md, w, errW)
	}
}

// chatInput 抽象交互输入：终端走 readline，非 TTY（管道/测试）回退 bufio。
type chatInput interface {
	SetPrompt(string)
	Readline() (string, error)
	// ReadSecret 读取不回显的一行输入（Secret 值）。
	ReadSecret() (string, error)
	Close() error
}

// readlineInput 包装 chzyer/readline：历史记录文件 + Ctrl+C 归一为 EOF。
type readlineInput struct {
	rl    *readline.Instance
	stdin *os.File
}

func (r *readlineInput) SetPrompt(p string) { r.rl.SetPrompt(p) }

// ReadSecret 走 x/term 关闭回显读取 Secret 值：值只在内存里流转，不进对话与日志。
func (r *readlineInput) ReadSecret() (string, error) {
	line, err := term.ReadPassword(int(r.stdin.Fd()))
	if err != nil {
		return "", err
	}
	return string(line), nil
}
func (r *readlineInput) Readline() (string, error) {
	line, err := r.rl.Readline()
	if err == readline.ErrInterrupt {
		return "", io.EOF
	}
	return line, err
}
func (r *readlineInput) Close() error { return r.rl.Close() }

// bufInput 非 TTY 回退实现：先打印已设提示符，再按行读取。
type bufInput struct {
	r      *bufio.Reader
	w      io.Writer
	prompt string
}

func (b *bufInput) SetPrompt(p string) { b.prompt = p }
func (b *bufInput) Readline() (string, error) {
	if b.prompt != "" {
		fmt.Fprint(b.w, b.prompt)
		b.prompt = ""
	}
	line, err := b.r.ReadString('\n')
	if err != nil {
		return strings.TrimRight(line, "\r\n"), err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// ReadSecret 非 TTY 无法屏蔽回显：按行读取（自动化场景请改用 $GENERATE）。
func (b *bufInput) ReadSecret() (string, error) { return b.Readline() }
func (b *bufInput) Close() error                { return nil }

// newChatInput 在真实终端启用 readline，否则回退 bufio（保持测试/管道可用）。
func newChatInput(in io.Reader, out io.Writer) chatInput {
	if f, ok := in.(*os.File); ok && isTerminal(f) {
		rl, err := readline.NewEx(&readline.Config{
			Prompt:       "> ",
			Stdin:        f,
			Stdout:       out,
			Stderr:       os.Stderr,
			HistoryFile:  filepath.Join(os.TempDir(), "k8s-ai-history"),
			HistoryLimit: 1000,
		})
		if err == nil {
			return &readlineInput{rl: rl, stdin: f}
		}
	}
	return &bufInput{r: bufio.NewReader(in), w: out}
}

// newMarkdownRenderer 创建 glamour 终端渲染器：AutoStyle 在管道/非 TTY 自动去样式。
func newMarkdownRenderer() *glamour.TermRenderer {
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithPreservedNewLines(),
		glamour.WithWordWrap(120),
	)
	if err != nil {
		return nil
	}
	return r
}

// renderMarkdown 渲染 Markdown 文本；渲染器不可用或文本为空时原样返回。
func renderMarkdown(md *glamour.TermRenderer, text string) string {
	if md == nil || strings.TrimSpace(text) == "" {
		return text
	}
	out, err := md.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimRight(out, "\n")
}

// renderEvents 渲染一轮对话的事件流；审批事件回读用户输入。
func renderEvents(events <-chan service.ChatEvent, respond chan<- string, in chatInput, md *glamour.TermRenderer, w, errW io.Writer) {
	streamedAnswer := ""
	reasoningShown := false
	for ev := range events {
		switch ev.Type {
		case service.ChatEventDelta, service.ChatEventAnswerDelta:
			// 终端保留流式接收，但延迟到 answer 事件统一做 Markdown 渲染，
			// 保持与 kubectl-ai 类似的完整报告效果。
			streamedAnswer += ev.Text
		case service.ChatEventReasoningDelta:
			// 终端不逐 token 展示原始思维链，只给一次轻量状态提示。
			if !reasoningShown {
				fmt.Fprintln(w, "  · 分析中...")
				reasoningShown = true
			}
		case service.ChatEventProgress:
			if ev.Text != "" {
				fmt.Fprintf(w, "  · %s\n", ev.Text)
			}
		case service.ChatEventText:
			// 用户输入回显（原文，不做 Markdown 渲染）。
			if ev.Text != "" {
				fmt.Fprintln(w, ev.Text)
			}
		case service.ChatEventAnswer:
			// 最终回答以 Markdown 渲染（表格/列表/代码块等），前置空行与工具调用分隔。
			answer := ev.Text
			if answer == "" {
				answer = streamedAnswer
			}
			if answer != "" {
				fmt.Fprintln(w)
				fmt.Fprintln(w, renderMarkdown(md, answer))
			}
			streamedAnswer = ""
		case service.ChatEventToolCall:
			if ev.Tool != nil {
				fmt.Fprintf(w, "  ↳ [Tool] %s %s\n", ev.Tool.Name, formatArgs(ev.Tool.Arguments))
			}
		case service.ChatEventToolResponse:
			if ev.Tool != nil && ev.Tool.Name == "create_resource" {
				renderCreateToolResult(w, ev.Tool.Data)
			}
			// 其它工具默认不打印原始结果，保持简洁。
		case service.ChatEventChoice:
			if ev.Choice != nil {
				if ev.Choice.Secret {
					// Secret 值：终端不回显，值不进对话历史、会话文件与审计日志。
					fmt.Fprintln(w, ev.Choice.Prompt)
					fmt.Fprint(w, "值（留空或输入 no 取消）: ")
					value, err := in.ReadSecret()
					fmt.Fprintln(w)
					if err != nil || strings.TrimSpace(value) == "" || strings.EqualFold(strings.TrimSpace(value), "no") {
						respond <- "no"
						continue
					}
					respond <- value
					continue
				}
				fmt.Fprintln(w, ev.Choice.Prompt)
				for i, o := range ev.Choice.Options {
					fmt.Fprintf(w, "[%d] %s\n", i+1, o.Label)
				}
				in.SetPrompt("选择: ")
				line, err := in.Readline()
				if err != nil {
					// 读失败（EOF/Ctrl+C）默认拒绝写操作。
					respond <- "no"
					continue
				}
				switch strings.TrimSpace(strings.ToLower(line)) {
				case "1", "y", "yes":
					respond <- "yes"
				case "2", "a", "always":
					respond <- "yes_and_dont_ask_again"
				default:
					respond <- "no"
				}
			}
		case service.ChatEventError:
			fmt.Fprintln(errW, "error:", ev.Text)
		}
	}
}

// renderCreateToolResult 渲染 create_resource 的提案/执行结果（终端版，不输出 Secret 原值）。
func renderCreateToolResult(w io.Writer, data any) {
	m, ok := data.(map[string]any)
	if !ok {
		return
	}
	mode, _ := m["mode"].(string)
	status, _ := m["status"].(string)
	risk, _ := m["risk"].(string)
	if mode == "" {
		mode = "dry_run"
	}
	fmt.Fprintf(w, "  -> [create_resource] mode=%s status=%s risk=%s\n", mode, status, risk)
	if reasons, ok := m["riskReasons"].([]string); ok {
		for _, r := range reasons {
			fmt.Fprintf(w, "     - %s\n", r)
		}
	} else if reasons, ok := m["riskReasons"].([]any); ok {
		for _, r := range reasons {
			fmt.Fprintf(w, "     - %v\n", r)
		}
	}
	if objs, ok := m["objects"].([]map[string]any); ok {
		for _, o := range objs {
			ref := fmt.Sprintf("%v %v", o["kind"], o["name"])
			if ns, ok := o["namespace"].(string); ok && ns != "" {
				ref = ref + " (" + ns + ")"
			}
			st, _ := o["status"].(string)
			fmt.Fprintf(w, "     %-40s %s\n", ref, st)
			if errMsg, ok := o["error"].(string); ok && errMsg != "" {
				fmt.Fprintf(w, "         error: %s\n", errMsg)
			}
			if note, ok := o["note"].(string); ok && note != "" {
				fmt.Fprintf(w, "         note: %s\n", note)
			}
		}
	} else if objs, ok := m["objects"].([]any); ok {
		for _, raw := range objs {
			o, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			ref := fmt.Sprintf("%v %v", o["kind"], o["name"])
			if ns, ok := o["namespace"].(string); ok && ns != "" {
				ref = ref + " (" + ns + ")"
			}
			st, _ := o["status"].(string)
			fmt.Fprintf(w, "     %-40s %s\n", ref, st)
			if errMsg, ok := o["error"].(string); ok && errMsg != "" {
				fmt.Fprintf(w, "         error: %s\n", errMsg)
			}
		}
	}
	if verify, ok := m["verify"]; ok {
		if vm, ok := verify.(map[string]any); ok {
			ready, _ := vm["ready"].(bool)
			fmt.Fprintf(w, "     verify: ready=%v", ready)
			if reason, ok := vm["reason"].(string); ok && reason != "" {
				fmt.Fprintf(w, " (%s)", reason)
			}
			fmt.Fprintln(w)
			if events, ok := vm["events"].([]string); ok {
				for _, e := range events {
					fmt.Fprintf(w, "         event: %s\n", e)
				}
			} else if events, ok := vm["events"].([]any); ok {
				for _, e := range events {
					fmt.Fprintf(w, "         event: %v\n", e)
				}
			}
		}
	}
}

func formatArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, 0, len(args))
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return "(" + strings.Join(parts, " ") + ")"
}

func stdinHasData(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok {
		return true // 非 *os.File（如测试注入的 Reader）按有数据处理
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice == 0
}

// serviceUsage 是 JSON 输出的 token 用量（避免 CLI 依赖 llm 包，ADR-015）。
type serviceUsage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

// chatJSONOutput 是 --format json 的输出契约（需求规格 §15.3）。
type chatJSONOutput struct {
	SessionID  string                     `json:"sessionId"`
	Answer     string                     `json:"answer"`
	Confidence float64                    `json:"confidence"`
	ToolCalls  []service.ChatToolCallView `json:"toolCalls"`
	Diagnosis  any                        `json:"diagnosis"`
	Plan       any                        `json:"plan"`
	Executed   bool                       `json:"executed"`
	Usage      *serviceUsage              `json:"usage"`
}
