package report

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// Writer 负责报告落盘（latest/daily）与目录创建。
type Writer struct {
	dir string
}

// NewWriter 创建写入器。
func NewWriter(dir string) *Writer {
	return &Writer{dir: dir}
}

// Write 按模式写出报告：none 不落盘；latest 写 latest.md + latest.json；
// daily 追加时间戳 Markdown。返回写入的文件路径列表。
func (w *Writer) Write(result *model.ScanResult, opts model.ReportOptions) ([]string, error) {
	if opts.Mode == "none" {
		return nil, nil
	}
	if opts.Directory != "" {
		w.dir = opts.Directory
	}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return nil, fmt.Errorf("create report dir: %w", err)
	}
	var paths []string

	mdPath := filepath.Join(w.dir, "latest.md")
	if err := os.WriteFile(mdPath, mustRender(MarkdownRenderer{}, result), 0o644); err != nil {
		return nil, err
	}
	paths = append(paths, mdPath)

	jsonPath := filepath.Join(w.dir, "latest.json")
	if err := os.WriteFile(jsonPath, mustRender(JSONRenderer{}, result), 0o644); err != nil {
		return nil, err
	}
	paths = append(paths, jsonPath)

	if opts.Mode == "daily" {
		stamp := time.Now().Format("2006-01-02-15-04-05")
		dailyPath := filepath.Join(w.dir, stamp+".md")
		if err := os.WriteFile(dailyPath, mustRender(MarkdownRenderer{}, result), 0o644); err != nil {
			return nil, err
		}
		paths = append(paths, dailyPath)
	}
	return paths, nil
}

func mustRender(r Renderer, result *model.ScanResult) []byte {
	out, err := r.Render(result)
	if err != nil {
		panic(err) // 渲染失败属于编程错误；测试覆盖保证不会发生
	}
	return out
}
