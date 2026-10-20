package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
)

// 内网与本机地址必须被拒绝。
//
// 这些正是 SSRF 最常用的目标：本机服务、内网管理面、云元数据端点。
func TestCheckHostBlocksInternalTargets(t *testing.T) {
	blocked := []string{
		"127.0.0.1",
		"127.1.2.3",
		"::1",
		"localhost",
		"LOCALHOST",
		"foo.localhost",
		"something.local",
		"db.internal",
		"10.0.0.1",
		"10.255.255.255",
		"172.16.0.1",
		"172.31.255.255",
		"192.168.1.1",
		"169.254.169.254", // 云元数据
		"169.254.1.1",
		"0.0.0.0",
		"0.1.2.3",
		"100.64.0.1", // 运营商级 NAT
		"100.127.255.255",
		"224.0.0.1", // 组播
		"ff02::1",   // IPv6 链路本地组播
		"fc00::1",   // IPv6 唯一本地
		"fd12:3456::1",
	}
	for _, host := range blocked {
		if _, err := CheckHost(host); err == nil {
			t.Errorf("CheckHost(%q) 应当被拒绝，实际放行", host)
		} else if !errors.Is(err, ErrBlocked) {
			t.Errorf("CheckHost(%q) 的错误应可识别为 ErrBlocked，实际 %v", host, err)
		}
	}
}

// 公网地址必须放行，否则这个防护会误伤正常使用。
func TestCheckHostAllowsPublicTargets(t *testing.T) {
	allowed := []string{
		"8.8.8.8",
		"1.1.1.1",
		"93.184.216.34",
		"2606:4700:4700::1111",
	}
	for _, host := range allowed {
		if _, err := CheckHost(host); err != nil {
			t.Errorf("CheckHost(%q) 应当放行，实际 %v", host, err)
		}
	}
}

// 172.16/12 的边界：172.15 与 172.32 都不是私网。
func TestCheckHostPrivateRangeBoundaries(t *testing.T) {
	if _, err := CheckHost("172.15.255.255"); err != nil {
		t.Errorf("172.15.255.255 不是私网，应放行，实际 %v", err)
	}
	if _, err := CheckHost("172.32.0.1"); err != nil {
		t.Errorf("172.32.0.1 不是私网，应放行，实际 %v", err)
	}
	if _, err := CheckHost("172.16.0.0"); err == nil {
		t.Errorf("172.16.0.0 是私网，应拒绝")
	}
}

// 空主机名不能放行。
func TestCheckHostEmpty(t *testing.T) {
	if _, err := CheckHost(""); err == nil {
		t.Errorf("空主机名应被拒绝")
	}
	if _, err := CheckHost("   "); err == nil {
		t.Errorf("空白主机名应被拒绝")
	}
}

// Control 是防 DNS rebinding 的最后一道：它拿到的就是即将连接的真实地址。
func TestControlBlocksRealDialTarget(t *testing.T) {
	cases := []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:6379", false},
		{"169.254.169.254:80", false},
		{"10.0.0.5:8080", false},
		{"[::1]:443", false},
		{"8.8.8.8:443", true},
	}
	for _, tc := range cases {
		err := Control("tcp", tc.addr, nil)
		if tc.ok && err != nil {
			t.Errorf("Control(%q) 应放行，实际 %v", tc.addr, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("Control(%q) 应拒绝，实际放行", tc.addr)
		}
	}
}

// 畸形地址不能让 Control 误放行。
func TestControlRejectsMalformed(t *testing.T) {
	for _, addr := range []string{"", "not-an-address", "hostname-without-port"} {
		if err := Control("tcp", addr, nil); err == nil {
			t.Errorf("Control(%q) 应报错，实际放行", addr)
		}
	}
}

// GuardedTransport 必须真的装上 Control，否则防护形同虚设。
func TestGuardedTransportInstallsControl(t *testing.T) {
	tr := GuardedTransport(nil)
	if tr.DialContext == nil {
		t.Fatalf("DialContext 未设置")
	}
	// 直连一个内网地址应当被挡在建连之前
	_, err := tr.DialContext(t.Context(), "tcp", "127.0.0.1:9")
	if err == nil {
		t.Fatalf("连内网地址应当失败")
	}
	if !strings.Contains(err.Error(), "内网") && !errors.Is(err, ErrBlocked) {
		t.Fatalf("错误信息应说明是地址被拦，实际 %v", err)
	}
}

// 公网明文地址的校验不应被 DNS 影响。
func TestGuardedTransportAllowsPublicIPLiteral(t *testing.T) {
	tr := GuardedTransport(nil)
	// 只验证「不被我们的策略拦掉」；是否连得上取决于网络，不在此断言
	if _, err := tr.DialContext(t.Context(), "tcp", "127.0.0.1:1"); err == nil {
		t.Fatal("回环应当被拦")
	}
	var ip net.IP = net.ParseIP("8.8.8.8")
	if blocked, _ := ipBlocked(ip); blocked {
		t.Fatal("8.8.8.8 不该被判为禁止")
	}
}

