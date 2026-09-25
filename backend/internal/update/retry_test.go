package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// 下载重试的测试。
//
// 为什么这些测试值得写：2026-09-25 在 47.108.173.29（国内阿里云）上实测，
// 同一份 checksums.txt 连试 6 次只有 2 次成功，其余是 20~25 秒后超时。
// 那台机器上「一键更新」第一次就因此失败了。重试是让这个功能可用的
// 关键一环，而重试最典型的错误是「重试了不该重试的错误」——
// 比如对一个 404 重试三次、或者把校验失败也重试掉（那会让
// 「文件被替换」看起来只是「网络抖了一下」）。
//
// 所以这里逐个把「该重试」与「不该重试」钉死。

// 暂时性退避会让测试变慢（2s + 4s）。这里把退避缩短到一个可接受的值，
// 测试结束时还原 —— 这不影响生产行为（生产用 release.go 里的常量）。
func shortenRetryBackoff(t *testing.T) {
	t.Helper()
	prev := retryBackoffBase
	retryBackoffBase = 5 * time.Millisecond
	t.Cleanup(func() { retryBackoffBase = prev })
}

func TestRetrySucceedsAfterTransientFailures(t *testing.T) {
	shortenRetryBackoff(t)

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 前两次 503（典型的暂时性故障），第三次成功 ——
		// 这正是国内链路上「时通时断」的形状
		if atomic.AddInt32(&calls, 1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	got, err := fetchSmallFile(context.Background(), noRedirectClient(), srv.URL+"/x", 1<<20, permissiveCheck)
	if err != nil {
		t.Fatalf("503 之后应当重试成功，实际报错: %v", err)
	}
	if string(got) != "ok" {
		t.Errorf("内容不对: %q", got)
	}
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Errorf("应当请求 3 次（2 次失败 + 1 次成功），实际 %d 次", n)
	}
}

func TestRetryGivesUpAfterMaxAttempts(t *testing.T) {
	shortenRetryBackoff(t)

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := fetchSmallFile(context.Background(), noRedirectClient(), srv.URL+"/x", 1<<20, permissiveCheck)
	if err == nil {
		t.Fatal("持续 503 应当最终失败")
	}
	if n := atomic.LoadInt32(&calls); n != maxDownloadAttempts {
		t.Errorf("应当尝试 %d 次后放弃，实际 %d 次", maxDownloadAttempts, n)
	}
}

// 404 重试一万次也不会有 —— 而且每次重试都要等退避，白让用户等
func TestRetryDoesNotRetry404(t *testing.T) {
	shortenRetryBackoff(t)

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := fetchSmallFile(context.Background(), noRedirectClient(), srv.URL+"/x", 1<<20, permissiveCheck); err == nil {
		t.Fatal("404 应当报错")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("404 不该重试，实际请求 %d 次", n)
	}
}

// 地址被白名单拒绝是确定性判断，重试毫无意义
func TestRetryDoesNotRetryRejectedTarget(t *testing.T) {
	shortenRetryBackoff(t)

	var checks int32
	check := func(string) error {
		atomic.AddInt32(&checks, 1)
		return errTestBlocked
	}

	if _, err := fetchSmallFile(context.Background(), noRedirectClient(), "https://example.invalid/x", 1<<20, check); err == nil {
		t.Fatal("被拒绝的地址应当报错")
	}
	if n := atomic.LoadInt32(&checks); n != 1 {
		t.Errorf("确定性拒绝不该重试，实际校验 %d 次", n)
	}
}

// 网络层错误（连不上）必须重试 —— 这是国内链路最主要的一种失败
func TestRetryOnConnectionError(t *testing.T) {
	shortenRetryBackoff(t)

	// 先起一个服务器拿到一个真实端口，再关掉它 —— 得到一个「连接被拒」的地址
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := srv.URL
	srv.Close()

	start := time.Now()
	_, err := fetchSmallFile(context.Background(), noRedirectClient(), deadURL+"/x", 1<<20, permissiveCheck)
	if err == nil {
		t.Fatal("连不上应当报错")
	}
	// 三次尝试之间有退避（测试里缩短过），所以只要确认不是立即返回
	if elapsed := time.Since(start); elapsed < 10*time.Millisecond {
		t.Errorf("应当在尝试之间退避，实际只用了 %v", elapsed)
	}
}

// 上下文取消时立刻停手，不能把取消当成暂时性失败继续重试
func TestRetryStopsOnContextCancel(t *testing.T) {
	// 这里用真实退避（几秒），但上下文在第一次失败后就已取消，
	// 所以不会真的等满
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := fetchSmallFile(ctx, noRedirectClient(), srv.URL+"/x", 1<<20, permissiveCheck)
	if err == nil {
		t.Fatal("取消后应当报错")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("错误应当能被 errors.Is(err, context.Canceled) 识别，实际: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n > 1 {
		t.Errorf("取消后不该继续请求，实际 %d 次", n)
	}
}
