package relay

import (
	"context"
	"testing"
	"time"
)

func TestRateLimiterAllowsUpToLimit(t *testing.T) {
	rl := NewRateLimiter()
	now := time.Unix(1700000000, 0)

	for i := 0; i < 3; i++ {
		if ok, _ := rl.Allow(1, 3, now); !ok {
			t.Fatalf("第 %d 次应放行", i+1)
		}
	}
	ok, wait := rl.Allow(1, 3, now)
	if ok {
		t.Fatal("超出限额应拒绝")
	}
	if wait <= 0 {
		t.Fatal("拒绝时应给出建议等待时间")
	}
	// 不同密钥互不影响
	if ok, _ := rl.Allow(2, 3, now); !ok {
		t.Fatal("另一个密钥不应受牵连")
	}
}

// 同一把密钥在不同调用方给的额度下应各自生效，额度不固化在限流器里。
func TestRateLimiterPerCallLimit(t *testing.T) {
	rl := NewRateLimiter()
	now := time.Unix(1700000000, 0)
	// 先用低额度把窗口填满
	if ok, _ := rl.Allow(1, 1, now); !ok {
		t.Fatal("首次应放行")
	}
	if ok, _ := rl.Allow(1, 1, now); ok {
		t.Fatal("额度 1 时第二次应拒绝")
	}
	// 同键改用更高额度（例如管理员刚调大限额），应立刻放行
	if ok, _ := rl.Allow(1, 5, now); !ok {
		t.Fatal("提高额度后应放行")
	}
}

// 被拒绝的请求不应计数，否则持续重试会把窗口永久撑满。
func TestRateLimiterRejectedDoesNotCount(t *testing.T) {
	rl := NewRateLimiter()
	now := time.Unix(1700000000, 0)
	rl.Allow(1, 2, now)
	rl.Allow(1, 2, now)
	for i := 0; i < 5; i++ {
		if ok, _ := rl.Allow(1, 2, now); ok {
			t.Fatal("已超限不应再放行")
		}
	}
	later := now.Add(61 * time.Second)
	if ok, _ := rl.Allow(1, 2, later); !ok {
		t.Fatal("窗口滑过后应重新放行")
	}
}

// 逐秒推进跨越整分钟时，所有桶都要被清掉，否则额度不会恢复。
func TestRateLimiterWindowSlidesSecondBySecond(t *testing.T) {
	rl := NewRateLimiter()
	base := time.Unix(1700000000, 0)
	rl.Allow(1, 2, base)
	rl.Allow(1, 2, base)

	for i := 1; i <= 60; i++ {
		rl.Allow(1, 2, base.Add(time.Duration(i)*time.Second))
	}
	if ok, _ := rl.Allow(1, 2, base.Add(61*time.Second)); !ok {
		t.Fatal("60 秒后应恢复额度")
	}
}

func TestRateLimiterDisabled(t *testing.T) {
	rl := NewRateLimiter()
	now := time.Unix(1700000000, 0)
	for i := 0; i < 100; i++ {
		if ok, _ := rl.Allow(1, 0, now); !ok {
			t.Fatal("limit<=0 表示不限制")
		}
	}
	var nilRL *RateLimiter
	if ok, _ := nilRL.Allow(1, 10, now); !ok {
		t.Fatal("空限流器应放行")
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		in   string
		want time.Duration
	}{
		{"30", 30 * time.Second},
		{"0", 0},
		{"-5", 0},
		{"", 0},
		{"abc", 0},
		{"999999", 24 * time.Hour}, // 离谱数值要封顶，避免渠道被长期摘掉
		{"Fri, 12 Sep 2026 10:00:30 GMT", 30 * time.Second},
		{"Fri, 12 Sep 2026 09:59:00 GMT", 0}, // 过去的时间不等待
	}
	for _, c := range cases {
		if got := ParseRetryAfter(c.in, now); got != c.want {
			t.Errorf("ParseRetryAfter(%q) = %v, 期望 %v", c.in, got, c.want)
		}
	}
}

