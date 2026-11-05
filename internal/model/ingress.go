package model

import "strings"

// BackendRefs 把采集器归一化后的后端串解析成 Service 引用。
//
// 采集器写入 Backends 的格式是 "ns/svc:port"，历史数据里也允许 "svc:port"
// （按 Ingress 自身命名空间解释）。这里集中一份解析逻辑，规则引擎、关联索引、
// 故障聚合三处都用它，避免同一个 Ingress 在三层里被解析成不同的后端。
// 端口不参与资源标识，因此解析结果只含 Namespace 与 Name。
func (i IngressInfo) BackendRefs() []ResourceRef {
	var out []ResourceRef
	seen := map[string]bool{}
	for _, be := range i.Backends {
		ref, ok := parseServiceBackend(be, i.Ref.Namespace)
		if !ok || seen[ref.Key()] {
			continue
		}
		seen[ref.Key()] = true
		out = append(out, ref)
	}
	return out
}

// parseServiceBackend 解析 "ns/svc:port" 或 "svc:port"（后者落在 defaultNS）。
func parseServiceBackend(backend, defaultNS string) (ResourceRef, bool) {
	rest := backend
	if idx := strings.LastIndex(rest, ":"); idx >= 0 {
		rest = rest[:idx]
	}
	ns, name := defaultNS, rest
	if slash := strings.LastIndex(rest, "/"); slash >= 0 {
		ns, name = rest[:slash], rest[slash+1:]
	}
	if name == "" {
		return ResourceRef{}, false
	}
	return ResourceRef{Kind: "Service", Namespace: ns, Name: name}, true
}
