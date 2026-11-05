// Package web 内嵌前端构建产物（P6.11 / D-30：形态 A 一体化交付）。
//
// 产物来源：仓库根 Dockerfile 的 node 阶段执行 npm ci + npm run build 后
// COPY 进 Go 构建阶段；本地开发由 web/ 的 vite build 产出同一目录。
// 干净检出时 dist 里只有占位文件 .gitkeep，此时 Empty() 为 true，静态路由
// 返回明确提示而不是 404 空页。
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// placeholder 是占位文件名，用于让空 dist 也能通过 go:embed 编译。
const placeholder = ".gitkeep"

// Dist 返回 dist 目录的文件系统视图（根即 dist）。
func Dist() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("web: embedded dist unavailable: " + err.Error())
	}
	return sub
}

// Empty 报告内嵌产物是否只有占位文件（未执行前端构建）。
func Empty() bool {
	return onlyPlaceholder(Dist())
}

func onlyPlaceholder(fsys fs.FS) bool {
	found := true
	_ = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if path == placeholder {
			return nil
		}
		found = false
		return fs.SkipAll
	})
	return found
}
