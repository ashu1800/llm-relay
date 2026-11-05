package netstat

import (
	"math"
	"testing"
	"time"
)

// 换算逻辑的单元测试：读网卡那一步依赖操作系统，没法跨平台测，
// 但「两次计数 → 速率」这一段是纯算术，也是唯一容易写错的地方
// （除数用错、回绕处理成负数、时间没走却照算）。

// stubReader 替换平台实现，返回脚本化的计数序列。
func stubReader(t *testing.T, seq ...Counter) {
	t.Helper()
	orig := readCounter
	readCounter = func(_ string) (Counter, error) {
		if len(seq) == 0 {
			return Counter{}, ErrUnsupported
		}
		c := seq[0]
		seq = seq[1:]
		return c, nil
	}
	t.Cleanup(func() { readCounter = orig })
}

// at 是可控时钟：每次调用把时间推进固定的量。
func at(t *testing.T, start time.Time, step time.Duration) func() time.Time {
	t.Helper()
	cur := start
	return func() time.Time {
		v := cur
		cur = cur.Add(step)
		return v
	}
}

func TestClockRateFromCounterDelta(t *testing.T) {
	stubReader(t,
		Counter{Interface: "eth0", RxBytes: 1_000, TxBytes: 2_000},
		Counter{Interface: "eth0", RxBytes: 1_000 + 4096, TxBytes: 2_000 + 2048},
	)
	c := NewClock("")
	c.now = at(t, time.Unix(1_700_000_000, 0), time.Second)

	first := c.Sample()
	if !first.Available {
		t.Fatalf("首拍应可用：%+v", first)
	}
	// 首拍没有上一拍可比，速率必须是 0 而不是「累计值除以某个猜出来的时间」
	if first.RxBps != 0 || first.TxBps != 0 {
		t.Fatalf("首拍速率应为 0，实际 rx=%.2f tx=%.2f", first.RxBps, first.TxBps)
	}
	if first.RxTotal != 0 || first.TxTotal != 0 {
		t.Fatalf("首拍累计应为 0（只统计进程观测到的增量），实际 rx=%d tx=%d", first.RxTotal, first.TxTotal)
	}

	second := c.Sample()
	if second.RxBps != 4096 || second.TxBps != 2048 {
		t.Fatalf("1 秒内 4096/2048 字节应换算成同值 B/s，实际 rx=%.2f tx=%.2f", second.RxBps, second.TxBps)
	}
	if second.RxTotal != 4096 || second.TxTotal != 2048 {
		t.Fatalf("累计值不对：rx=%d tx=%d", second.RxTotal, second.TxTotal)
	}
	if second.Interface != "eth0" {
		t.Fatalf("网卡名应原样透出，实际 %q", second.Interface)
	}
}

func TestClockRateUsesElapsedTime(t *testing.T) {
	stubReader(t,
		Counter{Interface: "eth0"},
		Counter{Interface: "eth0", RxBytes: 800, TxBytes: 400},
	)
	c := NewClock("")
	// 两拍之间隔了 2 秒：800 字节 / 2s = 400 B/s
	c.now = at(t, time.Unix(1_700_000_000, 0), 2*time.Second)

	c.Sample()
	got := c.Sample()
	if got.RxBps != 400 || got.TxBps != 200 {
		t.Fatalf("应按实际间隔换算，期望 rx=400 tx=200，实际 rx=%.2f tx=%.2f", got.RxBps, got.TxBps)
	}
}

// 计数器变小 = 网卡被重新枚举或计数器回绕。做减法会得到 16 EB/s 这种假读数，
// 必须放弃这一拍并把它当新基线。
func TestClockCounterResetDoesNotProduceHugeRate(t *testing.T) {
	stubReader(t,
		Counter{Interface: "eth0", RxBytes: 10_000_000, TxBytes: 10_000_000},
		Counter{Interface: "eth0", RxBytes: 100, TxBytes: 200},
		Counter{Interface: "eth0", RxBytes: 1_100, TxBytes: 1_200},
	)
	c := NewClock("")
	c.now = at(t, time.Unix(1_700_000_000, 0), time.Second)

	c.Sample()
	reset := c.Sample()
	if reset.RxBps != 0 || reset.TxBps != 0 {
		t.Fatalf("计数器回退的那一拍应报 0，实际 rx=%.2f tx=%.2f", reset.RxBps, reset.TxBps)
	}
	// 累计值也不能被这一步污染
	if reset.RxTotal != 0 || reset.TxTotal != 0 {
		t.Fatalf("回退的那一拍不该记账，实际 rx=%d tx=%d", reset.RxTotal, reset.TxTotal)
	}

	next := c.Sample()
	if next.RxBps != 1000 || next.TxBps != 1000 {
		t.Fatalf("回退之后应以新值为基线继续换算，实际 rx=%.2f tx=%.2f", next.RxBps, next.TxBps)
	}
}

