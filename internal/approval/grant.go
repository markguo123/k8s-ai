package approval

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// GrantRiskCeiling is the maximum risk that can be auto-approved by a
// structured grant in the Phase D first version. HIGH and CRITICAL always
// require a human approver even in smart mode.
const GrantRiskCeiling = model.RiskMedium

// GrantRequest describes the per-call context used to match a Grant.
type GrantRequest struct {
	TaskID    string
	Operation string
	Kind      string
	Name      string
	Namespace string
	Cluster   string
	Risk      model.RiskLevel
	Shell     bool
	// NoGrant opts a call out of the grant mechanism entirely. It is set when a
	// write call cannot be reduced to one kind/name target (for example
	// create_resource with several objects): an empty Kind would otherwise match
	// every grant of the same risk ceiling and silently authorize unrelated
	// mutations.
	NoGrant bool
}

// Grant is a task-scoped authorization record created after the user
// chooses "yes and don't ask again" for a MEDIUM-risk write operation.
type Grant struct {
	ID          string          `json:"id"`
	TaskID      string          `json:"taskId,omitempty"`
	Operation   string          `json:"operation,omitempty"`
	Kind        string          `json:"kind,omitempty"`
	Name        string          `json:"name,omitempty"`
	Namespace   string          `json:"namespace,omitempty"`
	Cluster     string          `json:"cluster,omitempty"`
	RiskCeiling model.RiskLevel `json:"riskCeiling,omitempty"`
	CreatedAt   time.Time       `json:"createdAt,omitempty"`
	ExpiresAt   time.Time       `json:"expiresAt,omitempty"`
}

// GrantEligibleRisk reports whether a write risk can become an auto grant
// in Phase D. Only LOW and MEDIUM are eligible; SAFE reads are already auto
// and HIGH/CRITICAL must stay behind a human approver.
func GrantEligibleRisk(r model.RiskLevel) bool {
	return r == model.RiskLow || r == model.RiskMedium
}

// NewGrant creates a task-scoped grant for a matching request. It returns
// false when the operation is not eligible (shell, SAFE, HIGH, CRITICAL).
func NewGrant(req GrantRequest) (Grant, bool) {
	if req.Shell || req.NoGrant || !GrantEligibleRisk(req.Risk) {
		return Grant{}, false
	}
	return Grant{
		ID:          newGrantID(),
		TaskID:      req.TaskID,
		Operation:   req.Operation,
		Kind:        req.Kind,
		Name:        req.Name,
		Namespace:   req.Namespace,
		Cluster:     req.Cluster,
		RiskCeiling: GrantRiskCeiling,
		CreatedAt:   time.Now(),
	}, true
}

// Matches reports whether the grant authorizes the given request. The
// request must be on the same task, within the same cluster, and satisfy
// every non-empty grant field. Shell and HIGH/CRITICAL requests never match.
func (g Grant) Matches(req GrantRequest) bool {
	if !g.ExpiresAt.IsZero() && time.Now().After(g.ExpiresAt) {
		return false
	}
	if req.Shell {
		return false
	}
	if req.NoGrant {
		return false
	}
	if riskOrder[req.Risk] > riskOrder[g.RiskCeiling] {
		return false
	}
	if g.TaskID != "" && g.TaskID != req.TaskID {
		return false
	}
	if g.Cluster != "" && g.Cluster != req.Cluster {
		return false
	}
	if g.Operation != "" && g.Operation != req.Operation {
		return false
	}
	if g.Kind != "" && g.Kind != req.Kind {
		return false
	}
	if g.Name != "" && g.Name != req.Name {
		return false
	}
	if g.Namespace != "" && g.Namespace != req.Namespace {
		return false
	}
	return true
}

// MatchGrant returns the first grant that matches the request, or nil.
func MatchGrant(grants []Grant, req GrantRequest) *Grant {
	for i := range grants {
		if grants[i].Matches(req) {
			return &grants[i]
		}
	}
	return nil
}

// CommandTarget 是 kubectl 命令的结构化操作目标，用于构建授权请求。
type CommandTarget struct {
	Operation string
	Kind      string
	Name      string
	Namespace string
}

// ParseCommandTarget 从普通（非 shell）kubectl 命令提取稳定目标。
// 含 shell 语法或无法可靠解析的命令返回零值（调用方按 Shell=true 排除授权匹配）。
func ParseCommandTarget(command string) CommandTarget {
	if HasShellSyntax(command) {
		return CommandTarget{}
	}
	fields := strings.Fields(command)
	if len(fields) < 2 {
		return CommandTarget{}
	}
	verbIdx := -1
	for i := 1; i < len(fields); i++ {
		if isKnownVerb(fields[i]) {
			verbIdx = i
			break
		}
	}
	if verbIdx < 0 {
		return CommandTarget{}
	}
	t := CommandTarget{Operation: fields[verbIdx]}
	rest := fields[verbIdx+1:]
	if t.Operation == "rollout" {
		if i := firstNonFlagIndex(rest); i >= 0 {
			t.Operation = "rollout " + rest[i]
			rest = rest[i+1:]
		}
	}
	// namespace flag 可能出现在动词之前，因此扫描整条命令。
	t.Namespace = namespaceFromFields(fields)
	var seenKind, seenName bool
	skipNext := false
	for _, tok := range rest {
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(tok, "-") {
			if strings.Contains(tok, "=") {
				continue
			}
			if valueFlags[tok] {
				skipNext = true
			}
			continue
		}
		kind := tok
		name := ""
		if idx := strings.Index(kind, "/"); idx >= 0 {
			name = kind[idx+1:]
			kind = kind[:idx]
		}
		if !seenKind {
			t.Kind = kind
			t.Name = name
			seenKind = true
			if name != "" {
				seenName = true
			}
			continue
		}
		if !seenName {
			if name == "" {
				name = tok
			}
			t.Name = name
			seenName = true
		}
	}
	return t
}

// firstNonFlagIndex 返回第一个非 flag 字段的下标，找不到返回 -1。
func firstNonFlagIndex(fields []string) int {
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if !strings.HasPrefix(f, "-") {
			return i
		}
		if strings.Contains(f, "=") {
			continue
		}
		if valueFlags[f] && i+1 < len(fields) {
			i++
		}
	}
	return -1
}

// namespaceFromFields 从 kubectl 参数中提取 -n/--namespace 的值。
func namespaceFromFields(fields []string) string {
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if f == "-n" || f == "--namespace" {
			if i+1 < len(fields) {
				return fields[i+1]
			}
			return ""
		}
		if strings.HasPrefix(f, "--namespace=") {
			return strings.TrimPrefix(f, "--namespace=")
		}
		if strings.HasPrefix(f, "-n=") {
			return strings.TrimPrefix(f, "-n=")
		}
	}
	return ""
}

func newGrantID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("g%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
