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

// shellMetachars 是触发 shell 模式的元字符集合（引号外出现时标记 isShell=true）。
var shellMetachars = `|&;<>$()` + "`"

// ParsedCommand 包含解析后的 argv 及是否需要 shell 执行。
type ParsedCommand struct {
	Argv    []string
	IsShell bool // 引号外含 shell 元字符 → 需 bash -c 执行
}

// ParseCommand 解析 kubectl 命令：
// - 引号感知切分（单/双引号）
// - 引号外的 shell 元字符不再拒绝，而是标记 IsShell=true 透传给 executor
// - 动词白名单 + 交互式命令拒绝（对第一个非 flag 动词校验）
// - 流式命令（logs -f / get -w）不在解析层拒绝，改由 executor 统一超时采样
// 返回的 Argv[0] 恒为 "kubectl"（shell 模式下 Argv 仅含原始命令字符串）。
func ParseCommand(command string) ([]string, error) {
	pc, err := ParseCommandDetail(command)
	if err != nil {
		return nil, err
	}
	return pc.Argv, nil
}

// ParseCommandDetail 是 ParseCommand 的详细版本，额外返回 IsShell 标记。
func ParseCommandDetail(command string) (ParsedCommand, error) {
	if len(command) > 4096 {
		return ParsedCommand{}, fmt.Errorf("command too long")
	}
	tokens, isShell, err := tokenize(command)
	if err != nil {
		return ParsedCommand{}, err
	}
	if len(tokens) == 0 {
		return ParsedCommand{}, fmt.Errorf("empty command")
	}
	// shell 模式：命令可能不以 kubectl 开头（如 cat <<EOF | kubectl apply），
	// 跳过 tokens[0] 检查和动词白名单，但扫描全命令拒绝交互式动词。
	if isShell {
		if err := rejectInteractiveAnywhere(command); err != nil {
			return ParsedCommand{}, err
		}
		return ParsedCommand{Argv: tokens, IsShell: true}, nil
	}
	// 非 shell 模式：严格校验 tokens[0] == kubectl + 动词白名单。
	if tokens[0] != "kubectl" {
		return ParsedCommand{}, fmt.Errorf("command must start with kubectl")
	}
	verb, rest := extractFirstNonFlag(tokens[1:])
	if verb == "" {
		return ParsedCommand{}, fmt.Errorf("missing kubectl verb")
	}
	if rejectedInteractiveVerbs[verb] {
		return ParsedCommand{}, fmt.Errorf("interactive command %q is not supported", verb)
	}
	if !allowedVerbs[verb] {
		return ParsedCommand{}, fmt.Errorf("verb %q is not allowed", verb)
	}
	if err := validateFlags(verb, rest); err != nil {
		return ParsedCommand{}, err
	}
	return ParsedCommand{Argv: tokens, IsShell: false}, nil
}

// rejectInteractiveAnywhere 在 shell 命令中扫描是否含交互式动词（如 edit/attach/port-forward）。
// shell 模式下动词可能在命令任意位置（如 cat <<EOF | kubectl edit ...）。
func rejectInteractiveAnywhere(command string) error {
	for verb := range rejectedInteractiveVerbs {
		if strings.Contains(command, " kubectl "+verb+" ") ||
			strings.Contains(command, " kubectl "+verb+"\t") ||
			strings.HasSuffix(command, " kubectl "+verb) {
			return fmt.Errorf("interactive command %q is not supported", verb)
		}
	}
	return nil
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

// tokenize 引号感知切分。引号外的 shell 元字符不再返回错误，
// 而是设置 isShell=true 并作为普通字符加入当前 token。
// 引号字符本身不写入 token（与非 shell 模式行为一致），
// executor 在 shell 模式下用原始 command 字符串执行 bash -c，不依赖 tokens。
func tokenize(command string) (tokens []string, isShell bool, err error) {
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
		case strings.ContainsRune(shellMetachars, r) || r == '\n' || r == '\r':
			// shell 元字符：标记 isShell，作为普通字符保留在 token 中。
			isShell = true
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
	}
	if inSingle || inDouble {
		return nil, false, fmt.Errorf("unbalanced quotes")
	}
	flush()
	return tokens, isShell, nil
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