// 时间没有推进（同一拍被采样两次、或系统时钟回拨到同一刻度）时算不出速率。
func TestClockZeroElapsed(t *testing.T) {
	stubReader(t,
		Counter{Interface: "eth0"},
		Counter{Interface: "eth0", RxBytes: 5_000},
	)
	c := NewClock("")
	fixed := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return fixed }

	c.Sample()
	got := c.Sample()
	if got.RxBps != 0 || got.TxBps != 0 {
		t.Fatalf("间隔为 0 时不得给出速率，实际 rx=%.2f tx=%.2f", got.RxBps, got.TxBps)
	}
	if math.IsInf(got.RxBps, 0) || math.IsNaN(got.RxBps) {
		t.Fatalf("间隔为 0 时算出了 Inf/NaN")
	}
}

// 跨重启恢复（2026-11-05）：累计从上次的数字接着走；同一张网卡且计数器
// 没有回退时，把「上次保存 → 本次启动」的增量补进来，但速率报 0 ——
// 那段间隔是服务不在的时间，不是采样窗口。
func TestClockResumeAddsGapBytesWithoutRate(t *testing.T) {
	stubReader(t, Counter{Interface: "eth0", RxBytes: 10_000, TxBytes: 5_000})
	savedAt := time.Unix(1_700_000_000, 0)
	c := NewClock("")
	c.now = func() time.Time { return savedAt.Add(10 * time.Second) }
	c.Resume(Persisted{
		RxTotal: 1_000_000, TxTotal: 2_000_000,
		RxCounter: 4_000, TxCounter: 2_000,
		Interface: "eth0", SavedAt: savedAt,
	})

	got := c.Sample()
	if got.RxTotal != 1_006_000 || got.TxTotal != 2_003_000 {
		t.Fatalf("停机期间的增量应补进累计，期望 rx=1006000 tx=2003000，实际 rx=%d tx=%d", got.RxTotal, got.TxTotal)
	}
	if got.RxBps != 0 || got.TxBps != 0 {
		t.Fatalf("跨重启的第一拍不该算速率（间隔不是采样窗口），实际 rx=%.2f tx=%.2f", got.RxBps, got.TxBps)
	}
}

// 机器重启 / 网卡重新枚举后计数器归零：只保留累计总数，不补增量 ——
// 归零前后的差值不属于同一本账。
func TestClockResumeIgnoresCounterReset(t *testing.T) {
	stubReader(t, Counter{Interface: "eth0", RxBytes: 100, TxBytes: 50})
	c := NewClock("")
	c.now = func() time.Time { return time.Unix(1_700_000_100, 0) }
	c.Resume(Persisted{
		RxTotal: 5_000, TxTotal: 6_000,
		RxCounter: 10_000, TxCounter: 20_000,
		Interface: "eth0", SavedAt: time.Unix(1_700_000_000, 0),
	})

	got := c.Sample()
	if got.RxTotal != 5_000 || got.TxTotal != 6_000 {
		t.Fatalf("计数器回退时累计应原样保留，实际 rx=%d tx=%d", got.RxTotal, got.TxTotal)
	}
	if got.RxBps != 0 || got.TxBps != 0 {
		t.Fatalf("回退的那一拍速率应为 0，实际 rx=%.2f tx=%.2f", got.RxBps, got.TxBps)
	}
}

// 换了网卡（默认路由变化 / 改了 bandwidth_interface）时，两边的计数器
// 不是同一本账：保留累计总数，但不拿旧网卡的计数当基线。
func TestClockResumeIgnoresInterfaceChange(t *testing.T) {
	stubReader(t, Counter{Interface: "eth1", RxBytes: 10_000, TxBytes: 5_000})
	c := NewClock("")
	c.now = func() time.Time { return time.Unix(1_700_000_100, 0) }
	c.Resume(Persisted{
		RxTotal: 5_000, TxTotal: 6_000,
		RxCounter: 4_000, TxCounter: 2_000,
		Interface: "eth0", SavedAt: time.Unix(1_700_000_000, 0),
	})

	got := c.Sample()
	if got.RxTotal != 5_000 || got.TxTotal != 6_000 {
		t.Fatalf("换网卡时累计应原样保留，实际 rx=%d tx=%d", got.RxTotal, got.TxTotal)
	}
	if got.RxBps != 0 || got.TxBps != 0 {
		t.Fatalf("跨网卡的第一拍速率应为 0，实际 rx=%.2f tx=%.2f", got.RxBps, got.TxBps)
	}
}

// 正常运行中默认路由换了网卡，也不能跨网卡相减；新网卡从当前值起算。
func TestClockInterfaceChangeStartsNewBaseline(t *testing.T) {
	stubReader(t,
		Counter{Interface: "eth0", RxBytes: 10_000, TxBytes: 10_000},
		Counter{Interface: "eth1", RxBytes: 1_000_000, TxBytes: 2_000_000},
		Counter{Interface: "eth1", RxBytes: 1_001_000, TxBytes: 2_000_500},
	)
	c := NewClock("")
	c.now = at(t, time.Unix(1_700_000_000, 0), time.Second)

	c.Sample()
	switched := c.Sample()
	if switched.RxBps != 0 || switched.TxBps != 0 {
		t.Fatalf("换网卡那一拍不该相减，实际 rx=%.2f tx=%.2f", switched.RxBps, switched.TxBps)
	}
	if switched.RxTotal != 0 || switched.TxTotal != 0 {
		t.Fatalf("换网卡不该产生累计增量，实际 rx=%d tx=%d", switched.RxTotal, switched.TxTotal)
	}

	next := c.Sample()
	if next.RxBps != 1000 || next.TxBps != 500 {
		t.Fatalf("新网卡应从当前值起算，期望 rx=1000 tx=500，实际 rx=%.2f tx=%.2f", next.RxBps, next.TxBps)
	}
}

