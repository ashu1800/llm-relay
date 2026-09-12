package web

import (
	"embed"
	"io/fs"
)

// dist 由前端构建产物填充（Dockerfile 中从前端阶段拷贝而来）。
//
//go:embed all:dist
var distFS embed.FS

// Dist 返回前端静态资源文件系统。
func Dist() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
