package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"llm-relay/internal/model"
	"llm-relay/internal/netguard"
	"llm-relay/internal/proxy"
)

// 图标抓取的公共一半：渠道图标（从上游站点猜路径抓 favicon）与客户端图标
// （从用户填的图片地址抓）走的是同一条链路 —— 同一套 SSRF 防护、同一个
// 体积上限、同一个超时、同一份 data URI 口径。两边各写一遍必然漂移，
// 所以这里只留一份实现，两个调用方各自只负责「要抓哪些地址」。
//
// 图标大小与超时。上游的 favicon 通常 1-5KB；给到 64KB 是留出
// apple-touch-icon 这类大图的余量，再大就不是图标了，拉回来只会把备份撑肥。
const maxIconBytes = 64 << 10
const iconFetchTimeout = 8 * time.Second

// iconFetcher 是一次抓取会话：一个 http.Client（可能带着渠道的出站代理）
// 加一个总超时上下文。
//
// 客户端与「单个地址的抓取」分开，是因为渠道要对多个候选地址复用同一个
// 客户端（建连复用 + 只解析一次代理配置），而客户端图标只有一个地址。
type iconFetcher struct {
	ctx    context.Context
	client *http.Client
}

// newIconFetcher 构建抓取会话。proxyID 非 0 时走该代理（渠道配了代理就必须
// 走代理：直连会把本机真实 IP 暴露给上游，而用户配代理正是为了避免这件事）。
// 客户端图标没有出站代理这一维（映射表上没这个东西），传 0 —— 但环境变量里的
// 系统代理照旧生效（Go 的默认 transport 本来就读它）。
//
// 返回的 cleanup 由调用方 defer，负责关掉空闲连接。
func (s *Server) newIconFetcher(ctx context.Context, proxyID uint) (*iconFetcher, func(), error) {
	client := &http.Client{Timeout: iconFetchTimeout}
	cleanup := func() {}
	// 代理地址要交给 netguard 豁免：代理常常就跑在本机（127.0.0.1:7890
	// 那类系统级 Clash），不豁免的话每个请求都撞在「目标地址指向内网或本机」
	// 上 —— 表现是「图标怎么也抓不到」，而用户还以为是自己地址填错了。
	// 只豁免代理自己那一个地址：NO_PROXY 会让一部分请求仍然直连，那些照旧校验。
	exempt := netguard.EnvProxyAddrs()
	if proxyID != 0 {
		var p model.Proxy
		if err := s.deps.Store.DB().First(&p, proxyID).Error; err != nil {
			return nil, cleanup, fmt.Errorf("这条渠道配了出站代理 #%d，但它不存在", proxyID)
		}
		if !p.Enabled {
			return nil, cleanup, fmt.Errorf("这条渠道的出站代理 %s 已停用", p.Name)
		}
		cfg, err := proxy.FromEntity(p, s.deps.Cipher)
		if err != nil {
			return nil, cleanup, err
		}
		tr, err := cfg.Transport(iconFetchTimeout)
		if err != nil {
			return nil, cleanup, err
		}
		client.Transport = tr
		cleanup = tr.CloseIdleConnections
		exempt = append(exempt, cfg.Address())
	}
	// 补上禁跳转 + 建连校验（代理地址除外）。
	// 禁跳转是必需的：目标通过 302 跳到内网时，如果跟随就等于绕过了下面的校验。
	// DialContext 的校验兜住 DNS rebinding（校验时是公网 IP、建连时是内网 IP）。
	client = netguard.GuardedClientExcept(client, exempt)
	return &iconFetcher{ctx: ctx, client: client}, cleanup, nil
}

// fetch 抓一个地址并转成 data URI。
//
// 存 data URI 而不是图标地址：外链随时可能被换掉、加上鉴权或直接下线，
// 那种时候界面上的图标会莫名其妙变成裂图；而且备份要能带着图标走。
// 对客户端图标来说还有一层：存地址等于「日志页每打开一次就替浏览器
// 去请求一次那个外链」（境内访问慢、还会把访问者 IP 漏给第三方）。
func (f *iconFetcher) fetch(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("图标地址为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s 不是一个可用的地址", raw)
	}
	// 只支持 http(s)：file:// 之类会让这个接口变成任意文件读取。
	// 这一条排在主机名之前：漏写协议头（example.com/a.png）与 file://
	// 都该得到「只支持 http/https」这句能照着改的提示，
	// 而不是笼统的「地址不可用」。
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("只支持从 http/https 地址抓图标")
	}
	if u.Host == "" {
		return "", fmt.Errorf("%s 不是一个可用的地址", raw)
	}
	// SSRF 防护：地址是调用者指定的，只校验 scheme 挡不住
	// http://127.0.0.1:6379/ 这类探测 —— 本机端口扫描、内网服务发现、
	// 读云元数据（169.254.169.254）都能做到，而且返回状态码与耗时会被回显，
	// 等于给了一个内网探测器。详见 netguard 的包注释。
	if _, err := netguard.CheckHost(u.Hostname()); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(f.ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	return readIconResponse(resp, u.String())
}

// readIconResponse 校验抓回来的响应并转成 data URI。
//
// 拆成独立函数是为了能测：状态码 / 内容类型 / 空内容 / 体积这四道校验
// 全在这里，测试直接构造 http.Response 就能覆盖，不必真的发网络请求
// （回环地址会被 netguard 拒掉，本机起的 httptest 服务器用不上）。
func readIconResponse(resp *http.Response, src string) (string, error) {
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// 3xx 单独说清楚：这里**不跟随跳转**（跟随会让「校验过的地址」与
		// 「最终请求的地址」不是同一个，等于绕过 SSRF 防护），而用户填的
		// 图片地址经常是短链或带跳转的老地址 —— 只回一句「HTTP 302」
		// 他会以为是抓取坏了，正确动作是让他粘贴跳转后的直达地址。
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return "", fmt.Errorf("%s 跳转到了别处（HTTP %d）：这里不跟随跳转，请改成跳转后的直达图片地址",
				src, resp.StatusCode)
		}
		return "", fmt.Errorf("%s 返回 HTTP %d", src, resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	// 有些站点会把 404 页面配上 image/png 的 Content-Type 发出来，
	// 所以还要看内容类型对不对得上
	if !strings.HasPrefix(ct, "image/") {
		return "", fmt.Errorf("%s 返回的不是图片（%s）", src, ct)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("%s 返回了空内容", src)
	}
	if len(data) > maxIconBytes {
		return "", fmt.Errorf("%s 的图标超过 %dKB", src, maxIconBytes>>10)
	}
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// isExternalIconURL 判断图标值是不是「要抓回来缓存的图片外链」。
//
// 前端 clientDisplay 的 img 形态认 data: 与 http(s) 两种；这里只关心后者 ——
// data URI 的内容已经在库里了，没什么可抓的。大小写不敏感（浏览器与
// 服务端都接受 HTTP:// 这种写法）。
func isExternalIconURL(icon string) bool {
	v := strings.ToLower(strings.TrimSpace(icon))
	return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")
}
