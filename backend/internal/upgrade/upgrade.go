// Package upgrade 实现「无感更新」：老进程把监听套接字交给新进程，新进程
// 立刻接手新连接，老进程把手上的请求跑完再退出。
//
// 为什么必须是两个进程：单进程无论怎么排期都躲不开二选一 —— 先关端口则
// 新连接被拒，先退进程则在途对话被切。只有「新进程先接手、老进程后退出」
// 才能两头都不丢。这也是 nginx / tableflip 那一类 graceful upgrade 的做法。
//
// 交接依赖两件事：
//  1. 监听套接字能传给子进程（Linux 的 exec.Cmd.ExtraFiles → 子进程 fd 3）；
//  2. 老进程退出时 systemd 不能把子进程一起清掉 —— 这要求单元是
//     Type=notify，且老进程用 MAINPID= 把「主进程」身份交给子进程，
//     否则默认的 KillMode=control-group 会把整个 cgroup 连同新进程杀掉。
//
// 任一前提不满足（旧 unit、非 systemd、Windows）时 Supported 返回 false，
// 调用方退回「退出让托管方拉起」的老路 —— 那条路只是会中断，不会坏。
package upgrade

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// 交接用的两个环境变量与文件描述符号。
//
// 描述符是**固定**的：ExtraFiles[0] 永远是 fd 3、ExtraFiles[1] 永远是 fd 4
// （Go 的约定），环境变量里写的就是这个数字，子进程照着 os.NewFile 取。
const (
	listenFDEnv = "LLM_RELAY_LISTEN_FD"
	readyFDEnv  = "LLM_RELAY_READY_FD"
	listenFD    = 3
	readyFD     = 4
)

// 子进程写完这一行才算「能服务了」。父进程等到它才放手，
// 在此之前一直由父进程继续 accept —— 交接窗口里没有任何一刻没人接连接。
const readyToken = "ready"

// Supported 报告当前进程能否做无感交接。
func Supported() bool { return UnsupportedReason() == "" }

// UnsupportedReason 返回不能交接的原因（能交接时返回空串）。
//
// 单独暴露原因而不是只给一个 bool：这条路径失败时的日志必须能回答
// 「为什么这次更新又中断了」，否则用户只会看到「说好的无感呢」。
func UnsupportedReason() string {
	if runtime.GOOS != "linux" {
		return "只有 Linux 支持套接字交接（当前 " + runtime.GOOS + "）"
	}
	if os.Getenv("NOTIFY_SOCKET") == "" {
		return "systemd 单元不是 Type=notify（没有 NOTIFY_SOCKET）：" +
			"老进程退出时 systemd 会把新进程一起清掉；重新运行 deploy/install.sh 升级单元后可启用"
	}
	return ""
}

// InheritedListener 取出父进程交过来的监听套接字。
//
// 第二个返回值表示「这个进程是被交接起来的」—— 调用方据此跳过 bind，
// 并且跳过 READY 上报（它还不是 systemd 的主进程）。
func InheritedListener() (net.Listener, bool) {
	raw := os.Getenv(listenFDEnv)
	if raw == "" {
		return nil, false
	}
	fd, err := strconv.Atoi(raw)
	if err != nil || fd < 3 {
		return nil, false
	}
	f := os.NewFile(uintptr(fd), "inherited-listener")
	if f == nil {
		return nil, false
	}
	// FileListener 会 dup 一份，这个 *os.File 用完就可以关
	defer f.Close()
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, false
	}
	return ln, true
}

// NotifyReady 告诉 systemd「服务已就绪」。
//
// Type=notify 的单元必须收到这一条才会把服务标成 active（否则启动超时被判失败）。
// 没有 NOTIFY_SOCKET 时是空操作。
func NotifyReady() {
	if err := notify("READY=1"); err != nil {
		// 上报失败不该影响服务本身：最坏情况是 systemd 认为启动慢，
		// 但进程在跑、端口在听，功能是好的。
		fmt.Fprintf(os.Stderr, "通知 systemd 就绪失败: %v\n", err)
	}
}

// NotifyMainPID 把「systemd 主进程」身份交给 pid。
//
// 这一步是交接的安全绳：主进程一换，老进程退出就不再触发单元的
// KillMode=control-group 清理，新进程得以活下来接管服务。
// 返回错误时调用方必须**终止子进程**并退回老路 —— 否则老进程一退，
// 子进程会被 systemd 杀掉，服务直接掉线。
func NotifyMainPID(pid int) error {
	if pid <= 0 {
		return errors.New("非法的子进程 pid")
	}
	return notify("MAINPID=" + strconv.Itoa(pid))
}

