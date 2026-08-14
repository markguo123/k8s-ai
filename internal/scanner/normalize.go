package scanner

import (
	"fmt"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// ref 构造资源引用；UID 仅用于内部关联，不参与指纹（DATA_MODEL.md §2）。
func ref(kind, namespace, name, uid string) model.ResourceRef {
	return model.ResourceRef{Kind: kind, Namespace: namespace, Name: name, UID: uid}
}

// conditionInfo 归一化 Condition 为精简字段。
func conditionInfo(condType, status, reason, message string) model.ConditionInfo {
	return model.ConditionInfo{Type: condType, Status: status, Reason: reason, Message: message}
}

// normalizeNamespace 归一化命名空间。
func normalizeNamespace(ns corev1.Namespace) model.NamespaceInfo {
	return model.NamespaceInfo{Ref: ref("Namespace", "", ns.Name, string(ns.UID)), Phase: string(ns.Status.Phase), Labels: ns.Labels}
}

// normalizePod 抽取 Pod 异常判定所需字段（FR-005）。
// 注意：annotations 目前原样保存；security 脱敏包落地后会在此边界处理（ADR-006）。
func normalizePod(p corev1.Pod) model.PodInfo {
	info := model.PodInfo{
		Ref:         ref("Pod", p.Namespace, p.Name, string(p.UID)),
		Phase:       string(p.Status.Phase),
		NodeName:    p.Spec.NodeName,
		Labels:      redactMap(p.Labels),
		Annotations: redactMap(p.Annotations),
		QoSClass:    string(p.Status.QOSClass),
	}
	if p.Status.StartTime != nil {
		info.StartTime = p.Status.StartTime.UTC().Format(time.RFC3339)
	}
	for _, cond := range p.Status.Conditions {
		info.Conditions = append(info.Conditions, conditionInfo(string(cond.Type), string(cond.Status), cond.Reason, cond.Message))
	}
	// 容器 spec 按名称索引，用于补全 requests/limits。
	specByName := make(map[string]corev1.Container, len(p.Spec.Containers))
	for _, c := range p.Spec.Containers {
		specByName[c.Name] = c
	}
	for _, cs := range p.Status.ContainerStatuses {
		ci := model.ContainerInfo{
			Name:         cs.Name,
			Image:        cs.Image,
			ImageID:      cs.ImageID,
			RestartCount: cs.RestartCount,
			Ready:        cs.Ready,
			Requests:     map[string]string{},
			Limits:       map[string]string{},
		}
		ci.State, ci.Reason, ci.Message, ci.ExitCode = containerState(cs.State)
		ci.LastState, ci.LastReason, _, ci.LastExitCode = containerState(cs.LastTerminationState)
		if spec, ok := specByName[cs.Name]; ok {
			for r, q := range spec.Resources.Requests {
				ci.Requests[string(r)] = q.String()
			}
			for r, q := range spec.Resources.Limits {
				ci.Limits[string(r)] = q.String()
			}
		}
		info.Containers = append(info.Containers, ci)
	}
	for _, o := range p.OwnerReferences {
		info.OwnerRefs = append(info.OwnerRefs, ref(o.Kind, p.Namespace, o.Name, string(o.UID)))
	}
	// 提取 Pod 声明的 PVC，用于存储关联链（P3.2）。
	for _, v := range p.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			info.PVCRefs = append(info.PVCRefs, ref("PVC", p.Namespace, v.PersistentVolumeClaim.ClaimName, ""))
		}
	}
	return info
}

// containerState 将 ContainerState 归一化为 state/reason/message/exitCode。
func containerState(st corev1.ContainerState) (state, reason, message string, exitCode int32) {
	switch {
	case st.Waiting != nil:
		return "Waiting", st.Waiting.Reason, st.Waiting.Message, 0
	case st.Running != nil:
		return "Running", "", "", 0
	case st.Terminated != nil:
		return "Terminated", st.Terminated.Reason, st.Terminated.Message, st.Terminated.ExitCode
	default:
		return "Unknown", "", "", 0
	}
}

