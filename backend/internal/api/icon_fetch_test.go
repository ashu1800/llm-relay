package api

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"llm-relay/internal/netguard"
)

// roundTripFunc 让测试能给抓取器装一个「一被调用就失败」的传输层：
// 守卫类用例靠它证明「不合格的地址在建连之前就被拒了」，
// 而不是碰巧连不上、也不是真的打了一次网络。
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func neverCalledClient(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("不合格的地址不该发起任何请求")
		return nil, errors.New("不该走到这里")
	})}
}

// 图标值是不是「要抓回来缓存的图片外链」——这是保存接口决定要不要抓的判据，
// 判错一边是白抓一次（data URI 也去抓），另一边是漏抓（外链直接落库）。
func TestIsExternalIconURL(t *testing.T) {
	external := []string{
		"http://example.com/a.png",
		"https://example.com/a.png",
		"HTTP://EXAMPLE.COM/A.PNG", // scheme 大小写不敏感，浏览器也认
		"  https://example.com/a.png  ",
	}
	for _, v := range external {
		if !isExternalIconURL(v) {
			t.Errorf("%q 是图片外链，应当判为需要抓取", v)
		}
	}
	local := []string{
		"",
		"   ",
		"data:image/png;base64,iVBORw0KGgo=",
		"deepseek.svg",        // 内置图标文件名
		"🤖",                   // 一两个字符
		"/client-icons/x.svg", // 站内路径本来就是本地资源
		"//example.com/a.png", // 协议相对地址前端不认（会回落内置），后端也不必猜
		"ftp://example.com/a.png",
	}
	for _, v := range local {
		if isExternalIconURL(v) {
			t.Errorf("%q 不是图片外链，不该触发抓取", v)
		}
	}
}

// 守卫必须排在发请求之前：非 http(s)、内网与本机地址都只能被拒绝，
// 且一次网络请求都不能发出去（见 neverCalledClient）。
func TestIconFetcherGuards(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		blocked  bool // 是否应当被判为「目标不允许」（netguard.ErrBlocked）
		contains string
	}{
		{name: "file 协议会让接口变成任意文件读取", raw: "file:///etc/passwd", contains: "只支持从 http/https"},
		{name: "ftp 同样不支持", raw: "ftp://example.com/a.png", contains: "只支持从 http/https"},
		{name: "漏写协议头", raw: "example.com/a.png", contains: "只支持从 http/https"},
		{name: "只有协议头没有主机", raw: "http://", contains: "不是一个可用的地址"},
		{name: "空地址", raw: "   ", contains: "图标地址为空"},
		{name: "回环地址", raw: "http://127.0.0.1:9/a.png", blocked: true},
		{name: "localhost 名字", raw: "http://localhost:9/a.png", blocked: true},
		{name: "私网地址", raw: "http://10.0.0.1/a.png", blocked: true},
		{name: "云元数据地址", raw: "http://169.254.169.254/latest/meta-data/", blocked: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &iconFetcher{ctx: t.Context(), client: neverCalledClient(t)}
			_, err := f.fetch(c.raw)
			if err == nil {
				t.Fatalf("%q 应当被拒绝，实际放行", c.raw)
			}
			if c.blocked && !errors.Is(err, netguard.ErrBlocked) {
				t.Errorf("应当判为地址被拦（ErrBlocked），实际 %v", err)
			}
			if c.contains != "" && !strings.Contains(err.Error(), c.contains) {
				t.Errorf("错误信息期望包含 %q，实际 %v", c.contains, err)
			}
		})
	}
}

// 成功路径：守卫放行 → 发请求 → 校验响应 → 转 data URI。
//
// 传输层是假的、地址用**公网 IP 字面量**（93.184.216.34，netguard 的单测
// 里就是它当「公网」样本）：这样 CheckHost 不需要 DNS、也不用真联网 ——
// 本机的 httptest 服务器是测不了的，回环地址正是守卫要拒的东西。
func TestIconFetcherSuccessPath(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G'}
	var gotURL string
	f := &iconFetcher{
		ctx: t.Context(),
		client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotURL = r.URL.String()
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"image/png"}},
				Body:       io.NopCloser(bytes.NewReader(png)),
			}, nil
		})},
	}

	icon, err := f.fetch("http://93.184.216.34/logo.png")
	if err != nil {
		t.Fatalf("应当抓到，实际 %v", err)
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	if icon != want {
		t.Errorf("期望 %q，实际 %q", want, icon)
	}
	if gotURL != "http://93.184.216.34/logo.png" {
		t.Errorf("请求地址应当原样发出（含路径），实际 %q", gotURL)
	}
}

