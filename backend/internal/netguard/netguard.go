// Package netguard 提供出站请求的目标校验，防止管理接口被当成 SSRF 跳板。
//
// 背景：图标抓取与代理测试这两个接口都接受用户指定的 URL 并发起真实请求。
// 只校验 scheme 是不够的 —— 攻击者可以填 http://127.0.0.1:6379/ 去探测内网端口，
// 或填云厂商的元数据地址 169.254.169.254 读实例凭据；再配合「返回状态码与
// 耗时会被回显」的差异，就能把内网服务摸一遍。
//
// 这里的策略是**双重校验**：
//  1. 解析主机名后逐个检查 IP，拒绝回环 / 私网 / 链路本地 / 未指定 / 组播；
//  2. 用 net.Dialer.Control 在真正建连那一刻再校验一次对端 IP。
//
// 只做第 1 步会被 DNS rebinding 绕过（校验时解析到公网 IP，建连时解析到内网），
// 所以第 2 步是必需的：Control 拿到的就是即将连接的真实地址。
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
)

// ErrBlocked 表示目标地址落在禁止访问的范围内。
var ErrBlocked = errors.New("目标地址指向内网或本机，已拒绝")

// ipBlocked 判断一个 IP 是否属于禁止访问的范围。
func ipBlocked(ip net.IP) (bool, string) {
	if ip == nil {
		return true, "地址无法解析"
	}
	switch {
	case ip.IsLoopback():
		return true, "回环地址"
	case ip.IsPrivate():
		return true, "私有网段"
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return true, "链路本地地址"
	case ip.IsInterfaceLocalMulticast():
		return true, "接口本地组播地址"
	case ip.IsMulticast():
		return true, "组播地址"
	case ip.IsUnspecified():
		return true, "未指定地址"
	}
	// IPv4 的 0.0.0.0/8（"本网络"）与 100.64/10（运营商级 NAT）
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 0 {
			return true, "0.0.0.0/8 网段"
		}
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true, "运营商级 NAT 网段"
		}
		// 云元数据服务：169.254.169.254 已被 IsLinkLocalUnicast 覆盖，
		// 这里再兜一层 169.254.0.0/16 整体
		if v4[0] == 169 && v4[1] == 254 {
			return true, "链路本地网段（含云元数据地址）"
		}
	}
	// IPv6 唯一本地地址 fc00::/7
	if len(ip) == net.IPv6len && ip.To4() == nil {
		if ip[0]&0xfe == 0xfc {
			return true, "IPv6 唯一本地地址"
		}
	}
	return false, ""
}

// CheckHost 校验主机名解析出的所有地址都必须是公网地址。
//
// 返回的 addrs 供调用方固定使用（避免校验与建连之间被重新解析）。
func CheckHost(host string) ([]net.IP, error) {
	h := strings.TrimSpace(host)
	if h == "" {
		return nil, fmt.Errorf("%w: 主机名为空", ErrBlocked)
	}
	// 主机名本身就是 IP 时直接判断，避免走一次 DNS
	if ip := net.ParseIP(h); ip != nil {
		if blocked, why := ipBlocked(ip); blocked {
			return nil, fmt.Errorf("%w: %s（%s）", ErrBlocked, h, why)
		}
		return []net.IP{ip}, nil
	}
	// "localhost" 及其它 .local 名字一律拒绝
	lower := strings.ToLower(h)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") ||
		strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") {
		return nil, fmt.Errorf("%w: %s", ErrBlocked, h)
	}

	addrs, err := net.LookupIP(h)
	if err != nil {
		return nil, fmt.Errorf("域名 %s 解析失败: %w", h, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("域名 %s 没有解析到任何地址", h)
	}
	for _, ip := range addrs {
		if blocked, why := ipBlocked(ip); blocked {
			return nil, fmt.Errorf("%w: %s 解析到 %s（%s）", ErrBlocked, h, ip, why)
		}
	}
	return addrs, nil
}

// Control 是给 net.Dialer.Control 用的回调。
//
// 它在 TCP 建连前被调用，address 是**即将连接**的真实地址。
// 有了它，即使 DNS 在校验之后才返回内网地址（DNS rebinding），
// 连接也会在这里被拦下。
func Control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("地址 %q 无法解析: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("地址 %q 不是合法 IP", host)
	}
	if blocked, why := ipBlocked(ip); blocked {
		return fmt.Errorf("%w: 实际连接目标是 %s（%s）", ErrBlocked, ip, why)
	}
	return nil
}

// GuardedTransport 在给定 transport 的基础上补上建连校验与禁跳转。
//
// 传 nil 时以 http.DefaultTransport 为基底。原有的 DialContext 会被保留
// （走代理的场景必须保留），但会在其外层再套一次 Control 校验 ——
// 走代理时被校验的是代理服务器地址，这正是我们要的：
// 代理是运维自己配的可信出口，而真实目标由代理解析，本进程管不到，
// 也不该管（能配代理的人本来就有出网自由）。
func GuardedTransport(base *http.Transport) *http.Transport {
	var t *http.Transport
	if base != nil {
		t = base.Clone()
	} else {
		t = http.DefaultTransport.(*http.Transport).Clone()
	}
	inner := t.DialContext
	d := &net.Dialer{Control: Control}
	if inner == nil {
		t.DialContext = d.DialContext
		return t
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		// 先做一次显式校验，让错误信息可读；真正的兜底在 Control
		host, _, err := net.SplitHostPort(addr)
		if err == nil {
			if _, err := CheckHost(host); err != nil {
				return nil, err
			}
		}
		return inner(ctx, network, addr)
	}
	return t
}

// GuardedClient 返回带 SSRF 防护的 http.Client：禁跳转 + 建连校验。
func GuardedClient(base *http.Client) *http.Client {
	c := &http.Client{}
	if base != nil {
		*c = *base
	}
	c.Transport = GuardedTransport(transportOf(c.Transport))
	// 禁止跟随跳转：跟随会让「校验过的地址」与「最终请求的地址」不是同一个
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return c
}

// transportOf 取出 *http.Transport，非该类型时返回 nil（用默认 transport）。
func transportOf(rt http.RoundTripper) *http.Transport {
	if t, ok := rt.(*http.Transport); ok {
		return t
	}
	return nil
}
