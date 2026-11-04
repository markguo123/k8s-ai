package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// 统一体检报告的派生区块（需求 §7 / 方案 18 §7）。
//
// 这一层的输入全是已经算出来的事实（findings / incidents / diagnoses / collection），
// 输出是「结论、资源健康、处置清单」。它一次 LLM 都不调：结论区一旦由模型自由发挥，
// 报告里就没有一段可以无条件相信的文字了。

// ComputeHealthScoreWithQuality 在规则罚分之上叠加采集完整性约束（需求 §7.7）：
//
//   - 关键数据缺失（insufficient）→ 封顶 60：连 Pod/Node/Events 都没采全，
//     「没有异常」这个结论本身不成立，不能给高分；
//   - 部分采集失败（partial）→ 封顶 85；
//   - 存在任何 failed/skipped 采集项 → 不得给满分 100。
//
// 分数只降不升：封顶不会掩盖问题，也不会替问题加分。
func ComputeHealthScoreWithQuality(findings []model.Finding, q *model.CollectionQuality) model.HealthScore {
	hs := ComputeHealthScore(findings)
	hs.DataIntegrity = model.IntegrityComplete
	if q == nil {
		return hs
	}
	hs.DataIntegrity = q.Integrity
	hs.Coverage = q.Coverage
	switch q.Integrity {
	case model.IntegrityInsufficient:
		capScore(&hs, 60, "关键数据（Pod/Node/工作负载/Events）采集不完整，结论可信度受限")
	case model.IntegrityPartial:
		capScore(&hs, 85, "部分资源类型采集失败，报告范围不完整")
	}
	if len(q.FailedKinds) > 0 || len(q.SkippedKinds) > 0 {
		capScore(&hs, hs.Max-1, "存在采集失败或跳过的资源类型，不给出满分")
	}
	return hs
}

func capScore(hs *model.HealthScore, max int, reason string) {
	if hs.Score <= max {
		return
	}
	hs.Score = max
	hs.Capped = true
	if hs.CapReason == "" {
		hs.CapReason = reason
		return
	}
	if !strings.Contains(hs.CapReason, reason) {
		hs.CapReason += "；" + reason
	}
}

// BuildConclusion 生成报告首部结论（需求 §7.2）。
func BuildConclusion(r *model.ScanResult) *model.Conclusion {
	if r == nil {
		return nil
	}
	memberIDs := map[string]bool{}
	rootIDs := map[string]bool{}
	for _, inc := range r.Incidents {
		for _, m := range inc.Members {
			memberIDs[m.ID] = true
		}
		if inc.Root.ID != "" {
			rootIDs[inc.Root.ID] = true
		}
	}
	var roots []model.Finding
	for _, f := range r.Findings {
		if f.Correlated {
			continue
		}
		// 链内症状不单独计数，但故障链的根因必须计入：否则一次所有异常都归进了
		// 同一条故障链的体检，会被读成「本次体检未发现异常」。
		if memberIDs[f.ID] && !rootIDs[f.ID] {
			continue
		}
		roots = append(roots, f)
	}
	bySev := map[model.Severity]int{}
	for _, f := range roots {
		bySev[f.Severity]++
	}

	c := &model.Conclusion{}
	integrity := model.IntegrityComplete
	if r.Collection != nil {
		integrity = r.Collection.Integrity
	}
	switch {
	case bySev[model.SeverityCritical] > 0:
		c.Level = "critical"
		c.Headline = fmt.Sprintf("检测到 %d 个严重问题，服务可用性已受影响", bySev[model.SeverityCritical])
	case bySev[model.SeverityHigh] > 0:
		c.Level = "attention"
		c.Headline = fmt.Sprintf("检测到 %d 个高风险问题，需尽快处理", bySev[model.SeverityHigh])
	case len(roots) > 0:
		c.Level = "attention"
		c.Headline = fmt.Sprintf("未发现严重故障，存在 %d 项需关注问题", len(roots))
	case integrity == model.IntegrityInsufficient:
		c.Level = "unknown"
		c.Headline = "关键数据采集不完整，无法判定集群是否健康"
	default:
		c.Level = "healthy"
		c.Headline = "本次体检未发现异常"
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("健康评分 %d/%d", r.HealthScore.Score, r.HealthScore.Max))
	if r.HealthScore.Capped {
		parts = append(parts, "评分已因数据完整性封顶（"+r.HealthScore.CapReason+"）")
	}
	if len(r.Incidents) > 0 {
		conf, prob, unk := 0, 0, 0
		for _, inc := range r.Incidents {
			switch inc.RootCauseStatus {
			case model.RootCauseConfirmed:
				conf++
			case model.RootCauseProbable:
				prob++
			default:
				unk++
			}
		}
		parts = append(parts, fmt.Sprintf("故障链 %d 条：根因确认 %d、疑似 %d、未能定位 %d",
			len(r.Incidents), conf, prob, unk))
	}
	if r.DiagnosisSummary != nil {
		parts = append(parts, "诊断口径："+diagnosisModeText(r.DiagnosisSummary.Mode))
	}
	if integrity != model.IntegrityComplete {
		parts = append(parts, "采集完整性："+integrity)
	}
	c.Summary = strings.Join(parts, "；") + "。"

	for _, f := range sortRoots(roots)[:minInt(len(roots), 5)] {
		c.TopRisks = append(c.TopRisks, fmt.Sprintf("[%s] %s/%s %s：%s",
			f.Severity, f.Resource.Namespace, f.Resource.Name, f.Rule, f.Title))
	}
	c.Priorities = priorityList(r)
	c.DataCaveats = dataCaveats(r)
	return c
}

