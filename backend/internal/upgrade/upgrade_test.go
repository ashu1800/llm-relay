// 交接机制的单测。真正的交接（传 fd、换 MAINPID）只能在 Linux + systemd
// 上跑，这里钉的是「判据与边界」——它们写错时的表现是「更新悄悄退回老路」
// 或「老进程退得太早」，两者都不会报错，只会让用户觉得「说好的无感呢」。
package upgrade

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 没有 NOTIFY_SOCKET 时一律不支持：systemd 的单元不是 notify 类型，
// 老进程一退就会把新进程连 cgroup 一起清掉。
func TestUnsupportedWithoutNotifySocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	reason := UnsupportedReason()
	if reason == "" {
		t.Fatal("没有 NOTIFY_SOCKET 时必须报不支持，否则会退化成「新进程被 systemd 杀掉」")
	}
	if runtime.GOOS == "linux" && !strings.Contains(reason, "install.sh") {
		t.Fatalf("原因里要指出怎么修（重跑 install.sh），实际: %s", reason)
	}
	if Supported() {
		t.Fatal("UnsupportedReason 非空时 Supported 必须为 false")
	}
}

func TestSupportedOnLinuxWithNotifySocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("只有 Linux 支持套接字交接")
	}
	t.Setenv("NOTIFY_SOCKET", "/run/systemd/notify")
	if !Supported() {
		t.Fatalf("Linux + NOTIFY_SOCKET 应当支持交接，实际原因: %s", UnsupportedReason())
	}
}

// 正常启动（不是被交接起来的）时拿不到继承套接字。
func TestInheritedListenerAbsent(t *testing.T) {
	t.Setenv(listenFDEnv, "")
	if ln, ok := InheritedListener(); ok || ln != nil {
		t.Fatal("没有 LLM_RELAY_LISTEN_FD 时不该返回监听器")
	}
	// 非法值同样按「没有」处理：宁可按正常启动去 bind，也不要拿一个
	// 来路不明的 fd 当监听器（那会得到一个服务不起任何作用的进程）。
	t.Setenv(listenFDEnv, "not-a-number")
	if _, ok := InheritedListener(); ok {
		t.Fatal("非法的 fd 值不该被接受")
	}
}

// 子进程没拿到 READY_FD（正常启动）时，上报是空操作而不是报错。
func TestNotifyChildReadyWithoutFD(t *testing.T) {
	t.Setenv(readyFDEnv, "")
	if err := NotifyChildReady(); err != nil {
		t.Fatalf("没有 READY_FD 时应当静默跳过: %v", err)
	}
}

// MAINPID= 的入参校验：pid 为 0 会把「主进程」指向 init，等于自废武功。
func TestNotifyMainPIDRejectsZero(t *testing.T) {
	if err := NotifyMainPID(0); err == nil {
		t.Fatal("pid=0 必须被拒绝")
	}
}

// 没有 systemd 通知套接字时，notify 是空操作（开发机、Windows 形态）。
func TestNotifyWithoutSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := notify("READY=1"); err != nil {
		t.Fatalf("没有 NOTIFY_SOCKET 时应当静默跳过: %v", err)
	}
}

// withEnv 要覆盖同名键而不是追加：上一代交接进程的环境变量还留在
// os.Environ() 里，同一个键出现两次时取值行为依赖平台。
func TestWithEnvReplacesDuplicates(t *testing.T) {
	base := []string{"PATH=/bin", listenFDEnv + "=9", "HOME=/root", readyFDEnv + "=7"}
	got := withEnv(base, map[string]string{listenFDEnv: "3", readyFDEnv: "4"})

	count := map[string]int{}
	value := map[string]string{}
	for _, kv := range got {
		name, v, _ := strings.Cut(kv, "=")
		count[name]++
		value[name] = v
	}
	if count[listenFDEnv] != 1 || value[listenFDEnv] != "3" {
		t.Fatalf("%s 应当只出现一次且为新值，实际 %v", listenFDEnv, got)
	}
	if count[readyFDEnv] != 1 || value[readyFDEnv] != "4" {
		t.Fatalf("%s 应当只出现一次且为新值，实际 %v", readyFDEnv, got)
	}
	if value["PATH"] != "/bin" || value["HOME"] != "/root" {
		t.Fatalf("无关的变量不该被改动，实际 %v", got)
	}
}

// notify 真能写到 unixgram 套接字（Linux/macOS）：写错地址时的表现是
// 「READY=1 没送到」，systemd 90 秒后判启动失败 —— 服务起来了却显示 failed。
func TestNotifyDeliversToUnixgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上 NOTIFY_SOCKET 这条链路不存在")
	}
	sock := filepath.Join(t.TempDir(), "notify.sock")
	addr, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Skipf("当前环境不支持 unixgram: %v", err)
	}
	defer addr.Close()
	t.Setenv("NOTIFY_SOCKET", sock)

	if err := notify("MAINPID=1234"); err != nil {
		t.Fatalf("写通知套接字失败: %v", err)
	}
	buf := make([]byte, 64)
	n, _, err := addr.ReadFrom(buf)
	if err != nil {
		t.Fatalf("读通知套接字失败: %v", err)
	}
	if got := string(buf[:n]); got != "MAINPID=1234" {
		t.Fatalf("通知内容不对: %q", got)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("套接字文件应当存在: %v", err)
	}
}
