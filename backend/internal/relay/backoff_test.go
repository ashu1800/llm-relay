package relay

import (
	"context"
	"net/http"
	"testing"
	"time"

	"llm-relay/internal/model"
)

// 退避的判据与边界。这些是纯函数，不需要 DB —— Router.Candidates 需要真库，
// 所以测试缝留在这里（也正因为如此，retryDelayFor 才被设计成纯函数）。
//
// 抖动使结果落在区间内而非等值，断言一律用区间。

func cand(id uint, baseURL string, proxyID uint) Candidate {
	return Candidate{
		Channel: model.Channel{ID: id, BaseURL: baseURL, ProxyID: proxyID},
	}
}

// 上游标识的归一化。无 scheme 的写法是重点：那是用户真实会填的形态，
// 而 net/url 对它解析失败会让退避静默失效。
func TestUpstreamHostNormalizes(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://d1api.xin/v1", "d1api.xin"},
		{"http://D1API.XIN/v1", "d1api.xin"},
		{"d1api.xin/v1", "d1api.xin"},                   // 不带 scheme（关键用例）
		{"d1api.xin", "d1api.xin"},                      // 只有主机名
		{"https://d1api.xin", "d1api.xin"},              // 无路径
		{"https://d1api.xin:8443/v1", "d1api.xin:8443"}, // 端口算进身份
		{"https://user:pass@d1api.xin/v1", "d1api.xin"}, // 砍掉凭据
		{"https://d1api.xin/v1?x=1", "d1api.xin"},       // 砍掉 query
		{"  https://d1api.xin/v1  ", "d1api.xin"},       // 去空白
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := UpstreamHost(c.in); got != c.want {
			t.Errorf("UpstreamHost(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 换到不同上游时必须不等 —— 这是「不回归延迟」的核心断言。
// 线上那 3 次 403 换的都是不同上游，正常容错路径不该被拖慢。
func TestRetryDelayDifferentUpstreamIsZero(t *testing.T) {
	opts := Options{RetrySameUpstreamDelay: 500 * time.Millisecond}
	prev := hopOf(cand(1, "https://a.com/v1", 0), nil)

	for _, base := range []string{
		"https://b.com/v1",      // 完全不同的域名
		"https://a.com:8443/v1", // 同域名不同端口 = 不同入口
		"https://api.a.com/v1",  // 子域不同
	} {
		if d := retryDelayFor(prev, cand(2, base, 0), opts); d != 0 {
			t.Errorf("下一个候选是 %s（不同上游），不该等待，实际 %v", base, d)
		}
	}
}

// 同一台上游要等，且落在抖动区间内。
func TestRetryDelaySameUpstreamWaits(t *testing.T) {
	opts := Options{RetrySameUpstreamDelay: 500 * time.Millisecond}
	prev := hopOf(cand(1, "https://d1api.xin/v1", 0), nil)

	// 无 scheme 与带 scheme 混用也必须判为同一上游（真实配置里会混）
	for _, base := range []string{
		"https://d1api.xin/v1",
		"d1api.xin/v1",
		"HTTPS://D1API.XIN/other",
	} {
		d := retryDelayFor(prev, cand(2, base, 0), opts)
		if d < 375*time.Millisecond || d > 625*time.Millisecond {
			t.Errorf("同上游（%s）应等约 500ms（±25%%），实际 %v", base, d)
		}
	}
}

// 同域名但出口代理不同 = 对上游而言是不同来源，不该等。
func TestRetryDelayRequiresSameProxy(t *testing.T) {
	opts := Options{RetrySameUpstreamDelay: 500 * time.Millisecond}
	prev := hopOf(cand(1, "https://d1api.xin/v1", 0), nil)

	if d := retryDelayFor(prev, cand(2, "https://d1api.xin/v1", 7), opts); d != 0 {
		t.Errorf("同域名但代理不同（0 vs 7），不该等待，实际 %v", d)
	}
}

// 429 带 Retry-After：采信上游给的值（同上游时）。
func TestRetryDelayUsesRetryAfterSameUpstream(t *testing.T) {
	opts := Options{RetrySameUpstreamDelay: 500 * time.Millisecond}
	att := &Attempt{StatusCode: 429, Headers: http.Header{"Retry-After": []string{"1"}}}
	prev := hopOf(cand(1, "https://d1api.xin/v1", 0), att)

	d := retryDelayFor(prev, cand(2, "https://d1api.xin/v1", 0), opts)
	if d < 750*time.Millisecond || d > 1250*time.Millisecond {
		t.Errorf("429 + Retry-After: 1 应等约 1s，实际 %v", d)
	}
}

// 上游给了离谱的 Retry-After（600s）时必须封顶，否则请求被挂死。
func TestRetryDelayClampsHugeRetryAfter(t *testing.T) {
	opts := Options{RetrySameUpstreamDelay: 500 * time.Millisecond}
	att := &Attempt{StatusCode: 429, Headers: http.Header{"Retry-After": []string{"600"}}}
	prev := hopOf(cand(1, "https://d1api.xin/v1", 0), att)

	d := retryDelayFor(prev, cand(2, "https://d1api.xin/v1", 0), opts)
	// 封顶在 maxRetryDelay 之后再叠抖动，所以上界是 maxRetryDelay 的 1.25 倍
	if d > maxRetryDelay*5/4 {
		t.Errorf("Retry-After 600s 必须封顶（含抖动）在 %v 以内，实际 %v", maxRetryDelay*5/4, d)
	}
	if d < 6*time.Second {
		t.Errorf("封顶后应接近 %v，实际 %v", maxRetryDelay, d)
	}
}

// 不同上游时不采信 Retry-After：那个上游根本没在限流，
// 等 30 秒不但白费，还会一次吃光重试预算。
func TestRetryDelayIgnoresRetryAfterForDifferentUpstream(t *testing.T) {
	opts := Options{RetrySameUpstreamDelay: 500 * time.Millisecond}
	att := &Attempt{StatusCode: 429, Headers: http.Header{"Retry-After": []string{"30"}}}
	prev := hopOf(cand(1, "https://a.com/v1", 0), att)

	if d := retryDelayFor(prev, cand(2, "https://b.com/v1", 0), opts); d != 0 {
		t.Errorf("换到别的上游时不该采信 Retry-After，实际等了 %v", d)
	}
}

// 5xx 里只有 503 是「请等一会儿」语义；500/502/504 是故障，
// 没有 Retry-After 就不该按它算。
func TestRetryDelayOnlyHonorsRetryAfterFor429And503(t *testing.T) {
	opts := Options{RetrySameUpstreamDelay: 500 * time.Millisecond}
	for _, code := range []int{500, 502, 504} {
		att := &Attempt{StatusCode: code, Headers: http.Header{"Retry-After": []string{"5"}}}
		prev := hopOf(cand(1, "https://a.com/v1", 0), att)
		d := retryDelayFor(prev, cand(2, "https://a.com/v1", 0), opts)
		// 应退回基线 500ms（±25%），而不是 5s
		if d > 700*time.Millisecond {
			t.Errorf("%d 不该采信 Retry-After，应退回基线，实际 %v", code, d)
		}
	}
}

// 零值 Options（既有测试与默认构造）必须完全不等 —— 向后兼容的硬保证。
func TestRetryDelayZeroOptionDisables(t *testing.T) {
	prev := hopOf(cand(1, "https://a.com/v1", 0),
		&Attempt{StatusCode: 429, Headers: http.Header{"Retry-After": []string{"30"}}})

	for _, next := range []Candidate{
		cand(2, "https://a.com/v1", 0), // 同上游
		cand(3, "https://b.com/v1", 0), // 不同上游
	} {
		if d := retryDelayFor(prev, next, Options{}); d != 0 {
			t.Errorf("Options{} 全零时必须不等待，实际 %v", d)
		}
	}
}

// 网络层失败（没有上游应答）也参与判定：同上游时同样要等。
func TestRetryDelayNetworkFailureSameUpstream(t *testing.T) {
	opts := Options{RetrySameUpstreamDelay: 500 * time.Millisecond}
	prev := hopOf(cand(1, "https://a.com/v1", 0), nil) // nil = 连接失败
	if prev.Status != 0 {
		t.Fatalf("网络层失败的 Status 应为 0，实际 %d", prev.Status)
	}
	d := retryDelayFor(prev, cand(2, "https://a.com/v1", 0), opts)
	if d < 375*time.Millisecond || d > 625*time.Millisecond {
		t.Errorf("网络层失败 + 同上游应等约 500ms，实际 %v", d)
	}
}

func TestWithJitterBounds(t *testing.T) {
	const base = 500 * time.Millisecond
	lo, hi := 375*time.Millisecond, 625*time.Millisecond

	allEqual := true
	first := withJitter(base)
	for i := 0; i < 200; i++ {
		d := withJitter(base)
		if d < lo || d > hi {
			t.Fatalf("抖动越界：%v 不在 [%v, %v]", d, lo, hi)
		}
		if d != first {
			allEqual = false
		}
	}
	// 防止「抖动写成了常量」这种静默失效
	if allEqual {
		t.Error("200 次采样结果完全相同，抖动没有生效")
	}
}

func TestWithJitterZeroAndNegative(t *testing.T) {
	if d := withJitter(0); d != 0 {
		t.Errorf("0 应返回 0，实际 %v", d)
	}
	if d := withJitter(-time.Second); d != 0 {
		t.Errorf("负值应返回 0，实际 %v", d)
	}
}

// 等待必须能被 ctx 取消打断：客户端关掉页面后不该把剩下的时间睡完。
func TestWaitBeforeRetryCancelledPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	ok := waitBeforeRetry(ctx, 5*time.Second)
	elapsed := time.Since(start)

	if ok {
		t.Error("ctx 已取消时应返回 false")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("取消后应立刻返回，实际等了 %v", elapsed)
	}
}

func TestWaitBeforeRetryCompletes(t *testing.T) {
	start := time.Now()
	if !waitBeforeRetry(context.Background(), 60*time.Millisecond) {
		t.Error("正常等待应返回 true")
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Errorf("应该真的等了 60ms，实际 %v", elapsed)
	}
}

func TestWaitBeforeRetryZeroReturnsImmediately(t *testing.T) {
	start := time.Now()
	if !waitBeforeRetry(context.Background(), 0) {
		t.Error("0 应返回 true")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Millisecond {
		t.Errorf("0 不该真的等待，实际 %v", elapsed)
	}
}

// 上游没配 base_url 时不该参与判定（否则会误判成「同一上游」而集体变慢）。
func TestSameUpstreamEmptyHostIsFalse(t *testing.T) {
	prev := hopOf(cand(1, "", 0), nil)
	if sameUpstream(prev, cand(2, "", 0)) {
		t.Error("两边都解析不出 host 时不该判为同一上游")
	}
	if sameUpstream(prev, cand(3, "https://a.com/v1", 0)) {
		t.Error("空 host 不该与任何东西判为同一上游")
	}
}
