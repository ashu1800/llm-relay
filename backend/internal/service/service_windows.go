//go:build windows

package service

import (
	"context"

	"golang.org/x/sys/windows/svc"
)

// IsWindowsService 判断当前进程是否由 SCM 启动。
//
// 这个判断决定「要不要走服务协议」：直接双击 exe 时它是 false，
// 必须按普通控制台程序跑（否则进程会去等一个永远不会来的
// StartServiceCtrlDispatcher，表现为双击后什么都没发生）。
func IsWindowsService() bool {
	is, err := svc.IsWindowsService()
	return err == nil && is
}

// runService 在 Windows 上分两条路：
//
//   - 由 SCM 启动 → 走 svc.Run，停止请求来自服务控制（Stop/Shutdown）；
//   - 由用户/命令行启动 → 走控制台信号的通用实现。
//
// 为什么不统一走 svc.Run：svc.Run 要求进程是 SCM 启动的，普通双击启动时
// 它会直接返回 ERROR_FAILED_SERVICE_CONTROLLER_CONNECT，服务根本跑不起来。
func runService(name string, mainFn func(context.Context) error) error {
	if !IsWindowsService() {
		return runConsole(mainFn)
	}
	return svc.Run(name, &handler{mainFn: mainFn})
}

// handler 实现 svc.Handler：把 SCM 的 Stop/Shutdown 翻译成 ctx 取消。
//
// 这是 Windows 支持里最关键的一段。没有它，SCM 的「停止服务」
// 不会变成任何 Go 信号，signal.NotifyContext 永远不返回 ——
// 于是 5 个后台循环停不下来、日志队列不 flush，
// SCM 等到 WaitToKillServiceTimeout 之后直接 TerminateProcess。
// 用户看到的是「每次停服务都要卡很久，而且最后一条日志永远是上一次的」。
type handler struct {
	mainFn func(context.Context) error
}

func (h *handler) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	// 先报「正在启动」再报 Running：SCM 据此判断服务有没有卡死。
	// 中间这段是真实的初始化（连数据库、跑迁移），可能几秒。
	changes <- svc.Status{State: svc.StartPending}
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	// 把 SCM 请求转成与信号路径相同的「关闭 stop 通道」。
	stop := make(chan struct{})
	go func() {
		for req := range r {
			switch req.Cmd {
			case svc.Stop, svc.Shutdown, svc.PreShutdown:
				close(stop)
				return
			case svc.Interrogate:
				// SCM 定期询问状态，必须回一个当前状态，否则它会认为服务无响应
				changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
			}
		}
	}()

	err := serve(stop, h.mainFn)

	// 收尾完成后再报 StopPending → Stopped。
	// 顺序反了（先 Stopped 再收尾）SCM 就会以为进程已经可以回收。
	changes <- svc.Status{State: svc.StopPending}

	exitCode := uint32(0)
	if err != nil {
		exitCode = 1
	}
	// 第二个返回值是「是否服务专有错误码」；false 表示用通用的 Win32 码。
	// 这里给 1 而不是具体 errno：SCM 只需要知道成功/失败，
	// 失败原因在应用日志里（install.ps1 配的日志文件）。
	return false, exitCode
}
