// 三期创建通道的「dry run 提案 → 人工确认 → apply」一致性存储（设计文档 D-09）。
//
// 规则：apply 只接受同一会话内、同一份清单已通过 server-side dry run 的指纹。
// 指纹存在 store 里而不是工具实例内部，因为 ChatService 每轮对话都会重建工具
// 实例，而 dry run 与 apply 通常发生在不同轮次。
package tools

import (
	"strings"
	"sync"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/manifestx"
)

const (
	// DefaultProposalTTL 是一次提案的有效期：超过就得重新 dry run，
	// 避免用户确认的是十分钟前的集群状态。
	DefaultProposalTTL = 15 * time.Minute
	// defaultProposalsPerSession 限制单会话堆积的提案数量。
	defaultProposalsPerSession = 20
)

// proposal 是一次通过 dry run 的清单记录。
type proposal struct {
	id           string
	fingerprints []string
	objects      []string
	manifest     string
	objs         []manifestx.Object
	expiresAt    time.Time
}

// ProposalStore 是按会话保存 dry run 提案的并发安全缓存。
type ProposalStore struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxPerSess int
	bySession  map[string][]proposal
}

// NewProposalStore 创建默认配置的提案存储。
func NewProposalStore() *ProposalStore {
	return NewProposalStoreWith(DefaultProposalTTL, defaultProposalsPerSession)
}

// NewProposalStoreWith 自定义有效期与单会话上限（测试用）。
func NewProposalStoreWith(ttl time.Duration, maxPerSession int) *ProposalStore {
	if ttl <= 0 {
		ttl = DefaultProposalTTL
	}
	if maxPerSession <= 0 {
		maxPerSession = defaultProposalsPerSession
	}
	return &ProposalStore{
		ttl:        ttl,
		maxPerSess: maxPerSession,
		bySession:  map[string][]proposal{},
	}
}

// Record 记录一次通过 dry run 的清单（指纹 + 对象展示名）。
// 保留旧签名：三期指纹一致性校验路径继续可用。
func (s *ProposalStore) Record(sessionID string, fingerprints, objects []string) {
	s.record(sessionID, "", "", fingerprints, objects, nil, nil)
}

// RecordFull 记录一次包含原始清单与已解析对象的 dry run 提案。
// dry run 先落盘对象和 manifest，后续 apply 只需携带 proposal_id 即可原样复用，
// 不依赖模型再次重建 manifest（避免指纹漂移导致 apply 被拒）。
func (s *ProposalStore) RecordFull(sessionID, id, manifest string, fingerprints, objects []string, objs []manifestx.Object) {
	s.record(sessionID, id, manifest, fingerprints, objects, objs, nil)
}

func (s *ProposalStore) record(sessionID, id, manifest string, fingerprints, objects []string, objs []manifestx.Object, objsCopied []manifestx.Object) {
	if sessionID == "" || len(fingerprints) == 0 || !allFingerprinted(fingerprints) {
		return
	}
	fp := append([]string(nil), fingerprints...)
	obj := append([]string(nil), objects...)
	if objsCopied == nil && len(objs) > 0 {
		objsCopied = make([]manifestx.Object, 0, len(objs))
		for i := range objs {
			objsCopied = append(objsCopied, objs[i].CloneForStore())
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := append(s.bySession[sessionID], proposal{
		id:           id,
		fingerprints: fp,
		objects:      obj,
		manifest:     manifest,
		objs:         objsCopied,
		expiresAt:    time.Now().Add(s.ttl),
	})
	// 只保留最近 N 条未过期提案。
	kept := make([]proposal, 0, len(list))
	now := time.Now()
	for _, p := range list {
		if now.Before(p.expiresAt) {
			kept = append(kept, p)
		}
	}
	if len(kept) > s.maxPerSess {
		kept = kept[len(kept)-s.maxPerSess:]
	}
	s.bySession[sessionID] = kept
}

// Match 判断指纹序列是否命中该会话的某条有效提案。
// 未命中时返回可展示给模型的原因。
func (s *ProposalStore) Match(sessionID string, fingerprints []string) (bool, string) {
	if sessionID == "" || len(fingerprints) == 0 || !allFingerprinted(fingerprints) {
		return false, "缺少清单指纹，无法确认这是已预演过的内容"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var live []proposal
	for _, p := range s.bySession[sessionID] {
		if now.Before(p.expiresAt) {
			live = append(live, p)
		}
	}
	if len(live) == 0 {
		return false, "本次会话没有已通过 dry run 的创建提案"
	}
	for _, p := range live {
		if sameSequence(p.fingerprints, fingerprints) {
			return true, ""
		}
	}
	return false, "清单内容与本会话已预演的提案指纹不一致（可能改动了字段），请用相同内容重新 dry run"
}

// Get 按 proposal_id 取回 dry run 已记录的原始对象清单。
// 未命中或已过期返回 nil（调用方按 proposal_required/proposal_not_found 处理）。
func (s *ProposalStore) Get(sessionID, id string) []manifestx.Object {
	if sessionID == "" || strings.TrimSpace(id) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, p := range s.bySession[sessionID] {
		if p.id == id && now.Before(p.expiresAt) {
			out := make([]manifestx.Object, 0, len(p.objs))
			for i := range p.objs {
				out = append(out, p.objs[i].CloneForStore())
			}
			return out
		}
	}
	return nil
}

// Count 返回某会话当前有效提案数（测试与展示用）。
func (s *ProposalStore) Count(sessionID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	n := 0
	for _, p := range s.bySession[sessionID] {
		if now.Before(p.expiresAt) {
			n++
		}
	}
	return n
}

// Forget 清空某会话的提案（会话清空时复用）。
func (s *ProposalStore) Forget(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bySession, sessionID)
}

func allFingerprinted(in []string) bool {
	for _, fp := range in {
		if strings.TrimSpace(fp) == "" {
			return false
		}
	}
	return true
}

func sameSequence(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