// normalizeNode 归一化节点状态、容量与污点（FR-006）。
func normalizeNode(n corev1.Node) model.NodeInfo {
	info := model.NodeInfo{
		Ref:           ref("Node", "", n.Name, string(n.UID)),
		Unschedulable: n.Spec.Unschedulable,
		Capacity:      resourceMap(n.Status.Capacity),
		Allocatable:   resourceMap(n.Status.Allocatable),
		Labels:        n.Labels,
	}
	for _, cond := range n.Status.Conditions {
		info.Conditions = append(info.Conditions, conditionInfo(string(cond.Type), string(cond.Status), cond.Reason, cond.Message))
	}
	for _, t := range n.Spec.Taints {
		info.Taints = append(info.Taints, fmt.Sprintf("%s=%s:%s", t.Key, t.Value, t.Effect))
	}
	return info
}

// resourceMap 将 ResourceList 转为字符串映射，避免 Quantity 直接序列化。
func resourceMap(list corev1.ResourceList) map[string]string {
	out := make(map[string]string, len(list))
	for r, q := range list {
		out[string(r)] = q.String()
	}
	return out
}

// normalizeService 归一化 Service 的 selector 与端口。
func normalizeService(s corev1.Service) model.ServiceInfo {
	info := model.ServiceInfo{
		Ref:        ref("Service", s.Namespace, s.Name, string(s.UID)),
		Selector:   s.Spec.Selector,
		ClusterIPs: s.Spec.ClusterIPs,
	}
	for _, p := range s.Spec.Ports {
		info.Ports = append(info.Ports, fmt.Sprintf("%d/%s", p.Port, p.Protocol))
	}
	return info
}

// normalizeEndpointSlice 归一化 EndpointSlice：统计 Ready/NotReady，
// 并保留所属 Service（kubernetes.io/service-name 标签）与端点 Pod（P3.3）。
func normalizeEndpointSlice(es discoveryv1.EndpointSlice) model.EndpointSliceInfo {
	info := model.EndpointSliceInfo{
		Ref:         ref("EndpointSlice", es.Namespace, es.Name, string(es.UID)),
		ServiceName: es.Labels[discoveryv1.LabelServiceName],
	}
	seenPods := map[string]bool{}
	for _, ep := range es.Endpoints {
		if ep.Conditions.Ready != nil && *ep.Conditions.Ready {
			info.Ready++
		} else {
			info.NotReady++
		}
		info.Addresses = append(info.Addresses, ep.Addresses...)
		if ep.TargetRef != nil && ep.TargetRef.Kind == "Pod" {
			ns := ep.TargetRef.Namespace
			if ns == "" {
				ns = es.Namespace
			}
			podRef := ref("Pod", ns, ep.TargetRef.Name, string(ep.TargetRef.UID))
			if !seenPods[podRef.Key()] {
				seenPods[podRef.Key()] = true
				info.TargetPods = append(info.TargetPods, podRef)
			}
		}
	}
	for _, p := range es.Ports {
		if p.Port != nil {
			info.Ports = append(info.Ports, fmt.Sprintf("%d/%s", *p.Port, protocol(p.Protocol)))
		}
	}
	return info
}

func protocol(p *corev1.Protocol) string {
	if p == nil {
		return string(corev1.ProtocolTCP)
	}
	return string(*p)
}

// workloadInfo 组装 WorkloadInfo（各 workload 适配器共用）。
func workloadInfo(kind, ns, name, uid string, desired, ready, available, updated *int32, conditions []model.ConditionInfo, selector map[string]string, ownerRefs []model.ResourceRef) model.WorkloadInfo {
	return model.WorkloadInfo{
		Ref:               ref(kind, ns, name, uid),
		OwnerRefs:         ownerRefs,
		DesiredReplicas:   desired,
		ReadyReplicas:     ready,
		AvailableReplicas: available,
		UpdatedReplicas:   updated,
		Conditions:        conditions,
		Selector:          selector,
	}
}

func int32p(v int32) *int32 { return &v }

func labelSelector(sel *metav1.LabelSelector) map[string]string {
	if sel == nil {
		return nil
	}
	return sel.MatchLabels
}

// normalizeDeployment 归一化 Deployment 副本数与 conditions。
func normalizeDeployment(d appsv1.Deployment) model.WorkloadInfo {
	var conds []model.ConditionInfo
	for _, c := range d.Status.Conditions {
		conds = append(conds, conditionInfo(string(c.Type), string(c.Status), c.Reason, c.Message))
	}
	return workloadInfo("Deployment", d.Namespace, d.Name, string(d.UID),
		d.Spec.Replicas, int32p(d.Status.ReadyReplicas), int32p(d.Status.AvailableReplicas), int32p(d.Status.UpdatedReplicas),
		conds, labelSelector(d.Spec.Selector), ownerRefs(d.OwnerReferences, d.Namespace))
}

