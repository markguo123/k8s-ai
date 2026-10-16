// Package webui 实现二期 Web chat UI（k8s-ai web）。
// 交互设计借鉴 kubectl-ai 的 HTML UI（会话侧栏 + SSE 消息流 + 审批卡片），
// 底层复用 service.ChatService（ADR-007），不再维护长驻 Agent。
package webui

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/sse"
)

//go:embed index.html
var indexHTML []byte

// chatService 是 Web UI 依赖的最小应用服务接口（service.ChatService 满足）。
type chatService interface {
	Chat(ctx context.Context, req service.ChatRequest, respond <-chan string) (<-chan service.ChatEvent, error)
	GetOrCreateSession(id, namespace, model string) (*session.Session, error)
	ListSessions() ([]session.Session, error)
	DeleteSession(id string) error
	RenameSession(id, name string) error
	PinSession(id string, pinned bool) error
}

// sseEmitter 是可为 ChatService 挂载统一事件广播的可选接口（P1.9）。
// 三期实现不需要支持；四期服务通过 NewChatWithSink/NewChatWithStoreAndSink 构造后实现，
// 使 SSE 同时承载统一新事件与三期旧事件（D-08：迁移期旧事件继续可消费）。
type sseEmitter interface {
	SetSink(sink func(sse.Event))
}

// Broadcaster 每会话一个，SSE 消息扇出（设计借鉴 kubectl-ai）。
type Broadcaster struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

func newBroadcaster() *Broadcaster {
	return &Broadcaster{clients: map[chan []byte]struct{}{}}
}

func (b *Broadcaster) add() chan []byte {
	ch := make(chan []byte, 256)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broadcaster) remove(ch chan []byte) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
}

func (b *Broadcaster) broadcast(msg []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		select {
		case ch <- msg:
		default:
			// 客户端消费慢则丢消息（SSE 重连时以历史快照兜底）。
		}
	}
}

// Server 是 Web chat HTTP 服务。
type Server struct {
	chat  chatService
	model string

	mu           sync.Mutex
	broadcasters map[string]*Broadcaster
	pending      map[string]chan string        // sessionID → 当前 Chat 的审批响应通道
	runs         map[string]context.CancelFunc // sessionID → 当前 Chat 运行取消函数
}

// New 创建 Web UI 服务。
func New(chat chatService, model string) *Server {
	s := &Server{
		chat:         chat,
		model:        model,
		broadcasters: map[string]*Broadcaster{},
		pending:      map[string]chan string{},
		runs:         map[string]context.CancelFunc{},
	}
	// 服务端事件路由：统一事件按 session_id 扇出到对应 Broadcaster；
	// 旧 ChatEvent JSON 仍在 handleSendMessage 里并行广播，两者不互相覆盖。
	if em, ok := chat.(sseEmitter); ok {
		em.SetSink(func(e sse.Event) {
			raw, err := json.Marshal(e)
			if err != nil {
				slog.Warn("webui: 统一事件序列化失败", "error", err)
				return
			}
			s.broadcaster(e.SessionID).broadcast(raw)
		})
	}
	return s
}

// Handler 返回路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.serveIndex)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("POST /api/sessions/{id}/rename", s.handleRenameSession)
	mux.HandleFunc("POST /api/sessions/{id}/pin", s.handlePinSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("GET /api/sessions/{id}/stream", s.handleStream)
	mux.HandleFunc("POST /api/sessions/{id}/send-message", s.handleSendMessage)
	mux.HandleFunc("POST /api/sessions/{id}/approve", s.handleApprove)
	mux.HandleFunc("POST /api/sessions/{id}/cancel", s.handleCancel)
	return mux
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	list, err := s.chat.ListSessions()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	type item struct {
		ID           string    `json:"id"`
		Name         string    `json:"name"`
		State        string    `json:"state"`
		Model        string    `json:"model,omitempty"`
		MessageCount int       `json:"messageCount"`
		LastModified time.Time `json:"lastModified"`
		// P7.5：置顶排序 + 标题自动提取（首条用户意图）。
		Pinned bool `json:"pinned,omitempty"`
		// Token 为会话级 token 用量合计（P2.13）；未装配四期 Store 时省略。
		Token *session.TokenUsage `json:"token,omitempty"`
	}
	// 置顶会话始终排最前；置顶集合内部与非置顶集合内部均按最近活跃降序。
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Pinned != list[j].Pinned {
			return list[i].Pinned
		}
		return list[i].LastModified.After(list[j].LastModified)
	})
	out := make([]item, 0, len(list))
	for _, sess := range list {
		out = append(out, item{
			ID:           sess.ID,
			Name:         sessionTitle(sess),
			State:        sess.State,
			Model:        sess.Model,
			MessageCount: len(sess.Messages),
			LastModified: sess.LastModified,
			Pinned:       sess.Pinned,
			Token:        s.sessionToken(sess.ID),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// sessionTitle 返回会话列表展示标题（P7.5）：用户显式重命名优先；
// 仍为自动生成的 "Session <id>" 时回退到首条用户消息意图，再回退"新会话"。
func sessionTitle(sess session.Session) string {
	if sess.Name != "" && sess.Name != "Session "+sess.ID {
		return sess.Name
	}
	for _, m := range sess.Messages {
		if m.Role != "user" || m.Type != "text" {
			continue
		}
		line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(m.Content), "\n", 2)[0])
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > 24 {
			return string(runes[:24]) + "…"
		}
		return line
	}
	return "新会话"
}

// sessionToken 读取会话级 token 用量合计（P2.13）；未装配 Store 或读取失败时返回 nil，
// 由前端退化为"不展示统计"，避免把读不到当成 0。
func (s *Server) sessionToken(sessionID string) *session.TokenUsage {
	provider, ok := s.chat.(service.SessionTokenizer)
	if !ok {
		return nil
	}
	usage, err := provider.SessionTokenUsage(context.Background(), sessionID)
	if err != nil {
		slog.Warn("webui: 读取会话 token 用量失败", "session", sessionID, "error", err)
		return nil
	}
	if usage.IsZero() {
		return nil
	}
	return &usage
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)
	sess, err := s.chat.GetOrCreateSession(body.ID, "", s.model)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if body.Name != "" && body.Name != sess.Name {
		if err := s.chat.RenameSession(sess.ID, body.Name); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		sess.Name = body.Name
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("name must not be empty"))
		return
	}
	if err := s.chat.RenameSession(id, body.Name); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "name": body.Name})
}

