package proxy

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"llm-relay/internal/model"
)

// startSOCKS5 起一个最小的 SOCKS5 服务端（RFC 1928）：无认证与用户名密码
// 认证两种模式 + CONNECT 命令，够验证客户端的握手与转发是否按规范来。
//
// 为什么不引第三方 mock：这条路径的正确性完全取决于「字节有没有按规范发」，
// 拿一个真实实现去测另一个真实实现，两边一起错就发现不了。
func startSOCKS5(t *testing.T, wantUser, wantPass string) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleSOCKS5(conn, wantUser, wantPass)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func handleSOCKS5(conn net.Conn, wantUser, wantPass string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)

	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil || head[0] != 0x05 {
		return
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}
	if wantUser == "" {
		if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
			return
		}
	} else {
		if _, err := conn.Write([]byte{0x05, 0x02}); err != nil {
			return
		}
		// 用户名密码认证（RFC 1929）
		if _, err := io.ReadFull(br, head); err != nil {
			return
		}
		u := make([]byte, int(head[1]))
		if _, err := io.ReadFull(br, u); err != nil {
			return
		}
		// 注意 PLEN 只有 1 个字节（RFC 1929）。
		// 照着 ULEN 的写法读 2 个字节会把密码首字节吞掉，
		// 之后服务端一直在等剩下的输入，客户端看到的是「超时」而不是「认证失败」
		plen := make([]byte, 1)
		if _, err := io.ReadFull(br, plen); err != nil {
			return
		}
		p := make([]byte, int(plen[0]))
		if _, err := io.ReadFull(br, p); err != nil {
			return
		}
		if string(u) != wantUser || string(p) != wantPass {
			_, _ = conn.Write([]byte{0x01, 0x01})
			return
		}
		if _, err := conn.Write([]byte{0x01, 0x00}); err != nil {
			return
		}
	}

	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil || req[1] != 0x01 {
		return
	}
	var host string
	switch req[3] {
	case 0x01:
		b := make([]byte, 4)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 0x03:
		if _, err := io.ReadFull(br, req[:1]); err != nil {
			return
		}
		b := make([]byte, int(req[0]))
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = string(b)
	case 0x04:
		b := make([]byte, 16)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = net.IP(b).String()
	default:
		return
	}
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(br, portBuf); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(portBuf)

	upstream, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(port)), 3*time.Second)
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	go func() { _, _ = io.Copy(upstream, br) }()
	_, _ = io.Copy(conn, upstream)
}

// SOCKS5 全链路：握手、CONNECT、转发都要真的走通。
func TestTestThroughSOCKS5(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	addr, stop := startSOCKS5(t, "", "")
	defer stop()
	proxyHost, proxyPort, _ := net.SplitHostPort(addr)

	cfg := Config{Protocol: model.ProxyProtocolSOCKS5, Host: proxyHost, Port: atoi(t, proxyPort)}
	res := Test(context.Background(), cfg, target.URL, 5*time.Second)
	if !res.OK {
		t.Fatalf("应当连通，实际失败: %s", res.Error)
	}
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("状态码应为 204，实际 %d", res.StatusCode)
	}
	if res.LatencyMs < 0 {
		t.Errorf("耗时不合法: %d", res.LatencyMs)
	}
}

// 密码错时必须报「认证失败」，而不是含糊的超时 —— 后者会让人去查网络。
func TestSOCKS5AuthFailure(t *testing.T) {
	addr, stop := startSOCKS5(t, "user", "right-pass")
	defer stop()
	host, port, _ := net.SplitHostPort(addr)

	cfg := Config{
		Protocol: model.ProxyProtocolSOCKS5, Host: host, Port: atoi(t, port),
		Username: "user", Password: "wrong-pass",
	}
	res := Test(context.Background(), cfg, "http://127.0.0.1:9/", 3*time.Second)
	if res.OK {
		t.Fatal("密码错误不该算连通")
	}
	if !strings.Contains(res.Error, "认证失败") {
		t.Errorf("错误信息应指出是认证问题，实际 %q", res.Error)
	}
}

