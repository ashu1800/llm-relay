//go:build linux

package netstat

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func init() { readCounter = readLinux }

// readLinux 从 /proc/net/dev 读计数器、从 /proc/net/route 定位默认路由网卡。
//
// 为什么不用 syscall/netlink：本程序只需要每秒一小笔读数，
// procfs 里这两份文本既稳定又没有任何依赖；netlink 需要自己拼消息、
// 处理多段响应，为一个监控读数引入这类代码不值得。
func readLinux(iface string) (Counter, error) {
	explicit := iface != ""
	if !explicit {
		// 自动选择时，路由解析失败就继续往下走：整段失败只会在 error 里
		// 说「没有可用的非回环网卡」，那是 fallback 已经试过的结论
		var err error
		if iface, err = defaultInterfaceLinux(); err != nil {
			return Counter{}, err
		}
	}
	c, err := interfaceCounterLinux(iface)
	if err != nil {
		// 指定了网卡但读不到：把可选网卡名列出来。
		// 「配错名字」是最常见的失败，而只说「网卡 xxx 不存在」时
		// 运维还得自己 ssh 上去翻 /proc/net/dev 才知道该填什么。
		if explicit {
			return Counter{}, fmt.Errorf("%w；可用网卡: %s", err, strings.Join(interfaceNamesLinux(), ", "))
		}
		return Counter{}, err
	}
	return c, nil
}

// interfaceNamesLinux 列出可用的网卡名，只用于拼错误提示。
//
// 与 Windows 侧同一个理由：/proc/net/dev 里混着 lo、docker0、veth*、
// br-* 这些永远是 0 的虚拟网卡，全列出来等于给用户一份噪声清单。
// 这里按「非 lo + 有过流量」筛：真实网卡只要跑过任何流量就会留下非零计数，
// 而一个刚装好、从没跑过流量的主机上清单为空 —— 那种情况下用户本来也
// 只能靠 ip addr 去查，列表给不出更多信息。
func interfaceNamesLinux() []string {
	counters, err := readProcNetDev()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(counters))
	for _, c := range counters {
		if c.Interface == "lo" {
			continue
		}
		if c.RxBytes+c.TxBytes == 0 {
			continue
		}
		names = append(names, c.Interface)
	}
	return names
}

// defaultInterfaceLinux 返回承载默认路由的网卡名。
//
// 读不到默认路由时（例如只有 IPv6 默认路由的纯 v6 主机）退回到
// 「第一张 UP 状态、非 lo、有非零流量的网卡」：这时宁可给出一个近似答案，
// 也好过让整个读数变成「不可用」—— 绝大多数部署都是双栈或纯 v4。
func defaultInterfaceLinux() (string, error) {
	if name := defaultRouteLinux(); name != "" {
		return name, nil
	}
	return busiestInterfaceLinux()
}

// defaultRouteLinux 解析 /proc/net/route，找目的地址为 0.0.0.0 的那条。
//
// 列定义见 `man 5 proc_pid_net_route`。Destination 与 Mask 是**小端**十六进制，
// 全零的 Destination 才配得上「默认路由」；同时要求 Flags 带 RTF_UP(0x1)
// 且 Gateway 非零 —— 没有网关的 0.0.0.0 条目是点对点链路，
// 服务器上还可能是 Tailscale/WireGuard 之类由用户态接管的虚拟网卡，
// 把它当默认网卡会让读数跟着 VPN 走。
func defaultRouteLinux() string {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Scan() // 跳过表头
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 {
			continue
		}
		if fields[1] != "00000000" || fields[2] == "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil || flags&0x1 == 0 {
			continue
		}
		return fields[0]
	}
	return ""
}

// busiestInterfaceLinux 是默认路由缺失时的兜底：取累计流量最大的非 lo 网卡。
//
// 用流量而不是「第一张 UP 的」来挑：纯 v6 主机上 lo 之外往往还挂着
// docker0、veth* 这些永远是 0 的网卡，按顺序取第一张会稳定地选中它们。
func busiestInterfaceLinux() (string, error) {
	counters, err := readProcNetDev()
	if err != nil {
		return "", err
	}
	best := ""
	var bestBytes uint64
	for _, c := range counters {
		if c.Interface == "lo" || c.Interface == "" {
			continue
		}
		if sum := c.RxBytes + c.TxBytes; sum > bestBytes {
			best, bestBytes = c.Interface, sum
		}
	}
	if best == "" {
		return "", fmt.Errorf("没有可用的非回环网卡")
	}
	return best, nil
}

func interfaceCounterLinux(name string) (Counter, error) {
	counters, err := readProcNetDev()
	if err != nil {
		return Counter{}, err
	}
	for _, c := range counters {
		if c.Interface == name {
			return c, nil
		}
	}
	return Counter{}, fmt.Errorf("网卡 %s 不存在", name)
}

// readProcNetDev 解析 /proc/net/dev。
//
// 每行形如：
//
//	eth0: 1234 5 0 0 0 0 0 0  6789 10 0 0 0 0 0 0
//
// 冒号左边是网卡名（可能含空格，所以要按最后一个冒号切），右边 16 列分别是
// 收/发的 bytes、packets、errs、drop、fifo、frame、compressed、multicast。
// 本包只关心第 1 列（收字节）与第 9 列（发字节）。
func readProcNetDev() ([]Counter, error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Counter
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		idx := strings.LastIndexByte(line, ':')
		if idx < 0 {
			continue // 两行表头
		}
		name := strings.TrimSpace(line[:idx])
		fields := strings.Fields(line[idx+1:])
		if len(fields) < 16 {
			continue
		}
		rx, err1 := strconv.ParseUint(fields[0], 10, 64)
		tx, err2 := strconv.ParseUint(fields[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, Counter{Interface: name, RxBytes: rx, TxBytes: tx})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
