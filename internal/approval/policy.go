package approval

import (
	"strings"
	"unicode"

	"mvdan.cc/sh/v3/syntax"
)

// Policy 是写操作审批策略（agent.require_approval / --skip-permissions / deny_ops）。
type Policy struct {
	RequireApproval bool
	SkipPermissions bool
	DenyOps         []string
	Mode            Mode
}

// RequiresApproval 判断写操作是否需要人类确认。
func (p Policy) RequiresApproval() bool {
	if p.SkipPermissions {
		return false
	}
	return p.RequireApproval
}

// IsDenied 判断命令是否命中 deny_ops 硬拦截（子串匹配，默认空）。
func IsDenied(denyOps []string, command string) bool {
	low := strings.ToLower(strings.TrimSpace(command))
	for _, op := range denyOps {
		if op != "" && strings.Contains(low, strings.ToLower(op)) {
			return true
		}
	}
	return false
}

// IsForbiddenCommand 判断命令是否触碰 LLM 数据安全硬红线，
// 命中则无论只读/写、无论是否审批，一律拒绝执行：
//   - 红线 1：任何形式的 Secret 资源访问——工具输出会进入 LLM 上下文，
//     违反 "Never send Kubernetes Secret data to the LLM"（AGENTS.md）。
//   - 红线 2：--raw flag——可绕过资源名直接请求任意 API 路径（含 secret data）。
//
// 匹配非 flag 的资源 token：secret、secrets、secret/name。
// 极端情况（如 -n secret 指向名为 secret 的命名空间）会误拦，
// 宁可过严不可漏放。
func IsForbiddenCommand(command string) bool {
	if forbidden, ok := forbiddenByShellParse(command); ok {
		return forbidden
	}
	// 解析失败（例如未闭合引号）时保守回退到轻量词法扫描。
	for _, tok := range shellTokens(command) {
		if tok == "--raw" || strings.HasPrefix(tok, "--raw=") {
			return true
		}
		if strings.HasPrefix(tok, "-") {
			continue
		}
		for _, part := range strings.FieldsFunc(tok, func(r rune) bool {
			return r == '/' || r == ','
		}) {
			switch strings.ToLower(part) {
			case "secret", "secrets":
				return true
			}
		}
	}
	return false
}

// forbiddenByShellParse 使用 mvdan 的 shell 解析器识别 Secret 资源与 --raw。
// 相比纯字符串扫描，它能正确处理引号拼接（sec"ret"）、命令替换（$(echo secret)）、
// 管道和复合命令中的每个 kubectl 调用。
func forbiddenByShellParse(command string) (bool, bool) {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return false, false
	}
	forbidden := false
	syntax.Walk(file, func(node syntax.Node) bool {
		w, ok := node.(*syntax.Word)
		if !ok {
			return !forbidden
		}
		if forbiddenToken(wordLiteral(w)) {
			forbidden = true
			return false
		}
		return true
	})
	return forbidden, true
}

// wordLiteral 把一个 shell Word 中所有字面量片段拼接起来，
// 用于识别 `sec"ret"` 这类跨引号拆分的敏感词。
func wordLiteral(w *syntax.Word) string {
	var b strings.Builder
	syntax.Walk(w, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.Lit:
			b.WriteString(n.Value)
		case *syntax.SglQuoted:
			b.WriteString(n.Value)
		}
		return true
	})
	return b.String()
}

func forbiddenToken(lit string) bool {
	if lit == "" {
		return false
	}
	if lit == "--raw" || strings.HasPrefix(lit, "--raw=") {
		return true
	}
	if strings.HasPrefix(lit, "-") {
		return false
	}
	for _, part := range strings.FieldsFunc(lit, func(r rune) bool {
		return r == '/' || r == ','
	}) {
		switch strings.ToLower(part) {
		case "secret", "secrets":
			return true
		}
	}
	return false
}

// shellTokens 对命令做一次轻量 shell 词法切分：跳过引号内容，遇到
// 空白或 shell 元字符（| & ; < > $ ( ) `）时结束当前 token。
// 它不执行命令替换，只负责把 `$(echo secret)` 或 `sec"ret"` 这种
// 经过引用/替换包装的字符串拆成可识别的裸词，避免红线检测被绕过。
func shellTokens(command string) []string {
	var tokens []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	inSingle, inDouble := false, false
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
		case unicode.IsSpace(r) || strings.ContainsRune("|&;<>$()`", r):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return tokens
}