// HTTP 代理：请求必须真的经过代理转发到目标。
//
// 代理在这里是一个会转发的真实实现（而不是「收到就回 204」的空壳）：
// 空壳只能证明客户端连上了代理，证明不了请求被转出去了 ——
// 而后者才是「代理可用」的定义。
//
// 目标用明文 HTTP：https 目标会走 CONNECT 隧道（net/http 的标准行为），
// 而自签证书的目标 + 校验严格的客户端在测试里没法组合出可信结论，
// 与其用一个恒真的断言凑数，不如把明文这条路径测扎实。
func TestTestThroughHTTPProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	var gotURI string
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 经 HTTP 代理的请求行是绝对 URI，这里正好能确认「确实走了代理」
		gotURI = r.URL.String()
		out, err := http.Get(gotURI)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer out.Body.Close()
		w.WriteHeader(out.StatusCode)
		_, _ = io.Copy(w, out.Body)
	}))
	defer proxySrv.Close()
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(proxySrv.URL, "http://"))

	cfg := Config{Protocol: model.ProxyProtocolHTTP, Host: host, Port: atoi(t, port)}
	res := Test(context.Background(), cfg, target.URL, 3*time.Second)
	if !res.OK {
		t.Fatalf("应当连通，实际: %s", res.Error)
	}
	// 只比主机部分：客户端会把空路径补成 "/"（httptest 给的 URL 没有尾斜杠）
	if strings.TrimSuffix(gotURI, "/") != strings.TrimSuffix(target.URL, "/") {
		t.Errorf("请求应当经代理发到目标 %s，实际代理收到 %q", target.URL, gotURI)
	}
}

// 配置校验：把常见手误挡在保存之前。
func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"协议不支持", Config{Protocol: "ftp", Host: "h", Port: 1}, "不支持的代理协议"},
		{"地址为空", Config{Protocol: model.ProxyProtocolHTTP, Host: "  ", Port: 1}, "不能为空"},
		{"端口越界", Config{Protocol: model.ProxyProtocolHTTP, Host: "h", Port: 0}, "1-65535"},
		{"地址带协议前缀", Config{Protocol: model.ProxyProtocolHTTP, Host: "http://h", Port: 1}, "不要带协议前缀"},
		{"合法", Config{Protocol: model.ProxyProtocolSOCKS5, Host: "127.0.0.1", Port: 1080}, ""},
	}
	for _, c := range cases {
		err := c.cfg.Validate()
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: 不该报错，实际 %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: 期望包含 %q，实际 %v", c.name, c.want, err)
		}
	}
}

// URL() 的契约：产出的字符串必须能被 url.Parse 无损还原 ——
// 更新模块（internal/update）拿到的是字符串，要用 url.Parse 交给 http.ProxyURL，
// 还原不回去的 URL 到了那边才报错，就太晚了。
func TestConfigURL(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			"socks5 无认证",
			Config{Protocol: model.ProxyProtocolSOCKS5, Host: "127.0.0.1", Port: 1080},
			"socks5://127.0.0.1:1080",
		},
		{
			"http 带认证",
			Config{Protocol: model.ProxyProtocolHTTP, Host: "proxy.example.com", Port: 8080, Username: "u", Password: "p"},
			"http://u:p@proxy.example.com:8080",
		},
		{
			"https 特殊字符密码转义",
			Config{Protocol: model.ProxyProtocolHTTPS, Host: "proxy.example.com", Port: 8443, Username: "user", Password: "p@ss:word"},
			"https://user:p%40ss%3Aword@proxy.example.com:8443",
		},
	}
	for _, c := range cases {
		got := c.cfg.URL()
		if got != c.want {
			t.Errorf("%s: 期望 %q，实际 %q", c.name, c.want, got)
			continue
		}
		u, err := url.Parse(got)
		if err != nil {
			t.Errorf("%s: 产出的 URL 无法被 url.Parse 还原: %v", c.name, err)
			continue
		}
		if u.Scheme != c.cfg.Protocol || u.Hostname() != c.cfg.Host || u.Port() != strconv.Itoa(c.cfg.Port) {
			t.Errorf("%s: 还原结果不符: scheme=%q host=%q port=%q", c.name, u.Scheme, u.Hostname(), u.Port())
		}
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		t.Fatalf("解析端口失败: %v", err)
	}
	return n
}
