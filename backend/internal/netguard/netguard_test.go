package netguard

import (
	"errors"
	"net"
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
