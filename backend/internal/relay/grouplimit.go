package relay

import (
	"fmt"
	"sync"
	"time"
)

// GroupLimiter 按分组做每分钟的请求数与 token 数限制。
//
// 语义（改这里之前先读一遍，这几条都是刻意的）：
//   - RPM 统计的是**发往上游的请求数**，重试也计入：重试同样消耗上游配额，
//     按「客户端请求数」计会让一个疯狂重试的请求把上游打爆却不触发限制；
//   - TPM 统计的是**上游回报的实际 token 数**，因此会滞后一刻：
//     先放过去一个超额的请求，之后的请求才被拦住。想做成精确的前置拦截
//     只能靠预估，而预估不准会误伤正常请求，得不偿失；
//   - 0 或负值表示不限制；
//   - 固定窗口：按分钟对齐，窗口切换时计数清零。滑动窗口更精确，但对
//     「保护上游」这个目的来说固定窗口足够，且实现与解释都简单得多。
type GroupLimiter struct {
	mu      sync.Mutex
	buckets map[uint]*groupWindow
}

type groupWindow struct {
	start    time.Time
	requests int
	tokens   int
}

// NewGroupLimiter 构造分组限流器。
func NewGroupLimiter() *GroupLimiter {
	return &GroupLimiter{buckets: map[uint]*groupWindow{}}
}

// window 取出（必要时新建）该分组在当前分钟的窗口。调用方必须持有锁。
func (l *GroupLimiter) window(id uint, now time.Time) *groupWindow {
	start := now.Truncate(time.Minute)
	w := l.buckets[id]
	if w == nil || !w.start.Equal(start) {
		w = &groupWindow{start: start}
		l.buckets[id] = w
	}
	return w
}

// Check 判断该分组当前是否还有额度。返回 false 时 reason 说明是哪一项超了。
//
// 只读，不改变计数：真正的计数发生在 Record（请求即将发出）与
// AddTokens（拿到上游用量）两处。
func (l *GroupLimiter) Check(id uint, rpm, tpm int, now time.Time) (bool, string) {
	if l == nil || (rpm <= 0 && tpm <= 0) {
		return true, ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.window(id, now)
	if rpm > 0 && w.requests >= rpm {
		return false, fmt.Sprintf("分组每分钟请求数已达上限 %d", rpm)
	}
	if tpm > 0 && w.tokens >= tpm {
		return false, fmt.Sprintf("分组每分钟 token 数已达上限 %d", tpm)
	}
	return true, ""
}

// Record 记一次即将发往上游的请求。
func (l *GroupLimiter) Record(id uint, now time.Time) {
	if l == nil || id == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.window(id, now).requests++
}

// AddTokens 记一次上游回报的 token 用量。负数与 0 直接忽略。
func (l *GroupLimiter) AddTokens(id uint, tokens int, now time.Time) {
	if l == nil || id == 0 || tokens <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.window(id, now).tokens += tokens
}

// Usage 返回该分组当前窗口的用量，供界面显示。
func (l *GroupLimiter) Usage(id uint, now time.Time) (requests, tokens int) {
	if l == nil || id == 0 {
		return 0, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.window(id, now)
	return w.requests, w.tokens
}

// Reset 清掉某个分组在当前窗口的计数，供接口删除分组 / 测试使用。
func (l *GroupLimiter) Reset(id uint) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, id)
}
