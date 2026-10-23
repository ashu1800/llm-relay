package api

import "testing"

// 这一组测试覆盖的是一次真实部署里踩到的坑（2026-10-23，Windows + 内网穿透）：
//
//	public_origin 留空时，中间件回落到「当前请求的 Host」。原来的实现按
//	c.Request.TLS 推断 scheme，再拼成 "http://" + Host 去比较。穿透隧道
//	（frp / ngrok / cloudflare tunnel）在边缘终结 TLS，转给本机的是明文
//	HTTP，于是 c.Request.TLS 为 nil、期望值变成 "http://xxx.example.com"，
//	而浏览器的 Origin 是 "https://xxx.example.com" —— scheme 不一致，403。
//
//	症状是「穿透之后后台登录成功，但所有管理接口都 403，页面一片空白」，
//	而被拒绝的原因在日志里看起来完全合理（跨站请求），查起来毫无方向。
//
// 结论：回落路径只比 host，不比 scheme。scheme 是否可信取决于隧道层，
// 本进程看不到；跨站防护靠的是 host 不匹配，scheme 在这里不提供任何额外
// 防护 —— 能伪造 Origin 的攻击者本来就能同时伪造 scheme。
func TestSameOriginMatchesFallbackIgnoresScheme(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		host   string
		want   bool
	}{
		{"明文直连，scheme 一致", "http://192.168.1.5:8888", "192.168.1.5:8888", true},
		{"隧道终结 TLS，Origin 是 https 而连接是明文", "https://relay.example.com", "relay.example.com", true},
		{"本机回环", "http://localhost:8888", "localhost:8888", true},
		{"端口不同必须拒绝", "https://relay.example.com:8443", "relay.example.com", false},
		{"主机不同必须拒绝", "https://evil.example.com", "relay.example.com", false},
		{"子域冒充必须拒绝", "https://relay.example.com.evil.com", "relay.example.com", false},
		{"带路径的 Origin 不合法", "https://relay.example.com/console", "relay.example.com", false},
		{"Origin 为 null 字符串（沙箱 iframe）必须拒绝", "null", "relay.example.com", false},
		{"空 Origin", "", "relay.example.com", false},
		{"Host 为空", "https://relay.example.com", "", false},
		{"大小写不敏感", "https://Relay.Example.COM", "relay.example.com", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameOriginMatches(tc.origin, tc.host); got != tc.want {
				t.Fatalf("sameOriginMatches(%q, %q) = %v，期望 %v", tc.origin, tc.host, got, tc.want)
			}
		})
	}
}

// 显式配置 public_origin 时仍是严格比较：scheme 与 host 都要一致。
// 这条路径没有「隧道终结 TLS」的问题 —— 用户写的就是浏览器里看到的地址。
func TestSameOriginValueStaysStrict(t *testing.T) {
	if sameOriginValue("https://relay.example.com", "http://relay.example.com") {
		t.Fatal("显式配置的来源必须连 scheme 一起比对")
	}
	if !sameOriginValue("https://relay.example.com", "https://relay.example.com") {
		t.Fatal("完全一致的来源应放行")
	}
}
