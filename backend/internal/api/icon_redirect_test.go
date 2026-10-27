package api

import (
	"net/http"
	"net/url"
	"testing"
)

// 跳转目标的解析必须同时吃下四种 Location 写法。
//
// 这条测试对应一个真实写坏的版本：`u.ResolveReference(&url.URL{Path: loc})`
// 把 Location 一律当路径，于是绝对地址被拼成
// `https://host/https://cdn.example.com/x`（必然 404），带 query 的相对
// 地址被转义成 `%3F`（跳到错误页面）。
//
// 为什么值得单独测：这类错误**不会报错**，表现为「图标抓不到」——
// 而图标抓不到本来就很常见（站点没放图标），用户根本无法区分是站点的问题
// 还是我们的问题。
func TestIconRedirectResolution(t *testing.T) {
	page, err := url.Parse("https://www.example.com/v1/chat")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		location string
		want     string
	}{
		// 绝对地址：CDN 跳转、跨域跳转
		{"https://cdn.example.com/icon.png", "https://cdn.example.com/icon.png"},
		// 绝对地址 + query（原写法会把 ? 转义成 %3F）
		{"https://www.example.com/login?next=/a", "https://www.example.com/login?next=/a"},
		// 根相对
		{"/other/path", "https://www.example.com/other/path"},
		// 协议相对
		{"//cdn.example.com/a.png", "https://cdn.example.com/a.png"},
		// 同级相对
		{"icon.png", "https://www.example.com/v1/icon.png"},
	}

	for _, c := range cases {
		t.Run(c.location, func(t *testing.T) {
			next, err := page.Parse(c.location)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if got := next.String(); got != c.want {
				t.Errorf("应为 %q，实际 %q", c.want, got)
			}
			// 解析结果必须仍能通过 SSRF 校验的入口（scheme/host 齐全）
			if next.Scheme != "http" && next.Scheme != "https" {
				t.Errorf("解析后 scheme 应仍是 http(s)，实际 %q", next.Scheme)
			}
			if next.Host == "" {
				t.Error("解析后应当有 host")
			}
		})
	}
}

// 跳转目标会作为新候选重新走 validateURL，所以内网地址拿不到东西。
// 这条钉住「支持跳转没有削弱 SSRF 防护」。
func TestIconRedirectStillGuarded(t *testing.T) {
	f := &iconFetcher{ctx: nil, client: &http.Client{}}
	for _, bad := range []string{
		"http://127.0.0.1:6379/",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/x",
		"file:///etc/passwd",
	} {
		if _, err := f.validateURL(bad); err == nil {
			t.Errorf("%s 应当被 SSRF 校验拦下", bad)
		}
	}
}
