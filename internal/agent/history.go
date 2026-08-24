package agent

import (
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/session"
)

// historyToMessages 将会话消息转成 LLM 消息，并裁剪到最近 maxTurns 轮用户对话。
// tool 消息必须紧跟在对应 assistant 消息之后（OpenAI 契约），按"轮"整体截取。
func historyToMessages(msgs []session.Message, maxTurns int) []llm.Message {
	if maxTurns <= 0 {
		maxTurns = 20
	}
	start := 0
	seen := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			seen++
			if seen == maxTurns {
				start = i
				break
			}
		}
	}
	if seen < maxTurns && len(msgs) > 0 {
		start = 0
	}
	out := make([]llm.Message, 0, len(msgs)-start)
	for _, m := range msgs[start:] {
		switch m.Role {
		case "tool":
			out = append(out, llm.Message{Role: "tool", ToolCallID: m.ToolCallID, Content: m.Content})
		default:
			out = append(out, llm.Message{Role: m.Role, Content: m.Content})
		}
	}
	return out
}