func diagnosisModeText(mode string) string {
	switch mode {
	case "llm_enhanced":
		return "确定性诊断 + LLM 增强"
	case "llm_degraded":
		return "LLM 未生效，结论来自确定性诊断"
	default:
		return "仅规则与确定性诊断（未使用 LLM）"
	}
}

// priorityList 从各故障链的诊断结论里取处置方向，按严重级排序。
func priorityList(r *model.ScanResult) []string {
	diagByIncident := map[string]model.Diagnosis{}
	for _, d := range r.Diagnoses {
		if d.IncidentID != "" {
			diagByIncident[d.IncidentID] = d
		}
	}
	incByFinding := map[string]model.Incident{}
	for _, inc := range r.Incidents {
		incByFinding[inc.ID] = inc
	}
	type pr struct {
		rank int
		text string
	}
	var list []pr
	for id, inc := range incByFinding {
		d, ok := diagByIncident[id]
		if !ok {
			continue
		}
		text := strings.TrimSpace(d.RemediationDirection)
		if text == "" {
			text = strings.TrimSpace(d.RemediationText)
		}
		if text == "" && len(d.Remediation) > 0 {
			text = d.Remediation[0].Text
		}
		if text == "" {
			continue
		}
		list = append(list, pr{rank: model.SeverityRank(inc.Severity),
			text: fmt.Sprintf("[%s] %s/%s：%s", inc.Severity, inc.Root.Resource.Namespace, inc.Root.Resource.Name, text)})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].rank != list[j].rank {
			return list[i].rank > list[j].rank
		}
		return list[i].text < list[j].text
	})
	var out []string
	for i, p := range list {
		if i >= 5 {
			break
		}
		out = append(out, p.text)
	}
	return out
}

// dataCaveats 汇总所有「本报告的结论因此受限」的事实。
func dataCaveats(r *model.ScanResult) []string {
	var out []string
	if r.Collection != nil {
		out = append(out, r.Collection.CriticalGaps...)
		out = append(out, r.Collection.MissingData...)
		for _, k := range r.Collection.SkippedKinds {
			out = append(out, fmt.Sprintf("本次未采集 %s（档位配置）", k))
		}
	}
	if r.LLMSummary.Enabled && r.LLMSummary.Degraded > 0 {
		out = append(out, fmt.Sprintf("LLM 增强有 %d 条降级，相关故障链的结论仅来自确定性诊断", r.LLMSummary.Degraded))
	}
	if r.LLMSummary.Enabled && r.LLMSummary.Rejected > 0 {
		out = append(out, fmt.Sprintf("LLM 结论有 %d 条未通过证据校验并被退回", r.LLMSummary.Rejected))
	}
	seen := map[string]bool{}
	var dedup []string
	for _, s := range out {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		dedup = append(dedup, s)
	}
	if len(dedup) > 12 {
		dedup = append(dedup[:12:12], fmt.Sprintf("…另有 %d 条数据缺口见「采集质量」", len(dedup)-12))
	}
	return dedup
}

// domainMapping 把资源 Kind 归入体检域（需求 §7.6）。
var domainMapping = map[string]string{
	"Pod": "计算", "Node": "节点",
	"Deployment": "工作负载与弹性", "ReplicaSet": "工作负载与弹性",
	"StatefulSet": "工作负载与弹性", "DaemonSet": "工作负载与弹性",
	"Job": "工作负载与弹性", "CronJob": "工作负载与弹性", "HPA": "工作负载与弹性",
	"Service": "网络", "EndpointSlice": "网络", "Ingress": "网络", "NetworkPolicy": "网络",
	"PVC": "存储", "PV": "存储", "StorageClass": "存储", "VolumeAttachment": "存储",
	"ConfigMap": "配置与密钥", "Secret": "配置与密钥",
	"ClusterRole": "权限与治理", "Namespace": "权限与治理", "PDB": "权限与治理",
}

// domainTotals 返回各域的可见资源总数（来自报告 Summary，采不到的不算总数）。
func domainTotals(r *model.ScanResult) map[string]int {
	s := r.Summary
	return map[string]int{
		"计算": s.Pods, "节点": s.Nodes, "工作负载与弹性": s.Workloads,
		"网络": s.Services + s.EndpointSlices + s.Ingresses + s.NetworkPolicies,
		"存储": s.Storage,
	}
}

