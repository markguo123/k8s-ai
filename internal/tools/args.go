package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseArgs 解析 Tool 入参 JSON 为 map[string]string，并校验必填字段。
// 数值/布尔等简单类型转字符串；复杂类型保留 JSON 文本。
func ParseArgs(input json.RawMessage, required ...string) (map[string]string, error) {
	var raw map[string]any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &raw); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	out := map[string]string{}
	for k, v := range raw {
		switch val := v.(type) {
		case string:
			out[k] = val
		case float64:
			out[k] = trimNum(val)
		case bool:
			out[k] = fmt.Sprintf("%v", val)
		default:
			b, err := json.Marshal(v)
			if err == nil {
				out[k] = string(b)
			}
		}
	}
	for _, r := range required {
		if strings.TrimSpace(out[r]) == "" {
			return nil, fmt.Errorf("missing required argument %q", r)
		}
	}
	return out, nil
}

// trimNum 把 JSON 数值转字符串（整数不带小数位）。
func trimNum(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%v", f)
}
