// Package rule 实现规则引擎（FR-013）。
// 异常发现全部收敛到规则层；Scanner 只采集不判断（ADR-002）。
package rule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// Rule 判定一条规则，产出 0..n 条 Finding。
type Rule interface {
	Name() string
	NeedsLogs() bool // 命中后是否需要在 Phase2 深度采集日志
	Evaluate(ctx *RuleContext) []*model.Finding
}

// Registry 规则注册表，支持按配置启停（FR-013）。
// Registry 规则注册表，支持按配置启停（FR-013）与按元数据过滤（P7.1）。
type Registry interface {
	Register(r Rule)
	RegisterWithMeta(r Rule, meta RuleMeta) error
	All() []Rule
	ByName(name string) Rule
	Filtered(enabled, disabled []string) []Rule
	Meta(name string) (RuleMeta, bool)
	AllWithMeta() []RuleWithMeta
	FilterWithMeta(filter RuleMetaFilter) []RuleWithMeta
}

type registry struct {
	rules []Rule
	meta  map[string]RuleMeta
}

// NewRegistry 创建空注册表。
func NewRegistry() Registry {
	return &registry{meta: make(map[string]RuleMeta)}
}

// Register 以默认元数据注册规则（P7.1 兼容保留）。
func (r *registry) Register(x Rule) {
	_ = r.RegisterWithMeta(x, RuleMeta{})
}

// RegisterWithMeta 注册规则并注入结构化元数据（SPEC §28.1）。
// 重复注册同一 Name 的规则时，仅更新该规则的元数据，不重复加入列表。
func (r *registry) RegisterWithMeta(x Rule, meta RuleMeta) error {
	m := normalizeMeta(x, meta)
	if err := validateMeta(x, m); err != nil {
		return err
	}
	if r.meta == nil {
		r.meta = make(map[string]RuleMeta)
	}
	if _, dup := r.meta[x.Name()]; !dup {
		r.rules = append(r.rules, x)
	}
	r.meta[x.Name()] = m
	return nil
}

// All 返回全部已注册规则。
func (r *registry) All() []Rule {
	return r.rules
}

// ByName 按规则名查找规则。
func (r *registry) ByName(name string) Rule {
	for _, x := range r.rules {
		if x.Name() == name {
			return x
		}
	}
	return nil
}

// Meta 返回规则名对应的元数据。
func (r *registry) Meta(name string) (RuleMeta, bool) {
	m, ok := r.meta[name]
	return m, ok
}

// AllWithMeta 返回全部已注册规则及其元数据。
func (r *registry) AllWithMeta() []RuleWithMeta {
	out := make([]RuleWithMeta, 0, len(r.rules))
	for _, x := range r.rules {
		if m, ok := r.meta[x.Name()]; ok {
			out = append(out, RuleWithMeta{Rule: x, Meta: m})
		}
	}
	return out
}

// FilterWithMeta 按元数据过滤规则；滤波器零值字段视为通配。
func (r *registry) FilterWithMeta(filter RuleMetaFilter) []RuleWithMeta {
	var out []RuleWithMeta
	for _, x := range r.rules {
		m, ok := r.meta[x.Name()]
		if !ok {
			continue
		}
		if filter.Category != "" && m.Category != filter.Category {
			continue
		}
		if filter.Kind != "" && m.Kind != filter.Kind {
			continue
		}
		if filter.RuleType != "" && m.RuleType != filter.RuleType {
			continue
		}
		if filter.Severity != "" && m.SeverityBase != filter.Severity {
			continue
		}
		out = append(out, RuleWithMeta{Rule: x, Meta: m})
	}
	return out
}

func (r *registry) Filtered(enabled, disabled []string) []Rule {
	disabledSet := toSet(disabled)
	enabledSet := toSet(enabled)
	var out []Rule
	for _, x := range r.rules {
		if disabledSet[x.Name()] {
			continue
		}
		if len(enabledSet) > 0 && !enabledSet[x.Name()] {
			continue
		}
		out = append(out, x)
	}
	return out
}

// Engine 编排所有启用的规则并做跨规则后处理（相关异常标记）。
type Engine interface {
	Evaluate(ctx context.Context, snapshot *model.ClusterSnapshot, index *model.CorrelationIndex, opts model.ScanOptions) []*model.Finding
}

