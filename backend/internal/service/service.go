// Package service 把「进程如何被托管」与「进程做什么」解耦。
//
// 两种托管形态：
//   - Linux：systemd（或直接跑在终端里），由信号驱动；
//   - Windows：SCM，由服务控制请求驱动。
//
// 上层（cmd/server）只调用 Run 一次并提供一个 mainFn，不需要知道
// 自己跑在哪一种形态下。这样「收到停止后要优雅关闭」这条逻辑
// 只有一份实现，不会出现「Linux 收得干净、Windows 被强杀」的偏差。
package service

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Run 以合适的托管形态运行 mainFn，并在托管方要求停止时取消它的 ctx。
//
// mainFn 返回即进程准备退出；它的返回值原样上抛（启动失败要让调用方
// 以非零码退出）。只有在 mainFn 已经收尾完成之后 Run 才会返回 ——
// 优雅关闭（日志队列 flush、在途请求收尾）全在 mainFn 里，
// 提前返回会让调用方的 os.Exit 把它们一笔勾销。
func Run(name string, mainFn func(context.Context) error) error {
	return runService(name, mainFn)
}

// serve 是「跑主函数 + 等停止请求」的唯一实现，控制台信号与 SCM
// 请求都汇到这里。它不关心停止请求从哪来，只关心「到了要取消 ctx，
// 并等 mainFn 自己收尾完」。
//
// stop 被关闭表示托管方要求停止。ctx 的取消只通知「该收尾了」，
// serve 仍然阻塞到 mainFn 返回 —— 这两件事的先后顺序是优雅关闭的全部要点。
func serve(stop <-chan struct{}, mainFn func(context.Context) error) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- mainFn(ctx) }()

	select {
	case err := <-done:
		// mainFn 自己退出了（启动失败，或监听出错），不必等停止请求
		return err
	case <-stop:
		cancel()
		return <-done
	}
}

// runConsole 用控制台信号驱动 mainFn。
//
// 这是「Linux 直接运行 / systemd 托管」与「Windows 命令行直接运行」
// 共用的路径。Windows 上 SIGTERM 同样有定义且会被 Go 映射
// （CTRL_CLOSE / LOGOFF / SHUTDOWN），所以两边语义一致：
// Ctrl-C 与关窗口都能触发优雅关闭。
//
// 注意它**不**适用于 Windows 服务形态：SCM 的停止请求不会变成控制台信号，
// 那条路径由 service_windows.go 的 svc.Handler 接管。
func runConsole(mainFn func(context.Context) error) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	stop := make(chan struct{})

	// 只转发**一个**信号：第二个 Ctrl-C 应当由用户自己去杀，
	// 而不是让我们再走一遍收尾流程（那时可能已经在等 flush 了）。
	go func() {
		<-sigCh
		close(stop)
	}()

	return serve(stop, mainFn)
}
