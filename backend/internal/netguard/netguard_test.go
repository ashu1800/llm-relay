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
