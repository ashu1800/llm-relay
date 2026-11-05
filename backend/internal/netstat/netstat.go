// Package netstat 采集本机网卡的累计收发字节，并换算成实时速率。
//
// 为什么口径选「网卡计数」而不是「网关转发的字节数」：
// 站主要的是「这台服务器的带宽用了多少」—— 它回答的是「这台机器还能不能再
// 接一个任务」，其中包含备份同步、系统更新、SSH 传输、以及所有别的进程。
// 只在 relay 的转发链路里累加（请求体 + 响应体）得到的只是「网关自己搬了多少」，
// 服务器上别的流量一概看不见 —— 那会给出一个看起来精确、却答错了问题的数字。
//
// 为什么只取**默认路由所在的那一张网卡**而不是所有网卡相加：
// Windows 上一台机器同时挂着 Hyper-V / WSL / Docker 的虚拟网卡是常态，
// 这些虚拟网卡承载的是同一份数据的二次拷贝（宿主网卡收一次、虚拟网卡再收一次），
// 全部相加会让读数凭空翻倍甚至更多。取默认路由网卡得到的才是「进出这台机器
// 的那条真实链路」。
package netstat

import (
	"errors"
	"time"
)

// ErrUnsupported 表示当前平台没有可用的网卡计数来源（例如 darwin 上的构建）。
var ErrUnsupported = errors.New("当前平台不支持网卡计数采集")

// Counter 是某张网卡在某一时刻的累计计数。
type Counter struct {
	// Interface 是网卡名（Linux 的 eth0 / Windows 的「以太网」）。
	Interface string
	RxBytes   uint64
	TxBytes   uint64
}

// Persisted 是跨进程重启保存的累计状态（2026-11-05 站主要求持久化）。
//
// 为什么不只存 RxTotal / TxTotal：只存总数的话，上次保存到进程退出之间的
// 增量只能丢掉（最多一个保存间隔）。连**最后一次成功读到的原始计数器**
// 一起存下来，重启后如果还是同一张网卡、计数器没有回退（说明机器没重启），
// 就能把这段增量补回来，一个字节都不丢。
//
// SavedAt 是那个原始计数器的读取时刻。补账时**不拿它算速率** ——
// 那段间隔是服务不在的时间，不是一秒采样窗口（见 Clock.Resume）。
type Persisted struct {
	RxTotal   uint64
	TxTotal   uint64
	RxCounter uint64
	TxCounter uint64
	Interface string
	SavedAt   time.Time
}

// readCounter 由各平台的实现（read_linux.go / read_windows.go）在 init 里挂上。
//
// 做成变量而不是带 build tag 的导出函数：这样本文件的换算逻辑、Stats 结构与
// 测试可以一份跨平台，平台差异只留在「怎么读到一张网卡的计数」这一步。
//
// 参数是运维指定的网卡名（空串 = 自动选默认路由那张网卡）。
//
// 默认值就是「不支持」：只有 linux / windows 两个实现会覆盖它，
// 其它平台（主要是 macOS 上的本地开发）因此不必再有一个空的 init ——
// Sample 会返回 Available=false 并带上 ErrUnsupported，
// 管理台据此显示「当前平台不支持」，而不是一个永远为 0 的假读数
// （后者会被读成「服务器完全没流量」）。
var readCounter = func(iface string) (Counter, error) { return Counter{}, ErrUnsupported }

// Frame 是推给前端的一帧带宽数据，也是这个包对外的线格式。
//
// 定义在领域包里而不是让 api 层用 gin.H 拼：同一形状原先在
// live.go 的实时推送、live.go 的建连补帧、以及测试的反序列化里各写了一遍，
// 三处任一处改了字段名都不会有编译错误，只有前端静默读不到值。
// History 只出现在建连补帧那一帧（omitempty 让每秒的实时帧仍是 {"latest":{…}}），
// 前端据此区分「替换整条曲线」与「追加一个点」。
type Frame struct {
	Latest  Stats   `json:"latest"`
	History []Stats `json:"history,omitempty"`
}

