// Package proxy 按「出站代理」的配置构造连接：socks5 / http / https。
//
// 为什么单独一个包：代理有两个消费方 ——
//
//	· 连通性测试（管理界面点「测试」）；
//	· 渠道转发（某个渠道指定走某个代理）。
//
// 两边必须是同一套拨号逻辑，否则会出现「测试通过但转发不通」这种最难查的问题。
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"

	"llm-relay/internal/model"
	"llm-relay/internal/secure"
)

// Config 是构造出站连接所需的全部信息（密码是明文，由调用方解密后传入）。
type Config struct {
	Protocol string
	Host     string
	Port     int
	Username string
	Password string
}

// Address 返回 host:port。
func (c Config) Address() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// Validate 校验配置是否可用。返回的错误是给用户看的中文。
func (c Config) Validate() error {
	switch c.Protocol {
	case model.ProxyProtocolSOCKS5, model.ProxyProtocolHTTP, model.ProxyProtocolHTTPS:
	default:
		return fmt.Errorf("不支持的代理协议 %q（只能是 socks5 / http / https）", c.Protocol)
	}
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("代理地址不能为空")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return errors.New("代理端口必须在 1-65535 之间")
	}
	if strings.ContainsAny(c.Host, " /") {
		// 常见写法错误：把 "http://host" 整串填进地址栏。
		// 直接拒绝并说清楚，比让它变成一个永远连不上的配置好
		return errors.New("代理地址只填主机名或 IP，不要带协议前缀或路径")
	}
	if strings.Contains(c.Host, ":") {
		// 另一个常见写法：把 "1.2.3.4:1080" 整个填进地址栏（端口另有输入框）。
		// 不拦的话 Address() 会拼成 "[1.2.3.4:1080]:1080"，
		// 用户拿到一条看不懂的拨号错误 —— 正是这个函数想避免的那类现象。
		// IPv6 字面量允许带冒号（[::1]），所以只在没有方括号时判定
		if !strings.HasPrefix(c.Host, "[") {
			return errors.New("代理地址不要带端口，端口请填在旁边的端口框里")
		}
	}
	return nil
}

// Redacted 返回可安全写进日志/界面的地址表示。
func (c Config) Redacted() string {
	if c.Username != "" {
		return fmt.Sprintf("%s://%s:***@%s", c.Protocol, c.Username, c.Address())
	}
	return fmt.Sprintf("%s://%s", c.Protocol, c.Address())
}

// DialContext 返回一个「经由该代理」建立 TCP 连接的函数。
//
// http 代理在这里用 CONNECT 隧道（HTTPS 目标必须用隧道，
// 明文 HTTP 目标走绝对 URI 转发，两种都由 net/http 的 Transport 处理）。
func (c Config) DialContext(timeout time.Duration) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	base := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}

	switch c.Protocol {
	case model.ProxyProtocolSOCKS5:
		var auth *xproxy.Auth
		if c.Username != "" {
			auth = &xproxy.Auth{User: c.Username, Password: c.Password}
		}
		d, err := xproxy.SOCKS5("tcp", c.Address(), auth, base)
		if err != nil {
			return nil, fmt.Errorf("构造 SOCKS5 拨号器失败: %w", err)
		}
		// SOCKS5 的握手与 CONNECT 都发生在 Dial 里：
		// 用户名密码错误会在这里返回，而不是在 HTTP 层。
		//
		// 这里必须自己套一层超时：net.Dialer 的 Timeout 只管「连上代理本身」，
		// 握手/认证/CONNECT 阶段只跟随 ctx 取消，而转发侧传进来的是
		// 请求的 ctx（没有 deadline）。一个「接受 TCP 但不回应」的代理
		// （典型：端口填错指到了别的服务）会让请求一直挂到客户端断开。
		cd, ok := d.(xproxy.ContextDialer)
		if !ok {
			return nil, errors.New("当前 SOCKS5 实现不支持带超时的拨号")
		}
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return cd.DialContext(dialCtx, network, addr)
		}, nil
	case model.ProxyProtocolHTTP, model.ProxyProtocolHTTPS:
		// HTTP/HTTPS 代理由 Transport 自己的 Proxy 字段处理，
		// 这里只需要保证「连到代理本身」这一步用得上超时。
		return base.DialContext, nil
	}
	return nil, fmt.Errorf("不支持的代理协议 %q", c.Protocol)
}

