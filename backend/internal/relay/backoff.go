package relay

import (
	"context"
	"math/rand/v2"
	"strings"
	"time"
)

// 故障转移前的退避。
//
// 参考 sub2api（backend/internal/handler/failover_loop.go）的处理方式：
// 那边的资源单位是「账号」，**重试同一个账号**时等待（sameAccountRetryDelay
// = 500ms，请求级瞬时错误再指数递增），而**换到别的账号默认不等待**。
//
// llm-relay 的资源单位是「渠道」，filterTried 按渠道 ID 去重，所以同一渠道
// 绝不会被重试 —— 那边「同账号重试」在语义上的对应物不是「同一个渠道」，
// 而是「同一个上游」：两条渠道共用 base_url 时，换渠道在上游看来就是
// 「同一个端点又来了」，与同账号重试对上游的效果完全一样。
//
// 线上就有这种配置（d1api.xin 被两条渠道共用），而换渠道是零间隔的：
// 同一份请求连打同一个上游两次，正是「上游以为在攻击他、直接熔断」的形态。
const (
	// maxRetryDelay 是单次等待的上限。照搬 sub2api 的
	// maxRequestScopedRetryDelay：重试配置再激进，也不该把单次请求
	// 拖进分钟级等待。
	//
	// 它同时兜住上游给的 Retry-After：那个值在冷却路径上允许到 24 小时
	// （见 ParseRetryAfter），原样用作「当前请求下一步」的等待会把请求挂死。
	maxRetryDelay = 8 * time.Second
	// retryJitterPercent 是等待时长的抖动幅度（±25%）。
	//
	// 为什么必须有而 sub2api 的同账号重试没有：那边默认基数 500ms、
	// 账号池很大，齐射被池子摊开了；而**上游故障是上游级的**，会被同一
	// 时刻的所有并发请求同时撞上。等待时长完全相同时，这批请求会在同一
	// 毫秒再次一起打上去 —— 退避反而把一次冲撞变成了周期性齐射，
	// 那恰恰是本功能要消灭的形态。
	//
	// 取 ±25% 而不是「全抖动」（0~d）：下限保证确实等了一会儿，上限不
	// 改变量级；全抖动会让「这次请求为什么慢」在日志里失去可解释性。
	retryJitterPercent = 25
)

