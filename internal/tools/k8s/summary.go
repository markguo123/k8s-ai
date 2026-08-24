package k8s

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func derefI32(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

// summarizePod 提取 Pod 关键状态（容器状态/重启/条件），供 LLM 分析。
func summarizePod(p *corev1.Pod) map[string]any {
	var totalRestarts int32
	containers := make([]map[string]any, 0, len(p.Status.ContainerStatuses))
	for _, cs := range p.Status.ContainerStatuses {
		totalRestarts += cs.RestartCount
		lastExit := int32(0)
		if lt := cs.LastTerminationState.Terminated; lt != nil {
			lastExit = lt.ExitCode
		}
		containers = append(containers, map[string]any{
			"name":         cs.Name,
			"ready":        cs.Ready,
			"restartCount": cs.RestartCount,
			"image":        cs.Image,
			"state":        containerState(cs.State),
			"lastState":    containerState(cs.LastTerminationState),
			"lastExitCode": lastExit,
		})
	}
	return map[string]any{
		"kind":         "Pod",
		"namespace":    p.Namespace,
		"name":         p.Name,
		"phase":        p.Status.Phase,
		"nodeName":     p.Spec.NodeName,
		"podIP":        p.Status.PodIP,
		"startTime":    p.Status.StartTime,
		"restartCount": totalRestarts,
		"containers":   containers,
		"conditions":   podConditions(p.Status.Conditions),
		"qosClass":     p.Status.QOSClass,
	}
}

func containerState(s corev1.ContainerState) string {
	switch {
	case s.Running != nil:
		return "running"
	case s.Waiting != nil:
		return "waiting:" + s.Waiting.Reason
	case s.Terminated != nil:
		return fmt.Sprintf("terminated:%s(exit=%d)", s.Terminated.Reason, s.Terminated.ExitCode)
	default:
		return "unknown"
	}
}

func podConditions(items []corev1.PodCondition) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, c := range items {
		out = append(out, map[string]any{"type": c.Type, "status": c.Status, "reason": c.Reason, "message": c.Message})
	}
	return out
}

func summarizePodListItem(p corev1.Pod) map[string]any {
	var restarts int32
	ready := 0
	for _, cs := range p.Status.ContainerStatuses {
		restarts += cs.RestartCount
		if cs.Ready {
			ready++
		}
	}
	return map[string]any{
		"namespace": p.Namespace, "name": p.Name, "phase": p.Status.Phase, "nodeName": p.Spec.NodeName,
		"restartCount": restarts, "readyContainers": ready, "totalContainers": len(p.Status.ContainerStatuses),
	}
}

func summarizeNode(n *corev1.Node) map[string]any {
	conds := make([]map[string]any, 0, len(n.Status.Conditions))
	for _, c := range n.Status.Conditions {
		conds = append(conds, map[string]any{"type": c.Type, "status": c.Status, "reason": c.Reason, "message": c.Message})
	}
	addrs := make([]map[string]any, 0, len(n.Status.Addresses))
	for _, a := range n.Status.Addresses {
		addrs = append(addrs, map[string]any{"type": a.Type, "address": a.Address})
	}
	taints := make([]map[string]any, 0, len(n.Spec.Taints))
	for _, t := range n.Spec.Taints {
		taints = append(taints, map[string]any{"key": t.Key, "effect": t.Effect, "value": t.Value})
	}
	return map[string]any{
		"kind": "Node", "name": n.Name,
		"unschedulable": n.Spec.Unschedulable,
		"conditions":    conds,
		"addresses":     addrs,
		"capacity":      n.Status.Capacity,
		"allocatable":   n.Status.Allocatable,
		"taints":        taints,
	}
}

func summarizeNodeListItem(n corev1.Node) map[string]any {
	ready := false
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			ready = c.Status == corev1.ConditionTrue
		}
	}
	return map[string]any{"name": n.Name, "ready": ready, "unschedulable": n.Spec.Unschedulable}
}

// workloadBase 组装 Workload 的通用状态字段。
// replicas 来自 spec（指针），ready/updated/available 来自 status（值）。
func workloadBase(kind, ns, name string, replicas *int32, ready, updated, available int32) map[string]any {
	return map[string]any{
		"kind": kind, "namespace": ns, "name": name,
		"replicas": derefI32(replicas), "readyReplicas": ready,
		"updatedReplicas": updated, "availableReplicas": available,
	}
}

func containerSpecs(containers []corev1.Container) []map[string]any {
	out := make([]map[string]any, 0, len(containers))
	for _, c := range containers {
		env := make([]string, 0, len(c.Env))
		for _, e := range c.Env {
			env = append(env, e.Name) // 只列 env 名，值可能含敏感信息
		}
		envFrom := make([]string, 0, len(c.EnvFrom))
		for _, ef := range c.EnvFrom {
			if ef.ConfigMapRef != nil {
				envFrom = append(envFrom, "configmap:"+ef.ConfigMapRef.Name)
			}
			if ef.SecretRef != nil {
				envFrom = append(envFrom, "secret:"+ef.SecretRef.Name)
			}
		}
		out = append(out, map[string]any{
			"name": c.Name, "image": c.Image,
			"resources": map[string]any{"requests": c.Resources.Requests, "limits": c.Resources.Limits},
			"env":       env, "envFrom": envFrom,
		})
	}
	return out
}