type engine struct {
	registry Registry
	docs     DocProvider
}

// NewEngine 创建引擎。
func NewEngine(r Registry) Engine {
	return &engine{registry: r, docs: NewDocumentProvider()}
}

func (e *engine) Evaluate(ctx context.Context, snapshot *model.ClusterSnapshot, index *model.CorrelationIndex, opts model.ScanOptions) []*model.Finding {
	rctx := &RuleContext{
		Snapshot: snapshot,
		Index:    index,
		Severity: DefaultSeverityPolicy(),
		Now:      time.Now(),
	}
	var findings []*model.Finding
	for _, r := range e.registry.Filtered(opts.RulesEnabled, opts.RulesDisabled) {
		select {
		case <-ctx.Done():
			return findings
		default:
		}
		start := time.Now()
		got := r.Evaluate(rctx)
		slog.Debug("rule: evaluate complete",
			"rule", r.Name(), "hits", len(got), "elapsed_ms", time.Since(start).Milliseconds())
		findings = append(findings, got...)
	}
	// 跨规则后处理：节点级根因标记下游 Pod 异常为 Correlated（评分去重）。
	markCorrelated(index, findings)
	// P7.7/P7.8：按规则元数据注入 Category/Kind/Suggestion（报告聚合用），
	// 开启 --with-doc 时再附带字段文档（仅静态资源，不发 API 请求）。
	for _, f := range findings {
		m, ok := e.registry.Meta(f.Rule)
		if !ok {
			continue
		}
		f.Category = m.Category
		f.Kind = m.Kind
		f.Suggestion = m.RuleType == RuleTypeSuggestion
		if opts.WithDoc && m.DocRef != "" {
			if d, ok := e.docs.Lookup(m.DocRef); ok {
				f.Doc = d.Text()
			}
		}
	}
	return findings
}

// RuleContext 是规则判定的只读上下文。
type RuleContext struct {
	Snapshot *model.ClusterSnapshot
	Index    *model.CorrelationIndex
	Severity SeverityPolicy
	Now      time.Time
}

// SeverityPolicy 控制严重级调整（FR-011）。
type SeverityPolicy struct {
	SystemNamespaces    []string
	ReplicaThreshold    int32
	ProductionLabelKeys []string
	ProductionValues    []string
	NonProdLabelKeys    []string
	NonProdValues       []string
}

// DefaultSeverityPolicy 返回默认分级策略。
func DefaultSeverityPolicy() SeverityPolicy {
	return SeverityPolicy{
		SystemNamespaces:    []string{"kube-system", "kube-public", "kube-node-lease"},
		ReplicaThreshold:    3,
		ProductionLabelKeys: []string{"environment", "env"},
		ProductionValues:    []string{"production", "prod"},
		NonProdLabelKeys:    []string{"environment", "env"},
		NonProdValues:       []string{"dev", "development", "test", "staging"},
	}
}

// Adjust 在基础严重级上按上下文调整（系统命名空间/生产环境/受影响面）。
func (p SeverityPolicy) Adjust(base model.Severity, ref model.ResourceRef, idx *model.CorrelationIndex) model.Severity {
	rank := model.SeverityRank(base)
	if rank == 0 {
		rank = 1
	}
	ns := idx.Namespace(ref.Namespace)
	if p.isSystemNamespace(ref.Namespace) {
		rank++
	} else if ns != nil && p.isProduction(ns) {
		rank++
	} else if ns != nil && p.isNonProduction(ns) {
		rank--
	}
	// 受影响面：Pod 被 Service 选中或所属 workload 大量副本不可用 → 提升。
	if pod := idx.Pod(ref.Key()); pod != nil {
		if len(idx.ServicesOfPod(ref.Key())) > 0 {
			rank++
		}
		if chain := idx.OwnerChain(pod); len(chain) > 0 {
			w := chain[0]
			if w.DesiredReplicas != nil && *w.DesiredReplicas >= p.ReplicaThreshold {
				ready := int32(0)
				if w.ReadyReplicas != nil {
					ready = *w.ReadyReplicas
				}
				if ready < *w.DesiredReplicas {
					rank++
				}
			}
		}
	}
	return severityFromRank(clamp(rank, 1, 5))
}

func (p SeverityPolicy) isSystemNamespace(name string) bool {
	for _, ns := range p.SystemNamespaces {
		if ns == name {
			return true
		}
	}
	return false
}

