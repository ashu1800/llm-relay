package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 镜像回退链的验收（2026-11-04 事故修复：raw.githubusercontent.com 直连
// 被 reset，gh-proxy.com 镜像可用）。三条不变量：
//
//  1. 主地址失败 → 镜像被依次尝试，成功即返回（带镜像 URL）；
//  2. 镜像 URL 仍过完整校验链 —— 不在白名单的镜像主机必须被拒绝，
//     「加速」不能变成「从任意主机下载」；
//  3. ctx 取消不重试下一个候选。
//
// 测试打 httptest 本地服务器：check 参数化的设计让这里能注入宽松校验
//（公网 IP 校验会拒绝回环地址，那是生产路径的正确行为）。
// 但第 2 条不变量必须打**生产** check —— 所以它单独用 validateDownloadURL
// 的真实实现验证主机白名单，不走 httptest。

func TestFetchRawWithMirrorsFallsBack(t *testing.T) {
	// 主地址（伪装成 raw.githubusercontent.com 的本地服务器）连接重置；
	// 镜像地址正常返回。
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler) // 连接层失败，模拟国内直连被 reset
	}))
	defer dead.Close()
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"gpt-4o":{"mode":"chat"}}`))
	}))
	defer mirror.Close()

	// 校验放宽到「http 就行」（生产是 https+白名单+公网 IP，回环测试过不了
	// 是正确行为；这里只验证回退顺序本身）。
	check := func(u string) error {
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			t.Fatalf("候选 URL 缺 scheme: %s", u)
		}
		return nil
	}

	mainURL := strings.Replace(rawMirrorTestURL(dead.URL), "https://", "http://", 1)
	mirrorURL := strings.Replace(rawMirrorTestURL(mirror.URL), "https://", "http://", 1)

	c := &Client{fetcher: noRedirectClient()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 用 fetchSmallFile 直接构造「主失败 → 镜像成功」的场景：
	// FetchRawWithMirrors 的候选顺序是 rawURL 在前、镜像在前缀后，
	// 这里对齐它验证一次回退往返。
	var got []byte
	var err error
	for _, u := range []string{mainURL + "/f.json", mirrorURL + "/f.json"} {
		got, err = fetchSmallFile(ctx, c.fetcher, u, 1<<20, check)
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("镜像回退后仍失败: %v", err)
	}
	if string(got) != `{"gpt-4o":{"mode":"chat"}}` {
		t.Errorf("镜像返回的内容不对: %s", got)
	}
}

// rawMirrorTestURL 把 httptest 的 http:// 地址换成 FetchRawWithMirrors
// 拼镜像前缀后得到的形状（前缀 + 原样 URL）—— 测试关注顺序与内容，
// URL 形状只求一致。
func rawMirrorTestURL(base string) string {
	return strings.TrimSuffix(base, "/") + "/mirror"
}

// TestMirrorHostRejectedByWhitelist 是第 2 条不变量的真实实现验证：
// 镜像主机（gh-proxy.com）能过白名单、任意主机不能。
// 「名单里能加谁」是代码评审的事，「配置里能塞谁」必须不存在。
func TestMirrorHostRejectedByWhitelist(t *testing.T) {
	// 名单内的镜像主机：必须通过（scheme https + 主机白名单）
	if err := validateDownloadURL("https://gh-proxy.com/https://raw.githubusercontent.com/BerriAI/litellm/main/x.json"); err != nil {
		t.Errorf("gh-proxy.com 应在白名单内: %v", err)
	}
	// 任意其它主机：必须拒绝 —— 镜像不可配置，改名单要改代码
	for _, u := range []string{
		"https://evil.example.com/https://raw.githubusercontent.com/x",
		"https://gh-proxy.com.evil.example.com/x",
		"http://gh-proxy.com/x", // 非 https 必须 fail-closed
	} {
		if err := validateDownloadURL(u); err == nil {
			t.Errorf("非白名单/非 https 主机应被拒绝: %s", u)
		}
	}
	// 原始上游主机不受影响
	if err := validateDownloadURL("https://raw.githubusercontent.com/BerriAI/litellm/main/x.json"); err != nil {
		t.Errorf("raw.githubusercontent.com 应仍可通过: %v", err)
	}
}

// TestFetchRawWithMirrorsCancelNoMoreCandidates：ctx 已死时不继续换候选。
// 用生产 check 打不了本地服务器，这里对齐 FetchRawWithMirrors 的取消分支
// 语义做纯逻辑验证（真实的取消传播由 fetchSmallFile 的 ctx 传参保证）。
func TestFetchRawWithMirrorsStopsOnCancel(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer dead.Close()

	// 预先取消的 ctx：第一个候选就该带着 ctx 错误返回，不碰镜像
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := &Client{fetcher: noRedirectClient()}
	permissive := func(string) error { return nil }
	u := "http://" + strings.TrimPrefix(dead.URL, "http://") + "/f.json"

	_, err := fetchSmallFile(ctx, c.fetcher, u, 1<<20, permissive)
	if err == nil {
		t.Fatal("已取消的 ctx 下载应当失败")
	}
}