// normalizeReplicaSet 归一化 ReplicaSet。
func normalizeReplicaSet(rs appsv1.ReplicaSet) model.WorkloadInfo {
	var conds []model.ConditionInfo
	for _, c := range rs.Status.Conditions {
		conds = append(conds, conditionInfo(string(c.Type), string(c.Status), c.Reason, c.Message))
	}
	return workloadInfo("ReplicaSet", rs.Namespace, rs.Name, string(rs.UID),
		rs.Spec.Replicas, int32p(rs.Status.ReadyReplicas), int32p(rs.Status.AvailableReplicas), nil,
		conds, labelSelector(rs.Spec.Selector), ownerRefs(rs.OwnerReferences, rs.Namespace))
}

// normalizeStatefulSet 归一化 StatefulSet。
func normalizeStatefulSet(sts appsv1.StatefulSet) model.WorkloadInfo {
	var conds []model.ConditionInfo
	for _, c := range sts.Status.Conditions {
		conds = append(conds, conditionInfo(string(c.Type), string(c.Status), c.Reason, c.Message))
	}
	return workloadInfo("StatefulSet", sts.Namespace, sts.Name, string(sts.UID),
		sts.Spec.Replicas, int32p(sts.Status.ReadyReplicas), int32p(sts.Status.AvailableReplicas), int32p(sts.Status.UpdatedReplicas),
		conds, labelSelector(sts.Spec.Selector), ownerRefs(sts.OwnerReferences, sts.Namespace))
}

// normalizeDaemonSet 归一化 DaemonSet（期望数来自 status）。
func normalizeDaemonSet(ds appsv1.DaemonSet) model.WorkloadInfo {
	var conds []model.ConditionInfo
	for _, c := range ds.Status.Conditions {
		conds = append(conds, conditionInfo(string(c.Type), string(c.Status), c.Reason, c.Message))
	}
	return workloadInfo("DaemonSet", ds.Namespace, ds.Name, string(ds.UID),
		int32p(ds.Status.DesiredNumberScheduled), int32p(ds.Status.NumberReady), int32p(ds.Status.NumberAvailable), int32p(ds.Status.UpdatedNumberScheduled),
		conds, labelSelector(ds.Spec.Selector), ownerRefs(ds.OwnerReferences, ds.Namespace))
}

// normalizeJob 归一化 Job；完成/失败状态由 conditions 表达。
func normalizeJob(j batchv1.Job) model.WorkloadInfo {
	var conds []model.ConditionInfo
	for _, c := range j.Status.Conditions {
		conds = append(conds, conditionInfo(string(c.Type), string(c.Status), c.Reason, c.Message))
	}
	return workloadInfo("Job", j.Namespace, j.Name, string(j.UID), j.Spec.Parallelism, nil, nil, nil, conds, labelSelector(j.Spec.Selector), ownerRefs(j.OwnerReferences, j.Namespace))
}

// normalizeCronJob 归一化 CronJob。
// normalizeCronJob 归一化 CronJob（当前 client-go 版本无 status.conditions 字段）。
func normalizeCronJob(cj batchv1.CronJob) model.WorkloadInfo {
	return workloadInfo("CronJob", cj.Namespace, cj.Name, string(cj.UID), nil, nil, nil, nil, nil, labelSelector(cj.Spec.JobTemplate.Spec.Selector), ownerRefs(cj.OwnerReferences, cj.Namespace))
}

// normalizePVC 归一化 PVC 状态，并保留绑定 PV 与 StorageClass 关联（P3.2）。
func normalizePVC(p corev1.PersistentVolumeClaim) model.StorageInfo {
	info := model.StorageInfo{
		Ref:        ref("PVC", p.Namespace, p.Name, string(p.UID)),
		Kind:       "PVC",
		Status:     string(p.Status.Phase),
		VolumeName: p.Spec.VolumeName, // 绑定的 PV 名
	}
	if p.Spec.StorageClassName != nil {
		info.StorageClassName = *p.Spec.StorageClassName
	}
	if len(p.Status.Conditions) > 0 {
		info.Reason = p.Status.Conditions[0].Reason
	}
	info.Capacity = firstResourceString(p.Status.Capacity)
	return info
}