func (p SeverityPolicy) isProduction(ns *model.NamespaceInfo) bool {
	if hasAnyLabelValue(ns.Labels, p.ProductionLabelKeys, p.ProductionValues) {
		return true
	}
	return strings.Contains(ns.Ref.Name, "prod")
}

func (p SeverityPolicy) isNonProduction(ns *model.NamespaceInfo) bool {
	return hasAnyLabelValue(ns.Labels, p.NonProdLabelKeys, p.NonProdValues) ||
		strings.Contains(ns.Ref.Name, "test") || strings.Contains(ns.Ref.Name, "staging") || strings.Contains(ns.Ref.Name, "dev")
}

func hasAnyLabelValue(labels map[string]string, keys, values []string) bool {
	for _, k := range keys {
		if v, ok := labels[k]; ok {
			for _, want := range values {
				if v == want {
					return true
				}
			}
		}
	}
	return false
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func severityFromRank(rank int) model.Severity {
	switch rank {
	case 5:
		return model.SeverityCritical
	case 4:
		return model.SeverityHigh
	case 3:
		return model.SeverityMedium
	case 2:
		return model.SeverityLow
	default:
		return model.SeverityInfo
	}
}

// newFinding 统一构建 Finding：证据排序编号 → 严重级调整 → 指纹 → 关联资源。
func newFinding(ctx *RuleContext, r Rule, ref model.ResourceRef, title string, base model.Severity, evs []model.Evidence) *model.Finding {
	evs = evidence.AssignIDs(evs)
	f := &model.Finding{
		Rule:     r.Name(),
		Severity: ctx.Severity.Adjust(base, ref, ctx.Index),
		Title:    title,
		Summary:  fmt.Sprintf("%s/%s：%s", ref.Namespace, ref.Name, title),
		Resource: ref,
		Evidence: evs,
		Related:  relatedRefs(ctx, ref),
	}
	f.ID = Fingerprint(ctx.Index, *f)
	return f
}

// Fingerprint 计算 Finding 稳定指纹（ADR-003）。
// Pod 有归属 workload 时使用 workload 的 kind+name（Deployment Pod 名随机，
// 直接按 Pod 名会导致 1.2 趋势把"持续问题"误判为"恢复+新增"）。
func Fingerprint(index *model.CorrelationIndex, f model.Finding) string {
	kind, name := f.Resource.Kind, f.Resource.Name
	if kind == "Pod" {
		if p := index.Pod(f.Resource.Key()); p != nil {
			if chain := index.OwnerChain(p); len(chain) > 0 {
				kind, name = chain[0].Ref.Kind, chain[0].Ref.Name
			}
		}
	}
	raw := strings.Join([]string{kind, f.Resource.Group, f.Resource.Namespace, name, f.Rule, evidenceSignature(f.Evidence)}, "|")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:16])
}