// handlePinSession 置顶/取消置顶会话（P7.5）。
func (s *Server) handlePinSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Pinned bool `json:"pinned"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.chat.PinSession(id, body.Pinned); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "pinned": body.Pinned})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.chat.DeleteSession(id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.mu.Lock()
	if cancel, ok := s.runs[id]; ok {
		cancel()
	}
	delete(s.broadcasters, id)
	delete(s.pending, id)
	delete(s.runs, id)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"deleted": id})
}

// handleStream 以 SSE 推送会话事件：先发历史快照，再转发实时事件。
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sess, err := s.chat.GetOrCreateSession(id, "", s.model)
	if err != nil {
		return
	}
	snapshot, _ := json.Marshal(map[string]any{
		"type":      "history",
		"sessionId": sess.ID,
		"state":     sess.State,
		"messages":  sess.Messages,
		// Token 为会话级累计用量（P2.13）：刷新/重开会话后右下角统计仍可读。
		"token": s.sessionToken(sess.ID),
	})
	fmt.Fprintf(w, "data: %s\n\n", snapshot)
	flusher.Flush()

	b := s.broadcaster(id)
	ch := b.add()
	defer b.remove(ch)
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

// handleSendMessage 发起一轮 Chat 并把事件广播到会话的 SSE 客户端。
func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Query           string   `json:"query"`
		SkipPermissions bool     `json:"skipPermissions"`
		// Skills 用户显式 @ 指定的 Skill 名称（P3.8.4），由 service 层强校验。
		Skills []string `json:"skills"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Query == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("query must not be empty"))
		return
	}
	s.mu.Lock()
	if _, running := s.pending[id]; running {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, fmt.Errorf("a chat is already running for this session"))
		return
	}
	respond := make(chan string, 1)
	chatCtx, cancelChat := context.WithCancel(context.Background())
	s.pending[id] = respond
	s.runs[id] = cancelChat
	s.mu.Unlock()

	// Chat 生命周期独立于 HTTP 请求：请求返回后 SSE 仍需持续推送。
	events, err := s.chat.Chat(chatCtx, service.ChatRequest{
		SessionID:       id,
		Query:           body.Query,
		SkipPermissions: body.SkipPermissions,
		Skills:          body.Skills,
	}, respond)
	if err != nil {
		cancelChat()
		s.mu.Lock()
		delete(s.pending, id)
		delete(s.runs, id)
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	b := s.broadcaster(id)
	go func() {
		defer cancelChat()
		defer func() {
			s.mu.Lock()
			delete(s.pending, id)
			delete(s.runs, id)
			s.mu.Unlock()
		}()
		for ev := range events {
			raw, err := json.Marshal(ev)
			if err != nil {
				slog.Warn("webui: 事件序列化失败", "error", err)
				continue
			}
			b.broadcast(raw)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"sessionId": id})
}

// handleCancel 取消当前会话正在执行的 Chat 运行。
func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	cancel, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusConflict, fmt.Errorf("no active run for session %s", id))
		return
	}
	cancel()
	writeJSON(w, http.StatusOK, map[string]string{"sessionId": id})
}

// handleApprove 把审批选择回传给当前 Chat 的 respond 通道。
func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Choice string `json:"choice"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.mu.Lock()
	respond, ok := s.pending[id]
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusConflict, fmt.Errorf("no pending approval for session %s", id))
		return
	}
	select {
	case respond <- body.Choice:
		// 不回显 choice：Secret 值经此通道提交，响应体必须原样返回会话 id。
		writeJSON(w, http.StatusOK, map[string]string{"sessionId": id})
	default:
		writeError(w, http.StatusConflict, fmt.Errorf("approval already answered"))
	}
}

func (s *Server) broadcaster(id string) *Broadcaster {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.broadcasters[id]; ok {
		return b
	}
	b := newBroadcaster()
	s.broadcasters[id] = b
	return b
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
