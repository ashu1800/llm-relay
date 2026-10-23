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
