//go:build windows

package netstat

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func init() { readCounter = readWindows }

// readWindows 用 IP Helper API 的 MIB_IF_TABLE2 读计数器。
//
// 为什么不用 net.Interface 的统计：标准库在 Windows 上不提供收发字节数。
// GetIfTable2 是微软推荐的现代接口（取代 GetIfTable 与性能计数器），
// 一次调用就带回全部网卡的 64 位计数器，不需要 WMI 或性能计数器那类
// 需要 COM 初始化、还可能被系统性能计数器损坏拖垮的路径。
func readWindows(iface string) (Counter, error) {
	var table *windows.MibIfTable2
	if err := windows.GetIfTable2Ex(windows.MibIfTableNormal, &table); err != nil {
		return Counter{}, fmt.Errorf("读取网卡表失败: %w", err)
	}
	if table == nil {
		return Counter{}, fmt.Errorf("读取网卡表失败: 返回空表")
	}
	// 这张表由 iphlpapi 分配、必须还给 FreeMibTable，不能交给 Go 的 GC
	defer windows.FreeMibTable(unsafe.Pointer(table))

	rows := unsafe.Slice(&table.Table[0], int(table.NumEntries))

	// 运维指定了网卡名：按 Alias（「以太网 2」这种显示名）匹配，
	// 与界面上显示的名字同一口径。
	if iface != "" {
		for i := range rows {
			if windows.UTF16ToString(rows[i].Alias[:]) == iface {
				return counterFromRow(&rows[i]), nil
			}
		}
		return Counter{}, fmt.Errorf("网卡 %q 不存在；可用网卡: %s", iface,
			strings.Join(interfaceNamesWindows(rows), ", "))
	}

	if idx := defaultInterfaceIndex(); idx != 0 {
		for i := range rows {
			if rows[i].InterfaceIndex == idx {
				return counterFromRow(&rows[i]), nil
			}
		}
		// 默认路由那张网卡刚好不在表里（极少见）：退回下面的兜底
	}
	for i := range rows {
		if usableRow(&rows[i]) {
			return counterFromRow(&rows[i]), nil
		}
	}
	return Counter{}, fmt.Errorf("没有可用的非回环网卡")
}

// isFilterLayer 判断一行是不是挂在真实网卡上的过滤驱动（WFP / QoS 等）。
//
// 用 MIB_IF_ROW2.InterfaceAndOperStatusFlags 的 **FilterInterface** 位
// （0x02，见 netioapi.h 的位域定义），而不是拿描述名做字符串匹配：
//
//   - 这是结构化的、与系统语言无关的判据。描述名匹配要列出
//     「-WFP 」「-QoS 」「Extension Adapter」等一串英文片段，
//     换一个非英文系统或微软改一次驱动命名就会失效 ——
//     而且失效时**没有任何报错**，只是过滤层混进候选清单。
//   - 2026-10-23 在本机实测（GetIfTable2Ex 全表 26 行）：11 个过滤层
//     全部 flags=0x02，其余 15 行全部为 0；真网卡是 0x05
//     （HardwareInterface|ConnectorPresent），WAN Miniport 拨号是 0x10
//     （NotMediaConnected）。
//
// 查过但不行的字段（一并记下来，免得后来者再试一遍）：PhysicalAddressLength
// 与 Mtu 过滤层继承了真实网卡的取值（6 / 1500）；Type 同为 EthernetCsmacd；
// PhysicalMediumType 同样是继承的（14=802_3）；MediaConnectState、
// ConnectionType、DirectionType 对所有行都一样。
func isFilterLayer(row *windows.MibIfRow2) bool {
	const filterInterface = 0x02
	return row.InterfaceAndOperStatusFlags&filterInterface != 0
}

// isLiveLink 是「这一行代表一条真实链路」的公共判据：运营中、非回环、
// 非隧道、不是过滤层。
//
// 抽出来是因为选网卡的兜底（usableRow）与拼错误提示（interfaceNamesWindows）
// 必须用同一套判据 —— 否则最要紧的那个调用点用的可能反而是最弱的一版。
//
// 不排除 Hyper-V / WSL / Docker 的虚拟网卡：装了 WSL2 或 Docker Desktop 的
// 机器上，主机自身的流量正是经虚拟交换机出去的，此时 vEthernet 就是正确目标。
// 正常情况下也走不到兜底（默认路由那一支会先命中）。
func isLiveLink(row *windows.MibIfRow2) bool {
	if row.OperStatus != windows.IfOperStatusUp || isFilterLayer(row) {
		return false
	}
	return row.Type != windows.IF_TYPE_SOFTWARE_LOOPBACK && row.Type != windows.IF_TYPE_TUNNEL
}