func TestChannelStateCooldown(t *testing.T) {
	cs := NewChannelState()
	now := time.Now()
	cs.Cooldown(7, 30*time.Second)

	if _, in := cs.InCooldown(7, now); !in {
		t.Fatal("刚设置的冷却应生效")
	}
	if _, in := cs.InCooldown(8, now); in {
		t.Fatal("其他渠道不应受影响")
	}
	if _, in := cs.InCooldown(7, now.Add(31*time.Second)); in {
		t.Fatal("冷却到期后应恢复")
	}
}

// 短的冷却不应把已经更长的冷却缩短。
func TestChannelStateCooldownNotShortened(t *testing.T) {
	cs := NewChannelState()
	cs.Cooldown(1, 60*time.Second)
	cs.Cooldown(1, time.Second)
	if left, _ := cs.InCooldown(1, time.Now()); left < 50*time.Second {
		t.Fatalf("较长冷却被缩短了，剩余 %v", left)
	}
	cs.ClearCooldown(1)
	if _, in := cs.InCooldown(1, time.Now()); in {
		t.Fatal("手动恢复应立即生效")
	}
}

// 温和熔断的计数行为：NoteFailure 累加连击、NoteSuccess 一次清零。
// 触发冷却的阈值判断在 Service.markChannelFailure 里，这里只钉住计数本身。
func TestChannelStateFailStreak(t *testing.T) {
	cs := NewChannelState()
	if got := cs.NoteFailure(1); got != 1 {
		t.Fatalf("首次失败连击应为 1，实际 %d", got)
	}
	if got := cs.NoteFailure(1); got != 2 {
		t.Fatalf("连续失败连击应为 2，实际 %d", got)
	}
	// 成功一次即完全康复
	cs.NoteSuccess(1)
	if got := cs.NoteFailure(1); got != 1 {
		t.Fatalf("成功后连击应重新从 1 计，实际 %d", got)
	}
	// 渠道之间互不影响
	cs.NoteFailure(2)
	if got := cs.NoteFailure(1); got != 2 {
		t.Fatalf("其他渠道的失败不应影响本渠道连击，实际 %d", got)
	}
}

// 手动恢复冷却不应顺手清掉连击计数 —— 两者语义不同：
// 冷却到期/清除只是「给一次机会」，连击记录着它最近的健康状况。
func TestChannelStateClearCooldownKeepsStreak(t *testing.T) {
	cs := NewChannelState()
	cs.NoteFailure(1)
	cs.NoteFailure(1)
	cs.ClearCooldown(1)
	if got := cs.NoteFailure(1); got != 3 {
		t.Fatalf("清除冷却后连击应继续累加，实际 %d", got)
	}
}

func TestChannelStateConcurrency(t *testing.T) {
	cs := NewChannelState()
	if !cs.Acquire(1, 2) || !cs.Acquire(1, 2) {
		t.Fatal("限额内应拿到名额")
	}
	if cs.Acquire(1, 2) {
		t.Fatal("超出并发上限应被拒绝")
	}
	if cs.Inflight(1) != 2 {
		t.Fatalf("在途数应为 2，实际 %d", cs.Inflight(1))
	}
	cs.Release(1)
	if !cs.Acquire(1, 2) {
		t.Fatal("释放后应能再拿名额")
	}
	// max<=0 表示不限
	for i := 0; i < 50; i++ {
		if !cs.Acquire(2, 0) {
			t.Fatal("未配置上限时不应拒绝")
		}
	}
	// 多余的 Release 不应把计数压成负数
	for i := 0; i < 10; i++ {
		cs.Release(1)
	}
	if cs.Inflight(1) != 0 {
		t.Fatalf("在途数不应为负，实际 %d", cs.Inflight(1))
	}
}