// 抓取用的 http.Client 必须是「禁跳转」的那一个：目标通过 302 跳到内网时
// 跟随过去就等于绕过了 CheckHost。这条断言盯的是 newIconFetcher 真的把
// netguard.GuardedClient 套上了（守卫本身的单测在 netguard 包里）。
func TestNewIconFetcherDisablesRedirects(t *testing.T) {
	// proxyID=0 的路径不碰 Store，所以零值 Server 就够
	f, cleanup, err := (&Server{}).newIconFetcher(t.Context(), 0)
	if err != nil {
		t.Fatalf("构建抓取会话失败: %v", err)
	}
	defer cleanup()
	if f.client.CheckRedirect == nil {
		t.Fatal("CheckRedirect 未设置：跳转会被跟随，SSRF 防护形同虚设")
	}
	if err := f.client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("应当以 ErrUseLastResponse 拒绝跟随跳转，实际 %v", err)
	}
}

// 环境变量里的系统代理必须被豁免。
//
// 这条是实测踩出来的：开发机上设着 HTTPS_PROXY=http://127.0.0.1:10808
// （本机 Clash 的常见形态），不豁免时每个图标请求都变成
// 「proxyconnect ... 目标地址指向内网或本机，已拒绝: 127.0.0.1」，
// 表现是「图标怎么也抓不到」，而用户只会以为是自己地址填错了。
func TestNewIconFetcherExemptsEnvProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7890")

	f, cleanup, err := (&Server{}).newIconFetcher(t.Context(), 0)
	if err != nil {
		t.Fatalf("构建抓取会话失败: %v", err)
	}
	defer cleanup()
	tr, ok := f.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport 类型不对: %T", f.client.Transport)
	}
	// 代理地址豁免：最多是连不上（本机没代理在听），不能报「被拦」
	if _, err := tr.DialContext(t.Context(), "tcp", "127.0.0.1:7890"); errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("环境变量里的系统代理不该被当成内网目标拦下，实际 %v", err)
	}
	// 豁免只针对代理自己那一个地址，真实目标照旧校验
	if _, err := tr.DialContext(t.Context(), "tcp", "169.254.169.254:80"); !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("云元数据地址必须照旧被拦，实际 %v", err)
	}
}

