package relay

// 正文交付观测（2026-09-30 立）：BodyTiming 与它落到日志里的两个字段。
//
// 为什么值得单独写：这两个数决定了「速度」这一列对哪些行显示数字、对哪些行显示 —。
// 判据在前端（frontend/src/components/speed.ts），但**事实**是这里记的 ——
// 记错了前端再对也白搭，而且症状很轻（只是速度那一格空了或不准），
// 不会有人注意到。所以把「怎么数」和「怎么落库」都钉住。

import (
	"testing"

	"llm-relay/internal/model"
)

// 上游逐帧返回：首块 120ms、末块 3120ms、中间若干块 → 跨度 3000ms。
func TestBodyTimingCountsReadsAndSpan(t *testing.T) {
	var b BodyTiming
	for _, at := range []int{120, 200, 900, 3120} {
		b.Observe(512, at)
	}
	if b.Reads != 4 {
		t.Errorf("应当记到 4 次读，实际 %d", b.Reads)
	}
	if b.FirstMs != 120 || b.LastMs != 3120 {
		t.Errorf("首块/末块时刻应为 120/3120，实际 %d/%d", b.FirstMs, b.LastMs)
	}
	if b.SpanMs() != 3000 {
		t.Errorf("送达跨度应为 3000ms，实际 %d", b.SpanMs())
	}
}

// 上游把整段正文一次发出：只有一次读 → 跨度 0（前端据此显示 —，不给一个假速度）。
func TestBodyTimingSingleReadHasNoSpan(t *testing.T) {
	var b BodyTiming
	b.Observe(4096, 7388)
	if b.Reads != 1 {
		t.Fatalf("应当记到 1 次读，实际 %d", b.Reads)
	}
	if b.FirstMs != 7388 || b.LastMs != 7388 {
		t.Errorf("首末块应为同一时刻，实际 %d/%d", b.FirstMs, b.LastMs)
	}
	if b.SpanMs() != 0 {
		t.Errorf("只有一次读时跨度必须是 0，实际 %d", b.SpanMs())
	}
}

// 空读不计数：读循环里 n == 0 会频繁出现（心跳、缓冲未就绪），
// 把它算进去会让 Reads 变成「循环次数」，Reads >= 2 这个判据就永远成立。
func TestBodyTimingIgnoresEmptyReads(t *testing.T) {
	var b BodyTiming
	b.Observe(0, 10)
	b.Observe(-1, 20)
	b.Observe(100, 30)
	b.Observe(0, 40)
	if b.Reads != 1 || b.FirstMs != 30 || b.LastMs != 30 {
		t.Fatalf("只应记下那一次非空读，实际 reads=%d first=%d last=%d", b.Reads, b.FirstMs, b.LastMs)
	}
}

// 首块时刻只认第一次：后续读不能把它覆盖掉，否则跨度会被越算越小。
func TestBodyTimingKeepsFirstObservation(t *testing.T) {
	var b BodyTiming
	b.Observe(10, 500)
	b.Observe(10, 900)
	b.Observe(10, 1500)
	if b.FirstMs != 500 {
		t.Fatalf("首块时刻应保持 500，实际 %d", b.FirstMs)
	}
	if b.SpanMs() != 1000 {
		t.Fatalf("跨度应为 1500-500=1000，实际 %d", b.SpanMs())
	}
}

// 落库：Timing 的四个值必须原样进到日志行，且不能被别的字段顶掉。
func TestBuildLogCarriesTiming(t *testing.T) {
	req := &RelayRequest{TraceID: "t-timing", PublicModel: "deepseek-chat"}
	res := &RelayResult{Attempt: &Attempt{StatusCode: 200}}

	entry := BuildLog(req, res, Usage{}, 200, "", Timing{
		FirstByteMs: 120,
		LastByteMs:  3120,
		BodyReads:   47,
		TotalMs:     14000,
	})

	if entry.FirstByteMs != 120 || entry.LastByteMs != 3120 || entry.BodyReads != 47 || entry.TotalMs != 14000 {
		t.Fatalf("时序四个值应原样落库，实际 first=%d last=%d reads=%d total=%d",
			entry.FirstByteMs, entry.LastByteMs, entry.BodyReads, entry.TotalMs)
	}
}

// 非流式/没观测的行：两个新字段保持 0，前端据此退回旧口径（而不是误判成一次交付）。
func TestBuildLogTimingZeroWhenNotObserved(t *testing.T) {
	req := &RelayRequest{TraceID: "t-timing-2", PublicModel: "deepseek-chat"}
	res := &RelayResult{Attempt: &Attempt{StatusCode: 200}}

	entry := BuildLog(req, res, Usage{}, 200, "", Timing{FirstByteMs: 7395, TotalMs: 7395})

	if entry.BodyReads != 0 || entry.LastByteMs != 0 {
		t.Fatalf("没观测时两个新字段应为 0，实际 reads=%d last=%d", entry.BodyReads, entry.LastByteMs)
	}
	// 这两个字段是 0 而不是 null：库表加列之前的历史行也是 0，
	// 前端只认「0 = 没有观测」这一种形态，不必再区分历史行与新行
	var zero model.RequestLog
	if zero.BodyReads != 0 || zero.LastByteMs != 0 {
		t.Fatal("零值应当是 0")
	}
}
