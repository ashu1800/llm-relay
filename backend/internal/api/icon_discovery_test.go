package api

import (
	"net/url"
	"testing"
)

// 图标发现是「兼容市面上绝大部分站点」的核心，全是纯函数，可以脱离网络测。
//
// 为什么值得一组专门的测试：这一段的行为是**概率性**的 —— 抓不到图标时
// 用户只看到「没找到」，无法判断是站点没有图标还是我们的发现逻辑漏了
// 某种写法。真实站点上的写法差异极大（属性无引号、rel 多值、大小写混写、
// 图标放 CDN、只声明 manifest），每一种漏掉都会让一批站点永远抓不到，
// 而且不会报错。

func TestDiscoverIconLinks(t *testing.T) {
	cases := []struct {
		name     string
		html     string
		wantURLs []string
		wantBase string
	}{
		{
			name:     "最常见形态",
			html:     `<html><head><link rel="icon" href="/favicon.ico"></head></html>`,
			wantURLs: []string{"/favicon.ico"},
		},
		{
			name:     "属性无引号（合法 HTML，example.com 就是这么写的）",
			html:     `<link rel=icon href=/favicon.png>`,
			wantURLs: []string{"/favicon.png"},
		},
		{
			name:     "rel 多值 shortcut icon",
			html:     `<link rel="shortcut icon" href="/a.ico">`,
			wantURLs: []string{"/a.ico"},
		},
		{
			name:     "大小写混写",
			html:     `<LINK REL="Icon" HREF="/A.ICO">`,
			wantURLs: []string{"/A.ICO"},
		},
		{
			name:     "单引号",
			html:     `<link rel='icon' href='/sq.ico'>`,
			wantURLs: []string{"/sq.ico"},
		},
		{
			name:     "apple-touch-icon 排在最前",
			html:     `<link rel="icon" href="/small.ico"><link rel="apple-touch-icon" href="/big.png">`,
			wantURLs: []string{"/big.png", "/small.ico"},
		},
		{
			name: "带 sizes 时大的优先",
			html: `<link rel="icon" sizes="16x16" href="/s.ico">` +
				`<link rel="icon" sizes="192x192" href="/l.png">`,
			wantURLs: []string{"/l.png", "/s.ico"},
		},
		{
			name:     "mask-icon",
			html:     `<link rel="mask-icon" href="/mask.svg" color="#000">`,
			wantURLs: []string{"/mask.svg"},
		},
		{
			name:     "属性跨行",
			html:     "<link\n  rel=\"icon\"\n  href=\"/multi.ico\"\n>",
			wantURLs: []string{"/multi.ico"},
		},
		{
			name:     "自闭合",
			html:     `<link rel="icon" href="/self.ico" />`,
			wantURLs: []string{"/self.ico"},
		},
		{
			name:     "带着 base href",
			html:     `<head><base href="https://cdn.example.com/assets/"><link rel="icon" href="icon.png"></head>`,
			wantURLs: []string{"icon.png"},
			wantBase: "https://cdn.example.com/assets/",
		},
		{
			name:     "没有图标声明",
			html:     `<html><head><title>x</title></head></html>`,
			wantURLs: nil,
		},
		{
			name:     "rel 不含 icon 的不收",
			html:     `<link rel="stylesheet" href="/a.css">`,
			wantURLs: nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			links, base := discoverIconLinks([]byte(c.html))
			if len(links) != len(c.wantURLs) {
				t.Fatalf("解析出 %d 个图标，期望 %d：%+v", len(links), len(c.wantURLs), links)
			}
			for i, want := range c.wantURLs {
				if links[i].URL != want {
					t.Errorf("第 %d 个图标应为 %q，实际 %q（顺序即优先级）", i, want, links[i].URL)
				}
			}
			if base != c.wantBase {
				t.Errorf("base href 应为 %q，实际 %q", c.wantBase, base)
			}
		})
	}
}

func TestResolveIconURL(t *testing.T) {
	page, _ := url.Parse("https://api.example.com/v1/chat")

	cases := []struct {
		name     string
		href     string
		baseHref string
		want     string
	}{
		{"绝对地址", "https://cdn.x.com/i.png", "", "https://cdn.x.com/i.png"},
		{"根相对", "/favicon.ico", "", "https://api.example.com/favicon.ico"},
		{"相对路径", "icon.png", "", "https://api.example.com/v1/icon.png"},
		{"协议相对", "//cdn.x.com/a.png", "", "https://cdn.x.com/a.png"},
		{"base 为绝对地址", "icon.png", "https://cdn.x.com/assets/", "https://cdn.x.com/assets/icon.png"},
		{"base 为根相对", "icon.png", "/static/", "https://api.example.com/static/icon.png"},
		{"data URI 原样返回", "data:image/png;base64,AAA", "", "data:image/png;base64,AAA"},
		{"空 href", "", "", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveIconURL(c.href, page, c.baseHref); got != c.want {
				t.Errorf("应为 %q，实际 %q", c.want, got)
			}
		})
	}
}

// manifest 是现代站点最常见的高清图标来源：只声明 <link rel="manifest">，
// 图标全在 json 里。不读它就只能拿到 16×16 的 favicon.ico。
func TestParseManifestIcons(t *testing.T) {
	data := []byte(`{
		"name": "App",
		"icons": [
			{"src": "/icon-192.png", "sizes": "192x192", "type": "image/png"},
			{"src": "/icon-512.png", "sizes": "512x512", "type": "image/png"},
			{"src": "/icon-any.png", "sizes": "any"}
		]
	}`)
	got := parseManifestIcons(data)
	if len(got) != 3 {
		t.Fatalf("应解析出 3 个图标，实际 %d", len(got))
	}
	// 大的排前面：512 才是想要的，顺序不对会先拿到 192
	if got[0].URL != "/icon-512.png" {
		t.Errorf("512 应排第一，实际 %q", got[0].URL)
	}
	if got[1].URL != "/icon-192.png" {
		t.Errorf("192 应排第二，实际 %q", got[1].URL)
	}

	// 非法 JSON 不能 panic，返回空即可（站点的问题不该让抓取崩掉）
	if out := parseManifestIcons([]byte(`{oops`)); len(out) != 0 {
		t.Errorf("非法 manifest 应返回空，实际 %+v", out)
	}
	// 没有 src 的条目跳过
	if out := parseManifestIcons([]byte(`{"icons":[{"sizes":"16x16"}]}`)); len(out) != 0 {
		t.Errorf("缺 src 应跳过，实际 %+v", out)
	}
}

func TestFindManifestHref(t *testing.T) {
	page := `<head><link rel="manifest" href="/site.webmanifest"><link rel="icon" href="/f.ico"></head>`
	if got := findManifestHref(page); got != "/site.webmanifest" {
		t.Errorf("应找到 manifest 地址，实际 %q", got)
	}
	if got := findManifestHref(`<head><link rel="icon" href="/f.ico"></head>`); got != "" {
		t.Errorf("没有 manifest 时应返回空，实际 %q", got)
	}
}

func TestSizeOf(t *testing.T) {
	cases := map[string]int{
		"192x192":     192,
		"16x16 32x32": 16,
		"any":         0,
		"":            0,
		"512X512":     512,
		"garbage":     0,
		"32x32,":      32,
	}
	for in, want := range cases {
		if got := sizeOf(in); got != want {
			t.Errorf("sizeOf(%q) 应为 %d，实际 %d", in, want, got)
		}
	}
}