// DomainHealthList 输出各资源域健康小结（需求 §7.6）。
func DomainHealthList(r *model.ScanResult) []model.DomainHealth {
	if r == nil {
		return nil
	}
	totals := domainTotals(r)
	unhealthy := map[string]int{}
	critical := map[string]int{}
	touched := map[string]bool{}
	for _, f := range r.Findings {
		d, ok := domainMapping[f.Kind]
		if !ok {
			d = "其他"
		}
		touched[d] = true
		if f.Correlated {
			continue
		}
		unhealthy[d]++
		if model.SeverityRank(f.Severity) >= model.SeverityRank(model.SeverityHigh) {
			critical[d]++
		}
	}
	for _, c := range r.Components {
		touched["系统组件"] = true
		if !c.Present || !c.Ready {
			unhealthy["系统组件"]++
			if !c.Present {
				critical["系统组件"]++
			}
		}
	}

	order := []string{"计算", "节点", "工作负载与弹性", "网络", "存储", "配置与密钥", "权限与治理", "系统组件", "其他"}
	var out []model.DomainHealth
	for _, d := range order {
		total := totals[d]
		if d == "系统组件" {
			total = r.Summary.Components
		}
		if total == 0 && !touched[d] {
			continue
		}
		dh := model.DomainHealth{Domain: d, Total: total, Unhealthy: unhealthy[d], Critical: critical[d]}
		switch {
		case dh.Unhealthy == 0 && total > 0:
			dh.Detail = "未发现异常"
		case dh.Unhealthy > 0:
			dh.Detail = fmt.Sprintf("%d 项异常，其中 %d 项高风险及以上", dh.Unhealthy, dh.Critical)
		default:
			dh.Detail = "本次未发现该域异常（未纳入资源计数）"
		}
		out = append(out, dh)
	}
	return out
}

// RemediationItems 汇总可复制的处置清单（排查 / 修复 / 验证）。
//
// 命令一律「只展示不执行」（ADR-014），修复类必须同时带上文字方向说明，
// 因为很多场景下正确的做法是「先确认再改」，而不是一条能直接贴的命令。
func RemediationItems(r *model.ScanResult) []model.RemediationItem {
	if r == nil {
		return nil
	}
	incSeverity := map[string]model.Severity{}
	incTarget := map[string]string{}
	for _, inc := range r.Incidents {
		incSeverity[inc.ID] = inc.Severity
		incTarget[inc.ID] = inc.Root.Resource.Namespace + "/" + inc.Root.Resource.Name
	}
	var out []model.RemediationItem
	add := func(kind string, cmd model.Command, target string, required bool) {
		text := strings.TrimSpace(cmd.Text)
		if text == "" {
			return
		}
		out = append(out, model.RemediationItem{
			Kind: kind, Text: text, Risk: string(cmd.Risk), Target: target, Required: required,
		})
	}
	for _, d := range r.Diagnoses {
		target := incTarget[d.IncidentID]
		if target == "/" {
			target = ""
		}
		required := model.SeverityRank(incSeverity[d.IncidentID]) >= model.SeverityRank(model.SeverityCritical)
		for _, cmd := range d.Investigation {
			add(string(model.CmdInvestigation), cmd, target, false)
		}
		if text := strings.TrimSpace(d.RemediationDirection); text != "" {
			out = append(out, model.RemediationItem{
				Kind: string(model.CmdRemediation), Text: text, Risk: string(model.RiskMedium),
				Target: target, Required: required,
			})
		}
		for _, cmd := range d.Remediation {
			add(string(model.CmdRemediation), cmd, target, required)
		}
		for _, cmd := range d.Verification {
			add(string(model.CmdVerification), cmd, target, false)
		}
	}
	if len(out) > 80 {
		out = append(out[:80:80], model.RemediationItem{
			Kind: string(model.CmdInvestigation), Text: fmt.Sprintf("…另有 %d 条处置建议见各故障链明细", len(out)-80),
		})
	}
	return out
}

// ParseReportJSON 读取报告 JSON 并做 schema 兼容（需求 §7：历史报告必须还能读）。
//
// schemaVersion 缺失 = v1（四期之前的报告）：新字段解出零值，调用方按
// 「该区块不存在」处理，禁止用当前版本的语义去解释旧报告。
func ParseReportJSON(data []byte) (*model.ScanResult, bool, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, false, fmt.Errorf("report payload is empty")
	}
	var r model.ScanResult
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, false, fmt.Errorf("parse report json: %w", err)
	}
	return &r, LegacySchema(&r), nil
}

// LegacySchema 判断一份报告是否为四期之前的 v1 结构。
func LegacySchema(r *model.ScanResult) bool {
	return r == nil || r.SchemaVersion == 0
}

func sortRoots(fs []model.Finding) []model.Finding {
	out := append([]model.Finding(nil), fs...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := model.SeverityRank(out[i].Severity), model.SeverityRank(out[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if out[i].Resource.Namespace != out[j].Resource.Namespace {
			return out[i].Resource.Namespace < out[j].Resource.Namespace
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
