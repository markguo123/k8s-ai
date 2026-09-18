package main

import (
	"fmt"
	"github.com/charmbracelet/glamour"
)

func main() {
	for _, sample := range []string{
		"- **标签：**值是普通文本\n",
		"- **整个列表项都是粗体**\n",
		"这是 **行内粗体** 继续文字\n",
	} {
		md, _ := glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithPreservedNewLines())
		out, _ := md.Render(sample)
		fmt.Printf("IN: %q\nOUT: %q\n---\n", sample, out)
	}
}
