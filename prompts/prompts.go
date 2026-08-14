// Package prompts 内嵌随二进制发布的系统提示词（go:embed）。
// 提示词随镜像一起发布，运行时无需外部文件。
package prompts

import _ "embed"

//go:embed system.md
var systemPrompt string

// SystemPrompt 返回内置 Kubernetes SRE 系统提示词（规范 §23）。
func SystemPrompt() string {
	return systemPrompt
}
