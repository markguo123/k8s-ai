package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/k8s-ai/k8s-ai/internal/executor"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

const (
	maxSecretKeysPerRead = 5
	maxSecretValueBytes  = 16 << 10
	maxSecretTotalBytes  = 32 << 10
)

// SecretValuesReader 是按 key 读取 Secret 的只读门面（internal/kubernetes.Client 实现）。
// 该接口只应注册为审批后的 get_secret_value 工具，不能用于巡检批量采集。
type SecretValuesReader interface {
	GetSecretValues(ctx context.Context, namespace, name string, keys []string) (map[string][]byte, error)
}

type secretValueTool struct {
	reader SecretValuesReader
}

type secretValueArgs struct {
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Keys      []string `json:"keys"`
	Reason    string   `json:"reason"`
}

type modelSecretValue struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Encoding string `json:"encoding"`
}

func (t *secretValueTool) Name() string { return "get_secret_value" }

func (t *secretValueTool) Description() string {
	return "按 key 读取一个 Kubernetes Secret 的内容，仅用于已经确认与 Secret 配置有关的故障诊断。" +
		"必须填写具体原因，每次调用都会请求用户审批；返回值只进入当前一轮模型上下文，" +
		"不会写入会话、审计、SSE 或最终回答。禁止一次读取整个 Secret，单次最多 5 个 key。"
}

func (t *secretValueTool) SchemaMetadata() string {
	return "[SENSITIVE READ][RISK: HIGH][APPROVAL REQUIRED: YES]"
}

func (t *secretValueTool) InputSchema() any {
	return objSchema(map[string]any{
		"namespace": strProp("Secret 所在命名空间"),
		"name":      strProp("Secret 名称"),
		"keys": map[string]any{
			"type":        "array",
			"description": "需要读取的 Secret key，单次最多 5 个",
			"items":       map[string]any{"type": "string"},
		},
		"reason": strProp("为什么必须查看该值，以及该值将如何帮助定位当前故障"),
	}, "namespace", "name", "keys", "reason")
}

func (t *secretValueTool) IsReadOnly(json.RawMessage) bool { return false }
func (t *secretValueTool) Risk(json.RawMessage) model.RiskLevel {
	return model.RiskHigh
}
func (t *secretValueTool) RequiresHumanApproval(json.RawMessage) bool { return true }

func (t *secretValueTool) PreviewApproval(input json.RawMessage) tools.ApprovalPlan {
	args, err := parseSecretValueArgs(input)
	if err != nil {
		return tools.ApprovalPlan{Tool: t.Name(), Title: "读取 Secret 值", Summary: "参数无法解析：" + err.Error()}
	}
	return tools.ApprovalPlan{
		Title:     "读取 Secret 值",
		Tool:      t.Name(),
		Operation: "secret.read",
		Risk:      string(model.RiskHigh),
		Target: tools.ApprovalTarget{
			Namespace: args.Namespace,
			Kind:      "Secret",
			Name:      args.Name,
		},
		Summary: fmt.Sprintf("读取 key=%s；原因：%s", strings.Join(args.Keys, ", "), args.Reason),
		Checks: []tools.ApprovalCheck{{
			Name:    "敏感数据",
			Status:  "warning",
			Message: "明文仅进入当前轮模型上下文；UI、SSE、会话、数据库与审计只保留脱敏指纹",
		}},
	}
}

