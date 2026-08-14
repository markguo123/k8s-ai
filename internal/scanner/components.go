package scanner

import (
	"fmt"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// componentMatcher 描述一个系统组件的 Pod 标签判定规则（FR-010）。
type componentMatcher struct {
	name  string
	match func(labels map[string]string) bool
}

// componentMatchers 覆盖规范关注的核心系统组件；不假设它们一定存在。
var componentMatchers = []componentMatcher{
	{name: "CoreDNS", match: labelAny(map[string][]string{
		"k8s-app":                {"kube-dns", "coredns"},
		"app.kubernetes.io/name": {"coredns"},
	})},
	{name: "kube-proxy", match: labelAny(map[string][]string{"k8s-app": {"kube-proxy"}})},
	{name: "CNI", match: labelAny(map[string][]string{
		"k8s-app": {"calico-node", "canal", "flannel", "cilium", "weave-net", "antrea-agent", "kube-router", "multus-daemon", "kindnet"},
	})},
	{name: "metrics-server", match: labelAny(map[string][]string{"k8s-app": {"metrics-server"}})},
	{name: "Ingress Controller", match: labelAny(map[string][]string{
		"k8s-app":                {"ingress-nginx", "nginx-ingress", "nginx-ingress-controller", "traefik"},
		"app.kubernetes.io/name": {"ingress-nginx"},
	})},
	{name: "CSI", match: func(labels map[string]string) bool {
		for _, v := range labels {
			if strings.Contains(v, "csi") {
				return true
			}
		}
		return false
	}},
}

// labelAny 任一 key 的值命中 values 即匹配。
func labelAny(want map[string][]string) func(map[string]string) bool {
	return func(labels map[string]string) bool {
		for key, values := range want {
			v, ok := labels[key]
			if !ok {
				continue
			}
			for _, wantV := range values {
				if v == wantV {
					return true
				}
			}
		}
		return false
	}
}

// detectComponents 基于 Phase1 的 Pod 标签动态发现系统组件（FR-010）。
// 缺失组件记为 Present=false，不视为错误。
func detectComponents(pods []model.PodInfo) []model.ComponentInfo {
	out := make([]model.ComponentInfo, 0, len(componentMatchers))
	for _, mc := range componentMatchers {
		comp := model.ComponentInfo{Name: mc.name, Present: false}
		var namespaces []string
		ready := false
		for _, p := range pods {
			if !mc.match(p.Labels) {
				continue
			}
			comp.Present = true
			namespaces = append(namespaces, p.Ref.Namespace)
			if p.Phase == "Running" && allContainersReady(p) {
				ready = true
			}
		}
		if comp.Present {
			comp.Namespace = firstNamespace(namespaces)
			comp.Ready = ready
			comp.Detail = fmt.Sprintf("pods=%d namespace=%s", len(namespaces), comp.Namespace)
		} else {
			comp.Detail = "未检测到"
		}
		out = append(out, comp)
	}
	return out
}

// allContainersReady 判断 Pod 所有容器是否 Ready。
func allContainersReady(p model.PodInfo) bool {
	if len(p.Containers) == 0 {
		return false
	}
	for _, c := range p.Containers {
		if !c.Ready {
			return false
		}
	}
	return true
}

func firstNamespace(namespaces []string) string {
	if len(namespaces) == 0 {
		return ""
	}
	return namespaces[0]
}
