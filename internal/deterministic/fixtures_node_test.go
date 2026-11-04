package deterministic_test

import (
	"github.com/k8s-ai/k8s-ai/internal/model"
)

func init() {
	evicted := func(s *model.ClusterSnapshot, ns, name, node string) {
		addPod(s, ns, name, func(p *model.PodInfo) {
			p.NodeName = node
			p.Phase, p.Reason = "Failed", "Evicted"
		})
	}

	scenarios = append(scenarios,
		scenario{
			name: "Node NotReady", id: "NODE_NOTREADY", rule: "NodeNotReady",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				addNode(s, "node-1", func(n *model.NodeInfo) {
					n.Conditions = []model.ConditionInfo{
						{Type: "Ready", Status: "False", Reason: "KubeletNotReady", Message: "container runtime network not ready"},
					}
				})
				addEvent(s, objRef("Node", "", "node-1"), "Warning", "NodeNotReady",
					"Kubelet stopped posting node status")
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Containers = []model.ContainerInfo{{Name: "web", State: "Running", Ready: true}}
				})
				return s
			},
			// 只有非 Ready 条件、没有 Ready 条件：不能凭「少了一个条件」判 NotReady。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				addNode(s, "node-1", func(n *model.NodeInfo) {
					n.Conditions = []model.ConditionInfo{{Type: "DiskPressure", Status: "False"}}
				})
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Nodes = nil },
			multiRootRule: "NodeNotReady",
			multi: func() *model.ClusterSnapshot {
				// 节点失联 → 该节点上的 Pod 被驱逐：根因必须是节点，不是那一堆 Evicted。
				s := newSnapshot()
				addNode(s, "node-1", func(n *model.NodeInfo) {
					n.Conditions = []model.ConditionInfo{{Type: "Ready", Status: "Unknown", Reason: "NodeHealthLost"}}
				})
				evicted(s, "prod", "web-1", "node-1")
				evicted(s, "prod", "web-2", "node-1")
				return s
			},
		},
		scenario{
			name: "Node Pressure", id: "NODE_PRESSURE", rule: "NodeDiskPressure",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				addNode(s, "node-1", func(n *model.NodeInfo) {
					n.Conditions = []model.ConditionInfo{
						{Type: "Ready", Status: "True"},
						{Type: "DiskPressure", Status: "True", Reason: "KubeletHasDiskPressure", Message: "free space available is less than the threshold"},
					}
				})
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				addNode(s, "node-1", func(n *model.NodeInfo) {
					n.Conditions = []model.ConditionInfo{
						{Type: "Ready", Status: "True"},
						{Type: "DiskPressure", Status: "False"},
						{Type: "MemoryPressure", Status: "False"},
					}
				})
				return s
			},
			// Unknown 是「探测不出来」而不是「有压力」，不能当故障上报。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				addNode(s, "node-1", func(n *model.NodeInfo) {
					n.Conditions = []model.ConditionInfo{
						{Type: "Ready", Status: "True"},
						{Type: "MemoryPressure", Status: "Unknown", Reason: "NodeStatusUnknown"},
					}
				})
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Nodes = nil },
			multiRootRule: "NodeDiskPressure",
			multi: func() *model.ClusterSnapshot {
				s := newSnapshot()
				addNode(s, "node-1", func(n *model.NodeInfo) {
					n.Conditions = []model.ConditionInfo{
						{Type: "Ready", Status: "True"},
						{Type: "DiskPressure", Status: "True", Reason: "KubeletHasDiskPressure"},
					}
				})
				evicted(s, "prod", "web-1", "node-1")
				return s
			},
		},
	)
}