// UpstreamHost 从 base_url 取归一化的上游标识（host[:port]，小写）。
//
// 刻意用字符串切分而不是 net/url：用户经常填 "d1api.xin/v1" 这种不带
// scheme 的地址（proxy.go 的 Validate 专门拒绝带前缀的写法，说明这类
// 填法是真实存在的）。url.Parse 对无 scheme 的串会当相对路径解析，
// Hostname() 返回空 —— 于是两条共用同一上游的渠道被判成不同主机，
// 退避**悄悄失效且没有任何报错**，正是本仓库最忌讳的静默失真。
//
// 字符串切分没有这种失败模式：最坏情况是原样返回，退化为「判为不同上游」，
// 那是个安全方向的误判（少等一次）而不是错等。判不出 host 时返回空串。
//
// 端口算进身份：同域名下的 :8443 与 :443 是两个入口。不做默认端口归一
// （https://a.com 与 https://a.com:443 会判成不同）—— 那是安全方向的
// 误判，而为此引入解析逻辑不值得。
func UpstreamHost(baseURL string) string {
	s := strings.ToLower(strings.TrimSpace(baseURL))
	if s == "" {
		return ""
	}
	// 砍掉 scheme
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	// 砍掉 user:pass@
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	// 砍掉路径 / query / fragment
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// retryHop 描述「刚失败的那次尝试」，是退避判定的输入。
type retryHop struct {
	ChannelID uint
	Host      string // UpstreamHost 的结果
	// ProxyID 参与判定：同一域名经不同出口代理出去，上游看到的来源不同，
	// 对上游而言是两条独立的路径，不需要克制。
	ProxyID uint
	// Status 是上游应答的状态码；0 表示网络层失败（连不上、握手失败），
	// 那种情况没有上游应答可供参考。
	Status int
	// RetryAfter 仅在上游明确给了 Retry-After 时非零（见 retryAfterOf）。
	RetryAfter time.Duration
}

// hopOf 把一次失败尝试浓缩成退避判定所需的输入。
//
// attempt 为 nil 表示网络层失败（没有上游应答）。
func hopOf(cand Candidate, attempt *Attempt) retryHop {
	h := retryHop{
		ChannelID: cand.Channel.ID,
		Host:      UpstreamHost(cand.Channel.BaseURL),
		ProxyID:   cand.EgressProxyID(),
	}
	if attempt != nil {
		h.Status = attempt.StatusCode
		h.RetryAfter = retryAfterOf(attempt)
	}
	return h
}

// sameUpstream 判断下一个候选是否还在打刚失败的那台上游。
//
// 这是 sub2api「同账号重试才延迟、换账号不延迟」在 llm-relay 里的对应物
// （见文件头的说明）。同一条渠道不会出现在这里 —— filterTried 保证它
// 不会被重试；这里比的是两条**不同**渠道是否指向同一个上游。
func sameUpstream(prev retryHop, next Candidate) bool {
	if prev.Host == "" {
		return false // 没配 base_url（或填得无法识别）时不参与判定
	}
	return prev.ProxyID == next.EgressProxyID() &&
		prev.Host == UpstreamHost(next.Channel.BaseURL)
}

// retryDelayFor 算出「从 prev 换到 next」之前该等多久。0 表示不等。
//
// 照搬 sub2api 的两条核心取舍：
//   - 只有还是同一台上游才等。换到别的上游不给谁造成压力，白等只是让
//     用户多等、还多占一份全局并发名额（见 Relay 循环里的注释）。
//   - 等待有上限（maxRetryDelay），防止重试配置把单次请求拖进分钟级。
//
// 以及它对 Retry-After 的处理：上游明确说了「过多久再来」，那比我们自己
// 猜准 —— 但同样要封顶。
func retryDelayFor(prev retryHop, next Candidate, opts Options) time.Duration {
	// 基线为 0 = 功能关闭。必须先判它：否则下面采信 Retry-After 的分支
	// 会在「没配延迟」的部署上凭空等起来（Retry-After 是上游给的，
	// 与配置无关）—— 那会让 Options{} 全零的既有测试与调用方出现
	// 无法解释的变慢。
	if opts.RetrySameUpstreamDelay <= 0 {
		return 0
	}
	// 规则刻意保持单一：同上游就等，不看状态码。
	// 401/403（密钥失效、被 Cloudflare 拦）等 500ms 对「上游压力」确实
	// 没有改善，但为单一状态码开分支会让「什么时候会等」变得需要查表 ——
	// 而这条规则的价值正在于可解释。
	if !sameUpstream(prev, next) {
		return 0
	}
	d := opts.RetrySameUpstreamDelay
	// 上游给了 Retry-After：同主机时它就是最准的值。
	// 只有 429 与 503 会真的说「请等一会儿」——5xx 里只有 503 是服务端
	// 主动退避的语义，500/502/504 是故障而不是限流。
	if prev.RetryAfter > 0 && (prev.Status == 429 || prev.Status == 503) {
		d = prev.RetryAfter
	}
	if d > maxRetryDelay {
		d = maxRetryDelay
	}
	return withJitter(d)
}

// retryAfterOf 取出这次上游应答里声明的退避时长。
//
// 判据集中在这里，与 applyCooldown 采信的口径一致（429/503）。两处各写
// 一份迟早漂移，而漂移的表现是「渠道被摘了但本次请求没等」或反过来，
// 都属于难以解释的行为。
func retryAfterOf(attempt *Attempt) time.Duration {
	if attempt == nil {
		return 0
	}
	if attempt.StatusCode != 429 && attempt.StatusCode != 503 {
		return 0
	}
	if attempt.Headers == nil {
		return 0
	}
	return ParseRetryAfter(attempt.Headers.Get("Retry-After"), time.Now())
}

// withJitter 给等待时长叠加 ±retryJitterPercent% 的抖动（见常量处的说明）。
func withJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	span := int64(d) * retryJitterPercent / 100
	if span <= 0 {
		return d
	}
	out := int64(d) + rand.Int64N(span*2+1) - span
	if out <= 0 {
		return 0
	}
	return time.Duration(out)
}

// waitBeforeRetry 等完这段退避，返回 false 表示 ctx 已取消。
//
// 与 sub2api 的 sleepWithContext 同构。刻意不用 time.Sleep：后者无法被
// ctx 打断 —— 客户端手动结束推理、关掉页面之后，这个 goroutine 还会抱着
// 一份全局并发名额把剩下的时间睡完，然后才去对着空气重试。
//
// 用 time.NewTimer 而不是 time.After：ctx 先到期时，After 的定时器仍会
// 挂在堆里到点才回收；高并发下每个被打断的请求都留下一个。
func waitBeforeRetry(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
