package update

// 后台定时检测的测试（2026-09-30 随这个功能一起立）。
//
// 为什么值得单独写：定时检测有三处「错了也不会报错」的地方 ——
//   1. 间隔/首次延迟写反：要么启动瞬间就打一次 GitHub（重启循环里烧限额），
//      要么根本跑不起来（但它只是不出现，没有任何症状）；
//   2. 与手动检测并发时各发一次请求：限额是共享的（未配 token 时 60 次/小时），
//      而「打开面板」与「定时任务」撞在一起是常态而不是巧合；
//   3. 日志策略：国内直连 GitHub 常年不通，若每 5 分钟记一条 WARN，
//      这台机器上真正重要的日志会被淹掉。这里把「只记事件」这条钉住。
//
// 全部用注入的 transport 假造 GitHub 响应，不发真实网络请求。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// shortenInitialCheckDelay 把「启动后等一分钟」缩到毫秒，测试结束还原。
// 只影响测试进程（生产用包级变量的初始值）。
func shortenInitialCheckDelay(t *testing.T, d time.Duration) {
	t.Helper()
	prev := initialCheckDelay
	initialCheckDelay = d
	t.Cleanup(func() { initialCheckDelay = prev })
}

// roundTripFunc 把函数适配成 http.RoundTripper（标准库里没有这个适配器）。
// 用它替代真实 transport，于是这些用例一个真实网络请求都不会发。
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// githubStub 是一个假的 GitHub：只认 /releases/latest，返回可控的版本号，
// 并记录被请求了多少次（限额意识全靠这个计数来验证）。
type githubStub struct {
	mu    sync.Mutex
	tag   string
	fail  bool
	calls int32
}

func (g *githubStub) setTag(tag string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tag = tag
}

func (g *githubStub) setFail(fail bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fail = fail
}

func (g *githubStub) roundTrip(req *http.Request) (*http.Response, error) {
	atomic.AddInt32(&g.calls, 1)
	g.mu.Lock()
	tag, fail := g.tag, g.fail
	g.mu.Unlock()

	if fail {
		return nil, io.ErrUnexpectedEOF // 网络层失败：不是限流也不是 404
	}
	body, _ := json.Marshal(Release{
		TagName:     tag,
		Name:        tag,
		Body:        "测试发布说明",
		PublishedAt: "2026-09-30T00:00:00Z",
		HTMLURL:     "https://github.com/o/n/releases/tag/" + tag,
	})
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}, nil
}

func (g *githubStub) callCount() int { return int(atomic.LoadInt32(&g.calls)) }

// newScheduledTestService 直构 Service（不走 NewService，免得读 settings 表）。
func newScheduledTestService(t *testing.T, stub *githubStub, logBuf *bytes.Buffer) *Service {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &Service{
		logger: logger,
		cfg:    Config{Enabled: true, Repo: "o/n"},
		client: &Client{repo: "o/n", api: &http.Client{Transport: roundTripFunc(stub.roundTrip)}},
	}
}

func TestScheduledCheckWritesSharedCache(t *testing.T) {
	stub := &githubStub{tag: "v9.9.9"}
	var logBuf bytes.Buffer
	s := newScheduledTestService(t, stub, &logBuf)

	s.runScheduledCheck(context.Background())
	if stub.callCount() != 1 {
		t.Fatalf("后台检测应当请求一次，实际 %d 次", stub.callCount())
	}

	// 关键：写进的是**手动检测同一个缓存** —— 打开面板必须立刻看到定时任务的结果，
	// 而不是再打一次 GitHub
	info := s.Check(context.Background(), false)
	if info.Latest != "9.9.9" {
		t.Fatalf("缓存里的最新版本应为 9.9.9，实际 %q", info.Latest)
	}
	if !info.Cached {
		t.Error("第二次读取应当命中缓存（Cached=true）")
	}
	if stub.callCount() != 1 {
		t.Errorf("命中缓存不该再请求 GitHub，实际 %d 次", stub.callCount())
	}
}

func TestScheduledCheckSkipsWhenDisabled(t *testing.T) {
	stub := &githubStub{tag: "v9.9.9"}
	var logBuf bytes.Buffer
	s := newScheduledTestService(t, stub, &logBuf)
	s.cfg.Enabled = false

	s.runScheduledCheck(context.Background())
	if n := stub.callCount(); n != 0 {
		t.Fatalf("更新检测关掉时不该访问 GitHub，实际 %d 次", n)
	}
	if strings.Contains(logBuf.String(), "WARN") {
		t.Errorf("关掉检测不算故障，不该记 WARN：%s", logBuf.String())
	}
}

