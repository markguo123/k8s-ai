// Package rule 实现规则引擎（FR-013）。
// default_meta.go：一期 23 条默认规则的结构化元数据（P7.2，SPEC §28.1）。
package rule

import "github.com/k8s-ai/k8s-ai/internal/model"

// RuleMeta 键为规则 Name()，值为注册时注入的结构化元数据。
// 元数据与检测逻辑解耦：规则只负责判定，元数据由注册层统一管理（ADR-002）。
var defaultRuleMeta = map[string]RuleMeta{
	"CrashLoopBackOff": {
		RuleID: "WORKLOAD_CRASHLOOPBACKOFF", Category: "Workload", Kind: "Pod",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityMedium,
		NeedsLogs: true, DocRef: "status.containerStatuses[].state.waiting",
	},
	"OOMKilled": {
		RuleID: "WORKLOAD_OOMKILLED", Category: "Workload", Kind: "Pod",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityMedium,
		NeedsLogs: true, DocRef: "status.containerStatuses[].lastState.terminated",
	},
	"ImagePullBackOff": {
		RuleID: "WORKLOAD_IMAGEPULLBACKOFF", Category: "Workload", Kind: "Pod",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityMedium,
		NeedsLogs: true, DocRef: "status.containerStatuses[].state.waiting",
	},
	"PendingPod": {
		RuleID: "WORKLOAD_PENDINGPOD", Category: "Workload", Kind: "Pod",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityMedium,
		NeedsLogs: false, DocRef: "spec.nodeName",
	},
	"PodTerminating": {
		RuleID: "WORKLOAD_PODTERMINATING", Category: "Workload", Kind: "Pod",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityMedium,
		NeedsLogs: false, DocRef: "metadata.deletionTimestamp",
	},
	"EvictedPod": {
		RuleID: "WORKLOAD_EVICTEDPOD", Category: "Workload", Kind: "Pod",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityMedium,
		NeedsLogs: false, DocRef: "status.phase",
	},
	"HighRestartCount": {
		RuleID: "WORKLOAD_HIGHRESTARTCOUNT", Category: "Workload", Kind: "Pod",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityLow,
		NeedsLogs: true, DocRef: "status.containerStatuses[].restartCount",
	},
	"ContainerCreateError": {
		RuleID: "WORKLOAD_CONTAINERCREATEERROR", Category: "Workload", Kind: "Pod",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityMedium,
		NeedsLogs: false, DocRef: "status.containerStatuses[].state.waiting",
	},
	"Unhealthy": {
		RuleID: "WORKLOAD_UNHEALTHY", Category: "Workload", Kind: "Pod",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityMedium,
		NeedsLogs: true, DocRef: "status.containerStatuses[].ready",
	},
	"IngressBackend": {
		RuleID: "NETWORK_INGRESSBACKEND", Category: "Network", Kind: "Ingress",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityMedium,
		NeedsLogs: false, DocRef: "spec.rules[].http.paths[].backend.service",
	},
	"NodeNotReady": {
		RuleID: "RELIABILITY_NODENOTREADY", Category: "Reliability", Kind: "Node",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityCritical,
		NeedsLogs: false, DocRef: "status.conditions[].type=Ready",
	},
	"NodeDiskPressure": {
		RuleID: "RELIABILITY_NODEDISKPRESSURE", Category: "Reliability", Kind: "Node",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityHigh,
		NeedsLogs: false, DocRef: "status.conditions[].type=DiskPressure",
	},
	"NodeMemoryPressure": {
		RuleID: "RELIABILITY_NODEMEMORYPRESSURE", Category: "Reliability", Kind: "Node",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityHigh,
		NeedsLogs: false, DocRef: "status.conditions[].type=MemoryPressure",
	},
	"NodePIDPressure": {
		RuleID: "RELIABILITY_NODEPIDPRESSURE", Category: "Reliability", Kind: "Node",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityHigh,
		NeedsLogs: false, DocRef: "status.conditions[].type=PIDPressure",
	},
	"PVCPending": {
		RuleID: "STORAGE_PVCPENDING", Category: "Storage", Kind: "PersistentVolumeClaim",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityHigh,
		NeedsLogs: false, DocRef: "status.phase",
	},
	"PVFailed": {
		RuleID: "STORAGE_PVFAILED", Category: "Storage", Kind: "PersistentVolume",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityHigh,
		NeedsLogs: false, DocRef: "status.phase",
	},
	"FailedMount": {
		RuleID: "STORAGE_FAILEDMOUNT", Category: "Storage", Kind: "Pod",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityMedium,
		NeedsLogs: false, DocRef: "spec.volumes[].persistentVolumeClaim",
	},
	"ServiceNoEndpoint": {
		RuleID: "NETWORK_SERVICENOENDPOINT", Category: "Network", Kind: "Service",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityMedium,
		NeedsLogs: false, DocRef: "spec.selector",
	},
	"DeploymentReplica": {
		RuleID: "WORKLOAD_DEPLOYMENTREPLICA", Category: "Workload", Kind: "Deployment",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityHigh,
		NeedsLogs: false, DocRef: "spec.replicas",
	},
	"StatefulSetReplica": {
		RuleID: "WORKLOAD_STATEFULSETREPLICA", Category: "Workload", Kind: "StatefulSet",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityHigh,
		NeedsLogs: false, DocRef: "spec.replicas",
	},
	"DaemonSetReplica": {
		RuleID: "WORKLOAD_DAEMONSETREPLICA", Category: "Workload", Kind: "DaemonSet",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityHigh,
		NeedsLogs: false, DocRef: "status.desiredNumberScheduled",
	},
	"DeploymentProgressDeadline": {
		RuleID: "WORKLOAD_PROGRESSDEADLINE", Category: "Workload", Kind: "Deployment",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityMedium,
		NeedsLogs: false, DocRef: "status.conditions[].type=Progressing",
	},
	"JobFailed": {
		RuleID: "WORKLOAD_JOBFAILED", Category: "Workload", Kind: "Job",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityHigh,
		NeedsLogs: true, DocRef: "status.conditions[].type=Failed",
	},
	"CronJobSuspended": {
		RuleID: "WORKLOAD_CRONJOBSUSPENDED", Category: "Workload", Kind: "CronJob",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityLow,
		NeedsLogs: false, DocRef: "spec.suspend",
	},
	"DeploymentOverReplicas": {
		RuleID: "WORKLOAD_DEPLOYMENTOVERREPLICAS", Category: "Workload", Kind: "Deployment",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityLow,
		NeedsLogs: false, DocRef: "spec.replicas",
	},
	"NetworkPolicyNoDefaultDeny": {
		RuleID: "NETWORK_NODEFAULTDENY", Category: "Network", Kind: "Namespace",
		RuleType: RuleTypeSuggestion, SeverityBase: model.SeverityLow,
		NeedsLogs: false, DocRef: "spec.podSelector",
	},
	"IngressHostConflict": {
		RuleID: "NETWORK_INGRESSHOSTCONFLICT", Category: "Network", Kind: "Ingress",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityMedium,
		NeedsLogs: false, DocRef: "spec.rules[].http.paths[].path",
	},
	"VolumeAttachmentStuck": {
		RuleID: "STORAGE_VOLUMEATTACHMENTSTUCK", Category: "Storage", Kind: "VolumeAttachment",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityMedium,
		NeedsLogs: false, DocRef: "status.attached",
	},
	"StorageClassNoProvisioner": {
		RuleID: "STORAGE_STORAGECLASSNOPROVISIONER", Category: "Storage", Kind: "StorageClass",
		RuleType: RuleTypeSuggestion, SeverityBase: model.SeverityLow,
		NeedsLogs: false, DocRef: "provisioner",
	},
	"ContainerHealthTimeout": {
		RuleID: "WORKLOAD_CONTAINERHEALTHTIMEOUT", Category: "Workload", Kind: "Pod",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityLow,
		NeedsLogs: true, DocRef: "status.containerStatuses[].ready",
	},
	"HpaReplicasMismatch": {
		RuleID: "WORKLOAD_HPAREPLICASMISMATCH", Category: "Workload", Kind: "HPA",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityMedium,
		NeedsLogs: false, DocRef: "status.desiredReplicas",
	},
	"HpaScaleTargetMissing": {
		RuleID: "WORKLOAD_HPASCALETARGETMISSING", Category: "Workload", Kind: "HPA",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityHigh,
		NeedsLogs: false, DocRef: "spec.scaleTargetRef",
	},
	"PdbBlocking": {
		RuleID: "POLICY_PDBBLOCKING", Category: "Policy", Kind: "PodDisruptionBudget",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityHigh,
		NeedsLogs: false, DocRef: "status.disruptionsAllowed",
	},
	"RbacOverPrivileged": {
		RuleID: "SECURITY_RBACOVERPRIVILEGED", Category: "Security", Kind: "ClusterRole",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityMedium,
		NeedsLogs: false, DocRef: "rules[].verbs",
	},
	"ConfigRefNotFound": {
		RuleID: "WORKLOAD_CONFIGREFNOTFOUND", Category: "Workload", Kind: "Pod",
		RuleType: RuleTypeSuggestion, SeverityBase: model.SeverityLow,
		NeedsLogs: false, DocRef: "spec.volumes[].configMap",
	},
}

// DefaultMeta 返回规则名对应的元数据；未注册返回 false。
func DefaultMeta(name string) (RuleMeta, bool) {
	m, ok := defaultRuleMeta[name]
	return m, ok
}

// AllDefaultWithMeta 返回一期全部默认规则及其元数据。
// 未显式配置元数据的规则（#noinspection 兼容 Register 兜底）自动生成默认值。
func AllDefaultWithMeta() []RuleWithMeta {
	defaults := AllDefault()
	out := make([]RuleWithMeta, 0, len(defaults))
	for _, r := range defaults {
		m, ok := DefaultMeta(r.Name())
		if !ok {
			m = RuleMeta{}
		}
		out = append(out, RuleWithMeta{Rule: r, Meta: normalizeMeta(r, m)})
	}
	return out
}
