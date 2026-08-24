package executor

import (
	"fmt"
	"strings"
	"unicode"
)

// 动词白名单：只读 + 非交互写。
var allowedVerbs = map[string]bool{
	// 只读
	"get": true, "describe": true, "logs": true, "events": true,
	"top": true, "explain": true, "api-resources": true, "version": true,
	// 写（审批后执行）
	"apply": true, "create": true, "delete": true, "patch": true,
	"scale": true, "rollout": true, "set": true, "annotate": true, "label": true,
	"exec": true, "drain": true, "cordon": true, "uncordon": true,
}

// rejectedInteractiveVerbs 是交互式/阻塞式命令，直接拒绝。
var rejectedInteractiveVerbs = map[string]bool{
	"edit": true, "attach": true, "port-forward": true, "proxy": true,
	"run": true, "cp": true, "debug": true, "auth": true, "plugin": true,
}

// ParseCommand 解析 kubectl 命令为 argv：
// - 拒绝引号外的 shell 元字符（| & ; < > $ ( ) ` 换行）
// - 引号感知切分（单/双引号）
// - 动词白名单 + 交互式/流式命令拒绝
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

// validateFlags 拒绝交互式/流式 flag（exec -it、logs -f、get -w 等）。
func validateFlags(verb string, tokens []string) error {
	for _, t := range tokens {
		switch verb {
		case "exec":
			if t == "-i" || t == "-t" || t == "-it" || t == "-ti" || t == "--stdin" || t == "--tty" {
				return fmt.Errorf("interactive exec is not supported; use non-interactive exec with a specific command")
			}
		case "logs":
			if t == "-f" || t == "--follow" {
				return fmt.Errorf("streaming logs (-f) is not supported; use --tail instead")
			}
		case "get":
			if t == "-w" || t == "--watch" || t == "-f" || t == "--follow" {
				return fmt.Errorf("streaming watch is not supported")
			}
		}
	}
	return nil
}
