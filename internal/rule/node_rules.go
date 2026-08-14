package rule

import (
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// nodeConditionFinding 命中节点条件时构建 Finding（节点类规则共用）。
func nodeConditionFinding(ctx *RuleContext, r Rule, n *model.NodeInfo, condType string, base model.Severity, title string, evs []model.Evidence) *model.Finding {
	src := "Node/" + n.Ref.Name + "/status.conditions"
	for _, c := range n.Conditions {
		if c.Type == condType {
			evs = append(evs, evidence.ObjectField(src, condType, c.Status+" ("+c.Reason+") "+c.Message))
		}
	}
	evs = append(evs, evidence.Derived("Node/"+n.Ref.Name, "affectedPods", fmt.Sprint(len(ctx.Index.PodsOfNode(n.Ref.Name)))))
	evs = append(evs, podEventEvidence(ctx, n.Ref)...)
	return newFinding(ctx, r, n.Ref, title, base, evs)
}

// NodeNotReadyRule 节点 NotReady。
type NodeNotReadyRule struct{}

func (NodeNotReadyRule) Name() string    { return "NodeNotReady" }
func (NodeNotReadyRule) NeedsLogs() bool { return false }

func (r NodeNotReadyRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Nodes {
		n := &ctx.Snapshot.Nodes[i]
		for _, c := range n.Conditions {
			if c.Type == "Ready" && c.Status != "True" {
				out = append(out, nodeConditionFinding(ctx, r, n, "Ready", model.SeverityCritical, "节点 NotReady", nil))
				break
			}
		}
	}
	return out
}

// NodeDiskPressureRule 节点磁盘压力。
type NodeDiskPressureRule struct{}

func (NodeDiskPressureRule) Name() string    { return "NodeDiskPressure" }
func (NodeDiskPressureRule) NeedsLogs() bool { return false }

func (r NodeDiskPressureRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Nodes {
		n := &ctx.Snapshot.Nodes[i]
		for _, c := range n.Conditions {
			if c.Type == "DiskPressure" && c.Status == "True" {
				out = append(out, nodeConditionFinding(ctx, r, n, "DiskPressure", model.SeverityHigh, "节点 DiskPressure", nil))
				break
			}
		}
	}
	return out
}

// NodeMemoryPressureRule 节点内存压力。
type NodeMemoryPressureRule struct{}

func (NodeMemoryPressureRule) Name() string    { return "NodeMemoryPressure" }
func (NodeMemoryPressureRule) NeedsLogs() bool { return false }

func (r NodeMemoryPressureRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.Nodes {
		n := &ctx.Snapshot.Nodes[i]
		for _, c := range n.Conditions {
			if c.Type == "MemoryPressure" && c.Status == "True" {
				out = append(out, nodeConditionFinding(ctx, r, n, "MemoryPressure", model.SeverityHigh, "节点 MemoryPressure", nil))
				break
			}
		}
	}
	return out
}