func TestScheduledCheckSkipsWhileAnotherCheckRuns(t *testing.T) {
	stub := &githubStub{tag: "v9.9.9"}
	var logBuf bytes.Buffer
	s := newScheduledTestService(t, stub, &logBuf)

	// 模拟「用户正点着检测更新」：锁被占住
	s.checkMu.Lock()
	done := make(chan struct{})
	go func() {
		s.runScheduledCheck(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		s.checkMu.Unlock()
		t.Fatal("有人在查时后台循环应当直接跳过，而不是排队等锁")
	}
	s.checkMu.Unlock()

	if n := stub.callCount(); n != 0 {
		t.Fatalf("有人在查时不该再发一次请求，实际 %d 次", n)
	}
}

func TestCheckCoalescesQueuedCallIntoFreshCache(t *testing.T) {
	stub := &githubStub{tag: "v1.0.0"}
	var logBuf bytes.Buffer
	s := newScheduledTestService(t, stub, &logBuf)

	s.checkMu.Lock()
	result := make(chan *Info, 1)
	go func() { result <- s.Check(context.Background(), true) }() // force：会去抢锁
	time.Sleep(50 * time.Millisecond)                             // 让它先卡在锁上
	// 模拟「持锁的那次检测刚查完并写了缓存」
	s.toCache(&Info{Current: "0.1.0", Latest: "9.9.9", HasUpdate: true, CheckedAt: time.Now().UTC()}, "o/n")
	s.checkMu.Unlock()

	select {
	case info := <-result:
		if info.Latest != "9.9.9" {
			t.Fatalf("应当复用排队期间刷新的缓存，实际 latest=%q", info.Latest)
		}
		if !info.Cached {
			t.Error("复用缓存的结果应当标 Cached=true")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Check 没有返回")
	}
	if n := stub.callCount(); n != 0 {
		t.Fatalf("排队期间已有人查过，不该再发请求，实际 %d 次", n)
	}
}

// 日志策略：只记事件。这条测试同时是「会不会刷屏」的回归。
func TestScheduledCheckLogsOnlyOnEvents(t *testing.T) {
	stub := &githubStub{tag: "v0.1.11"}
	var logBuf bytes.Buffer
	s := newScheduledTestService(t, stub, &logBuf)
	ctx := context.Background()

	count := func(marker string) int { return strings.Count(logBuf.String(), marker) }

	s.runScheduledCheck(ctx) // 首次成功
	if count("后台检测更新就绪") != 1 {
		t.Fatalf("首次成功应当记一条「就绪」：%s", logBuf.String())
	}
	s.runScheduledCheck(ctx) // 无变化
	if count("后台检测更新就绪") != 1 || count("检测到新版本") != 0 {
		t.Errorf("状态没变时不该再记日志：%s", logBuf.String())
	}

	stub.setTag("v0.1.12") // 出现新版本
	s.runScheduledCheck(ctx)
	if count("检测到新版本") != 1 {
		t.Fatalf("应当记一条「检测到新版本」：%s", logBuf.String())
	}
	s.runScheduledCheck(ctx) // 还是同一个新版本
	if count("检测到新版本") != 1 {
		t.Errorf("同一个新版本不该重复记：%s", logBuf.String())
	}

	stub.setFail(true) // 开始失败
	s.runScheduledCheck(ctx)
	if count("后台检测更新失败") != 1 {
		t.Fatalf("第一次失败应当记一条 WARN：%s", logBuf.String())
	}
	s.runScheduledCheck(ctx) // 继续失败
	if count("后台检测更新失败") != 1 {
		t.Errorf("同类失败不该每次都记（会刷屏）：%s", logBuf.String())
	}

	stub.setFail(false) // 恢复
	s.runScheduledCheck(ctx)
	if count("后台检测更新已恢复") != 1 {
		t.Fatalf("恢复时应当记一条：%s", logBuf.String())
	}
}

// 循环本身：起得来、按间隔跑、ctx 结束就停。
func TestStartCheckLoopRunsAndStopsWithContext(t *testing.T) {
	shortenInitialCheckDelay(t, 5*time.Millisecond)
	stub := &githubStub{tag: "v9.9.9"}
	var logBuf bytes.Buffer
	s := newScheduledTestService(t, stub, &logBuf)

	ctx, cancel := context.WithCancel(context.Background())
	s.StartCheckLoop(ctx, 10*time.Millisecond)

	// 等它至少跑两次（首次延迟 + 一个 tick）
	deadline := time.Now().Add(3 * time.Second)
	for stub.callCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := stub.callCount(); n < 2 {
		cancel()
		t.Fatalf("循环应当定时跑起来，实际只请求了 %d 次", n)
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
	after := stub.callCount()
	time.Sleep(100 * time.Millisecond)
	if n := stub.callCount(); n != after {
		t.Fatalf("ctx 结束后不该再检测：取消时 %d 次，100ms 后 %d 次", after, n)
	}
}

// 间隔这个数字是需求本身（「每 5 分钟检测一次」），把它钉住：
// 顺手改成别的值不会编译失败、也不会让任何用例变红，只会让人以为功能坏了。
func TestBackgroundCheckIntervalIsFiveMinutes(t *testing.T) {
	if BackgroundCheckInterval != 5*time.Minute {
		t.Fatalf("后台检测间隔应为 5 分钟，实际 %s", BackgroundCheckInterval)
	}
}

// interval <= 0 时回落默认值 —— 由启动日志里的 interval 观察。
func TestStartCheckLoopFallsBackToDefaultInterval(t *testing.T) {
	shortenInitialCheckDelay(t, time.Hour) // 只关心启动那一行日志
	stub := &githubStub{tag: "v9.9.9"}
	var logBuf bytes.Buffer
	s := newScheduledTestService(t, stub, &logBuf)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartCheckLoop(ctx, 0)

	if !strings.Contains(logBuf.String(), "interval=5m0s") {
		t.Fatalf("interval<=0 应当回落 5 分钟，启动日志: %s", logBuf.String())
	}
}

// TestRealGitHubCheckAgainstDefaultRepo 是真访问 api.github.com 的冒烟测试。
//
// 默认跳过：日常 go test 与 CI 不该依赖外网（未配 token 时限额只有 60 次/小时，
// 国内机器更是常年连不上，要靠代理）。需要确认「代理 / token 配好之后真能通」
// 时手动跑：
//
//	LLMRELAY_TEST_NET=1 go test ./internal/update -run TestRealGitHubCheck -v
//
// 它走的是 stub 用例覆盖不到的那半条链路：netguard 的建连校验、
// api.github.com 的响应解析、版本号比较。
func TestRealGitHubCheckAgainstDefaultRepo(t *testing.T) {
	if os.Getenv("LLMRELAY_TEST_NET") != "1" {
		t.Skip("未设置 LLMRELAY_TEST_NET=1，跳过真实 GitHub 冒烟测试")
	}
	s := &Service{logger: slog.Default(), cfg: DefaultConfig()}
	client, err := NewClient(ClientOptions{Repo: s.cfg.Repo, ProxyURL: os.Getenv("LLMRELAY_TEST_PROXY")})
	if err != nil {
		t.Fatalf("构造客户端失败: %v", err)
	}
	s.client = client

	info := s.Check(context.Background(), true)
	if info.Warning != "" {
		t.Fatalf("真实检测失败: %s", info.Warning)
	}
	if info.Latest == "" {
		t.Fatal("应当拿到一个最新版本号")
	}
	t.Logf("仓库 %s：当前 %s，最新 %s，有新版本=%v", s.cfg.Repo, info.Current, info.Latest, info.HasUpdate)
}

// 首次检测延后：这一条防的是「进程一启动就打 GitHub」——
// 崩溃重启循环里那会持续烧共享限额。
func TestStartCheckLoopDelaysFirstCheck(t *testing.T) {
	shortenInitialCheckDelay(t, 150*time.Millisecond)
	stub := &githubStub{tag: "v9.9.9"}
	var logBuf bytes.Buffer
	s := newScheduledTestService(t, stub, &logBuf)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartCheckLoop(ctx, time.Hour) // 间隔足够长，只看首次

	time.Sleep(50 * time.Millisecond)
	if n := stub.callCount(); n != 0 {
		t.Fatalf("首次检测应当延后，启动 50ms 内不该有请求，实际 %d 次", n)
	}
	deadline := time.Now().Add(2 * time.Second)
	for stub.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if stub.callCount() == 0 {
		t.Fatal("首次延迟结束后应当检测一次")
	}
}