func deploymentConditions(items []appsv1.DeploymentCondition) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, c := range items {
		out = append(out, map[string]any{"type": c.Type, "status": c.Status, "reason": c.Reason, "message": c.Message})
	}
	return out
}

func summarizeService(s *corev1.Service) map[string]any {
	ports := make([]map[string]any, 0, len(s.Spec.Ports))
	for _, p := range s.Spec.Ports {
		ports = append(ports, map[string]any{"name": p.Name, "port": p.Port, "targetPort": p.TargetPort.String(), "protocol": p.Protocol, "nodePort": p.NodePort})
	}
	return map[string]any{
		"kind": "Service", "namespace": s.Namespace, "name": s.Name,
		"type": s.Spec.Type, "clusterIP": s.Spec.ClusterIP, "clusterIPs": s.Spec.ClusterIPs,
		"selector": s.Spec.Selector, "ports": ports,
		"externalIPs": s.Spec.ExternalIPs, "sessionAffinity": s.Spec.SessionAffinity,
	}
}

func summarizeEndpoints(e *corev1.Endpoints) map[string]any {
	subsets := make([]map[string]any, 0, len(e.Subsets))
	for _, ss := range e.Subsets {
		addrs := make([]string, 0, len(ss.Addresses))
		for _, a := range ss.Addresses {
			addrs = append(addrs, a.IP)
		}
		ports := make([]map[string]any, 0, len(ss.Ports))
		for _, p := range ss.Ports {
			ports = append(ports, map[string]any{"name": p.Name, "port": p.Port, "protocol": p.Protocol})
		}
		subsets = append(subsets, map[string]any{"addresses": addrs, "ports": ports})
	}
	return map[string]any{
		"kind": "Endpoints", "namespace": e.Namespace, "name": e.Name,
		"subsets": subsets, "endpointCount": len(subsets),
	}
}

func summarizeEndpointSlice(es discoveryv1.EndpointSlice) map[string]any {
	eps := make([]map[string]any, 0, len(es.Endpoints))
	for _, ep := range es.Endpoints {
		ready := false
		if ep.Conditions.Ready != nil {
			ready = *ep.Conditions.Ready
		}
		eps = append(eps, map[string]any{"addresses": ep.Addresses, "ready": ready, "nodeName": ep.NodeName, "hostname": ep.Hostname})
	}
	ports := make([]map[string]any, 0, len(es.Ports))
	for _, p := range es.Ports {
		var port any
		if p.Port != nil {
			port = *p.Port
		}
		ports = append(ports, map[string]any{"name": p.Name, "port": port, "protocol": p.Protocol})
	}
	return map[string]any{
		"name": es.Name, "addressType": es.AddressType,
		"endpoints": eps, "ports": ports,
		"labels": es.Labels,
	}
}

func summarizePVC(p *corev1.PersistentVolumeClaim) map[string]any {
	return map[string]any{
		"kind": "PersistentVolumeClaim", "namespace": p.Namespace, "name": p.Name,
		"phase":        string(p.Status.Phase),
		"capacity":     p.Status.Capacity,
		"requested":    p.Spec.Resources.Requests,
		"storageClass": derefStr(p.Spec.StorageClassName),
		"volumeName":   p.Spec.VolumeName,
		"accessModes":  p.Spec.AccessModes,
		"volumeMode":   derefVolumeMode(p.Spec.VolumeMode),
	}
}

func summarizePV(p *corev1.PersistentVolume) map[string]any {
	claim := ""
	if p.Spec.ClaimRef != nil {
		claim = p.Spec.ClaimRef.Namespace + "/" + p.Spec.ClaimRef.Name
	}
	return map[string]any{
		"kind": "PersistentVolume", "name": p.Name,
		"phase": string(p.Status.Phase), "capacity": p.Spec.Capacity,
		"storageClass": p.Spec.StorageClassName, "accessModes": p.Spec.AccessModes,
		"reclaimPolicy": p.Spec.PersistentVolumeReclaimPolicy, "claimRef": claim,
	}
}

func summarizeStorageClass(sc *storagev1.StorageClass) map[string]any {
	return map[string]any{
		"kind": "StorageClass", "name": sc.Name,
		"provisioner": sc.Provisioner, "reclaimPolicy": sc.ReclaimPolicy,
		"volumeBindingMode": sc.VolumeBindingMode, "allowVolumeExpansion": derefBool(sc.AllowVolumeExpansion),
	}
}

func summarizeNamespace(ns *corev1.Namespace) map[string]any {
	return map[string]any{
		"kind": "Namespace", "name": ns.Name, "phase": string(ns.Status.Phase),
		"labels": ns.Labels, "annotations": ns.Annotations,
	}
}

func summarizeEvent(e corev1.Event) map[string]any {
	return map[string]any{
		"namespace":     e.Namespace,
		"kind":          e.InvolvedObject.Kind,
		"name":          e.InvolvedObject.Name,
		"type":          e.Type,
		"reason":        e.Reason,
		"message":       e.Message,
		"count":         e.Count,
		"lastTimestamp": e.LastTimestamp,
		"source":        e.Source.Component,
	}
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefVolumeMode(p *corev1.PersistentVolumeMode) string {
	if p == nil {
		return ""
	}
	return string(*p)
}

func derefBool(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

func timeString(t *metav1.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}
