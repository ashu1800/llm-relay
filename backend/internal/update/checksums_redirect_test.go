package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// errTestBlocked 是测试用的「这一跳被拒绝」错误，模拟白名单拒绝。
var errTestBlocked = errors.New("该跳被白名单拒绝")

// noRedirectClient 复刻生产客户端的跳转策略。
//
// **这一步不能省**：httptest.Server.Client() 用的是默认策略（自动跟随
// 最多 10 跳），拿它做测试会得到全绿的结果 —— 但测的是 Go 标准库，
// 不是我们的循环。生产上 api/fetcher 两个 client 都把 CheckRedirect
// 设成 ErrUseLastResponse，所以 302 才会被代码看到（旧实现正是死在
// 收到 302 就报错）。测试必须用同一种 client 才有意义。
func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// 跳转抓取的回归测试。
//
// 背景（v0.1.1 首次一键更新实测踩到）：GitHub 的 release 资产——包括
// checksums.txt——都会 302 到 release-assets.githubusercontent.com 的签名
// 地址。旧实现假设「校验和地址不跳转」，遇到 302 直接报
// 「下载校验和返回 302」，整次更新失败。
//
// 测试直接打 fetchSmallFile 而不是 FetchChecksums：后者的校验强制
// https + 公网 IP，httptest 起的本地 http 服务器根本进不去，于是跳转
// 这段逻辑会永远测不到。校验作为参数注入，循环本身被真实覆盖；
// validateDownloadURL / netguard 各自有独立测试（见 TestValidateDownloadURL）。

// permissiveCheck 只在测试里用：放行所有地址，让循环逻辑可达。
func permissiveCheck(string) error { return nil }

func TestFetchSmallFileFollowsRedirect(t *testing.T) {
	const body = "abc123  llm-relay_0.1.1_linux_amd64.tar.gz\n"

	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer cdn.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, cdn.URL+"/asset", http.StatusFound)
	})
	main := httptest.NewServer(mux)
	defer main.Close()

	got, err := fetchSmallFile(context.Background(), noRedirectClient(), main.URL+"/redirect", 1<<20, permissiveCheck)
	if err != nil {
		t.Fatalf("302 后应能取到内容，实际报错: %v", err)
	}
	if strings.TrimSpace(string(got)) != strings.TrimSpace(body) {
		t.Errorf("内容不一致：got %q want %q", got, body)
	}
}

// 多级跳转：GitHub 实测会 302 到 release-assets，那个域还可能再跳一次
func TestFetchSmallFileFollowsMultipleHops(t *testing.T) {
	const body = "deadbeef  llm-relay_0.1.1_linux_amd64.tar.gz\n"

	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer final.Close()

	mid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/asset", http.StatusFound)
	}))
	defer mid.Close()

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, mid.URL+"/next", http.StatusMovedPermanently)
	}))
	defer first.Close()

	got, err := fetchSmallFile(context.Background(), noRedirectClient(), first.URL+"/start", 1<<20, permissiveCheck)
	if err != nil {
		t.Fatalf("多级跳转应能取到内容，实际报错: %v", err)
	}
	if strings.TrimSpace(string(got)) != strings.TrimSpace(body) {
		t.Errorf("内容不一致：got %q want %q", got, body)
	}
}

// 跳转循环必须**每一跳**都过校验，不能只在开头校验一次
func TestFetchSmallFileChecksEveryHop(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("attacker-controlled"))
	}))
	defer target.Close()

	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/asset", http.StatusFound)
	}))
	defer main.Close()

	// 第一跳放行、第二跳拒绝：模拟「白名单域名被 302 劫持」
	seen := 0
	check := func(string) error {
		seen++
		if seen > 1 {
			return errTestBlocked
		}
		return nil
	}

	_, err := fetchSmallFile(context.Background(), noRedirectClient(), main.URL+"/start", 1<<20, check)
	if err == nil {
		t.Fatal("第二跳被拒时应报错，实际却成功了（说明只校验了第一跳）")
	}
	if seen < 2 {
		t.Errorf("校验应至少执行两次，实际 %d 次", seen)
	}
}

func TestFetchSmallFileReportsNonOKStatus(t *testing.T) {
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer main.Close()

	_, err := fetchSmallFile(context.Background(), noRedirectClient(), main.URL+"/missing", 1<<20, permissiveCheck)
	if err == nil {
		t.Fatal("404 应当报错")
	}
	// 报错里要能看出状态码，否则排障时只知道「失败了」不知道「为什么」
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("报错应包含状态码 404，实际: %v", err)
	}
}

func TestFetchSmallFileEnforcesSizeLimit(t *testing.T) {
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer main.Close()

	if _, err := fetchSmallFile(context.Background(), noRedirectClient(), main.URL+"/big", 1024, permissiveCheck); err == nil {
		t.Fatal("超过上限应当报错")
	}
}

// 302 但没有 Location：不能当成成功，也不能死循环
func TestFetchSmallFileRejectsRedirectWithoutLocation(t *testing.T) {
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusFound)
	}))
	defer main.Close()

	if _, err := fetchSmallFile(context.Background(), noRedirectClient(), main.URL+"/bad", 1<<20, permissiveCheck); err == nil {
		t.Fatal("302 缺 Location 应当报错")
	}
}

// 无限跳转必须中止（这正是手动跟随跳转要设上限的原因）
func TestFetchSmallFileStopsEndlessRedirect(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/again", http.StatusFound)
	}))
	defer srv.Close()

	if _, err := fetchSmallFile(context.Background(), noRedirectClient(), srv.URL+"/loop", 1<<20, permissiveCheck); err == nil {
		t.Fatal("无限跳转应当被中止")
	}
}
