package deterministic_test

import (
	"github.com/k8s-ai/k8s-ai/internal/model"
)

func init() {
	crashing := func() model.ContainerInfo {
		return model.ContainerInfo{
			Name: "web", Image: "repo/web:1.2", State: "Waiting", Reason: "CrashLoopBackOff",
			RestartCount: 9, LastState: "Terminated", LastReason: "Error", LastExitCode: 1,
		}
	}

	scenarios = append(scenarios,
		scenario{
			name: "Deployment 副本不足", id: "WORKLOAD_REPLICAS_UNAVAILABLE", rule: "DeploymentReplica",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "Deployment", "prod", "web", func(w *model.WorkloadInfo) {
					w.Selector = map[string]string{"app": "web"}
					w.DesiredReplicas, w.ReadyReplicas = int32p(3), int32p(1)
					w.Conditions = []model.ConditionInfo{
						{Type: "Available", Status: "False", Reason: "MinimumReplicasUnavailable"},
					}
				})
				addEvent(s, objRef("Deployment", "prod", "web"), "Warning", "ScalingReplicaSet",
					"Scaled up replica set web-abc to 3")
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "Deployment", "prod", "web", func(w *model.WorkloadInfo) {
					w.Selector = map[string]string{"app": "web"}
					w.DesiredReplicas, w.ReadyReplicas = int32p(3), int32p(3)
				})
				return s
			},
			// 缩容到 0 的 Deployment 是「故意不跑」，不是副本不足。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "Deployment", "prod", "web", func(w *model.WorkloadInfo) {
					w.DesiredReplicas, w.ReadyReplicas = int32p(0), int32p(0)
				})
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Workloads = nil },
			multiRootRule: "CrashLoopBackOff",
			multi: func() *model.ClusterSnapshot {
				// 副本不足是症状，容器持续崩溃才是根因：结论不能停在「副本数不够」。
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "Deployment", "prod", "web", func(w *model.WorkloadInfo) {
					w.Selector = map[string]string{"app": "web"}
					w.DesiredReplicas, w.ReadyReplicas = int32p(2), int32p(0)
				})
				addWorkload(s, "ReplicaSet", "prod", "web-abc", func(w *model.WorkloadInfo) {
					w.OwnerRefs = []model.ResourceRef{objRef("Deployment", "prod", "web")}
				})
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Labels = map[string]string{"app": "web"}
					p.OwnerRefs = []model.ResourceRef{objRef("ReplicaSet", "prod", "web-abc")}
					p.Containers = []model.ContainerInfo{crashing()}
				})
				addEvent(s, s.Pods[0].Ref, "Warning", "BackOff", "Back-off restarting failed container")
				return s
			},
		},
		scenario{
			name: "StatefulSet 异常", id: "WORKLOAD_REPLICAS_UNAVAILABLE", rule: "StatefulSetReplica",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "StatefulSet", "prod", "db", func(w *model.WorkloadInfo) {
					w.Selector = map[string]string{"app": "db"}
					w.DesiredReplicas, w.ReadyReplicas = int32p(3), int32p(2)
				})
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "StatefulSet", "prod", "db", func(w *model.WorkloadInfo) {
					w.DesiredReplicas, w.ReadyReplicas = int32p(3), int32p(3)
				})
				return s
			},
			// 滚动更新中 current 落后于 updateRevision 但就绪数已够：不算异常。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "StatefulSet", "prod", "db", func(w *model.WorkloadInfo) {
					w.DesiredReplicas, w.ReadyReplicas, w.UpdatedReplicas = int32p(3), int32p(3), int32p(1)
				})
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Workloads = nil },
			multiRootRule: "PVCPending",
			multi: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "StatefulSet", "prod", "db", func(w *model.WorkloadInfo) {
					w.Selector = map[string]string{"app": "db"}
					w.DesiredReplicas, w.ReadyReplicas = int32p(2), int32p(1)
				})
				addStorage(s, "PVC", "prod", "data-db-1", func(st *model.StorageInfo) { st.Status = "Pending" })
				addPod(s, "prod", "db-1", func(p *model.PodInfo) {
					p.Phase, p.CreationTimestamp = "Pending", oldTimestamp
					p.Labels = map[string]string{"app": "db"}
					p.OwnerRefs = []model.ResourceRef{objRef("StatefulSet", "prod", "db")}
					p.PVCRefs = []model.ResourceRef{objRef("PVC", "prod", "data-db-1")}
				})
				return s
			},
		},
		scenario{
			name: "DaemonSet 未覆盖节点", id: "WORKLOAD_DAEMONSET_GAP", rule: "DaemonSetReplica",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "DaemonSet", "kube-system", "agent", func(w *model.WorkloadInfo) {
					w.Selector = map[string]string{"k8s-app": "agent"}
					w.DesiredReplicas, w.ReadyReplicas = int32p(3), int32p(2)
				})
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "DaemonSet", "kube-system", "agent", func(w *model.WorkloadInfo) {
					w.DesiredReplicas, w.ReadyReplicas = int32p(3), int32p(3)
				})
				return s
			},
			// desiredNumberScheduled=0（节点全部被污点排除）时没有「漏覆盖」一说。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "DaemonSet", "kube-system", "agent", func(w *model.WorkloadInfo) {
					w.DesiredReplicas, w.ReadyReplicas = int32p(0), int32p(0)
				})
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Workloads = nil },
			multiRootRule: "NodeNotReady",
			multi: func() *model.ClusterSnapshot {
				// 节点 NotReady → 该节点上的 agent 起不来 → DaemonSet 少一个覆盖节点。
				s := newSnapshot()
				addNode(s, "node-2", func(n *model.NodeInfo) {
					n.Conditions = []model.ConditionInfo{{Type: "Ready", Status: "False", Reason: "KubeletNotReady"}}
				})
				addNode(s, "node-1", func(n *model.NodeInfo) {
					n.Conditions = []model.ConditionInfo{{Type: "Ready", Status: "True"}}
				})
				addWorkload(s, "DaemonSet", "kube-system", "agent", func(w *model.WorkloadInfo) {
					w.DesiredReplicas, w.ReadyReplicas = int32p(2), int32p(1)
				})
				addPod(s, "kube-system", "agent-x1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.OwnerRefs = []model.ResourceRef{objRef("DaemonSet", "kube-system", "agent")}
					p.Containers = []model.ContainerInfo{{Name: "agent", State: "Running", Ready: true}}
				})
				addPod(s, "kube-system", "agent-x2", func(p *model.PodInfo) {
					p.NodeName, p.Phase, p.Reason = "node-2", "Failed", "Evicted"
					p.OwnerRefs = []model.ResourceRef{objRef("DaemonSet", "kube-system", "agent")}
				})
				return s
			},
		},
		scenario{
			name: "Job 执行失败", id: "WORKLOAD_JOB_FAILED", rule: "JobFailed",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "Job", "prod", "migrate", func(w *model.WorkloadInfo) {
					w.Selector = map[string]string{"job-name": "migrate"}
					w.Conditions = []model.ConditionInfo{
						{Type: "Failed", Status: "True", Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit"},
					}
				})
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "Job", "prod", "migrate", func(w *model.WorkloadInfo) {
					w.Conditions = []model.ConditionInfo{{Type: "Complete", Status: "True"}}
				})
				return s
			},
			// Failed=False 的在跑 Job 不能当成失败（条件类型比状态字符串更容易读错）。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "Job", "prod", "migrate", func(w *model.WorkloadInfo) {
					w.Conditions = []model.ConditionInfo{{Type: "Failed", Status: "False", Reason: "JobHasActivePods"}}
				})
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Workloads = nil },
			multiRootRule: "OOMKilled",
			multi: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "Job", "prod", "migrate", func(w *model.WorkloadInfo) {
					w.Selector = map[string]string{"job-name": "migrate"}
					w.Conditions = []model.ConditionInfo{{Type: "Failed", Status: "True", Reason: "BackoffLimitExceeded"}}
				})
				addPod(s, "prod", "migrate-1", func(p *model.PodInfo) {
					p.NodeName, p.Phase = "node-1", "Failed"
					p.Labels = map[string]string{"job-name": "migrate"}
					p.OwnerRefs = []model.ResourceRef{objRef("Job", "prod", "migrate")}
					p.Containers = []model.ContainerInfo{{
						Name: "job", State: "Terminated", Reason: "OOMKilled", LastExitCode: 137,
					}}
				})
				addEvent(s, s.Pods[0].Ref, "Warning", "OOMKilling", "Memory cgroup out of memory")
				return s
			},
		},
		scenario{
			name: "HPA 异常", id: "WORKLOAD_HPA_NOT_SCALING", rule: "HpaReplicasMismatch",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "Deployment", "prod", "web", func(w *model.WorkloadInfo) {
					w.DesiredReplicas, w.ReadyReplicas = int32p(2), int32p(2)
				})
				s.Hpas = []model.HpaInfo{{
					Ref: objRef("HPA", "prod", "web"), MinReplicas: int32p(2), MaxReplicas: 10,
					DesiredReplicas: 5, CurrentReplicas: 2, ScaleTarget: "Deployment/web",
				}}
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "Deployment", "prod", "web", func(w *model.WorkloadInfo) {
					w.DesiredReplicas, w.ReadyReplicas = int32p(3), int32p(3)
				})
				s.Hpas = []model.HpaInfo{{
					Ref: objRef("HPA", "prod", "web"), MaxReplicas: 10,
					DesiredReplicas: 3, CurrentReplicas: 3, ScaleTarget: "Deployment/web",
				}}
				return s
			},
			// desiredReplicas=0 表示 HPA 还没算出目标（指标未就绪），不是扩不上去。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				s.Hpas = []model.HpaInfo{{
					Ref: objRef("HPA", "prod", "web"), MaxReplicas: 10,
					DesiredReplicas: 0, CurrentReplicas: 2, ScaleTarget: "Deployment/web",
				}}
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Hpas = nil },
			multiRootRule: "HpaScaleTargetMissing",
			multi: func() *model.ClusterSnapshot {
				// 伸缩目标指向不存在的负载：这是确定的根因，副本不足只是它的下游表现。
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "Deployment", "prod", "web", func(w *model.WorkloadInfo) {
					w.Selector = map[string]string{"app": "web"}
					w.DesiredReplicas, w.ReadyReplicas = int32p(3), int32p(1)
				})
				s.Hpas = []model.HpaInfo{{
					Ref: objRef("HPA", "prod", "web"), MaxReplicas: 10,
					DesiredReplicas: 5, CurrentReplicas: 1, ScaleTarget: "Deployment/web-old",
				}}
				return s
			},
		},
		scenario{
			name: "PDB 阻断驱逐", id: "WORKLOAD_PDB_BLOCKING", rule: "PdbBlocking",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				s.Pdbs = []model.PdbInfo{{
					Ref: objRef("PDB", "prod", "web"), MinAvailable: "3",
					Selector: map[string]string{"app": "web"}, DisruptionsAllowed: 0,
				}}
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				s.Pdbs = []model.PdbInfo{{
					Ref: objRef("PDB", "prod", "web"), MaxUnavailable: "1",
					Selector: map[string]string{"app": "web"}, DisruptionsAllowed: 2,
				}}
				return s
			},
			// selector 为空的 PDB 不保护任何 Pod，disruptionsAllowed=0 是空策略的结果而非阻塞。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				s.Pdbs = []model.PdbInfo{{
					Ref: objRef("PDB", "prod", "web"), MinAvailable: "1", DisruptionsAllowed: 0,
				}}
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Pdbs = nil },
			multiRootRule: "PdbBlocking",
			multi: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				s.Pdbs = []model.PdbInfo{{
					Ref: objRef("PDB", "prod", "web"), MinAvailable: "2",
					Selector: map[string]string{"app": "web"}, DisruptionsAllowed: 0,
				}}
				addWorkload(s, "Deployment", "prod", "web", func(w *model.WorkloadInfo) {
					w.Selector = map[string]string{"app": "web"}
					w.DesiredReplicas, w.ReadyReplicas = int32p(2), int32p(2)
				})
				return s
			},
		},
		scenario{
			name: "配置引用缺失", id: "WORKLOAD_CONFIG_REF_MISSING", rule: "ConfigRefNotFound",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.ConfigMaps = []model.ResourceRef{objRef("ConfigMap", "prod", "web-settings")}
				})
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				s.ConfigMaps = []model.ResourceRef{objRef("ConfigMap", "prod", "web-settings")}
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.ConfigMaps = []model.ResourceRef{objRef("ConfigMap", "prod", "web-settings")}
					p.Containers = []model.ContainerInfo{{Name: "web", State: "Running", Ready: true}}
				})
				return s
			},
			// 命名空间为空的引用不做存在性判定（快照里没有该命名空间的清单，说了也不算）。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.ConfigMaps = []model.ResourceRef{objRef("ConfigMap", "", "web-settings")}
				})
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Pods = nil },
			multiRootRule: "ConfigRefNotFound",
			multi: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addWorkload(s, "Deployment", "prod", "web", func(w *model.WorkloadInfo) {
					w.Selector = map[string]string{"app": "web"}
					w.DesiredReplicas, w.ReadyReplicas = int32p(1), int32p(0)
				})
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName, p.Phase = "node-1", "Pending"
					p.Labels = map[string]string{"app": "web"}
					p.OwnerRefs = []model.ResourceRef{objRef("Deployment", "prod", "web")}
					p.ConfigMaps = []model.ResourceRef{objRef("ConfigMap", "prod", "web-settings")}
				})
				return s
			},
		},
	)
}
