package manifestx

import (
	"fmt"
)

// buildObject 组装单个对象：身份字段 → 映射与 scope → 命名空间 → 门禁 →
// 静态检查 → Secret 指纹 → 指纹与展示 manifest。
func buildObject(index int, content map[string]any, opts Options) Object {
	o := Object{Index: index, content: content, Scope: ScopeUnknown}
	o.APIVersion = stringAt(content, "apiVersion")
	o.Kind = stringAt(content, "kind")
	if meta, ok := content["metadata"].(map[string]any); ok {
		o.Name = stringAt(meta, "name")
		o.Namespace = stringAt(meta, "namespace")
	}

	var findings []Finding
	findings = append(findings, validateRequired(&o)...)
	findings = append(findings, resolveMapping(&o, opts.Mapper)...)
	findings = append(findings, applyNamespace(&o, opts.DefaultNamespace)...)
	findings = append(findings, opts.Policy.check(&o)...)
	findings = append(findings, scanImages(&o)...)
	codes, dangerFindings := scanDangerous(content)
	findings = append(findings, dangerFindings...)
	o.Dangerous = codes

	keys, secretFindings := inspectSecrets(&o)
	findings = append(findings, secretFindings...)
	o.SecretKeys = keys
	o.Findings = findings
	o.Fingerprint = Fingerprint(content)
	o.Manifest = DisplayManifest(content, keys)
	return o
}

// resolveMapping 解析 GVK→GVR 与 scope。
//
// 映射失败按拒绝处理：没有 GVR 就无法用 dynamic client 提交，
// 而集群里已安装的 CRD 能通过 discovery 正常映射，因此不影响"不限制 CRD"（D-11）。
func resolveMapping(o *Object, mapper Mapper) []Finding {
	o.GVK = ParseGVK(o.APIVersion, o.Kind)
	if o.Kind == "" || o.APIVersion == "" {
		return nil // 必填字段缺失已经报错，映射判定无意义
	}
	if mapper == nil {
		// 离线校验（仅单测使用）：只按内置表判定 cluster-scoped，其余保持 unknown。
		o.Scope = staticScope(o.GVK)
		return nil
	}
	m, err := mapper.MappingFor(o.GVK)
	if err != nil {
		o.UnknownKind = true
		o.Scope = staticScope(o.GVK)
		return []Finding{{Code: CodeUnmappedKind, Severity: SeverityError, Field: "apiVersion",
			Message: fmt.Sprintf("集群中找不到类型 %s：%v", o.GVK.String(), err),
			Fix:     "确认 apiVersion/kind 拼写；若为 CR，请先安装对应 CRD"}}
	}
	o.Mapped = true
	o.GVR = m.Resource
	o.Scope = m.Scope
	return nil
}
