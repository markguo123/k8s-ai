package incident

import "github.com/k8s-ai/k8s-ai/internal/model"

var impactRules = map[string]bool{
	"DeploymentReplica":          true,
	"StatefulSetReplica":         true,
	"DaemonSetReplica":           true,
	"ServiceNoEndpoint":          true,
	"IngressBackend":             true,
	"DeploymentProgressDeadline": true,
}

// AssignRoles 在故障链聚合后为 Finding 标注报告语义。
// 事实本身不增不减，只避免把主信号、派生影响和建议项当成同一类问题展示。
func AssignRoles(findings []*model.Finding, incs []model.Incident) {
	byID := make(map[string]*model.Finding, len(findings))
	for _, finding := range findings {
		byID[finding.ID] = finding
		if finding.Suggestion {
			finding.Role = model.FindingRoleAdvisory
		} else {
			finding.Role = model.FindingRoleIndependent
		}
	}
	for _, inc := range incs {
		if root := byID[inc.Root.ID]; root != nil {
			if root.Suggestion {
				root.Role = model.FindingRoleAdvisory
			} else {
				root.Role = model.FindingRoleAnchor
			}
		}
		for _, member := range inc.Members {
			finding := byID[member.ID]
			if finding == nil {
				continue
			}
			rule := member.Rule
			if rule == "" {
				rule = finding.Rule
			}
			switch {
			case finding.Suggestion:
				finding.Role = model.FindingRoleAdvisory
			case impactRules[rule]:
				finding.Role = model.FindingRoleImpact
			default:
				finding.Role = model.FindingRoleSymptom
			}
		}
	}
}