// 响应的四道校验：状态码 / 内容类型 / 空内容 / 体积。
//
// 这些都用构造出来的 http.Response 覆盖 —— 起不了本机 httptest 服务器
// 来测（回环地址会被 netguard 拒掉，那正是它该做的事），
// 所以把校验逻辑单独拆成 readIconResponse 就是为了能在这里逐条钉住。
func TestReadIconResponse(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	respOf := func(status int, ct string, body []byte) *http.Response {
		h := http.Header{}
		if ct != "" {
			h.Set("Content-Type", ct)
		}
		return &http.Response{
			StatusCode: status,
			Header:     h,
			Body:       io.NopCloser(bytes.NewReader(body)),
		}
	}

	t.Run("正常图片转成 data URI", func(t *testing.T) {
		got, err := readIconResponse(respOf(200, "image/png", png), "https://a.com/a.png")
		if err != nil {
			t.Fatalf("应当成功，实际 %v", err)
		}
		// data URI 必须是「原字节的 base64」：浏览器拿到的就是这张图
		want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		if got != want {
			t.Errorf("data URI 期望 %q，实际 %q", want, got)
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, "data:image/png;base64,"))
		if err != nil || !bytes.Equal(raw, png) {
			t.Errorf("base64 解码后应与原文件一致，实际 %v / %v", raw, err)
		}
	})

	t.Run("SVG 也认（客户端图标常用 svg）", func(t *testing.T) {
		svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
		got, err := readIconResponse(respOf(200, "image/svg+xml", svg), "https://a.com/a.svg")
		if err != nil {
			t.Fatalf("应当成功，实际 %v", err)
		}
		if !strings.HasPrefix(got, "data:image/svg+xml;base64,") {
			t.Errorf("Content-Type 应当原样进 data URI，实际 %q", got)
		}
	})

	t.Run("Content-Type 的参数被丢弃（上游可控，不能原样拼进库）", func(t *testing.T) {
		// 这条头是上游返回的，Go 允许约 10MB 的响应头：原样拼进 data URI
		// 会让入库值远超「64KB 图片 ≈ 87KB base64」的口径，而界面上看不出
		// 异常。只保留 `image/<子类型>`，参数一律丢。
		junk := strings.Repeat("x", 200<<10)
		got, err := readIconResponse(
			respOf(200, "image/png; file="+junk, png), "https://a.com/a.png")
		if err != nil {
			t.Fatalf("带参数的正确图片应当通过，实际 %v", err)
		}
		if !strings.HasPrefix(got, "data:image/png;base64,") {
			t.Errorf("data URI 前缀应只含 media type，实际 %q", got[:min(len(got), 80)])
		}
		if len(got) > maxIconBytes*2 {
			t.Errorf("data URI 长度 %d 超出图片上限对应的编码长度 %d", len(got), maxIconBytes*2)
		}
	})

	t.Run("Content-Type 本身畸形时按非图片拒", func(t *testing.T) {
		_, err := readIconResponse(respOf(200, "image/png; =", png), "https://a.com/a.png")
		if err == nil || !strings.Contains(err.Error(), "返回的不是图片") {
			t.Errorf("畸形 Content-Type 应当被拒，实际 %v", err)
		}
	})

	t.Run("状态码非 200", func(t *testing.T) {
		_, err := readIconResponse(respOf(404, "image/png", png), "https://a.com/a.png")
		if err == nil || !strings.Contains(err.Error(), "返回 HTTP 404") {
			t.Errorf("应当报状态码，实际 %v", err)
		}
	})

	t.Run("跳转要给出可照做的提示", func(t *testing.T) {
		// 不跟随跳转是刻意的（见 readIconResponse），但错误信息必须让用户
		// 知道下一步做什么：粘贴跳转后的直达地址，而不是以为抓取坏了
		_, err := readIconResponse(respOf(302, "image/png", png), "https://a.com/short")
		if err == nil || !strings.Contains(err.Error(), "不跟随跳转") || !strings.Contains(err.Error(), "直达图片地址") {
			t.Errorf("跳转的报错应当说明「不跟随跳转」并给出改法，实际 %v", err)
		}
	})

	t.Run("404 页面配了图片 Content-Type", func(t *testing.T) {
		_, err := readIconResponse(respOf(200, "text/html", []byte("<html>not found</html>")), "https://a.com/a.png")
		if err == nil || !strings.Contains(err.Error(), "返回的不是图片") {
			t.Errorf("非 image/ 内容类型应当被拒，实际 %v", err)
		}
	})

	t.Run("没有 Content-Type", func(t *testing.T) {
		if _, err := readIconResponse(respOf(200, "", png), "https://a.com/a.png"); err == nil {
			t.Error("缺 Content-Type 时应当被拒（浏览器也渲染不了）")
		}
	})

	t.Run("空内容", func(t *testing.T) {
		_, err := readIconResponse(respOf(200, "image/png", nil), "https://a.com/a.png")
		if err == nil || !strings.Contains(err.Error(), "空内容") {
			t.Errorf("空内容应当被拒，实际 %v", err)
		}
	})

	t.Run("正好 64KB 通过", func(t *testing.T) {
		body := bytes.Repeat([]byte{'x'}, maxIconBytes)
		if _, err := readIconResponse(respOf(200, "image/png", body), "https://a.com/a.png"); err != nil {
			t.Errorf("上限值应当通过，实际 %v", err)
		}
	})

	t.Run("超 1 字节就拒", func(t *testing.T) {
		body := bytes.Repeat([]byte{'x'}, maxIconBytes+1)
		_, err := readIconResponse(respOf(200, "image/png", body), "https://a.com/a.png")
		if err == nil || !strings.Contains(err.Error(), "超过 64KB") {
			t.Errorf("超限应当被拒，实际 %v", err)
		}
	})
}
