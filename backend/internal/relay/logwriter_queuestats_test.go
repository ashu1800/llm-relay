package relay

import (
	"testing"
	"time"
)

// 队列水位与丢弃计数是看板「日志队列」状态的两组数据源
// （服务端 health 帧，见 api.liveStatsLoop）。
//
// 测试用的库连不上（见 newTestWriter）：每条入队的记录写入都会失败，
// loop 记告警后继续 —— 这正好让「写入慢/失败」与「投递」解耦，
// 本组测试只关心两件事：水位有没有如实反映队列占用，丢弃有没有如实累计。

// 正常路径：投递 N 条、队列容量正确。不等落库 —— 写入失败不影响水位语义。
func TestLogWriterQueueStatsWater(t *testing.T) {
	w := newTestWriter(8)
	defer w.Close()

	if st := w.QueueStats(); st.Capacity != 8 || st.Queued != 0 || st.Dropped != 0 {
		t.Fatalf("初始水位应为 0/8、丢弃 0，实际 %+v", st)
	}

	for i := 0; i < 3; i++ {
		w.Enqueue(mustLog(i), nil)
	}
	st := w.QueueStats()
	// 库连不上，loop 消费失败即弃，队列占用是波动的 —— 判据卡两头：
	// 水位必须大于 0（投递过）且不超过容量（物理上装不下更多）
	if st.Queued == 0 || st.Queued > 8 {
		t.Fatalf("投递 3 条后水位应落在 (0, 8]，实际 %d", st.Queued)
	}
	if st.Dropped != 0 {
		t.Fatalf("没满队列不该有丢弃，实际 %d", st.Dropped)
	}
}

// 满队投递：装不下的必须被计数（不是静默消失）。
// 丢弃计数是看板「已丢 N 条」的来源，少计一条，统计就多一分没人知道的黑洞。
//
// 不启动消费协程（直接构造 LogWriter 而不是 newTestWriter）：
// 曾经用 newTestWriter(2) 连投 10 条断言「至少丢 8」，依赖「投递期间
// loop 来不及腾出队列位」的时序假设 —— 不带 -race 时成立，带上就偶发
// 失败（实测连挂 6 次）：消费协程中途腾位后，成功入队可以远大于容量。
// 没有消费者时队列行为才是确定的：容量 2、连投 10 条，恰好丢 8、
// 水位恰为 2，断言因此从「至少」收紧到「恰好」。
// 消费协程与丢弃计数互不相干（各条 Enqueue 独立原子计数），
// 「有 loop 在跑」时的水位语义由上面的 Water 测试覆盖。
func TestLogWriterQueueStatsDroppedWhenFull(t *testing.T) {
	w := &LogWriter{queue: make(chan queuedLog, 2), capacity: 2}

	for i := 0; i < 10; i++ {
		w.Enqueue(mustLog(i), nil)
	}
	st := w.QueueStats()
	if st.Dropped != 8 {
		t.Fatalf("灌 10 条进容量 2 的队列，累计丢弃应恰好 8，实际 %d", st.Dropped)
	}
	if st.Queued != 2 {
		t.Fatalf("无人消费时队列水位应恰为容量 2，实际 %d", st.Queued)
	}
}

// 关闭后的投递同样计入丢弃（「服务正在退出」那个分支），
// 且计数与满队丢弃走的是同一个数 —— 看板只有一个「已丢」。
func TestLogWriterQueueStatsDroppedAfterClose(t *testing.T) {
	w := newTestWriter(4)
	if !w.CloseAndFlush(time.Second, nil) {
		t.Fatal("空队列 CloseAndFlush 应立即成功")
	}
	for i := 0; i < 5; i++ {
		w.Enqueue(mustLog(i), nil)
	}
	if got := w.QueueStats().Dropped; got != 5 {
		t.Fatalf("关闭后投递 5 条应全部计入丢弃，实际 %d", got)
	}
}
