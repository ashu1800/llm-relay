package relay

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ============================ 每分钟请求数限流 ============================

// rpmWindow 是某个密钥的滑动窗口计数。
// 用 60 个按秒分桶的计数器代替时间戳切片，内存恒定且无需定期清理。
type rpmWindow struct {
	lastSec int64
	counts  [60]int32
}

// advance 把窗口推进到当前秒，顺带清掉滑出窗口的桶。
func (w *rpmWindow) advance(nowSec int64) {
	if w.lastSec == 0 {
		w.lastSec = nowSec
		return
	}
	diff := nowSec - w.lastSec
	if diff <= 0 {
		return // 时钟回拨或同一秒内，不做处理
	}
	if diff >= 60 {
		for i := range w.counts {
			w.counts[i] = 0
		}
	} else {
		for i := int64(1); i <= diff; i++ {
			w.counts[(w.lastSec+i)%60] = 0
		}
	}
	w.lastSec = nowSec
}

func (w *rpmWindow) total() int {
	sum := 0
	for _, c := range w.counts {
		sum += int(c)
	}
	return sum
}

// RateLimiter 按键做每分钟请求数限制。
// 额度由调用方传入而不是存在限流器里：每个密钥可以有自己的上限，
// 取不到密钥级配置时回退到全局默认值。
type RateLimiter struct {
	mu      sync.Mutex
	windows map[uint]*rpmWindow
}

// NewRateLimiter 构造限流器。
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{windows: map[uint]*rpmWindow{}}
}

// Allow 判断该键当前是否还能再发一次请求，超出时返回建议的重试等待时间。
// limit 为该键生效的每分钟上限，<=0 表示不限制。
// 只有通过时才会计数，被拒绝的请求不会继续把窗口撑大。
func (l *RateLimiter) Allow(key uint, limit int, now time.Time) (bool, time.Duration) {
	if l == nil || limit <= 0 {
		return true, 0
	}
	nowSec := now.Unix()
	l.mu.Lock()
	defer l.mu.Unlock()

	w := l.windows[key]
	if w == nil {
		w = &rpmWindow{}
		l.windows[key] = w
	}
	w.advance(nowSec)

	if w.total() >= limit {
		// 等待时间按「最早一个仍有计数的那一秒」算：它滑出 60 秒窗口的那一刻
		// 就有一个名额放出来。不能按墙钟分钟边界（60-nowSec%60）估 ——
		// 那与窗口起点无关，可能告诉客户端 Retry-After: 1s 而实际要等 59s。
		wait := time.Duration(0)
		for back := 59; back >= 1; back-- {
			if w.counts[(nowSec-int64(back))%60] > 0 {
				wait = time.Duration(60-back) * time.Second
				break
			}
		}
		if wait <= 0 {
			wait = time.Second // 理论上到不了这里（total>0 必有非零桶），兜底
		}
		return false, wait
	}
	w.counts[nowSec%60]++
	return true, 0
}

// Sweep 清掉长期不用的窗口，避免密钥删改后残留。
func (l *RateLimiter) Sweep(now time.Time) {
	if l == nil {
		return
	}
	cutoff := now.Unix() - 120
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, w := range l.windows {
		if w.lastSec < cutoff {
			delete(l.windows, k)
		}
	}
}

// StartSweeper 周期性执行 Sweep。窗口本身没有任何到期回收机制，
// 不挂这个循环的话 map 会随密钥数量只增不减（删掉的密钥也留着）。
func (l *RateLimiter) StartSweeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				l.Sweep(time.Now())
			}
		}
	}()
}

// Sweep 清理已过期的冷却条目。
//
// until 只在「该渠道再次被查询」时才被动清理，删除的渠道会留下永久条目；
// 挂上周期回收（StartSweeper）后这些垃圾有了确定的出口。
// latency / failStreak 刻意不清：两者没有时间戳，要判断陈旧就得给每条
// 写入加时钟，而它们每渠道只有几十字节、渠道总量由人工配置决定，
// 有界 —— 为了回收这点内存不值得把每次成功/失败的写入路径变重。
func (s *ChannelState) Sweep(now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, until := range s.until {
		if now.After(until) {
			delete(s.until, id)
		}
	}
}

