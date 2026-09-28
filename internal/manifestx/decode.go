package manifestx

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// DecodeDocuments 把一段 manifest 解析成若干对象文档（JSON 是 YAML 子集，同样适用）。
//
// 容错：空文档（只有 --- 或只有注释）直接跳过；数字统一成 int64，
// 映射键统一成 string，保证后续 canonical 序列化与 unstructured 转换稳定。
func DecodeDocuments(manifest string) ([]map[string]any, error) {
	dec := yaml.NewDecoder(strings.NewReader(manifest))
	docs := make([]map[string]any, 0, 4)
	for {
		var raw any
		err := dec.Decode(&raw)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("第 %d 个文档解析失败：%w", len(docs)+1, err)
		}
		if raw == nil {
			continue
		}
		obj, ok := normalize(raw).(map[string]any)
		if !ok {
			return nil, fmt.Errorf("第 %d 个文档不是对象；每个文档必须是一个 Kubernetes 资源（apiVersion/kind/metadata）", len(docs)+1)
		}
		docs = append(docs, obj)
	}
	return docs, nil
}

// normalize 递归归一化 yaml.v3 的解码结果：map[any]any→map[string]any、int→int64。
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalize(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = normalize(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalize(val)
		}
		return out
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case uint64:
		return int64(t)
	default:
		return v
	}
}

// templateProblem 检测未渲染的模板（helm/kustomize/go template）。
// 这类内容不是可提交的 Kubernetes 对象，必须整段拒绝而不是逐对象报错。
func templateProblem(manifest string) (string, bool) {
	for i, line := range strings.Split(manifest, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if templateExprLine(line) {
			return fmt.Sprintf("manifest 第 %d 行含模板表达式（%s）；请先渲染出最终 YAML 再创建", i+1, firstToken(trimmed)), true
		}
		if strings.Contains(line, "kustomize.config.k8s.io") ||
			strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(trimmed, "kind:")), "Kustomization") {
			return fmt.Sprintf("manifest 第 %d 行是 kustomize 输入；请提交渲染后的资源清单（kustomize build 输出）", i+1), true
		}
	}
	return "", false
}

// templateKeywords 是 go-template / helm / sprig 动作的常见起始词。
var templateKeywords = map[string]bool{
	"if": true, "else": true, "end": true, "range": true, "with": true,
	"template": true, "define": true, "block": true, "call": true,
	"print": true, "printf": true, "println": true, "len": true,
	"index": true, "slice": true, "env": true, "include": true,
	"required": true, "fail": true, "lookup": true, "tpl": true,
	"toYaml": true, "toJson": true, "indent": true, "nindent": true,
	"quote": true, "squote": true, "default": true, "list": true,
	"dict": true, "get": true, "set": true, "hasKey": true,
	"b64enc": true, "sha256sum": true, "uuidv4": true, "kindOf": true,
}

// templateExprLine 判断一行里是否有未渲染的模板动作：只认 "{{" 起始且动作体以
// "."、"$" 或模板关键字开头（如 "{{ .Values.name }}"、"{{- if .x }}"）。
// JSON 清单里连续出现的右花括号（"metadata":{"name":"c"}}）不是模板，不能误判。
func templateExprLine(line string) bool {
	for i := 0; i+1 < len(line); i++ {
		if line[i] != '{' || line[i+1] != '{' {
			continue
		}
		body := strings.TrimLeft(line[i+2:], " +-")
		if body == "" {
			continue
		}
		if body[0] == '.' || body[0] == '$' {
			return true
		}
		word := body
		for j := 0; j < len(word); j++ {
			c := word[j]
			if c != '-' && c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
				word = word[:j]
				break
			}
		}
		if templateKeywords[word] {
			return true
		}
	}
	return false
}

// firstToken 取一行里适合回显的前 60 个字符（避免把整段模板塞进错误信息）。
func firstToken(s string) string {
	if len(s) > 60 {
		return s[:60] + "..."
	}
	return s
}
