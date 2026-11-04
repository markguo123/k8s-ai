package deterministic_test

import (
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func init() {
	scenarios = append(scenarios,
		scenario{
			name: "PVC Pending", id: "STORAGE_PVC_PENDING", rule: "PVCPending",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addStorage(s, "PVC", "prod", "data-web-0", func(st *model.StorageInfo) {
					st.Status, st.Reason = "Pending", "WaitingForFirstConsumer"
					st.StorageClassName = "fast-ssd"
				})
				addPod(s, "prod", "web-0", func(p *model.PodInfo) {
					p.Phase, p.CreationTimestamp = "Pending", oldTimestamp
					p.PVCRefs = []model.ResourceRef{objRef("PVC", "prod", "data-web-0")}
				})
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addStorage(s, "PVC", "prod", "data-web-0", func(st *model.StorageInfo) {
					st.Status, st.VolumeName = "Bound", "pv-web-0"
				})
				addPod(s, "prod", "web-0", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.PVCRefs = []model.ResourceRef{objRef("PVC", "prod", "data-web-0")}
					p.Containers = []model.ContainerInfo{{Name: "web", State: "Running", Ready: true}}
				})
				return s
			},
			// 已绑定但容量字段还没刷出来的 PVC 不是故障：只有 phase 才是判定依据。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addStorage(s, "PVC", "prod", "data-web-0", func(st *model.StorageInfo) {
					st.Status, st.Capacity = "Bound", ""
				})
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Storage = nil },
			multiRootRule: "PVCPending",
			multi: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addStorage(s, "PVC", "prod", "data-web-0", func(st *model.StorageInfo) { st.Status = "Pending" })
				addPod(s, "prod", "web-0", func(p *model.PodInfo) {
					p.Phase, p.CreationTimestamp = "Pending", oldTimestamp
					p.PVCRefs = []model.ResourceRef{objRef("PVC", "prod", "data-web-0")}
				})
				addEvent(s, s.Pods[0].Ref, "Warning", "FailedMount", "Unable to attach or mount volumes")
				return s
			},
		},
		scenario{
			name: "PV 异常", id: "STORAGE_PV_UNAVAILABLE", rule: "PVFailed",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addStorage(s, "PV", "", "pv-web-0", func(st *model.StorageInfo) {
					st.Status, st.Reason = "Failed", "volume data read only"
				})
				addStorage(s, "PVC", "prod", "data-web-0", func(st *model.StorageInfo) {
					st.Status, st.VolumeName = "Lost", "pv-web-0"
				})
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addStorage(s, "PV", "", "pv-web-0", func(st *model.StorageInfo) { st.Status = "Bound" })
				return s
			},
			// Released 在 Retain 回收策略下属于正常生命周期，规则只给 Low 级提示，
			// 不能和 Failed 混成同一个「存储坏了」的结论。
			boundary:      func() *model.ClusterSnapshot { return releasedPV() },
			boundaryHits:  true,
			drop:          func(s *model.ClusterSnapshot) { s.Storage = nil },
			multiRootRule: "PVCPending",
			multi: func() *model.ClusterSnapshot {
				// PV 坏了 → PVC 丢绑定 → Pod 挂不上卷：整条链必须聚成一个故障，
				// 且 PV→Pod 的因果边要能在关系图上找到 PV→PVC→Pod 的真实路径。
				s := newSnapshot()
				nodeOK(s)
				addStorage(s, "PV", "", "pv-web-0", func(st *model.StorageInfo) {
					st.Status, st.Reason = "Failed", "node affinity mismatch"
				})
				addStorage(s, "PVC", "prod", "data-web-0", func(st *model.StorageInfo) {
					st.Status, st.VolumeName = "Lost", "pv-web-0"
				})
				addPod(s, "prod", "web-0", func(p *model.PodInfo) {
					p.Phase, p.CreationTimestamp = "Pending", oldTimestamp
					p.PVCRefs = []model.ResourceRef{objRef("PVC", "prod", "data-web-0")}
				})
				addEvent(s, s.Pods[0].Ref, "Warning", "FailedMount",
					"MountVolume.SetUp failed for volume \"pv-web-0\" : node affine mismatch")
				return s
			},
		},
		scenario{
			name: "StorageClass 异常", id: "STORAGE_CLASS_INVALID", rule: "StorageClassNoProvisioner",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addStorage(s, "StorageClass", "", "fast-ssd", func(st *model.StorageInfo) { st.Driver = "" })
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addStorage(s, "StorageClass", "", "fast-ssd", func(st *model.StorageInfo) {
					st.Driver = "ebs.csi.aws.com"
				})
				return s
			},
			// no-provisioner 是显式声明的本地存储类（静态供给），不是漏配 provisioner。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addStorage(s, "StorageClass", "", "local-static", func(st *model.StorageInfo) {
					st.Driver = "kubernetes.io/no-provisioner"
				})
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Storage = nil },
			multiRootRule: "StorageClassNoProvisioner",
			multi: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addStorage(s, "StorageClass", "", "fast-ssd", func(st *model.StorageInfo) { st.Driver = "" })
				addStorage(s, "PVC", "prod", "data-web-0", func(st *model.StorageInfo) {
					st.Status, st.StorageClassName = "Pending", "fast-ssd"
				})
				return s
			},
		},
		scenario{
			name: "VolumeAttachment 卡住", id: "STORAGE_VOLUME_ATTACHMENT_STUCK", rule: "VolumeAttachmentStuck",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addStorage(s, "VolumeAttachment", "", "csi-0abc", func(st *model.StorageInfo) {
					st.Status, st.Reason = "", "AttachVolume.Attach failed: rpc error: timeout"
					st.CreatedAt = "2020-01-01T00:00:00Z"
				})
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addStorage(s, "VolumeAttachment", "", "csi-0abc", func(st *model.StorageInfo) { st.Status = "Attached" })
				return s
			},
			// 刚创建 30 秒、还没有 attachError：CSI 正在附加，不能判卡住。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addStorage(s, "VolumeAttachment", "", "csi-0abc", func(st *model.StorageInfo) {
					st.Status = ""
					st.CreatedAt = time.Now().UTC().Add(-30 * time.Second).Format(time.RFC3339)
				})
				return s
			},
			drop: func(s *model.ClusterSnapshot) { s.Storage = nil },
		},
	)
}

// releasedPV 是 Retain 策略下的正常 PV 生命周期状态（规则只报 Low 级提示）。
func releasedPV() *model.ClusterSnapshot {
	s := newSnapshot()
	nodeOK(s)
	addStorage(s, "PV", "", "pv-web-0", func(st *model.StorageInfo) { st.Status = "Released" })
	return s
}