// StartSweeper 周期性执行 Sweep，跟着传入 ctx 的生命周期走。
func (s *ChannelState) StartSweeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.Sweep(time.Now())
			}
		}
	}()
}

// ============================ Retry-After ============================

// ParseRetryAfter 解析 Retry-After 响应头。
// 按 RFC 允许两种格式：秒数，或 HTTP 日期。无法解析时返回 0。
func ParseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		// 上限一天，避免上游返回离谱数值把渠道长时间摘掉
		if secs > 86400 {
			secs = 86400
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := time.Parse(time.RFC1123, v); err == nil {
		d := t.Sub(now)
		if d <= 0 {
			return 0
		}
		if d > 24*time.Hour {
			d = 24 * time.Hour
		}
		return d
	}
	return 0
}

// ============================ 渠道运行期状态 ============================

// ChannelState 记录渠道的运行期状态：冷却截止时间与在途请求数。
//
// 冷却解决的是「上游在限流，我们却还在往上打」的问题：
// 收到 429 后按 Retry-After 把渠道暂时摘掉，让流量去别的渠道，
// 而不是把同一个已限流的上游打得更惨。
type ChannelState struct {
	mu       sync.Mutex
	until    map[uint]time.Time
	inflight map[uint]int
	rejected int64
	// latency 是各渠道最近成功响应的首包延迟（EWMA，毫秒），
	// 供 least_latency 策略排序。只记成功：失败渠道该被冷却，而不是参与比快。
	latency map[uint]float64
	// failStreak 是各渠道的连续失败计数，供「温和熔断」用：
	// 连续失败达到阈值的渠道自动冷却一段时间（见 Service.markChannelFailure），
	// 避免「全部渠道都挂」时每个请求都把候选链完整撞一遍。
	// 成功一次即清零。
	failStreak map[uint]int
}

// NewChannelState 构造渠道状态表。
func NewChannelState() *ChannelState {
	return &ChannelState{
		until:      map[uint]time.Time{},
		inflight:   map[uint]int{},
		latency:    map[uint]float64{},
		failStreak: map[uint]int{},
	}
}

// Cooldown 把渠道摘掉一段时间。已存在的更长冷却不会被缩短。
func (s *ChannelState) Cooldown(id uint, d time.Duration) {
	if s == nil || id == 0 || d <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	until := time.Now().Add(d)
	if until.After(s.until[id]) {
		s.until[id] = until
	}
}

// InCooldown 判断渠道是否在冷却中，返回剩余时间。
func (s *ChannelState) InCooldown(id uint, now time.Time) (time.Duration, bool) {
	if s == nil {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.until[id]
	if !ok || !until.After(now) {
		if ok {
			delete(s.until, id) // 到期即清，避免表无限增长
		}
		return 0, false
	}
	return until.Sub(now), true
}

// ClearCooldown 手动恢复渠道。
func (s *ChannelState) ClearCooldown(id uint) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.until, id)
	s.mu.Unlock()
}

// AcquireWait 在超时内轮询等待名额，拿不到返回 false，由调用方决定是否放行。
// 渠道并发是软约束：宁可偶尔超一点，也不该让用户的请求直接失败。
func (s *ChannelState) AcquireWait(ctx context.Context, id uint, max int, timeout time.Duration) bool {
	if s == nil || max <= 0 {
		return true
	}
	if s.Acquire(id, max) {
		return true
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if s.Acquire(id, max) {
				return true
			}
		}
	}
	return false
}

