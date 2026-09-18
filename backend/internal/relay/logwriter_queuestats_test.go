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
func TestLogWriterQueueStatsDroppedWhenFull(t *testing.T) {
	w := newTestWriter(2)
	defer w.Close()

	// 先把队列灌满再投递溢出的：loop 可能并发消费，所以不用固定条数断言，
	// 而是投到「必然溢出」为止 —— 队列容量 2，连投 10 条，至少 8 条入不了队。
	// 每条入不了队的都会走 warnDropped → dropped + 1。
	for i := 0; i < 10; i++ {
		w.Enqueue(mustLog(i), nil)
	}
	// loop 消费（失败）需要一点时间；丢弃计数单调不减，等一轮再读，
	// 避免「断言时还没投满」的竞态
	deadline := time.Now().Add(2 * time.Second)
	for {
		if w.QueueStats().Dropped >= 8 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("灌 10 条进容量 2 的队列，累计丢弃应至少 8，实际 %d", w.QueueStats().Dropped)
		}
		time.Sleep(10 * time.Millisecond)
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
