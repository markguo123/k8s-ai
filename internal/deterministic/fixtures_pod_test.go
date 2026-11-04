package deterministic_test

import (
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// nodeOK 是一个健康节点（Pod 已调度时规则不会额外报 NodeNotReady）。
func nodeOK(s *model.ClusterSnapshot) {
	addNode(s, "node-1", func(n *model.NodeInfo) {
		n.Conditions = []model.ConditionInfo{{Type: "Ready", Status: "True"}}
	})
}

func init() {
	crashContainer := func() model.ContainerInfo {
		return model.ContainerInfo{
			Name: "web", Image: "repo/web:1.2", State: "Waiting", Reason: "CrashLoopBackOff",
			RestartCount: 17, LastState: "Terminated", LastReason: "Error", LastExitCode: 2,
		}
	}

	scenarios = append(scenarios,
		scenario{
			name: "CrashLoopBackOff", id: "POD_CRASHLOOP", rule: "CrashLoopBackOff",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName, p.Phase = "node-1", "Running"
					p.Containers = []model.ContainerInfo{crashContainer()}
				})
				addEvent(s, s.Pods[0].Ref, "Warning", "BackOff", "Back-off restarting failed container")
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Containers = []model.ContainerInfo{{Name: "web", State: "Running", Ready: true, Image: "repo/web:1.2"}}
				})
				return s
			},
			// 重启过但当前已就绪：不能报 CrashLoopBackOff（HighRestartCount 是另一条规则）。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Containers = []model.ContainerInfo{{Name: "web", State: "Running", Ready: true, RestartCount: 17}}
				})
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Pods = nil },
			multiRootRule: "CrashLoopBackOff",
			multi: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "Deployment", "prod", "web", func(w *model.WorkloadInfo) {
					w.Selector = map[string]string{"app": "web"}
					w.DesiredReplicas, w.ReadyReplicas = int32p(3), int32p(0)
				})
				addWorkload(s, "ReplicaSet", "prod", "web-abc", func(w *model.WorkloadInfo) {
					w.OwnerRefs = []model.ResourceRef{objRef("Deployment", "prod", "web")}
				})
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Labels = map[string]string{"app": "web"}
					p.OwnerRefs = []model.ResourceRef{objRef("ReplicaSet", "prod", "web-abc")}
					p.Containers = []model.ContainerInfo{crashContainer()}
				})
				addEvent(s, s.Pods[0].Ref, "Warning", "BackOff", "Back-off restarting failed container")
				addService(s, "prod", "web", map[string]string{"app": "web"})
				s.EndpointSlices = []model.EndpointSliceInfo{{
					Ref: objRef("EndpointSlice", "prod", "web-abc"), ServiceName: "web", Ready: 0, NotReady: 1,
				}}
				return s
			},
		},
		scenario{
			name: "ImagePullBackOff", id: "POD_IMAGEPULL", rule: "ImagePullBackOff",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName, p.Phase = "node-1", "Pending"
					p.Containers = []model.ContainerInfo{{
						Name: "web", Image: "registry.internal/web:1.2", State: "Waiting",
						Reason: "ImagePullBackOff", Message: "pull access denied for registry.internal/web",
					}}
				})
				addEvent(s, s.Pods[0].Ref, "Warning", "Failed",
					"Failed to pull image \"registry.internal/web:1.2\": pull access denied")
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Containers = []model.ContainerInfo{{Name: "web", Image: "registry.internal/web:1.2", State: "Running", Ready: true}}
				})
				return s
			},
			// 正在创建容器（ContainerCreating）不是拉取失败，不能提前定罪。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName, p.Phase = "node-1", "Pending"
					p.Containers = []model.ContainerInfo{{Name: "web", State: "Waiting", Reason: "ContainerCreating"}}
				})
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Pods = nil },
			multiRootRule: "ImagePullBackOff",
			multi: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "Deployment", "prod", "web", func(w *model.WorkloadInfo) {
					w.DesiredReplicas, w.ReadyReplicas = int32p(2), int32p(0)
				})
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName, p.Phase = "node-1", "Pending"
					p.OwnerRefs = []model.ResourceRef{objRef("Deployment", "prod", "web")}
					p.Containers = []model.ContainerInfo{{Name: "web", State: "Waiting", Reason: "ErrImagePull", Message: "manifest unknown"}}
				})
				return s
			},
		},
		scenario{
			name: "Pending", id: "POD_UNSCHEDULABLE", rule: "PendingPod",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.Phase, p.CreationTimestamp = "Pending", oldTimestamp
					p.Conditions = []model.ConditionInfo{{Type: "PodScheduled", Status: "False", Reason: "Unschedulable"}}
				})
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Conditions = []model.ConditionInfo{{Type: "PodScheduled", Status: "True"}}
					p.Containers = []model.ContainerInfo{{Name: "web", State: "Running", Ready: true}}
				})
				return s
			},
			// 刚创建 10 秒的 Pending 是正常调度过程，不该报故障。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.Phase = "Pending"
					p.CreationTimestamp = time.Now().UTC().Add(-10 * time.Second).Format(time.RFC3339)
				})
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Pods = nil },
			multiRootRule: "NodeNotReady",
			multi: func() *model.ClusterSnapshot {
				s := newSnapshot()
				addNode(s, "node-1", func(n *model.NodeInfo) {
					n.Conditions = []model.ConditionInfo{{Type: "Ready", Status: "False", Reason: "KubeletNotReady"}}
				})
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.Phase, p.CreationTimestamp = "Pending", oldTimestamp
					p.NodeName = "node-1"
				})
				return s
			},
		},
		scenario{
			name: "FailedScheduling", id: "POD_UNSCHEDULABLE", rule: "PendingPod",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				addNode(s, "node-1", func(n *model.NodeInfo) {
					n.Conditions = []model.ConditionInfo{{Type: "Ready", Status: "True"}}
					n.Capacity = map[string]string{"cpu": "2"}
					n.Allocatable = map[string]string{"cpu": "500m"}
				})
				addPod(s, "prod", "big-1", func(p *model.PodInfo) {
					p.Phase, p.CreationTimestamp = "Pending", oldTimestamp
					p.Conditions = []model.ConditionInfo{{Type: "PodScheduled", Status: "False", Reason: "Unschedulable"}}
					p.Containers = []model.ContainerInfo{{Name: "c", Requests: map[string]string{"cpu": "32"}}}
				})
				addEvent(s, s.Pods[0].Ref, "Warning", "FailedScheduling",
					"0/1 nodes are available: 1 Insufficient cpu. preemption: 0/1 nodes are available: 1 No preemption victims found for incoming pod.")
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Containers = []model.ContainerInfo{{Name: "c", State: "Running", Ready: true, Requests: map[string]string{"cpu": "100m"}}}
				})
				return s
			},
			// 刚创建 10 秒、带一条 FailedScheduling 事件：调度器重试前的瞬时事件，
			// 不能凭它定罪；同理，事件里没有资源不足证据时也不能凭空说「配额不够」。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				addNode(s, "node-1", func(n *model.NodeInfo) {
					n.Conditions = []model.ConditionInfo{{Type: "Ready", Status: "True"}}
					n.Capacity = map[string]string{"cpu": "2"}
					n.Allocatable = map[string]string{"cpu": "500m"}
				})
				addPod(s, "prod", "big-1", func(p *model.PodInfo) {
					p.Phase = "Pending"
					p.CreationTimestamp = time.Now().UTC().Add(-10 * time.Second).Format(time.RFC3339)
					p.Containers = []model.ContainerInfo{{Name: "c", Requests: map[string]string{"cpu": "32"}}}
				})
				addEvent(s, s.Pods[0].Ref, "Warning", "FailedScheduling",
					"0/1 nodes are available: 1 Insufficient cpu.")
				return s
			},
			// 主体资源是 Pod 本身：Pod 没采到时不该有任何调度类结论。
			drop: func(s *model.ClusterSnapshot) { s.Pods = nil },
		},
		scenario{
			name: "FailedMount", id: "POD_FAILEDMOUNT", rule: "FailedMount",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.Phase, p.CreationTimestamp = "Pending", oldTimestamp
					p.PVCRefs = []model.ResourceRef{objRef("PVC", "prod", "data-web-1")}
				})
				addStorage(s, "PVC", "prod", "data-web-1", func(st *model.StorageInfo) {
					st.Status, st.Reason = "Pending", "WaitForFirstConsumer"
				})
				addEvent(s, s.Pods[0].Ref, "Warning", "FailedMount",
					"MountVolume.SetUp failed for volume \"data\" : persistentvolumeclaim \"data-web-1\" not found")
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.PVCRefs = []model.ResourceRef{objRef("PVC", "prod", "data-web-1")}
					p.Containers = []model.ContainerInfo{{Name: "web", State: "Running", Ready: true}}
				})
				addStorage(s, "PVC", "prod", "data-web-1", nil)
				return s
			},
			// 拉取镜像凭据失败不是挂载失败，不能混成一类。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.Phase, p.CreationTimestamp = "Pending", oldTimestamp
				})
				addEvent(s, s.Pods[0].Ref, "Warning", "FailedToRetrieveImagePullSecret",
					"Unable to retrieve some image pull secrets (regcred)")
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.EventsIndex = nil },
			multiRootRule: "PVCPending",
			multi: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.Phase, p.CreationTimestamp = "Pending", oldTimestamp
					p.PVCRefs = []model.ResourceRef{objRef("PVC", "prod", "data-web-1")}
				})
				addStorage(s, "PVC", "prod", "data-web-1", func(st *model.StorageInfo) { st.Status = "Pending" })
				addEvent(s, s.Pods[0].Ref, "Warning", "FailedMount",
					"Unable to attach or mount volumes: unmounted volumes=[data]")
				return s
			},
		},
		scenario{
			name: "Pod OOMKilled", id: "POD_OOMKILLED", rule: "OOMKilled",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "cache-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Containers = []model.ContainerInfo{{
						Name: "cache", State: "Waiting", Reason: "CrashLoopBackOff",
						LastState: "Terminated", LastReason: "OOMKilled", LastExitCode: 137,
						Limits: map[string]string{"memory": "256Mi"}, Requests: map[string]string{"memory": "256Mi"},
					}}
				})
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "cache-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Containers = []model.ContainerInfo{{
						Name: "cache", State: "Running", Ready: true,
						Limits: map[string]string{"memory": "512Mi"},
					}}
				})
				return s
			},
			// 退出码 1 的普通崩溃不能记成 OOM。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "cache-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Containers = []model.ContainerInfo{{
						Name: "cache", State: "Terminated", LastState: "Terminated",
						LastReason: "Error", LastExitCode: 1,
					}}
				})
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Pods = nil },
			multiRootRule: "OOMKilled",
			multi: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "Deployment", "prod", "cache", func(w *model.WorkloadInfo) {
					w.DesiredReplicas, w.ReadyReplicas = int32p(2), int32p(0)
				})
				addPod(s, "prod", "cache-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.OwnerRefs = []model.ResourceRef{objRef("Deployment", "prod", "cache")}
					p.Containers = []model.ContainerInfo{{
						Name: "cache", LastState: "Terminated", LastReason: "OOMKilled",
						LastExitCode: 137, Limits: map[string]string{"memory": "256Mi"},
					}}
				})
				return s
			},
		},
		scenario{
			name: "Pod Evicted", id: "POD_EVICTED", rule: "EvictedPod",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				addNode(s, "node-1", func(n *model.NodeInfo) {
					n.Conditions = []model.ConditionInfo{
						{Type: "Ready", Status: "True"},
						{Type: "DiskPressure", Status: "True", Reason: "KubeletOutOfStorage"},
					}
				})
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName, p.Phase, p.Reason = "node-1", "Failed", "Evicted"
					p.Containers = []model.ContainerInfo{{Name: "web", State: "Terminated", LastExitCode: 137}}
				})
				addEvent(s, s.Pods[0].Ref, "Warning", "Evicted",
					"The node was low on resource: ephemeral-storage.")
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
			// 容器自身失败（Phase=Failed 但 Reason 不是 Evicted）不是驱逐。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName, p.Phase, p.Reason = "node-1", "Failed", "ContainerStatusUnknown"
				})
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Pods = nil },
			multiRootRule: "NodeDiskPressure",
		},
		scenario{
			name: "探针持续失败", id: "POD_PROBE_FAILING", rule: "Unhealthy",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Containers = []model.ContainerInfo{{Name: "web", State: "Running", Ready: false}}
				})
				addEvent(s, s.Pods[0].Ref, "Warning", "Unhealthy",
					"Readiness probe failed: HTTP probe failed with statuscode: 503")
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
			// 探针曾经失败但容器现在已就绪：不能按持续失败报。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Containers = []model.ContainerInfo{{Name: "web", State: "Running", Ready: true}}
				})
				addEvent(s, s.Pods[0].Ref, "Warning", "Unhealthy", "Readiness probe failed: connection refused")
				return s
			},
			drop: func(s *model.ClusterSnapshot) { s.EventsIndex = nil },
		},
	)
}