// Acquire 尝试占用一个在途名额。max <= 0 表示不限制。
func (s *ChannelState) Acquire(id uint, max int) bool {
	if s == nil || max <= 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight[id] >= max {
		s.rejected++
		return false
	}
	s.inflight[id]++
	return true
}

// RecordLatency 记录一次成功响应的首包延迟（毫秒），EWMA 平滑。
// alpha 取 0.3：既跟得上变化，又不至于被单次抖动带偏。
func (s *ChannelState) RecordLatency(id uint, ms int) {
	if s == nil || id == 0 || ms <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	const alpha = 0.3
	if prev, ok := s.latency[id]; ok && prev > 0 {
		s.latency[id] = prev*(1-alpha) + float64(ms)*alpha
	} else {
		s.latency[id] = float64(ms)
	}
}

// Latency 返回该渠道的平滑首包延迟；没有观测值时返回 0。
func (s *ChannelState) Latency(id uint) float64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latency[id]
}

// NoteFailure 把渠道的连续失败计数加一，返回加一后的连击数。
// 调用方（markChannelFailure）用它对比熔断阈值。
func (s *ChannelState) NoteFailure(id uint) int {
	if s == nil || id == 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failStreak[id]++
	return s.failStreak[id]
}

// NoteSuccess 清零渠道的连续失败计数：成功一次即完全康复。
// 刻意不在触发熔断后清零 —— 冷却到期后渠道若还在失败，
// 一次就能再次触发冷却；只有真正成功过一次才算恢复。
func (s *ChannelState) NoteSuccess(id uint) {
	if s == nil || id == 0 {
		return
	}
	s.mu.Lock()
	delete(s.failStreak, id)
	s.mu.Unlock()
}

// Inflight 返回该渠道当前占用的在途名额数。
// 除了测试，路由诊断页也可以用它说明「某个渠道正忙」。
func (s *ChannelState) Inflight(id uint) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inflight[id]
}

// Release 释放名额。
func (s *ChannelState) Release(id uint) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight[id] > 0 {
		s.inflight[id]--
	}
	if s.inflight[id] == 0 {
		delete(s.inflight, id)
	}
}

// ============================ 全局并发闸门 ============================

// ConcurrencyGate 限制进程内同时进行的在途请求数。
//
// 没有这道闸门时，客户端并发一高就会把上游连接数和本地 goroutine 一起推上去，
// 上游一旦开始限流，重试又会放大压力。宁可在这里排队，也不要把它传导给上游。
type ConcurrencyGate struct {
	sem chan struct{}
	max int
}

// NewConcurrencyGate 构造闸门。max <= 0 表示不限制。
func NewConcurrencyGate(max int) *ConcurrencyGate {
	if max <= 0 {
		return &ConcurrencyGate{}
	}
	return &ConcurrencyGate{sem: make(chan struct{}, max), max: max}
}

// Acquire 取一个名额。排队期间 ctx 被取消（客户端断开）会立即返回。
func (g *ConcurrencyGate) Acquire(ctx context.Context) error {
	if g == nil || g.sem == nil {
		return nil
	}
	select {
	case g.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release 归还名额。多余的调用是空操作。
func (g *ConcurrencyGate) Release() {
	if g == nil || g.sem == nil {
		return
	}
	select {
	case <-g.sem:
	default:
	}
}

// Max 返回配置的上限，0 表示不限。
func (g *ConcurrencyGate) Max() int {
	if g == nil {
		return 0
	}
	return g.max
}

// Inflight 返回当前在途数。
func (g *ConcurrencyGate) Inflight() int {
	if g == nil || g.sem == nil {
		return 0
	}
	return len(g.sem)
}

// ChannelMaxConcurrency 从渠道扩展配置里读并发上限。
// 配置存在 jsonb 里，整数取出来是 float64，需要容忍两种类型。
func ChannelMaxConcurrency(extra map[string]any) int {
	if extra == nil {
		return 0
	}
	v, ok := extra["max_concurrency"]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}