// evidenceSignature 取排序后前 3 条非日志证据的 key:value 作为指纹签名。
func evidenceSignature(evs []model.Evidence) string {
	var parts []string
	seen := 0
	for _, e := range evs {
		if e.Kind == model.EvLog || seen >= 3 {
			continue
		}
		parts = append(parts, e.Key+":"+e.Value)
		seen++
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// relatedRefs 汇总与资源关联的 Workload/Node/Service/存储，供报告与 LLM 上下文使用。
func relatedRefs(ctx *RuleContext, ref model.ResourceRef) []model.ResourceRef {
	var out []model.ResourceRef
	seen := map[string]bool{}
	add := func(r model.ResourceRef) {
		if r.Key() == "" || seen[r.Key()] {
			return
		}
		seen[r.Key()] = true
		out = append(out, r)
	}
	if p := ctx.Index.Pod(ref.Key()); p != nil {
		for _, w := range ctx.Index.OwnerChain(p) {
			add(w.Ref)
		}
		if p.NodeName != "" {
			add(model.ResourceRef{Kind: "Node", Name: p.NodeName})
		}
		for _, s := range ctx.Index.ServicesOfPod(ref.Key()) {
			add(s.Ref)
		}
		for _, st := range ctx.Index.StorageChain(p) {
			add(st.Ref)
		}
	}
	return out
}

// markCorrelated 把节点级根因（NotReady/DiskPressure/MemoryPressure/PIDPressure）影响下的
// Pod Finding 标记为 Correlated，避免健康评分重复扣分（FR-020）。
func markCorrelated(index *model.CorrelationIndex, findings []*model.Finding) {
	nodeRoots := map[string]bool{}
	for _, f := range findings {
		if f.Resource.Kind == "Node" && isNodeRootRule(f.Rule) {
			nodeRoots[f.Resource.Name] = true
		}
	}
	if len(nodeRoots) == 0 {
		return
	}
	for _, f := range findings {
		if f.Resource.Kind != "Pod" || f.Correlated {
			continue
		}
		if p := index.Pod(f.Resource.Key()); p != nil && nodeRoots[p.NodeName] {
			f.Correlated = true
		}
	}
}

func isNodeRootRule(name string) bool {
	switch name {
	case "NodeNotReady", "NodeDiskPressure", "NodeMemoryPressure", "NodePIDPressure":
		return true
	}
	return false
}

// isConfigNotFound 判断容器启动/挂载失败的根因是否为 ConfigMap/Secret 缺失
// （消息含 "xxx not found"）。引用即缺失会使服务完全不可用，等级应提升为 HIGH。
func isConfigNotFound(msg string) bool {
	l := strings.ToLower(msg)
	return strings.Contains(l, "not found") &&
		(strings.Contains(l, "configmap") || strings.Contains(l, "secret"))
}

// LogTargets 返回需要 Phase2 深度采集日志的异常 Pod（FR-004）。
// 对于资源本身不是 Pod 但 NeedsLogs 的规则（如 JobFailed），通过关联索引
// 反查其下属 Pod 作为日志采集目标。
func LogTargets(reg Registry, findings []*model.Finding, index *model.CorrelationIndex) []model.ResourceRef {
	var out []model.ResourceRef
	seen := map[string]bool{}
	add := func(ref model.ResourceRef) {
		if seen[ref.Key()] {
			return
		}
		seen[ref.Key()] = true
		out = append(out, ref)
	}
	for _, f := range findings {
		r := reg.ByName(f.Rule)
		if r == nil || !r.NeedsLogs() {
			continue
		}
		if f.Resource.Kind == "Pod" {
			add(f.Resource)
			continue
		}
		// 非 Pod 资源（如 Job）：反查其下属 Pod 采集日志。
		if index != nil {
			for _, p := range index.PodsOfWorkload(f.Resource.Key()) {
				add(p.Ref)
			}
		}
	}
	return out
}

// AllDefault 返回一期全部默认规则。
func AllDefault() []Rule {
	return []Rule{
		CrashLoopBackOffRule{},
		OOMKilledRule{},
		ImagePullBackOffRule{},
		PendingPodRule{},
		PodTerminatingRule{},
		EvictedPodRule{},
		HighRestartCountRule{},
		ContainerCreateErrorRule{},
		UnhealthyRule{},
		IngressBackendRule{},
		NodeNotReadyRule{},
		NodeDiskPressureRule{},
		NodeMemoryPressureRule{},
		NodePIDPressureRule{},
		PVCPendingRule{},
		PVFailedRule{},
		FailedMountRule{},
		ServiceNoEndpointRule{},
		DeploymentReplicaRule{},
		StatefulSetReplicaRule{},
		DaemonSetReplicaRule{},
		DeploymentProgressDeadlineRule{},
		JobFailedRule{},
		CronJobSuspendedRule{},
		DeploymentOverReplicasRule{},
		NetworkPolicyNoDefaultDenyRule{},
		IngressHostConflictRule{},
		VolumeAttachmentStuckRule{},
		StorageClassNoProvisionerRule{},
		ContainerHealthTimeoutRule{},
		HpaReplicasMismatchRule{},
		HpaScaleTargetMissingRule{},
		PdbBlockingRule{},
		RbacOverPrivilegedRule{},
		ConfigRefNotFoundRule{},
	}
}

func toSet(items []string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, it := range items {
		out[it] = true
	}
	return out
}

// podEventEvidence 取资源关联的事件证据。
func podEventEvidence(ctx *RuleContext, ref model.ResourceRef) []model.Evidence {
	var out []model.Evidence
	for _, e := range ctx.Index.EventsFor(ref) {
		out = append(out, evidence.TruncateValue(evidence.Event(e), 2048))
	}
	return out
}