// Stats 是一次采样换算出的读数。
type Stats struct {
	At time.Time `json:"at"`
	// Interface 是本次读数来自哪张网卡：排障时第一个要问的问题
	// （「我明明走了专线，读数怎么是 0」—— 答案是取错了网卡）。
	Interface string `json:"interface"`
	// RxBps / TxBps 是**本次采样区间**的平均速率，单位 **字节/秒**，不是 bit/s。
	//
	// 用字节而不是比特：界面上按 KB/s、MB/s 显示时不用再除 8，少一处换算就
	// 少一处写错的机会；要显示成 Mbps 的调用方乘 8 即可。
	RxBps float64 `json:"rx_bytes_per_sec"`
	TxBps float64 `json:"tx_bytes_per_sec"`
	// RxTotal / TxTotal 是**本服务累计观测到的**字节，不是网卡自开机以来的。
	// 它会持久化到数据库，进程重启后接着上次的数字继续涨（2026-11-05）——
	// 直接拿网卡的累计值不行：机器重启 / 换网卡都会让它倒着跳。
	// 服务停机期间如果网卡计数器仍然连续，这段增量也会补进来（见 Clock.Resume）。
	RxTotal uint64 `json:"rx_total_bytes"`
	TxTotal uint64 `json:"tx_total_bytes"`
	// Available=false 表示这次没读到（权限、平台不支持、网卡消失），
	// 此时速率字段全是 0，原因在 Error 里。
	//
	// 必须与「速率为 0」区分开：空闲站点上一整夜都可能是 0，
	// 把「读不到」也显示成 0 就永远发现不了采集坏了。
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

// Clock 把逐次采样换算成速率，并维护累计字节。
//
// 累计值本身不持久化 —— 由调用方通过 Snapshot / Resume 存到数据库
// （api 层把它落在 settings 表里），本包只负责「怎么算」。
//
// 它持有上一拍的状态，因此不是并发安全的 —— 由调用方（api 层的推送循环）
// 保证只有一个 goroutine 在采样。
type Clock struct {
	// now 可替换，测试用它把「一秒」压成零成本的时间跳变
	now func() time.Time

	// iface 是运维指定的网卡名，空串表示自动选择（默认路由那张）
	iface string

	prev     Counter
	hasPrev  bool
	lastSeen time.Time

	rxTotal uint64
	txTotal uint64

	// resumed 表示上一拍是「从持久化状态恢复」的：第一次成功采样要把
	// 停机期间的增量补进累计，但不能把它当成采样窗口算速率。
	resumed bool
}

// NewClock 构造一个采样器。iface 为空时自动选择承载默认路由的网卡。
func NewClock(iface string) *Clock { return &Clock{now: time.Now, iface: iface} }

// Resume 用上次持久化的状态初始化采样器（2026-11-05）。
//
// 累计总数从 p 接着走；p 里带着完整的原始计数器信息时，把它当作「上一拍」——
// 首次 Sample 会把「上次保存 → 本次启动」之间的增量补进累计
// （要求同一张网卡且计数器没有回退），但**不计算速率**：那段间隔是服务
// 不在的时间，不是采样窗口，算出来的只会是一个没有意义的平均值。
//
// 机器重启后网卡计数器归零（cur < p.RxCounter）：此时只保留累计总数，
// 不补增量 —— 归零前后的差值不属于同一本账；那段时间的流量只能放弃。
// 换网卡（默认路由变化 / 改了 bandwidth_interface）同理。
func (c *Clock) Resume(p Persisted) {
	c.rxTotal = p.RxTotal
	c.txTotal = p.TxTotal
	if p.Interface == "" || p.SavedAt.IsZero() {
		return
	}
	c.prev = Counter{Interface: p.Interface, RxBytes: p.RxCounter, TxBytes: p.TxCounter}
	c.hasPrev = true
	c.lastSeen = p.SavedAt
	c.resumed = true
}

// Snapshot 返回可持久化的累计状态。
//
// 还没成功采样过时，计数器与网卡名留空（SavedAt 为零值）—— 调用方据此
// 只保存累计总数，下次启动仍能接着累计，只是补不了停机期间的增量。
func (c *Clock) Snapshot() Persisted {
	p := Persisted{RxTotal: c.rxTotal, TxTotal: c.txTotal}
	if c.hasPrev {
		p.RxCounter = c.prev.RxBytes
		p.TxCounter = c.prev.TxBytes
		p.Interface = c.prev.Interface
		p.SavedAt = c.lastSeen.UTC()
	}
	return p
}

// Sample 读一次网卡计数并返回换算后的读数。
//
// 它**不返回 error**：速率是持续推送的，一次读取失败不该让调用方去处理分支 ——
// 失败信息放在 Stats.Available / Error 里，推送循环可以原样转发给前端，
// 界面上就能直接说出「读不到」而不是悄悄显示 0。
func (c *Clock) Sample() Stats {
	now := c.now()
	cur, err := readCounter(c.iface)
	if err != nil {
		return Stats{
			At:        now.UTC(),
			Available: false,
			Error:     err.Error(),
			RxTotal:   c.rxTotal,
			TxTotal:   c.txTotal,
		}
	}

	st := Stats{
		At:        now.UTC(),
		Interface: cur.Interface,
		Available: true,
	}

	if c.hasPrev {
		elapsed := now.Sub(c.lastSeen).Seconds()
		// 网卡换了（默认路由切换 / 运维改了 bandwidth_interface）：两边的
		// 计数器不是同一本账，相减会得到一个无意义的增量 —— 只换基线。
		sameIface := cur.Interface == c.prev.Interface
		// 时间没走（测试里的同一拍、或系统时钟回拨）：算不出速率，
		// 也不能拿上次的旧值冒充 —— 直接按「本拍没有观测」处理。
		if elapsed > 0 && sameIface {
			// 计数器变小只有两种可能：网卡被重新枚举（切换网络）或计数器回绕。
			// 两种情况下做减法都会得到一个巨大的假速率（16 EB/s 这种），
			// 所以在阈值上直接放弃这一拍，把当前值当新基线。
			if cur.RxBytes >= c.prev.RxBytes && cur.TxBytes >= c.prev.TxBytes {
				rxDelta := cur.RxBytes - c.prev.RxBytes
				txDelta := cur.TxBytes - c.prev.TxBytes
				c.rxTotal += rxDelta
				c.txTotal += txDelta
				// 跨重启恢复的第一拍：增量照记（那些字节确实流过去了），
				// 但速率报 0 —— 间隔是服务不在的那段时间，不是采样窗口。
				if !c.resumed {
					st.RxBps = float64(rxDelta) / elapsed
					st.TxBps = float64(txDelta) / elapsed
				}
			}
		}
	}

	c.resumed = false
	c.prev = cur
	c.hasPrev = true
	c.lastSeen = now
	st.RxTotal = c.rxTotal
	st.TxTotal = c.txTotal
	return st
}