// Transport 返回一个走该代理的 http.Transport。
//
// 返回的 Transport 与 net/http 默认值的关键差异（每一条都是踩过的坑）：
//   - DisableCompression=false：中继要读 usage，压缩字节会让统计退化；
//   - ForceAttemptHTTP2=true：Anthropic / Gemini 走 HTTP/2 更稳；
//   - 超时由调用方通过 ctx 控制，这里只设连接与握手超时。
func (c Config) Transport(timeout time.Duration) (*http.Transport, error) {
	tr := &http.Transport{
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}

	if c.Protocol == model.ProxyProtocolSOCKS5 {
		dial, err := c.DialContext(timeout)
		if err != nil {
			return nil, err
		}
		tr.DialContext = dial
		return tr, nil
	}

	u := &url.URL{Scheme: "http", Host: c.Address()}
	if c.Protocol == model.ProxyProtocolHTTPS {
		// 注意：这里是「连代理本身用 TLS」，不是「代理转发 HTTPS」
		u.Scheme = "https"
	}
	if c.Username != "" {
		u.User = url.UserPassword(c.Username, c.Password)
	}
	tr.Proxy = http.ProxyURL(u)
	tr.DialContext = (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext
	return tr, nil
}

// FromEntity 由库里的代理行构造拨号配置，密文在这里解开。
//
// 刻意**不**检查 Enabled：这里只回答「这条记录描述的是哪个代理」。
// 「停用的代理不许用来转发」是转发侧的规则（见 cmd/server 的 resolver），
// 而管理界面要能测一个停用中的代理 —— 用户往往就是先停用它、
// 改完地址再测一次是否修好，测试接口要是也拦，就没法验证修改有没有用。
// 第二个返回值是错误而不是 bool：调用方需要区分「代理不存在/停用」
// 与「密码解不开」—— 两者的排障方向完全不同。解不开密文时仍然把
// 已填的协议/地址/用户名带回去，界面上的测试按钮才能给出
// 「密码解密失败」而不是「不支持的代理协议 ""」这种莫名其妙的结论。
func FromEntity(p model.Proxy, c *secure.Cipher) (Config, error) {
	cfg := Config{Protocol: p.Protocol, Host: p.Host, Port: p.Port, Username: p.Username}
	if p.PasswordEnc == "" {
		return cfg, nil
	}
	if c == nil {
		return cfg, errors.New("没有可用的主密钥，无法解开代理密码")
	}
	plain, err := c.Decrypt(p.PasswordEnc)
	if err != nil {
		return cfg, errors.New("代理密码解密失败（主密钥是否变过？）")
	}
	cfg.Password = plain
	return cfg, nil
}

// TestResult 是一次连通性测试的结果。
type TestResult struct {
	OK         bool
	LatencyMs  int
	StatusCode int
	Error      string
}

// DefaultTestURL 是默认的探测目标。
//
// 选它是因为它返回 204 且**不需要鉴权**：任何 2xx/3xx 都说明隧道通了，
// 不依赖用户在目标站点上有账号。想换成别的目标时由调用方传 testURL。
const DefaultTestURL = "https://www.gstatic.com/generate_204"

// Test 通过代理请求 testURL，返回是否连通与耗时。
//
// 耗时口径：从开始拨号到收到响应头。这包含了「连代理 + 代理连目标 + TLS」，
// 正是用户关心的那个数字（只测 TCP 连上代理毫无意义：代理本身活着但出口不通
// 是最常见的情况）。
func Test(ctx context.Context, cfg Config, testURL string, timeout time.Duration) TestResult {
	if testURL == "" {
		testURL = DefaultTestURL
	}
	start := time.Now()
	tr, err := cfg.Transport(timeout)
	if err != nil {
		return TestResult{Error: err.Error()}
	}
	defer tr.CloseIdleConnections()

	client := &http.Client{
		Transport: tr,
		Timeout:   timeout,
		// 不跟随跳转：探测目标是 204，跳转说明链路被劫持或代理要求登录，
		// 那种情况要如实报出来而不是算成功
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testURL, nil)
	if err != nil {
		return TestResult{Error: "探测地址不合法: " + err.Error()}
	}
	resp, err := client.Do(req)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		return TestResult{LatencyMs: latency, Error: FriendlyError(err, cfg)}
	}
	defer resp.Body.Close()
	// 必须把正文读干净：不读会让这条连接无法复用，
	// 而且有些代理是「先返回响应头再断流」，读的时候才暴露问题
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))

	if resp.StatusCode >= 400 {
		return TestResult{
			LatencyMs:  latency,
			StatusCode: resp.StatusCode,
			Error:      fmt.Sprintf("代理返回 HTTP %d（链路可能是通的，但出口被拒或被要求认证）", resp.StatusCode),
		}
	}
	return TestResult{OK: true, LatencyMs: latency, StatusCode: resp.StatusCode}
}

// FriendlyError 把底层错误翻译成能指导用户下一步动作的中文。
//
// 直接把 err.Error() 抛给用户是最省事的做法，但那种消息形如
// "dial tcp 1.2.3.4:1080: i/o timeout" 或者
// "proxyconnect tcp: dial tcp 1.2.3.4:9: connect: connection refused"，
// 用户看不出该改哪一项。转发路径也用这个：错误会落进请求日志，
// 那正是排障时唯一能看的东西。
func FriendlyError(err error, cfg Config) string {
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "timeout"):
		return fmt.Sprintf("连接超时：%s 在超时时间内没有响应（地址/端口是否正确？代理是否在运行？）", cfg.Address())
	// TLS 判断必须排在认证之前：x509 的证书错误原文是
	// "certificate signed by unknown authority"，里面有 "authority"，
	// 用子串 "auth" 匹配认证会把它整条吃掉 —— 用户看到「用户名或密码不对」
	// 就会去反复改密码，而真正的问题是代理用了自签证书。
	case strings.Contains(msg, "certificate") || strings.Contains(msg, "x509") || strings.Contains(msg, "tls:"):
		return "TLS 握手失败：如果代理本身不是 TLS 的，协议要选 http 而不是 https"
	case strings.Contains(msg, "authentication failed") || strings.Contains(msg, "auth failed") ||
		strings.Contains(msg, "invalid username") || strings.Contains(msg, "invalid password") ||
		strings.Contains(msg, "username/password"):
		return "认证失败：用户名或密码不对"
	case strings.Contains(msg, "connection refused"):
		return fmt.Sprintf("连接被拒绝：%s 上没有服务在监听", cfg.Address())
	case strings.Contains(msg, "no such host"):
		return fmt.Sprintf("域名解析失败：找不到 %s", cfg.Host)
	case errors.Is(err, context.Canceled):
		return "测试被取消"
	}
	if cfg.Protocol == model.ProxyProtocolHTTPS {
		return msg + "（提示：协议选 https 表示用 TLS 连代理本身）"
	}
	return msg
}
