package manifestx

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
)

// PlaceholderGenerate 是 Secret 值占位符：值由 Executor 在提交前生成随机内容，
// 模型编写的 manifest 里只出现占位符（三期 D-06/D-12，create_only 通道）。
//
// 选型约束：这个串会由模型直接写进 YAML 且通常不带引号，因此它不能以 YAML
// 指示符开头（@ ! % & * ` 等会让整段清单解析失败，"$GENERATE" 是安全的）。
const PlaceholderGenerate = "$GENERATE"

// secretValueSource 是 Secret key 的值来源。
const (
	SecretSourceStringData = "stringData"
	SecretSourceData       = "data"
	SecretSourceGenerate   = "generate"
)

// inspectSecrets 检测 Secret 的 data / stringData：
// 产出 key 名、值长度与 SHA-256 指纹（值本身不外流），并校验值来源白名单。
func inspectSecrets(o *Object) ([]SecretKeyFingerprint, []Finding) {
	if !o.IsSecret() {
		return nil, nil
	}
	var keys []SecretKeyFingerprint
	var findings []Finding

	if raw, ok := o.content["stringData"].(map[string]any); ok {
		for _, name := range sortedKeys(raw) {
			value, isString := raw[name].(string)
			if !isString {
				findings = append(findings, Finding{Code: CodeSecretInvalidData, Severity: SeverityError,
					Field:   "stringData." + name,
					Message: "stringData 的值必须是字符串", Fix: "把值写成字符串，或使用 " + PlaceholderGenerate})
				continue
			}
			if value == PlaceholderGenerate {
				keys = append(keys, SecretKeyFingerprint{Key: name, Source: SecretSourceGenerate, Generated: true})
				continue
			}
			keys = append(keys, fingerprintOf(name, []byte(value), SecretSourceStringData))
			findings = append(findings, literalValueFinding("stringData."+name, o.Kind))
		}
	}
	if raw, ok := o.content["data"].(map[string]any); ok {
		for _, name := range sortedKeys(raw) {
			value, isString := raw[name].(string)
			if !isString {
				findings = append(findings, Finding{Code: CodeSecretInvalidData, Severity: SeverityError,
					Field: "data." + name, Message: "data 的值必须是 base64 字符串", Fix: "改用 stringData 或 " + PlaceholderGenerate})
				continue
			}
			if value == PlaceholderGenerate {
				findings = append(findings, Finding{Code: CodeSecretBadDataPlaceh, Severity: SeverityError,
					Field:   "data." + name,
					Message: PlaceholderGenerate + " 只能用在 stringData，data 需要 base64 编码",
					Fix:     "把该 key 移到 stringData 并填 " + PlaceholderGenerate})
				continue
			}
			decoded, err := base64.StdEncoding.DecodeString(value)
			if err != nil {
				findings = append(findings, Finding{Code: CodeSecretInvalidData, Severity: SeverityError,
					Field: "data." + name, Message: fmt.Sprintf("data.%s 不是合法 base64：%v", name, err),
					Fix: "改用 stringData（无需编码）或 " + PlaceholderGenerate})
				continue
			}
			keys = append(keys, fingerprintOf(name, decoded, SecretSourceData))
			findings = append(findings, literalValueFinding("data."+name, o.Kind))
		}
	}
	if len(keys) == 0 && len(findings) == 0 {
		findings = append(findings, Finding{Code: CodeSecretNoKeys, Severity: SeverityWarn, Field: "data",
			Message: "Secret 未定义任何 key", Fix: "在 stringData 里为每个 key 填 " + PlaceholderGenerate})
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Key < keys[j].Key })
	return keys, findings
}

// literalValueFinding 拒绝模型直接写进 manifest 的 Secret 明文值：
// 值必须来自用户审批载荷或 Executor 生成，否则会进入 LLM 上下文、会话与审计。
func literalValueFinding(field, kind string) Finding {
	return Finding{Code: CodeSecretLiteralValue, Severity: SeverityError, Field: field,
		Message: fmt.Sprintf("%s 的 %s 含明文值；%s 的创建不接受模型编写的 Secret 值", kind, field, kind),
		Fix:     fmt.Sprintf("把该值改为 %s（由 Executor 生成随机值），或让用户在审批环节提供", PlaceholderGenerate)}
}

// fingerprintOf 计算 key 的脱敏指纹。
func fingerprintOf(key string, value []byte, source string) SecretKeyFingerprint {
	sum := sha256.Sum256(value)
	return SecretKeyFingerprint{Key: key, Length: len(value), SHA256: hex.EncodeToString(sum[:]), Source: source}
}

// redactSecret 返回深拷贝并把 data / stringData 的值替换为脱敏标记（提案与展示用）。
func redactSecret(content map[string]any, keys []SecretKeyFingerprint) map[string]any {
	out := deepCopyMap(content)
	byKey := make(map[string]SecretKeyFingerprint, len(keys))
	for _, k := range keys {
		byKey[k.Key] = k
	}
	for _, section := range []string{"stringData", "data"} {
		raw, ok := out[section].(map[string]any)
		if !ok {
			continue
		}
		for name, value := range raw {
			if s, isString := value.(string); isString && s == PlaceholderGenerate {
				continue
			}
			raw[name] = redactedMark(byKey[name])
		}
	}
	return out
}

// redactedMark 是脱敏后的展示值（保留长度与指纹，便于人工核对）。
func redactedMark(k SecretKeyFingerprint) string {
	if k.Key == "" {
		return "<redacted>"
	}
	if k.Generated {
		return PlaceholderGenerate + "（Executor 生成随机值）"
	}
	return fmt.Sprintf("<redacted length=%d sha256=%s>", k.Length, shortHash(k.SHA256))
}

func shortHash(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func deepCopyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return deepCopyMap(t)
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = deepCopyValue(item)
		}
		return out
	default:
		return v
	}
}
