package executor

import (
	"fmt"
	"strings"
	"unicode"
)

// 动词白名单：只读 + 非交互写。
// 写动词分类对齐 approval/risk.go 与 kubectl-ai kubectl_filter.go（autoscale/expose/
// taint/reconcile/certificate 等纳入审批流；edit/attach/port-forward 等交互式命令拒绝）。
var allowedVerbs = map[string]bool{
	// 只读
	"get": true, "describe": true, "logs": true, "events": true,
	"top": true, "explain": true, "api-resources": true, "version": true,
	// 写（审批后执行）
	"apply": true, "create": true, "delete": true, "patch": true,
	"scale": true, "rollout": true, "set": true, "annotate": true, "label": true,
	"exec": true, "drain": true, "cordon": true, "uncordon": true,
	"autoscale": true, "expose": true, "taint": true, "reconcile": true, "certificate": true,
}

// rejectedInteractiveVerbs 是交互式/阻塞式命令，直接拒绝。
var rejectedInteractiveVerbs = map[string]bool{
	"edit": true, "attach": true, "port-forward": true, "proxy": true,
	"run": true, "cp": true, "debug": true, "auth": true, "plugin": true,
}

// ParseCommand 解析 kubectl 命令为 argv：
// - 拒绝引号外的 shell 元字符（| & ; < > $ ( ) ` 换行）
// - 引号感知切分（单/双引号）
// - 动词白名单 + 交互式命令拒绝
// 流式命令（logs -f / get -w）不再在解析层拒绝，改由 executor 统一超时捕获部分输出。
// 返回的 argv[0] 恒为 "kubectl"。
func ParseCommand(command string) ([]string, error) {
	if len(command) > 2048 {
		return nil, fmt.Errorf("command too long")
	}
	tokens, err := tokenize(command)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	if tokens[0] != "kubectl" {
		return nil, fmt.Errorf("command must start with kubectl")
	}
	verb, rest := extractFirstNonFlag(tokens[1:])
	if verb == "" {
		return nil, fmt.Errorf("missing kubectl verb")
	}
	if rejectedInteractiveVerbs[verb] {
		return nil, fmt.Errorf("interactive command %q is not supported", verb)
	}
	if !allowedVerbs[verb] {
		return nil, fmt.Errorf("verb %q is not allowed", verb)
	}
	if err := validateFlags(verb, rest); err != nil {
		return nil, err
	}
	return tokens, nil
}

// IsStreaming 判断已解析的 argv 是否为需超时采样的流式命令（logs -f / get -w）。
// 交互式流（attach/exec -it）在 ParseCommand 阶段已拒绝，不会到达此处。
// 借鉴 kubectl-ai streaming.go DetectKubectlStreaming。
func IsStreaming(argv []string) bool {
	if len(argv) < 2 {
		return false
	}
	verb, rest := extractFirstNonFlag(argv[1:])
	switch verb {
	case "logs":
		return containsFlag(rest, "-f", "--follow")
	case "get":
		return containsFlag(rest, "-w", "--watch")
	}
	return false
}

// containsFlag 在 tokens 中查找 flag（精确匹配或 flag=value 形式）。
func containsFlag(tokens []string, flags ...string) bool {
	for _, t := range tokens {
		for _, f := range flags {
			if t == f || strings.HasPrefix(t, f+"=") {
				return true
			}
		}
	}
	return false
}

// tokenize 引号感知切分并拒绝 shell 元字符。
func tokenize(command string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	inSingle, inDouble := false, false
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for _, r := range command {
		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
			} else {
				cur.WriteRune(r)
			}
		case inDouble:
			if r == '"' {
				inDouble = false
			} else {
				cur.WriteRune(r)
			}
		case r == '\'':
			inSingle = true
		case r == '"':
			inDouble = true
		case unicode.IsSpace(r):
			flush()
		case strings.ContainsRune(`|&;<>$()`+"`", r) || r == '\n' || r == '\r':
			return nil, fmt.Errorf("shell metacharacter %q is not allowed", r)
		default:
			cur.WriteRune(r)
		}
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("unbalanced quotes")
	}
	flush()
	return tokens, nil
}

func extractFirstNonFlag(tokens []string) (string, []string) {
	for i, t := range tokens {
		if !strings.HasPrefix(t, "-") {
			return t, tokens[i+1:]
		}
	}
	return "", nil
}

// validateFlags 拒绝交互式 flag（exec -i/-t）。
// 流式 flag（logs -f / get -w）不再拒绝，改由 executor 统一超时捕获部分输出。
func validateFlags(verb string, tokens []string) error {
	if verb != "exec" {
		return nil
	}
	for _, t := range tokens {
		if t == "-i" || t == "-t" || t == "-it" || t == "-ti" || t == "--stdin" || t == "--tty" {
			return fmt.Errorf("interactive exec is not supported; use non-interactive exec with a specific command")
		}
	}
	return nil
}
