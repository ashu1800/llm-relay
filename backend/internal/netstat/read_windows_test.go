//go:build windows

package netstat

import (
	"testing"

	"golang.org/x/sys/windows"
)

// 网卡筛选判据的回归。喂的是 2026-10-23 在本机 GetIfTable2Ex 抓到的真实行，
// 不是构造的假数据 —— 这条判据要挡的就是真实存在的噪声。
//
// 关键的一条是过滤层：11 个过滤层全部 InterfaceAndOperStatusFlags=0x02
// （FilterInterface），真网卡是 0x05，其余伪接口为 0x00 / 0x10。
// 这条用例锁住「过滤层必须被 isFilterLayer 拦下」，而不是靠描述名里的
// 「-WFP」「-QoS」字样 —— 后者在非英文系统上会失效。

func row(alias, desc string, flags uint8, oper uint32, ifType uint32) windows.MibIfRow2 {
	r := windows.MibIfRow2{InterfaceAndOperStatusFlags: flags, OperStatus: oper, Type: ifType}
	copy(r.Alias[:], windows.StringToUTF16(alias))
	copy(r.Description[:], windows.StringToUTF16(desc))
	return r
}

func TestIsFilterLayer(t *testing.T) {
	// 实测的过滤层行：flags 全是 0x02
	filter := row("以太网-WFP Native MAC Layer LightWeight Filter-0000",
		"Realtek PCIe GbE Family Controller-WFP Native", 0x02, 1, windows.IF_TYPE_ETHERNET_CSMACD)
	if !isFilterLayer(&filter) {
		t.Error("FilterInterface 位为 1 的行没被识别成过滤层")
	}
	if isLiveLink(&filter) {
		t.Error("过滤层不该被当作真实链路")
	}

	// 实测的真网卡：flags=0x05（HardwareInterface|ConnectorPresent）
	real := row("以太网", "Realtek PCIe GbE Family Controller", 0x05, 1, windows.IF_TYPE_ETHERNET_CSMACD)
	if isFilterLayer(&real) {
		t.Error("真网卡被误判成过滤层")
	}
	if !isLiveLink(&real) {
		t.Error("运营中的真网卡应算真实链路")
	}
	if !usableRow(&real) {
		// 未跑过流量时 usableRow 为 false 是对的；这里补上流量再判
		real.InOctets, real.OutOctets = 1, 1
		if !usableRow(&real) {
			t.Error("跑过流量的真网卡应可作兜底候选")
		}
	}
}

// 回环与隧道无论 flags 如何都要排除。
func TestIsLiveLinkExcludesLoopbackAndTunnel(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  windows.MibIfRow2
	}{
		{"loopback", row("Loopback Pseudo-Interface 1", "Software Loopback Interface 1", 0, 1, windows.IF_TYPE_SOFTWARE_LOOPBACK)},
		{"tunnel", row("Teredo Tunneling Pseudo-Interface", "Microsoft Teredo Tunneling Adapter", 0, 1, windows.IF_TYPE_TUNNEL)},
		{"down", row("本地连接* 1", "WAN Miniport (SSTP)", 0x10, windows.IfOperStatusDown, windows.IF_TYPE_TUNNEL)},
	} {
		if isLiveLink(&tc.row) {
			t.Errorf("%s 不该算真实链路", tc.name)
		}
	}
}

// 错误提示里的网卡清单只该剩下真实网卡（本机实测 27 行里只有 1 个）。
func TestInterfaceNamesWindowsFiltersNoise(t *testing.T) {
	rows := []windows.MibIfRow2{
		row("以太网", "Realtek PCIe GbE Family Controller", 0x05, 1, windows.IF_TYPE_ETHERNET_CSMACD),
		row("以太网-WFP Native MAC Layer LightWeight Filter-0000",
			"Realtek PCIe GbE Family Controller-WFP Native MAC Layer LightWeight Filter-0000", 0x02, 1, windows.IF_TYPE_ETHERNET_CSMACD),
		row("以太网-QoS Packet Scheduler-0000",
			"Realtek PCIe GbE Family Controller-QoS Packet Scheduler-0000", 0x02, 1, windows.IF_TYPE_ETHERNET_CSMACD),
		row("vSwitch (FSE Switch)-Hyper-V Virtual Switch Extension Filter-0000",
			"Hyper-V Virtual Switch Extension Adapter-Hyper-V Virtual Switch Extension Filter", 0x02, 1, windows.IF_TYPE_ETHERNET_CSMACD),
		row("本地连接* 6", "WAN Miniport (IP)", 0x00, 1, windows.IF_TYPE_ETHERNET_CSMACD),
		row("Loopback Pseudo-Interface 1", "Software Loopback Interface 1", 0, 1, windows.IF_TYPE_SOFTWARE_LOOPBACK),
		row("Teredo Tunneling Pseudo-Interface", "Microsoft Teredo Tunneling Adapter", 0, 6, windows.IF_TYPE_TUNNEL),
		row("", "no alias", 0x05, 1, windows.IF_TYPE_ETHERNET_CSMACD),
	}
	got := interfaceNamesWindows(rows)
	if len(got) != 1 || got[0] != "以太网" {
		t.Fatalf("应只剩「以太网」，实际 %v", got)
	}
}