// NotifyChildReady 由**子进程**调用：告诉父进程「我已经开始服务，你可以放手」。
//
// 没有 READY_FD（正常启动）时是空操作。
func NotifyChildReady() error {
	raw := os.Getenv(readyFDEnv)
	if raw == "" {
		return nil
	}
	fd, err := strconv.Atoi(raw)
	if err != nil || fd < 3 {
		return nil
	}
	f := os.NewFile(uintptr(fd), "upgrade-ready")
	if f == nil {
		return nil
	}
	defer f.Close()
	_, err = f.WriteString(readyToken)
	return err
}

// Spawn 用新的可执行文件启动子进程，把监听套接字交给它，并等它就绪。
//
// 返回时子进程**已经能服务**（它先写完 READY 才返回），所以调用方可以
// 立刻停止自己的 accept，不存在「两边都没在收」的空档。
// 超时或子进程提前退出时返回错误，调用方应继续服务并退回老路。
func Spawn(exe string, ln net.Listener, args []string, timeout time.Duration) (*os.Process, error) {
	tcp, ok := ln.(*net.TCPListener)
	if !ok {
		return nil, fmt.Errorf("监听器不是 TCP，无法导出套接字")
	}
	// File() 返回的是 dup 出来的副本：交给子进程后本进程仍持有原监听器，
	// 交接窗口里继续 accept（见包注释）。
	f, err := tcp.File()
	if err != nil {
		return nil, fmt.Errorf("导出监听套接字: %w", err)
	}
	defer f.Close()

	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("建就绪管道: %w", err)
	}
	defer pr.Close()

	cmd := exec.Command(exe, args...)
	cmd.Env = withEnv(os.Environ(), map[string]string{
		listenFDEnv: strconv.Itoa(listenFD),
		readyFDEnv:  strconv.Itoa(readyFD),
	})
	// ExtraFiles[i] 在子进程里是 fd 3+i。日志直接继承：子进程的输出仍进
	// journald，运维在 `journalctl -u llm-relay` 里看不到断档。
	cmd.ExtraFiles = []*os.File{f, pw}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		pw.Close()
		return nil, fmt.Errorf("启动新进程: %w", err)
	}
	// 父进程必须关掉写端，否则子进程还活着时 ReadAll 永远等不到 EOF
	// （子进程正常就绪时会写 ready 并关闭，提前退出时 EOF 也会到）。
	pw.Close()
	// 回收子进程，避免僵尸。Wait 的返回值不影响交接结果：
	// 该进程活得越久越好，退出只说明它已经完成任务或已失败。
	go func() { _ = cmd.Wait() }()

	ready := make(chan error, 1)
	go func() {
		buf, err := io.ReadAll(pr)
		if err != nil {
			ready <- err
			return
		}
		if got := strings.TrimSpace(string(buf)); got != readyToken {
			// 子进程没写完就退出（启动失败）时会走到这里
			ready <- fmt.Errorf("新进程没有就绪（收到 %q）", got)
			return
		}
		ready <- nil
	}()

	select {
	case err := <-ready:
		if err != nil {
			_ = cmd.Process.Kill()
			return nil, err
		}
		return cmd.Process, nil
	case <-time.After(timeout):
		// 超时杀掉：这个子进程还占着监听套接字的副本，留着它会让
		// 「谁在 accept」变得说不清。
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("新进程 %s 内没有就绪，已终止", timeout)
	}
}

// withEnv 返回一份去掉了 kv 里那些键、再补上新值的环境变量。
//
// 不能直接 append：上一代交接出来的进程，os.Environ() 里还留着自己的
// LLM_RELAY_LISTEN_FD / READY_FD，同一个键出现两次时取值行为依赖平台
// （目前两份的值恰好相同，但不能指望这件事永远成立）。
func withEnv(base []string, kv map[string]string) []string {
	out := make([]string, 0, len(base)+len(kv))
	for _, pair := range base {
		name, _, ok := strings.Cut(pair, "=")
		if ok {
			if _, drop := kv[name]; drop {
				continue
			}
		}
		out = append(out, pair)
	}
	for k, v := range kv {
		out = append(out, k+"="+v)
	}
	return out
}

// notify 往 systemd 的通知套接字写一条状态行。
//
// NOTIFY_SOCKET 可能是抽象命名空间（值以 @ 开头）：Go 的 net 包把
// "@name" 当抽象地址处理，所以这里不用手工把 @ 换成 \0。
func notify(state string) error {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return nil
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err
}
