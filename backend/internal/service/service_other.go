//go:build !windows

package service

import "context"

// runService 在非 Windows 平台上只有控制台/signal 一条路径。
// systemd 的 SIGTERM 与终端里的 Ctrl-C 走的是同一段代码。
func runService(_ string, mainFn func(context.Context) error) error {
	return runConsole(mainFn)
}

// IsWindowsService 在非 Windows 平台上恒为 false。
//
// 存在的意义是让调用方可以无条件地问这个问题（例如决定日志该不该
// 写文件），而不必自己散落 runtime.GOOS 判断。
func IsWindowsService() bool { return false }