// normalizePV 归一化 PV 状态，并保留 StorageClass 关联（P3.2）。
func normalizePV(p corev1.PersistentVolume) model.StorageInfo {
	info := model.StorageInfo{
		Ref:              ref("PV", "", p.Name, string(p.UID)),
		Kind:             "PV",
		Status:           string(p.Status.Phase),
		Reason:           p.Status.Reason,
		StorageClassName: p.Spec.StorageClassName,
	}
	info.Capacity = firstResourceString(p.Spec.Capacity)
	return info
}

// normalizeStorageClass 归一化 StorageClass（关注 provisioner）。
func normalizeStorageClass(sc storagev1.StorageClass) model.StorageInfo {
	return model.StorageInfo{Ref: ref("StorageClass", "", sc.Name, string(sc.UID)), Kind: "StorageClass", Status: "Present", Driver: sc.Provisioner}
}

// normalizeVolumeAttachment 归一化 VolumeAttachment（CSI 挂载状态）。
func normalizeVolumeAttachment(va storagev1.VolumeAttachment) model.StorageInfo {
	info := model.StorageInfo{Ref: ref("VolumeAttachment", "", va.Name, string(va.UID)), Kind: "VolumeAttachment", Driver: va.Spec.Attacher}
	if va.Status.Attached {
		info.Status = "Attached"
	} else {
		info.Status = "Detached"
	}
	if va.Status.AttachError != nil {
		info.Reason = va.Status.AttachError.Message
	}
	return info
}

func firstResourceString(list corev1.ResourceList) string {
	for _, q := range list {
		return q.String()
	}
	return ""
}

// normalizeIngress 提取 ingress 后端信息（FR-009）。
func normalizeIngress(ing networkingv1.Ingress) model.IngressInfo {
	info := model.IngressInfo{Ref: ref("Ingress", ing.Namespace, ing.Name, string(ing.UID))}
	for _, rule := range ing.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, p := range rule.HTTP.Paths {
			if p.Backend.Service == nil {
				continue
			}
			port := "?"
			if p.Backend.Service.Port.Number != 0 {
				port = strconv.Itoa(int(p.Backend.Service.Port.Number))
			} else if p.Backend.Service.Port.Name != "" {
				port = p.Backend.Service.Port.Name
			}
			info.Backends = append(info.Backends, fmt.Sprintf("%s/%s:%s", ing.Namespace, p.Backend.Service.Name, port))
		}
	}
	return info
}

// normalizeNetworkPolicy 归一化 NetworkPolicy 的 Pod 选择器。
func normalizeNetworkPolicy(np networkingv1.NetworkPolicy) model.NetworkPolicyInfo {
	return model.NetworkPolicyInfo{
		Ref:         ref("NetworkPolicy", np.Namespace, np.Name, string(np.UID)),
		PodSelector: labelSelector(&np.Spec.PodSelector),
	}
}

// normalizeEvent 归一化事件并保留 involvedObject 用于本地索引（ADR-010）。
func normalizeEvent(e corev1.Event) model.EventInfo {
	return model.EventInfo{
		Reason:         e.Reason,
		Message:        redactor.Redact(e.Message), // 事件消息不可信，采集边界脱敏（ADR-006）
		Type:           e.Type,
		Count:          e.Count,
		FirstTimestamp: e.FirstTimestamp.UTC().Format(time.RFC3339),
		LastTimestamp:  e.LastTimestamp.UTC().Format(time.RFC3339),
		InvolvedObject: model.ResourceRef{Kind: e.InvolvedObject.Kind, Namespace: e.InvolvedObject.Namespace, Name: e.InvolvedObject.Name, UID: string(e.InvolvedObject.UID)},
	}
}

// redactMap 对 map 的值按键名或内容脱敏（采集边界，ADR-006）。
func redactMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = redactor.RedactValueByKey(k, v)
	}
	return out
}

// ownerRefs 归一化 OwnerReferences 为 ResourceRef 列表（用于归属链解析，P3.1）。
func ownerRefs(refs []metav1.OwnerReference, namespace string) []model.ResourceRef {
	out := make([]model.ResourceRef, 0, len(refs))
	for _, o := range refs {
		out = append(out, ref(o.Kind, namespace, o.Name, string(o.UID)))
	}
	return out
}
