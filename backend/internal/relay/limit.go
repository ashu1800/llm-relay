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
		// 最早的那一秒滑出窗口后就能再放行，据此给出等待时间
		oldest := (w.lastSec + 1) % 60
		wait := time.Duration(60-(nowSec%60)) * time.Second
		_ = oldest
		if wait <= 0 {
			wait = time.Second
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
}

// NewChannelState 构造渠道状态表。
func NewChannelState() *ChannelState {
	return &ChannelState{until: map[uint]time.Time{}, inflight: map[uint]int{}}
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