// 豁免名单只放行列出的那个地址，其余照旧拦 —— 这条是「按地址豁免」的核心语义，
// 也是 update 包用来放行环境变量代理（127.0.0.1:7890）的依据。
func TestGuardedTransportExceptSkipsOnlyListedAddr(t *testing.T) {
	var dialed []string
	// 自带 DialContext：既避免真连网络，也验证「豁免时确实交给了底层拨号器」
	base := &http.Transport{DialContext: func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		c1, c2 := net.Pipe()
		_ = c2.Close()
		return c1, nil
	}}
	tr := GuardedTransportExcept(base, []string{"127.0.0.1:7890", "  "})

	if _, err := tr.DialContext(t.Context(), "tcp", "127.0.0.1:7890"); err != nil {
		t.Fatalf("名单里的地址（可信代理）应当放行，实际 %v", err)
	}
	if len(dialed) != 1 || dialed[0] != "127.0.0.1:7890" {
		t.Fatalf("豁免的地址应当交给底层拨号器，实际拨号记录 %v", dialed)
	}

	// 同一个主机、不同端口：不在名单里，照旧拦
	if _, err := tr.DialContext(t.Context(), "tcp", "127.0.0.1:7891"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("未列入名单的回环地址应被拦，实际 %v", err)
	}
	// 内网网关同理
	if _, err := tr.DialContext(t.Context(), "tcp", "10.0.0.1:7890"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("未列入名单的内网地址应被拦，实际 %v", err)
	}
	if len(dialed) != 1 {
		t.Fatalf("被拦的地址不该真的拨号，实际拨号记录 %v", dialed)
	}
}

// 名单为空（= GuardedTransport）时一个地址都不放行。
func TestGuardedTransportExceptEmptyListBlocksAll(t *testing.T) {
	tr := GuardedTransportExcept(&http.Transport{}, nil)
	if _, err := tr.DialContext(t.Context(), "tcp", "127.0.0.1:7890"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("空名单不该放行任何地址，实际 %v", err)
	}
}

// clearProxyEnv 清掉环境变量里的代理。
//
// 这些用例验的是「直连」与「豁免名单」两条路径，而开发机上完全可能设着
// HTTPS_PROXY=127.0.0.1:7890（本机跑 Clash 的常见形态）—— 那会让豁免名单
// 多出一项，断言随之漂移。t.Setenv 顺带保证用例不并行。
func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "all_proxy", "no_proxy",
	} {
		t.Setenv(k, "")
	}
}

// GuardedClient 必须把豁免名单一路带到建连校验里（图标抓取用它放行本机代理），
// 同时保留「禁跳转」这条 SSRF 防护。
func TestGuardedClientExceptExemptsOnlyListedAddr(t *testing.T) {
	c := GuardedClientExcept(&http.Client{}, []string{"127.0.0.1:7890"})
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport 类型不对: %T", c.Transport)
	}
	if _, err := tr.DialContext(t.Context(), "tcp", "127.0.0.1:7890"); errors.Is(err, ErrBlocked) {
		t.Fatal("名单里的地址（可信代理）不该被当成内网目标拦下")
	}
	if _, err := tr.DialContext(t.Context(), "tcp", "127.0.0.1:7891"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("名单外的回环地址应被拦，实际 %v", err)
	}
	if c.CheckRedirect == nil || !errors.Is(c.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
		t.Fatal("禁跳转必须保留：跟随 302 会绕过 CheckHost")
	}
}

// EnvProxyAddrs 的归一化：没写端口时按 scheme 补默认端口，
// 否则与 Go 实际拨号用的 "host:80"/"host:443" 对不上，豁免会静默失效；
// 没写 scheme 的简写要与 Go 一致地按 http 处理。
//
// ALL_PROXY 刻意**不**在读取清单里：Go 的 httpproxy 不认它（只读
// HTTP_PROXY / HTTPS_PROXY / NO_PROXY），豁免一个不会被拨打的地址
// 只会扩大安全面上的白名单。见 EnvProxyAddrs 的注释。
func TestEnvProxyAddrsNormalizesDefaultPorts(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	t.Setenv("HTTP_PROXY", "http://proxy.example")
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1080")

	got := EnvProxyAddrs()
	want := map[string]bool{
		"proxy.example:3128": true,
		"proxy.example:80":   true,
	}
	if len(got) != len(want) {
		t.Fatalf("应得到 %d 个地址，实际 %v", len(want), got)
	}
	for _, a := range got {
		if !want[a] {
			t.Errorf("出现了预期外的地址 %q（全部：%v）", a, got)
		}
	}
}

// socks5 的免端口写法是常见配置（Clash 的 socks 入口常写成
// socks5://127.0.0.1）。Go 的 schemePort 对 socks5/socks5h 补 1080，
// 这里不补就会豁免 "127.0.0.1" 而实际拨号 "127.0.0.1:1080" ——
// 精确匹配失效，合法代理被当成 SSRF 目标拦下（2026-10-19 实测）。
func TestEnvProxyAddrsSocks5DefaultPort(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "socks5://127.0.0.1")

	got := EnvProxyAddrs()
	if len(got) != 1 || got[0] != "127.0.0.1:1080" {
		t.Fatalf("socks5 免端口应归一成 127.0.0.1:1080，实际 %v", got)
	}
}

// Unicode 域名：Transport 在拨号前会做 IDNA ToASCII，豁免地址也必须转成
// punycode，否则两者对不上。
func TestEnvProxyAddrsIDN(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "http://bücher.example:3128")

	got := EnvProxyAddrs()
	if len(got) != 1 || got[0] != "xn--bcher-kva.example:3128" {
		t.Fatalf("IDN 主机应转成 punycode，实际 %v", got)
	}
}

func TestEnvProxyAddrsAcceptsSchemalessShorthand(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "127.0.0.1:7890") // 常见简写：Clash 用户常这么写

	got := EnvProxyAddrs()
	if len(got) != 1 || got[0] != "127.0.0.1:7890" {
		t.Fatalf("没写 scheme 的代理地址应当照样豁免，实际 %v", got)
	}
}
