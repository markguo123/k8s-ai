package deterministic_test

import (
	"github.com/k8s-ai/k8s-ai/internal/model"
)

func init() {
	crashPod := func(s *model.ClusterSnapshot, ns, name string, msgs ...string) {
		addPod(s, ns, name, func(p *model.PodInfo) {
			p.NodeName = "node-1"
			p.Containers = []model.ContainerInfo{{
				Name: "web", Image: "repo/web:1.2", State: "Waiting", Reason: "CrashLoopBackOff",
				RestartCount: 6, LastState: "Terminated", LastReason: "Error",
			}}
		})
		for _, m := range msgs {
			addEvent(s, s.Pods[len(s.Pods)-1].Ref, "Warning", "BackOff", m)
		}
	}

	scenarios = append(scenarios,
		scenario{
			name: "Service 无 Endpoint", id: "NETWORK_SERVICE_NO_ENDPOINT", rule: "ServiceNoEndpoint",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addService(s, "prod", "web", map[string]string{"app": "web"})
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName, p.CreationTimestamp = "node-1", oldTimestamp
					p.Labels = map[string]string{"app": "web"}
					p.Containers = []model.ContainerInfo{{Name: "web", State: "Waiting", Reason: "CrashLoopBackOff"}}
				})
				s.EndpointSlices = []model.EndpointSliceInfo{{
					Ref: objRef("EndpointSlice", "prod", "web-abc"), ServiceName: "web", Ready: 0, NotReady: 1,
				}}
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addService(s, "prod", "web", map[string]string{"app": "web"})
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Labels = map[string]string{"app": "web"}
					p.Containers = []model.ContainerInfo{{Name: "web", State: "Running", Ready: true}}
				})
				s.EndpointSlices = []model.EndpointSliceInfo{{
					Ref: objRef("EndpointSlice", "prod", "web-abc"), ServiceName: "web", Ready: 1,
				}}
				return s
			},
			// 滚动发布中匹配 Pod 全部是 2 分钟内新建的：端点还没就绪属正常暂态。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addService(s, "prod", "web", map[string]string{"app": "web"})
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.CreationTimestamp = "2020-01-01T00:00:00Z"
					p.Labels = map[string]string{"app": "web"}
				})
				// 把创建时间改成「刚刚」，模拟新 Pod 尚在就绪探针窗口内。
				s.Pods[0].CreationTimestamp = nowRFC3339()
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Services = nil },
			multiRootRule: "CrashLoopBackOff",
			multi: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addService(s, "prod", "web", map[string]string{"app": "web"})
				crashPod(s, "prod", "web-1", "Back-off restarting failed container")
				s.Pods[0].Labels = map[string]string{"app": "web"}
				s.EndpointSlices = []model.EndpointSliceInfo{{
					Ref: objRef("EndpointSlice", "prod", "web-abc"), ServiceName: "web", Ready: 0, NotReady: 1,
				}}
				return s
			},
		},
		scenario{
			name: "EndpointSlice 异常", id: "NETWORK_ENDPOINTSLICE_INCONSISTENT", rule: "ServiceNoEndpoint",
			fault: func() *model.ClusterSnapshot {
				// Pod 侧 ready=true，端点侧却是 NotReady：两处数据自相矛盾才是本场景。
				s := newSnapshot()
				nodeOK(s)
				addService(s, "prod", "api", map[string]string{"app": "api"})
				addPod(s, "prod", "api-1", func(p *model.PodInfo) {
					p.NodeName, p.CreationTimestamp = "node-1", oldTimestamp
					p.Labels = map[string]string{"app": "api"}
					p.Containers = []model.ContainerInfo{{Name: "api", State: "Running", Ready: true}}
				})
				s.EndpointSlices = []model.EndpointSliceInfo{{
					Ref: objRef("EndpointSlice", "prod", "api-abc"), ServiceName: "api",
					Ready: 0, NotReady: 1, Addresses: []string{"10.42.0.7"},
				}}
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addService(s, "prod", "api", map[string]string{"app": "api"})
				addPod(s, "prod", "api-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Labels = map[string]string{"app": "api"}
					p.Containers = []model.ContainerInfo{{Name: "api", State: "Running", Ready: true}}
				})
				s.EndpointSlices = []model.EndpointSliceInfo{{
					Ref: objRef("EndpointSlice", "prod", "api-abc"), ServiceName: "api", Ready: 1,
				}}
				return s
			},
			// 端点数与 Ready Pod 数对不上但只要有一个就绪端点，Service 就是通的：
			// 这条结论要求 Service 侧确实拿不到后端，否则只是脏数据不是故障。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addService(s, "prod", "api", map[string]string{"app": "api"})
				addPod(s, "prod", "api-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Labels = map[string]string{"app": "api"}
					p.Containers = []model.ContainerInfo{{Name: "api", State: "Running", Ready: true}}
				})
				s.EndpointSlices = []model.EndpointSliceInfo{{
					Ref: objRef("EndpointSlice", "prod", "api-abc"), ServiceName: "api", Ready: 1, NotReady: 1,
				}}
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Services = nil },
			multiRootRule: "ServiceNoEndpoint",
			multi: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addService(s, "prod", "api", map[string]string{"app": "api"})
				addPod(s, "prod", "api-1", func(p *model.PodInfo) {
					p.NodeName, p.CreationTimestamp = "node-1", oldTimestamp
					p.Labels = map[string]string{"app": "api"}
					p.Containers = []model.ContainerInfo{{Name: "api", State: "Running", Ready: true}}
				})
				s.EndpointSlices = []model.EndpointSliceInfo{{
					Ref: objRef("EndpointSlice", "prod", "api-abc"), ServiceName: "api", Ready: 0, NotReady: 1,
				}}
				addService(s, "prod", "web", map[string]string{"app": "web"})
				return s
			},
		},
		scenario{
			name: "Ingress 后端异常", id: "NETWORK_INGRESS_BACKEND_UNAVAILABLE", rule: "IngressBackend",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				s.Ingresses = []model.IngressInfo{{
					Ref:      objRef("Ingress", "prod", "web"),
					Backends: []string{"prod/web:80"},
					Rules:    []model.IngressRuleInfo{{Host: "web.example.com", Path: "/", Backend: "prod/web:80"}},
				}}
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addService(s, "prod", "web", map[string]string{"app": "web"})
				s.Ingresses = []model.IngressInfo{{
					Ref:      objRef("Ingress", "prod", "web"),
					Backends: []string{"prod/web:80"},
				}}
				return s
			},
			// 后端 Service 存在但一个就绪端点都没有：那是 Service 侧的故障，
			// 不能报成「Ingress 后端不存在」（处置动作完全不同）。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addService(s, "prod", "web", map[string]string{"app": "web"})
				s.Ingresses = []model.IngressInfo{{
					Ref:      objRef("Ingress", "prod", "web"),
					Backends: []string{"prod/web:80"},
				}}
				return s
			},
			drop:          func(s *model.ClusterSnapshot) { s.Ingresses = nil },
			multiRootRule: "ServiceNoEndpoint",
			multi: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addService(s, "prod", "web", map[string]string{"app": "web"})
				s.Ingresses = []model.IngressInfo{{
					Ref: objRef("Ingress", "prod", "web"),
					// prod/web 存在但没有就绪端点（ServiceNoEndpoint），
					// prod/gone 这个后端 Service 根本不存在（IngressBackend）：
					// 两个故障必须落进同一个 Incident，且根因留在 Service 侧。
					Backends: []string{"prod/web:80", "prod/gone:80"},
				}}
				return s
			},
		},
		scenario{
			// DNS 没有独立规则可挂：只读体检里 DNS 故障只能表现为「Pod 的日志/事件里
			// 出现解析失败」，因此用一个真实存在的 Pod 故障规则承载这条链。
			name: "DNS 异常", id: "PLATFORM_DNS_ABNORMAL", rule: "CrashLoopBackOff",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				crashPod(s, "prod", "web-1")
				addEvent(s, s.Pods[0].Ref, "Warning", "FailedToResolve",
					"dial tcp: lookup payments.prod.svc.cluster.local on 10.96.0.10:53: server misbehaving")
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
			// Pod 确实在 CrashLoop，但事件里没有任何解析失败信号：
			// 规则该报，DNS 诊断器不该跟着编一个 DNS 根因。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				crashPod(s, "prod", "web-1", "Back-off restarting failed container")
				addEvent(s, s.Pods[0].Ref, "Warning", "Unhealthy",
					"Readiness probe failed: dial tcp 10.42.0.7:8080: connect: connection refused")
				return s
			},
			boundaryHits:        true,
			boundaryNoDiagnoser: true,
			drop:                func(s *model.ClusterSnapshot) { s.Pods = nil; s.Components = nil },
		},
		scenario{
			// 同样是「策略基线」而不是「已证实拦截」：诊断器必须说不清就不下结论。
			name: "NetworkPolicy 潜在通信问题", id: "NETWORK_POLICY_POSTURE", rule: "NetworkPolicyNoDefaultDeny",
			fault: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Labels = map[string]string{"app": "web"}
					p.Containers = []model.ContainerInfo{{Name: "web", State: "Running", Ready: true}}
				})
				s.NetworkPolicies = []model.NetworkPolicyInfo{{
					Ref:         objRef("NetworkPolicy", "prod", "allow-web"),
					PodSelector: map[string]string{"app": "web"},
					PolicyTypes: []string{"Ingress"},
				}}
				return s
			},
			normal: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				addPod(s, "prod", "web-1", func(p *model.PodInfo) {
					p.NodeName = "node-1"
					p.Labels = map[string]string{"app": "web"}
					p.Containers = []model.ContainerInfo{{Name: "web", State: "Running", Ready: true}}
				})
				// 空 podSelector = 覆盖全命名空间的默认拒绝，基线已建立。
				s.NetworkPolicies = []model.NetworkPolicyInfo{{
					Ref:         objRef("NetworkPolicy", "prod", "default-deny"),
					PolicyTypes: []string{"Ingress"},
				}}
				return s
			},
			// 有策略但集群里一个 Pod 都没采到：没有对象可保护，报「缺默认拒绝」是噪音。
			boundary: func() *model.ClusterSnapshot {
				s := newSnapshot()
				nodeOK(s)
				s.NetworkPolicies = []model.NetworkPolicyInfo{{
					Ref:         objRef("NetworkPolicy", "prod", "allow-web"),
					PodSelector: map[string]string{"app": "web"},
					PolicyTypes: []string{"Ingress"},
				}}
				return s
			},
			drop: func(s *model.ClusterSnapshot) { s.NetworkPolicies = nil },
		},
	)
}