// 名额被占满时应在超时后放弃等待，而不是永久阻塞。
func TestChannelStateAcquireWait(t *testing.T) {
	cs := NewChannelState()
	cs.Acquire(1, 1)

	start := time.Now()
	if cs.AcquireWait(context.Background(), 1, 1, 200*time.Millisecond) {
		t.Fatal("名额已满时不应立即成功")
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("应在超时后才返回，实际耗时 %v", elapsed)
	}

	cs.Release(1)
	if !cs.AcquireWait(context.Background(), 1, 1, time.Second) {
		t.Fatal("释放后应能拿到名额")
	}

	// 未配置上限时直接放行
	if !cs.AcquireWait(context.Background(), 9, 0, time.Second) {
		t.Fatal("未配置上限应直接放行")
	}

	// 客户端已断开时应立即返回，不必等满超时
	cs2 := NewChannelState()
	cs2.Acquire(5, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	begin := time.Now()
	if cs2.AcquireWait(ctx, 5, 1, 5*time.Second) {
		t.Fatal("上下文已取消时不应成功")
	}
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("上下文取消后应立即返回，实际耗时 %v", elapsed)
	}
}

func TestChannelMaxConcurrencyParsing(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want int
	}{
		{"空", nil, 0},
		{"无键", map[string]any{"a": 1}, 0},
		// jsonb 取出来的整数是 float64，必须容忍
		{"float64", map[string]any{"max_concurrency": float64(8)}, 8},
		{"int", map[string]any{"max_concurrency": 4}, 4},
		{"字符串", map[string]any{"max_concurrency": "8"}, 0},
	}
	for _, c := range cases {
		if got := ChannelMaxConcurrency(c.in); got != c.want {
			t.Errorf("%s: 得到 %d，期望 %d", c.name, got, c.want)
		}
	}
}

func TestRateLimiterSweep(t *testing.T) {
	rl := NewRateLimiter()
	now := time.Unix(1700000000, 0)
	rl.Allow(1, 10, now)
	rl.Allow(2, 10, now.Add(10*time.Minute))
	// 键 1 的窗口已过期，应被清掉；键 2 的保留
	rl.Sweep(now.Add(10 * time.Minute))
	rl.mu.Lock()
	n := len(rl.windows)
	rl.mu.Unlock()
	if n != 1 {
		t.Fatalf("清理后应只剩 1 个窗口，实际 %d", n)
	}
}

// 全局闸门在没有配置上限时应完全放行，且 Release 多余调用不出错。
func TestConcurrencyGate(t *testing.T) {
	unlimited := NewConcurrencyGate(0)
	for i := 0; i < 100; i++ {
		if err := unlimited.Acquire(context.Background()); err != nil {
			t.Fatalf("未配置上限不应阻塞: %v", err)
		}
	}
	unlimited.Release()

	g := NewConcurrencyGate(2)
	ctx := context.Background()
	if err := g.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if err := g.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if g.Inflight() != 2 || g.Max() != 2 {
		t.Fatalf("在途/上限应为 2，实际 %d/%d", g.Inflight(), g.Max())
	}

	// 已满时等待应被上下文取消打断
	cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := g.Acquire(cctx); err == nil {
		t.Fatal("闸门已满且超时，应返回错误")
	}

	g.Release()
	g.Release()
	g.Release() // 多余的释放不应 panic
	if g.Inflight() != 0 {
		t.Fatalf("在途数应为 0，实际 %d", g.Inflight())
	}
}

// 被限流时给出的等待时间应等于「最早那次占用滑出窗口」的剩余时间，
// 而不是墙钟分钟边界 —— 两者在窗口中途可相差数十秒，
// 客户端拿到的 Retry-After 会严重失真。
func TestRateLimiterWaitMatchesWindow(t *testing.T) {
	rl := NewRateLimiter()
	// 选在某分钟的中间（第 50 秒），避免与分钟边界重合掩盖差异
	base := time.Unix(1700000030, 0)
	if base.Unix()%60 != 50 {
		t.Fatalf("基准秒应落在分钟中段，实际 %d", base.Unix()%60)
	}
	if ok, _ := rl.Allow(1, 1, base); !ok {
		t.Fatal("首次应放行")
	}
	ok, wait := rl.Allow(1, 1, base.Add(2*time.Second))
	if ok {
		t.Fatal("额度 1 时第二次应拒绝")
	}
	// 名额在 base+60s 滑出窗口；现在是 base+2s，还应等 58 秒
	if wait != 58*time.Second {
		t.Fatalf("等待时间应为 58s（按窗口起点算），实际 %v", wait)
	}
}
