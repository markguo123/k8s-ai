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
	fingerprints []string
	objects      []string
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
func (s *ProposalStore) Record(sessionID string, fingerprints, objects []string) {
	if sessionID == "" || len(fingerprints) == 0 || !allFingerprinted(fingerprints) {
		return
	}
	fp := append([]string(nil), fingerprints...)
	obj := append([]string(nil), objects...)
	s.mu.Lock()
	defer s.mu.Unlock()
	list := append(s.bySession[sessionID], proposal{
		fingerprints: fp,
		objects:      obj,
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