// Snapshot 要把累计与「最后一拍成功读到的原始计数」一起交出来 ——
// 后者是重启后补账的唯一依据。
func TestClockSnapshot(t *testing.T) {
	stubReader(t,
		Counter{Interface: "eth0", RxBytes: 1_000, TxBytes: 2_000},
		Counter{Interface: "eth0", RxBytes: 3_000, TxBytes: 4_000},
	)
	c := NewClock("")
	base := time.Unix(1_700_000_000, 0)
	c.now = at(t, base, time.Second)
	c.Sample()
	c.Sample()

	p := c.Snapshot()
	if p.RxTotal != 2_000 || p.TxTotal != 2_000 {
		t.Fatalf("累计不对：rx=%d tx=%d", p.RxTotal, p.TxTotal)
	}
	if p.RxCounter != 3_000 || p.TxCounter != 4_000 {
		t.Fatalf("原始计数应是最后一拍的值：rx=%d tx=%d", p.RxCounter, p.TxCounter)
	}
	if p.Interface != "eth0" {
		t.Fatalf("网卡名不对：%q", p.Interface)
	}
	if !p.SavedAt.Equal(base.Add(time.Second)) {
		t.Fatalf("SavedAt 应是最后一拍的时刻，实际 %v", p.SavedAt)
	}
}

// 还没采样过就退出：快照只带累计（可能来自 Resume），计数器信息留空，
// 下次启动仍能接着累计，只是补不了停机期间的增量。
func TestClockSnapshotBeforeFirstSample(t *testing.T) {
	c := NewClock("")
	c.Resume(Persisted{RxTotal: 123, TxTotal: 456})
	p := c.Snapshot()
	if p.RxTotal != 123 || p.TxTotal != 456 {
		t.Fatalf("累计应保留，实际 rx=%d tx=%d", p.RxTotal, p.TxTotal)
	}
	if p.Interface != "" || !p.SavedAt.IsZero() {
		t.Fatalf("没有采样时不该带计数器信息：%+v", p)
	}
}

// 读取失败要与「速率为 0」区分开：前者要能在界面上说出来。
func TestClockUnavailableIsDistinguishable(t *testing.T) {
	stubReader(t) // 空序列 → 返回 ErrUnsupported
	c := NewClock("")
	c.now = at(t, time.Unix(1_700_000_000, 0), time.Second)

	got := c.Sample()
	if got.Available {
		t.Fatal("读取失败时 Available 应为 false")
	}
	if got.Error == "" {
		t.Fatal("读取失败时应带上原因，否则界面上无法区分「空闲」与「采集坏了」")
	}
}

// 失败之后恢复：累计值不该被清掉（那是进程内真实的观测），
// 速率按「上一次成功采样」到现在这一整段求平均。
//
// 为什么不是「恢复的第一拍报 0」：字节在那段读不到的时间里**确实流过去了**，
// 而计数器是单调累计的，上一次成功的读数仍然是一个有效基线。按整段平均
// 得到的数字比 0 更接近事实 —— 报 0 会让界面上出现一个凭空的流量空档。
// （代价是长故障后这一拍是「平均值」而不是「瞬时值」，但故障本身
// 已经通过 Available=false 的那几帧显示出来了。）
func TestClockRecoversAfterFailure(t *testing.T) {
	orig := readCounter
	defer func() { readCounter = orig }()
	calls := 0
	readCounter = func(_ string) (Counter, error) {
		calls++
		switch calls {
		case 1:
			return Counter{Interface: "eth0", RxBytes: 1_000}, nil
		case 2:
			return Counter{}, ErrUnsupported
		default:
			return Counter{Interface: "eth0", RxBytes: 3_000}, nil
		}
	}
	c := NewClock("")
	c.now = at(t, time.Unix(1_700_000_000, 0), time.Second)

	c.Sample()
	c.Sample() // 失败
	recovered := c.Sample()
	if !recovered.Available {
		t.Fatalf("恢复后应可用：%+v", recovered)
	}
	// 两拍之间的 2000 字节摊到 2 秒上
	if recovered.RxBps != 1000 {
		t.Fatalf("恢复时应按上一拍成功基线平均，期望 1000 B/s，实际 %.2f", recovered.RxBps)
	}
	if recovered.RxTotal != 2000 {
		t.Fatalf("失败期间的字节应补记进累计，实际 %d", recovered.RxTotal)
	}
}