// usableRow 在 isLiveLink 之上再加「跑过流量」：兜底选网卡时不该选中一张
// 从没被用过的空网卡。
func usableRow(row *windows.MibIfRow2) bool {
	return isLiveLink(row) && row.InOctets+row.OutOctets > 0
}

// interfaceNamesWindows 列出**可用**的网卡显示名，只用于拼错误提示。
//
// 为什么不能把 GetIfTable2 的全部条目都列出来：实测本机 27 个条目里只有
// **1 个**（「以太网」）是运维真正会填的名字，其余是过滤层、Teredo / 6to4 /
// IP-HTTPS / WAN Miniport 伪接口与 Loopback。把 26 个噪声名字塞进界面的
// title 里等于让用户自己从里面挑。
//
// 过滤层走 isLiveLink 的结构化判据（见 isFilterLayer）；剩下的伪接口
// 只能按名字认 —— 它们是微软几个固定的 miniport 名称，且都处在
// OperStatus != Up 或隧道类型上，isLiveLink 之后基本已经清干净了，
// 这里这张表是最后一道，只保留真正会漏过来的几条。
func interfaceNamesWindows(rows []windows.MibIfRow2) []string {
	names := make([]string, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		name := windows.UTF16ToString(row.Alias[:])
		if name == "" || !isLiveLink(row) {
			continue
		}
		if isPseudoMiniport(windows.UTF16ToString(row.Description[:])) {
			continue
		}
		names = append(names, name)
	}
	return names
}

// pseudoMiniportDesc 是 Windows 内置伪适配器的固定描述名片段。
//
// 这张表故意只留极短的几条：过滤层已经由 isFilterLayer 结构化地排除了，
// 其余伪接口的 OperStatus 普遍不是 Up、类型也多是 Tunnel。
// 名字匹配在这里只是兜住漏网之鱼，不是主判据 —— 所以不必（也不该）
// 把「-WFP 」「-QoS 」这类驱动名再抄一遍。
var pseudoMiniportDesc = []string{"WAN Miniport", "Kernel Debug", "Loopback"}

func isPseudoMiniport(desc string) bool {
	for _, bad := range pseudoMiniportDesc {
		if strings.Contains(desc, bad) {
			return true
		}
	}
	return false
}

// counterFromRow 把一行 MIB_IF_ROW2 转成本包的 Counter。
//
// InOctets / OutOctets 是**含帧头**的累计字节（与 Linux /proc/net/dev 的
// 口径一致），所以两个平台的读数可以直接比较。InUcastOctets 那一族不含
// 帧头、且不把组播算进单播里，用它会比 Linux 小一截。
func counterFromRow(row *windows.MibIfRow2) Counter {
	return Counter{
		Interface: windows.UTF16ToString(row.Alias[:]),
		RxBytes:   row.InOctets,
		TxBytes:   row.OutOctets,
	}
}

// defaultInterfaceIndex 找到承载默认路由的网卡索引（0 = 没找到）。
//
// 两个来源依次尝试：
//
//  1. GetBestInterfaceEx 问系统「去公网地址该走哪张网卡」。这是最准确的答案，
//     也是微软文档推荐的做法；但它要求给出一个目的地，而「公网地址」在
//     内网隔离环境里可能根本没有路由 —— 此时它会失败，于是走第 2 条。
//  2. GetIpForwardTable2 找前缀长度 0（默认路由）、非回环、非自动配置的那条。
//     多条默认路由时取 metric 最小的，与系统选路的规则一致。
func defaultInterfaceIndex() uint32 {
	// 只借路由表做一次本地查询，**不真的发包** —— GetBestInterfaceEx 是纯本地调用
	var idx uint32
	sa := &windows.SockaddrInet4{Addr: [4]byte{8, 8, 8, 8}}
	if err := windows.GetBestInterfaceEx(sa, &idx); err == nil && idx != 0 {
		return idx
	}

	var table *windows.MibIpForwardTable2
	// AF_UNSPEC：v4 与 v6 的默认路由一起看，纯 v6 主机也能选中
	if err := windows.GetIpForwardTable2(windows.AF_UNSPEC, &table); err != nil || table == nil {
		return 0
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))

	bestMetric := ^uint32(0)
	var bestIdx uint32
	for _, row := range table.Rows() {
		if row.Loopback != 0 || row.AutoconfigureAddress != 0 {
			continue
		}
		// DestinationPrefix.PrefixLength == 0 就是默认路由（0.0.0.0/0 或 ::/0）
		if row.DestinationPrefix.PrefixLength != 0 {
			continue
		}
		if row.Metric < bestMetric {
			bestMetric, bestIdx = row.Metric, row.InterfaceIndex
		}
	}
	return bestIdx
}
