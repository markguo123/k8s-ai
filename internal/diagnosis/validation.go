package diagnosis

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// LLM 结论一致性校验（需求 §4.3「结果校验」阶段）。
//
// 架构图里「结果校验」是 LLM 之后、报告之前的一道独立阶段，它只回答一个问题：
// 模型说的这些话，在已采集的事实里有没有依据。三条口径：
//  1. 证据编号必须真实存在（含跨故障链成员的 findingID#E-ID 引用）；
//  2. 提到的资源必须真实存在——只在「命名空间确实存在而该对象不存在」时判定为编造，
//     避免把 api/v1、linux/amd64 这类文本误判成资源名；
//  3. 根因不引用任何证据就不允许保持高置信度。
//
// 校验不通过的处置是「退回确定性结论 + 记原因」，不是把 LLM 的输出静默丢掉：
// 报告必须能看出这次是模型越界了，而不是程序没调用。
const validationRule = "p9-evidence-consistency-v1"

var resourceMention = regexp.MustCompile(`[A-Za-z0-9]([A-Za-z0-9.\-_]*[A-Za-z0-9])?/[A-Za-z0-9]([A-Za-z0-9.\-_]*[A-Za-z0-9])?`)

// validateIncidentDiagnosis 就地写入 d.Validation，返回是否通过。
func validateIncidentDiagnosis(d *model.Diagnosis, inc model.Incident, findingByID map[string]model.Finding, dc *Context) bool {
	if d == nil || !d.LLMUsed {
		return true
	}
	v := &model.DiagnosisValidation{Rule: validationRule, Passed: true}
	if d.Validation != nil {
		// schema 层已经把编造的证据编号从 EvidenceChain 里摘掉，
		// 这里必须把它接住，否则越界记录会被静默抹掉。
		v.FabricatedEvidence = append(v.FabricatedEvidence, d.Validation.FabricatedEvidence...)
	}

	universe := incidentEvidenceUniverse(inc, findingByID)
	for _, ref := range d.EvidenceChain {
		if !universe[strings.TrimSpace(ref)] {
			v.FabricatedEvidence = append(v.FabricatedEvidence, ref)
		}
	}

	if dc != nil && dc.Snapshot != nil {
		keys, namespaces := knownResourceKeys(dc.Snapshot)
		mentioned := map[string]bool{}
		for _, text := range []string{d.RootCause, d.Summary, d.CausalChain, d.Impact, d.RemediationText} {
			for _, m := range resourceMention.FindAllString(text, -1) {
				parts := strings.SplitN(m, "/", 2)
				if len(parts) != 2 || !namespaces[parts[0]] || mentioned[m] {
					continue // 前缀不是真实命名空间：大概率是版本/路径文本，不作判定
				}
				mentioned[m] = true
				if !keys[strings.ToLower(m)] {
					v.UnknownResources = append(v.UnknownResources, m)
				}
			}
		}
	}

	// 根因没有证据支撑：不得维持高置信度。
	hanging := false
	if strings.TrimSpace(d.RootCause) != "" && len(d.EvidenceChain) == 0 {
		hanging = true
		v.Issues = append(v.Issues, "根因未引用任何证据编号，置信度已下调")
	}
	if len(v.FabricatedEvidence) > 0 {
		v.Issues = append(v.Issues, fmt.Sprintf("引用了 %d 个不存在的证据编号", len(v.FabricatedEvidence)))
	}
	if len(v.UnknownResources) > 0 {
		v.Issues = append(v.Issues, fmt.Sprintf("提到了快照中不存在的资源：%s", strings.Join(v.UnknownResources, "、")))
	}

	v.Passed = len(v.FabricatedEvidence) == 0 && len(v.UnknownResources) == 0
	if hanging || !v.Passed {
		if d.Confidence > 0.5 {
			d.Confidence = 0.5
			v.Downgraded = true
		}
		d.ConfidenceLevel = "POSSIBLE"
	}
	if !v.Passed {
		// 编造证据/资源时不允许声称已确认根因。
		d.RootCauseStatus = model.RootCauseProbable
	} else if d.RootCauseStatus == "" {
		d.RootCauseStatus = rootCauseStatusFromConfidence(d.Confidence)
	}
	d.Validation = v
	return v.Passed
}

// rootCauseStatusFromConfidence 把模型自报的数值置信度折算成三档根因状态。
//
// 注意这是「上限」：confirmed 只能由确定性诊断层给出，LLM 自述再高也只到 probable。
func rootCauseStatusFromConfidence(conf float64) string {
	if conf >= 0.85 {
		return model.RootCauseProbable
	}
	if conf >= 0.3 {
		return model.RootCauseProbable
	}
	return model.RootCauseUnknown
}

// incidentEvidenceUniverse 返回本故障链真实可用的证据引用集合：
// 根因 Finding 用 `E3`，派生症状用 `<findingID>#E3`。
func incidentEvidenceUniverse(inc model.Incident, findingByID map[string]model.Finding) map[string]bool {
	out := map[string]bool{}
	if f, ok := findingByID[inc.Root.ID]; ok {
		for _, e := range f.Evidence {
			out[e.ID] = true
			out[inc.Root.ID+"#"+e.ID] = true
		}
	}
	for _, m := range inc.Members {
		f, ok := findingByID[m.ID]
		if !ok {
			continue
		}
		for _, e := range f.Evidence {
			out[m.ID+"#"+e.ID] = true
		}
	}
	return out
}

// knownResourceKeys 返回快照里真实存在的资源（小写 ns/name 与 kind/ns/name 两种键）
// 与真实命名空间集合。
func knownResourceKeys(snap *model.ClusterSnapshot) (keys map[string]bool, namespaces map[string]bool) {
	keys = map[string]bool{}
	namespaces = map[string]bool{}
	add := func(refs ...model.ResourceRef) {
		for _, r := range refs {
			if r.Name == "" {
				continue
			}
			if r.Namespace == "" {
				namespaces[r.Name] = true // 集群级对象名不当作命名空间
				keys[strings.ToLower(r.Kind+"/"+r.Name)] = true
				keys[strings.ToLower(r.Name)] = true
				continue
			}
			namespaces[r.Namespace] = true
			keys[strings.ToLower(r.Namespace+"/"+r.Name)] = true
			keys[strings.ToLower(r.Kind+"/"+r.Name)] = true
		}
	}
	for _, n := range snap.Namespaces {
		namespaces[n.Ref.Name] = true
	}
	for i := range snap.Pods {
		add(snap.Pods[i].Ref)
	}
	for i := range snap.Nodes {
		add(snap.Nodes[i].Ref)
	}
	for i := range snap.Workloads {
		add(snap.Workloads[i].Ref)
	}
	for i := range snap.Services {
		add(snap.Services[i].Ref)
	}
	for i := range snap.EndpointSlices {
		add(snap.EndpointSlices[i].Ref)
	}
	for i := range snap.Ingresses {
		add(snap.Ingresses[i].Ref)
	}
	for i := range snap.Storage {
		add(snap.Storage[i].Ref)
	}
	for i := range snap.ConfigMaps {
		add(snap.ConfigMaps[i])
	}
	for i := range snap.Secrets {
		add(snap.Secrets[i].Ref)
	}
	for i := range snap.NetworkPolicies {
		add(snap.NetworkPolicies[i].Ref)
	}
	for i := range snap.Hpas {
		add(snap.Hpas[i].Ref)
	}
	for i := range snap.Pdbs {
		add(snap.Pdbs[i].Ref)
	}
	return keys, namespaces
}