func (t *secretValueTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := parseSecretValueArgs(input)
	if err != nil {
		return tools.ResultErrorWithCode(tools.ErrorInvalidArgument, err.Error()), nil
	}
	if t.reader == nil {
		return tools.ResultErrorWithCode(tools.ErrorToolUnavailable, "Secret 按需读取通道未装配"), nil
	}
	values, err := t.reader.GetSecretValues(ctx, args.Namespace, args.Name, args.Keys)
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	total := 0
	for _, key := range args.Keys {
		if len(values[key]) > maxSecretValueBytes {
			return tools.ResultErrorWithCode(tools.ErrorInvalidArgument,
				fmt.Sprintf("Secret %s/%s key %q is %d bytes; diagnostic reads are capped at %d bytes per key", args.Namespace, args.Name, key, len(values[key]), maxSecretValueBytes)), nil
		}
		total += len(values[key])
	}
	if total > maxSecretTotalBytes {
		return tools.ResultErrorWithCode(tools.ErrorInvalidArgument,
			fmt.Sprintf("Secret %s/%s requested keys total %d bytes; diagnostic reads are capped at %d bytes per call", args.Namespace, args.Name, total, maxSecretTotalBytes)), nil
	}

	modelValues := make([]modelSecretValue, 0, len(args.Keys))
	items := make([]map[string]any, 0, len(args.Keys))
	statuses := make([]executor.SecretKeyStatus, 0, len(args.Keys))
	sensitive := make([]string, 0, len(args.Keys)*2)
	for _, key := range args.Keys {
		raw := values[key]
		modelValue, encoding, variants := secretValueForModel(raw)
		modelValues = append(modelValues, modelSecretValue{Key: key, Value: modelValue, Encoding: encoding})
		sensitive = append(sensitive, variants...)
		sum := sha256.Sum256(raw)
		digest := hex.EncodeToString(sum[:])
		items = append(items, map[string]any{
			"key":      key,
			"length":   len(raw),
			"sha256":   digest,
			"encoding": encoding,
			"value":    "[REDACTED]",
		})
		statuses = append(statuses, executor.SecretKeyStatus{
			Key: key, Length: len(raw), SHA256: digest, Source: "read_on_demand",
		})
	}

	data := map[string]any{
		"kind":      "SecretValueResult",
		"namespace": args.Namespace,
		"name":      args.Name,
		"items":     items,
		tools.DataSecretTouches: []tools.SecretTouch{{
			Kind: "Secret", Namespace: args.Namespace, Name: args.Name, Keys: statuses,
		}},
	}
	return tools.ToolResult{
		Data: data,
		Text: fmt.Sprintf("已读取 Secret %s/%s 的 %d 个 key；明文仅当前轮模型可见", args.Namespace, args.Name, len(items)),
		ModelOnly: map[string]any{
			"namespace": args.Namespace,
			"name":      args.Name,
			"values":    modelValues,
		},
		SensitiveValues: sensitive,
	}, nil
}

func parseSecretValueArgs(input json.RawMessage) (secretValueArgs, error) {
	var args secretValueArgs
	if err := json.Unmarshal(input, &args); err != nil {
		return secretValueArgs{}, fmt.Errorf("invalid arguments: %w", err)
	}
	args.Namespace = strings.TrimSpace(args.Namespace)
	args.Name = strings.TrimSpace(args.Name)
	args.Reason = strings.TrimSpace(args.Reason)
	if args.Namespace == "" || args.Name == "" {
		return secretValueArgs{}, fmt.Errorf("namespace and name are required")
	}
	if args.Reason == "" {
		return secretValueArgs{}, fmt.Errorf("reason is required so the user can decide whether to reveal the value")
	}
	seen := make(map[string]bool, len(args.Keys))
	keys := make([]string, 0, len(args.Keys))
	for _, key := range args.Keys {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return secretValueArgs{}, fmt.Errorf("at least one Secret key is required")
	}
	if len(keys) > maxSecretKeysPerRead {
		return secretValueArgs{}, fmt.Errorf("at most %d Secret keys can be read per call", maxSecretKeysPerRead)
	}
	args.Keys = keys
	return args, nil
}

// secretValueForModel 返回文本值；二进制值以 base64 提供，并把两种可输出形式都
// 登记为精确脱敏目标。
func secretValueForModel(raw []byte) (value, encoding string, sensitive []string) {
	encoded := base64.StdEncoding.EncodeToString(raw)
	if utf8.Valid(raw) && !strings.ContainsRune(string(raw), '\x00') {
		value = string(raw)
		encoding = "utf-8"
		sensitive = append(sensitive, value, encoded)
		return value, encoding, sensitive
	}
	return encoded, "base64", []string{encoded}
}
