package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/version"
)

// bannerInnerWidth 是横幅卡片内部宽度（显示列数）。
const bannerInnerWidth = 46

// printChatBanner 打印交互模式欢迎横幅：终端显示边框卡片，管道/测试降级纯文本。
func printChatBanner(w io.Writer, cluster string) {
	lines := []string{"k8s-ai · Kubernetes AI 助手", "版本 " + version.Version}
	if cluster != "" {
		lines = append(lines, "集群 "+cluster)
	}
	f, isTTY := w.(*os.File)
	if isTTY && isTerminal(f) {
		fmt.Fprintln(w, "╭"+strings.Repeat("─", bannerInnerWidth)+"╮")
		for _, l := range lines {
			fmt.Fprintf(w, "│ %s%s │\n", l, strings.Repeat(" ", bannerInnerWidth-1-displayWidth(l)))
		}
		fmt.Fprintln(w, "╰"+strings.Repeat("─", bannerInnerWidth)+"╯")
	} else {
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "我能帮你：")
	fmt.Fprintln(w, "  · 巡检集群健康，定位 Pod/节点/存储/网络故障根因")
	fmt.Fprintln(w, "  · 解读资源状态、事件与日志")
	fmt.Fprintln(w, "  · 执行变更操作（写操作会先征求你确认）")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "命令：exit 退出 · clear 清空上下文 · Ctrl+C 直接退出")
	fmt.Fprintln(w)
}

// displayWidth 估算字符串的终端显示宽度：CJK 全角字符按 2 列计。
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if isWideRune(r) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

// isWideRune 判断是否为终端中占两列的全角字符（覆盖常用 CJK 区间）。
func isWideRune(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // 韩文 Jamo
		r >= 0x2E80 && r <= 0x303E, // CJK 部首/符号
		r >= 0x3041 && r <= 0x33FF, // 日文假名/CJK 兼容
		r >= 0x3400 && r <= 0x4DBF, // CJK 扩展 A
		r >= 0x4E00 && r <= 0x9FFF, // CJK 统一表意
		r >= 0xA000 && r <= 0xA4CF, // 彝文
		r >= 0xAC00 && r <= 0xD7A3, // 韩文音节
		r >= 0xF900 && r <= 0xFAFF, // CJK 兼容表意
		r >= 0xFE30 && r <= 0xFE4F, // CJK 兼容形式
		r >= 0xFF00 && r <= 0xFF60, // 全角形式
		r >= 0xFFE0 && r <= 0xFFE6: // 全角符号
		return true
	}
	return false
}
