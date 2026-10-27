package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
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

// maxHTMLBytes 是读 HTML/Manifest 做图标发现时的上限。
//
// 与图标本身的上限分开：404 页面动辄几十 KB，而要发现图标只需要 <head>，
// 真实站点里超过 512KB 的 HTML 已经是异类。限住它才能让「发现」这一步
// 不会因为某个巨型页面把整个抓取预算烧光。
const maxHTMLBytes = 512 << 10

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
// 返回的 cleanup 由调用方 defer，负责关掉空闲连接（在**最终**的 client 上，
// 见函数末尾的说明）。
func (s *Server) newIconFetcher(ctx context.Context, proxyID uint) (*iconFetcher, func(), error) {
	client := &http.Client{Timeout: iconFetchTimeout}
	noop := func() {}
	// 代理地址要交给 netguard 豁免：代理常常就跑在本机（127.0.0.1:7890
	// 那类系统级 Clash），不豁免的话每个请求都撞在「目标地址指向内网或本机」
	// 上 —— 表现是「图标怎么也抓不到」，而用户还以为是自己地址填错了。
	// 只豁免代理自己那一个地址：NO_PROXY 会让一部分请求仍然直连，那些照旧校验。
	exempt := netguard.EnvProxyAddrs()
	if proxyID != 0 {
		var p model.Proxy
		if err := s.deps.Store.DB().First(&p, proxyID).Error; err != nil {
			return nil, noop, fmt.Errorf("这条渠道配了出站代理 #%d，但它不存在", proxyID)
		}
		if !p.Enabled {
			return nil, noop, fmt.Errorf("这条渠道的出站代理 %s 已停用", p.Name)
		}
		cfg, err := proxy.FromEntity(p, s.deps.Cipher)
		if err != nil {
			return nil, noop, err
		}
		tr, err := cfg.Transport(iconFetchTimeout)
		if err != nil {
			return nil, noop, err
		}
		client.Transport = tr
		exempt = append(exempt, cfg.Address())
	}
	// 补上禁跳转 + 建连校验（代理地址除外）。
	// 禁跳转是必需的：目标通过 302 跳到内网时，如果跟随就等于绕过了下面的校验。
	// DialContext 的校验兜住 DNS rebinding（校验时是公网 IP、建连时是内网 IP）。
	client = netguard.GuardedClientExcept(client, exempt)
	// cleanup 必须绑定**最终**的 client：GuardedClientExcept 内部会 Clone
	// 传入的 transport（idle 池是新建的），上面那个 tr 根本不会被任何人使用。
	// 绑在 tr 上的话 CloseIdleConnections 关的是一个空池，实际连接只能等
	// IdleConnTimeout（90 秒）自己回收 —— 而这条路径每次「从上游获取图标」
	// 都会新建一个 transport。
	return &iconFetcher{ctx: ctx, client: client}, client.CloseIdleConnections, nil
}

// fetch 抓一个地址并转成 data URI。
//
// 存 data URI 而不是图标地址：外链随时可能被换掉、加上鉴权或直接下线，
// 那种时候界面上的图标会莫名其妙变成裂图；而且备份要能带着图标走。
// 对客户端图标来说还有一层：存地址等于「日志页每打开一次就替浏览器
// 去请求一次那个外链」（境内访问慢、还会把访问者 IP 漏给第三方）。
//
// 3xx 会跟随一次（http→https 的重定向对图标托管极其常见：站点填的是
// http 地址，而 CDN 强制 https）。跟随的方式是「取 Location 重新走一遍
// 完整校验」，不是让 http.Client 自己跳 —— 后者会让「校验过的地址」与
// 「真正请求的地址」脱钩，等于绕开 SSRF 防护。
func (f *iconFetcher) fetch(raw string) (string, error) {
	const maxRedirects = 2

	cur := raw
	for hop := 0; hop <= maxRedirects; hop++ {
		u, err := f.validateURL(cur)
		if err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(f.ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return "", err
		}
		// 图标 URL 常被 CDN 按 UA/Referer 拦，带上与 HTML 抓取一致的 UA
		req.Header.Set("User-Agent", iconUserAgent)
		resp, err := f.client.Do(req)
		if err != nil {
			return "", err
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := strings.TrimSpace(resp.Header.Get("Location"))
			_ = resp.Body.Close()
			if loc == "" {
				return "", fmt.Errorf("%s 返回 HTTP %d 但没有给出跳转地址", u, resp.StatusCode)
			}
			next, err := u.Parse(loc)
			if err != nil {
				return "", fmt.Errorf("%s 的跳转地址无法解析", u)
			}
			cur = next.String()
			continue
		}
		return readIconResponse(resp, u.String())
	}
	return "", fmt.Errorf("%s 跳转次数过多（超过 %d 次）", raw, maxRedirects)
}

// validateURL 做 scheme / 主机名 / SSRF 三件校验，返回解析好的 URL。
//
// 抽出来是因为「发现图标」时要先抓 HTML 再抓图片，两次请求必须用同一套
// 校验 —— 复制一份的话，漏掉 SSRF 校验的那一处就能被用来探测内网。
func (f *iconFetcher) validateURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("图标地址为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s 不是一个可用的地址", raw)
	}
	// 只支持 http(s)：file:// 之类会让这个接口变成任意文件读取。
	// 这一条排在主机名之前：漏写协议头（example.com/a.png）与 file://
	// 都该得到「只支持 http/https」这句能照着改的提示，
	// 而不是笼统的「地址不可用」。
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("只支持从 http/https 地址抓图标")
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%s 不是一个可用的地址", raw)
	}
	// SSRF 防护：地址是调用者指定的，只校验 scheme 挡不住
	// http://127.0.0.1:6379/ 这类探测 —— 本机端口扫描、内网服务发现、
	// 读云元数据（169.254.169.254）都能做到，而且返回状态码与耗时会被回显，
	// 等于给了一个内网探测器。详见 netguard 的包注释。
	if _, err := netguard.CheckHost(u.Hostname()); err != nil {
		return nil, err
	}
	return u, nil
}

// fetchHTML 抓一个页面的正文（限制在 maxHTMLBytes 内）。
//
// 不校验 Content-Type 必须是 text/html：不少站点把首页发成
// application/xhtml+xml 或干脆不带 Content-Type，而图标声明就藏在里面。
// 这里的产物只用来提取 <link> 标签，不做渲染，所以类型宽松一些是安全的。
//
// 返回的 redirect 非空表示站点给了 3xx：这里**不跟随**（跟随会让「校验过的
// 地址」与「最终请求的地址」不是同一个，等于绕过 SSRF 防护），而是把
// Location 交回调用方，由它当成一个新候选重新走一遍校验 ——
// 这样既支持了 http→https、www→裸域这类**极常见**的跳转，
// 又不给「302 到 169.254.169.254」留口子。
func (f *iconFetcher) fetchHTML(raw string) (body []byte, pageURL *url.URL, redirect string, err error) {
	u, err := f.validateURL(raw)
	if err != nil {
		return nil, nil, "", err
	}
	req, err := http.NewRequestWithContext(f.ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, "", err
	}
	req.Header.Set("User-Agent", iconUserAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := strings.TrimSpace(resp.Header.Get("Location"))
		if loc == "" {
			return nil, nil, "", fmt.Errorf("%s 返回 HTTP %d 但没有给出跳转地址", u, resp.StatusCode)
		}
		// 必须用 u.Parse(loc) 而不是 &url.URL{Path: loc}：
		// Location 可以是绝对地址（CDN 跳转、http→https 都是），塞进 Path 会
		// 拼成 https://host/https://cdn/... ；带 query 的相对地址还会被转义成
		// %3F。u.Parse 同时正确处理绝对 / 根相对 / 协议相对 / 相对四种写法。
		next, err := u.Parse(loc)
		if err != nil {
			return nil, nil, "", fmt.Errorf("%s 的跳转地址无法解析", u)
		}
		return nil, nil, next.String(), nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, "", fmt.Errorf("%s 返回 HTTP %d", u, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxHTMLBytes))
	if err != nil {
		return nil, nil, "", err
	}
	return data, u, "", nil
}

// iconUserAgent 是抓 HTML 时用的 UA。
//
// 不带 UA 会被相当一部分 CDN/反爬挡住（直接 403），而我们要的只是 <head>
// 里的几行 <link>。用常见的桌面浏览器 UA 而不是 Go 默认值
// （Go-http-client/1.1 基本等于自报「我是脚本」）。
const iconUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

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
	// 所以还要看内容类型对不对得上。
	//
	// 没有 Content-Type 时按字节嗅探：一部分静态托管（对象存储、nginx 配错
	// mime.types）压根不发这个头，而内容是能用的 PNG/ICO。直接拒绝会把
	// 这类站点的图标全判死，理由还只有一句「不是图片」。
	mediaType := ""
	if mt, _, err := mime.ParseMediaType(ct); err == nil && strings.HasPrefix(mt, "image/") {
		mediaType = mt
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
	if mediaType == "" {
		// Content-Type 不对或缺失，就自己认头像魔数
		sniffed := sniffImageType(data)
		if sniffed == "" {
			return "", fmt.Errorf("%s 返回的不是图片（%s）", src, ct)
		}
		mediaType = sniffed
	}
	// 用 ParseMediaType 的结果而不是原始头：Content-Type 是上游可控的，
	// 原样拼进 data URI 会把 `image/png; x=<任意长参数>` 整段带进库里
	// （Go 允许约 10MB 的响应头），入库值因此可能远超文档承诺的约 87KB。
	// 只保留 `image/<子类型>`，参数一律丢弃。
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// sniffImageType 按文件头判断图片类型，认不出返回空串。
//
// 覆盖浏览器图标生态里实际会遇到的全部格式：ICO / CUR（Windows 时代，
// 绝大多数站点还留着）、PNG、JPEG、GIF、WebP，以及 SVG（文本型，单独判）。
// 不引入图片解码库 —— 这里只需要「这是什么类型」，做个前缀比对就够，
// 而解码器会为了一个 16×16 的图标把整个进程的二进制撑大。
func sniffImageType(data []byte) string {
	switch {
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	case len(data) >= 4 && data[0] == 0x00 && data[1] == 0x00 && (data[2] == 0x01 || data[2] == 0x02) && data[3] == 0x00:
		// ICO(01) 与 CUR(02) 的文件头完全同构，浏览器对两者的渲染一样，
		// 所以统一报 image/x-icon —— 不区分这两种，省掉一个没有意义的 if。
		return "image/x-icon"
	case len(data) >= 4 && (string(data[:4]) == "II*\x00" || string(data[:4]) == "MM\x00*"):
		return "image/tiff"
	}
	// SVG 是文本，允许前面有 BOM / XML 声明 / 空白 / 注释。
	// 外层判「是不是 XML/空白开头」是廉价的快速否决（绝大多数非图片内容
	// 连这一步都过不了），内层才去找 <svg —— 两件事分开写比一个大条件好读。
	if head := strings.TrimSpace(string(data[:min(len(data), 1024)])); head != "" {
		lower := strings.ToLower(head)
		if strings.HasPrefix(lower, "<svg") || strings.HasPrefix(lower, "<?xml") || strings.HasPrefix(lower, "\ufeff") {
			// 只认「XML 声明之后确实有 <svg」：只有 <?xml?> 而没有 <svg>
			// 的是别的东西（RSS、SVG 之外的 XML），不能当图标
			if strings.Contains(lower, "<svg") {
				return "image/svg+xml"
			}
		}
	}
	return ""
}

// iconLink 是图标发现的结果：一个地址，以及它声明的尺寸与用途。
type iconLink struct {
	URL   string
	Sizes string
	Rel   string
}

// 常见的图标声明属性。
//
// 市面上站点的写法差异极大，这里覆盖实际会遇到的形态：
//   - rel="icon" / rel="shortcut icon" / rel="apple-touch-icon" /
//     rel="apple-touch-icon-precomposed" / rel="mask-icon" / rel="fluid-icon"
//   - rel 里用空格分隔多个值（"shortcut icon"）或大小写混写，浏览器一律按
//     「包含 icon 这个词」判断
//   - href 可能是相对路径、绝对路径、协议相对（//cdn/a.png）甚至 data URI
//
// iconLinkRels 是全部认得的图标 rel。
//
// 这里其实只需要 "icon" 一个：浏览器图标生态里的 rel 全都含这个词 ——
// icon / shortcut icon / apple-touch-icon / apple-touch-icon-precomposed /
// mask-icon / fluid-icon。曾把六种写法逐一列出，但匹配用的也是「包含」，
// 而 "icon" 排在首位、必然先命中，后面五项永远走不到 —— 留着会让读代码的人
// 以为它们各有分工，改错时也不确定该动哪一行。
//
// 反过来要小心「包含」的副作用：万一将来出现含 icon 却**不是**图标的 rel，
// 这里会误收。届时改成按空白切分 rel 后逐词精确比对。
var iconLinkRels = []string{"icon"}

// 不用 goquery / x/net/html 解析整棵树：图标声明只可能在 <head> 的 <link> 上，
// 而为了几个属性把完整的 HTML5 解析器拉进二进制不划算。用正则逐个读标签，
// 遇到不自闭合、属性顺序不固定、单引号双引号混用都能吃下。

// linkTagRe 匹配 <link …> 与 <base …>，兼容自闭合与不成对的写法。
//
// 允许标签名后没有空白（`<link/>`）、属性值用单引号、属性里带换行。
// 大小写不敏感：XHTML 与手写页面里 HTML 标签大写并不少见。
var linkTagRe = regexp.MustCompile(`(?is)<(link|base)\b([^>]*)>`)

// attrRe 从标签内的属性串里取某个属性的值。
var attrRe = regexp.MustCompile(`(?is)\b([a-z][a-z0-9:_-]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)

// parseLinkAttrs 把一个标签的属性串解析成 map（键统一小写）。
func parseLinkAttrs(s string) map[string]string {
	out := map[string]string{}
	for _, m := range attrRe.FindAllStringSubmatch(s, -1) {
		key := strings.ToLower(m[1])
		val := m[2]
		if val == "" {
			val = m[3]
		}
		if val == "" {
			val = m[4]
		}
		// 实体只需要处理最常见的几种，图标地址里几乎只会出现 &amp;
		val = strings.NewReplacer("&amp;", "&", "&#38;", "&", "&quot;", `"`, "&#34;", `"`).Replace(val)
		out[key] = val
	}
	return out
}

// discoverIconLinks 从 HTML 里提取图标声明，按「优先尝试」的顺序返回。
//
// 排序理由（影响抓取成败，不是随便排的）：
//  1. apple-touch-icon：通常是 180×180 的 PNG，最清晰，且几乎总是存在的
//     高质量图标；
//  2. 声明了 sizes 的 icon：站点明确给了多尺寸时，取最大的那个；
//  3. 普通 icon / shortcut icon：最常见，但可能是 16×16；
//  4. mask-icon / fluid-icon：单色 SVG，能用但不是首选。
//
// <base href> 会被解析出来交给调用方，因为相对路径要以它为基准 ——
// 忽略它会让一大批把图标放 CDN 子目录的站点抓不到。
func discoverIconLinks(page []byte) (links []iconLink, baseHref string) {
	body := string(page)
	for _, m := range linkTagRe.FindAllStringSubmatch(body, -1) {
		tag := strings.ToLower(m[1])
		attrs := parseLinkAttrs(m[2])
		if tag == "base" {
			if href := strings.TrimSpace(attrs["href"]); href != "" && baseHref == "" {
				baseHref = href
			}
			continue
		}
		rel := strings.ToLower(strings.TrimSpace(attrs["rel"]))
		href := strings.TrimSpace(attrs["href"])
		if href == "" || rel == "" {
			continue
		}
		matched := false
		for _, want := range iconLinkRels {
			// rel 可能带多个值（"shortcut icon"），按「包含」判断
			if strings.Contains(rel, want) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		links = append(links, iconLink{URL: href, Sizes: attrs["sizes"], Rel: rel})
	}
	// 稳定排序：apple-touch 优先，其次按声明的尺寸从大到小
	sortIconLinks(links)
	return links, baseHref
}

// sortIconLinks 按「值得先试」的顺序排序（原地）。
func sortIconLinks(links []iconLink) {
	rank := func(l iconLink) int {
		switch {
		case strings.Contains(l.Rel, "apple-touch-icon"):
			return 0
		case strings.Contains(l.Rel, "icon"):
			return 1
		default:
			return 2
		}
	}
	// 用插入排序：候选个数是个位数，而它天然稳定（同 rank 保持文档顺序，
	// 也就是站点自己声明的优先级）
	for i := 1; i < len(links); i++ {
		cur := links[i]
		j := i - 1
		for j >= 0 && (rank(links[j]) > rank(cur) ||
			(rank(links[j]) == rank(cur) && sizeOf(links[j].Sizes) < sizeOf(cur.Sizes))) {
			links[j+1] = links[j]
			j--
		}
		links[j+1] = cur
	}
}

// sizeOf 把 "192x192" 解析成 192；认不出返回 0（排在同等 rank 的最后）。
func sizeOf(sizes string) int {
	s := strings.ToLower(strings.TrimSpace(sizes))
	if s == "" || s == "any" {
		return 0
	}
	// "16x16 32x32" 这类多值取第一个
	if i := strings.IndexAny(s, " \t,"); i > 0 {
		s = s[:i]
	}
	parts := strings.SplitN(s, "x", 2)
	n := 0
	for _, r := range parts[0] {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// resolveIconURL 把 HTML 里的 href 解析成绝对地址。
//
// 处理四种写法：绝对地址、协议相对（//cdn/a.png）、根相对（/a.png）、
// 相对路径（a.png / ../a.png）。<base href> 优先于页面地址作为基准。
// data: URI 原样返回（调用方会直接用，不需要再抓）。
func resolveIconURL(href string, pageURL *url.URL, baseHref string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	// data: URI 里的内容已经在页面里了，不必也不该再去请求
	if strings.HasPrefix(strings.ToLower(href), "data:") {
		return href
	}
	ref, err := url.Parse(href)
	if err != nil {
		return ""
	}
	base := pageURL
	// <base href> 可能写绝对地址，也可能写相对目录（/assets/、cdn/）。
	// 相对写法要以页面地址为基准先解析一次，否则基准会是一个没有 Host 的
	// 相对 URL，下面 ResolveReference 出来的地址缺主机名、请求直接失败。
	if b, err := url.Parse(strings.TrimSpace(baseHref)); err == nil && baseHref != "" && base != nil {
		base = base.ResolveReference(b)
	}
	if base == nil {
		return ""
	}
	return base.ResolveReference(ref).String()
}

// manifestIcons 从 Web App Manifest 里提取图标地址。
//
// 现代站点（尤其是 PWA）经常只声明 <link rel="manifest">，图标全在
// manifest.json 的 icons 数组里。不读它的结果就是「站点明明有高清图标，
// 抓取却只能拿到 404」。
type manifestIcons struct {
	Icons []struct {
		Src     string `json:"src"`
		Sizes   string `json:"sizes"`
		Type    string `json:"type"`
		Purpose string `json:"purpose"`
	} `json:"icons"`
}

// parseManifestIcons 解析 manifest JSON 并返回按尺寸降序的图标地址。
//
// 用 encoding/json 而不是正则：manifest 是严格 JSON，正则在这里只会错。
// 解析失败返回空切片（非法 manifest 是站点的锅，不该让整个抓取失败）。
func parseManifestIcons(data []byte) []iconLink {
	var m manifestIcons
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	out := make([]iconLink, 0, len(m.Icons))
	for _, ic := range m.Icons {
		src := strings.TrimSpace(ic.Src)
		if src == "" {
			continue
		}
		out = append(out, iconLink{URL: src, Sizes: ic.Sizes, Rel: "manifest"})
	}
	// 大的排前面：manifest 里的顺序不保证，而 512×512 才是想要的
	for i := 1; i < len(out); i++ {
		cur := out[i]
		j := i - 1
		for j >= 0 && sizeOf(out[j].Sizes) < sizeOf(cur.Sizes) {
			out[j+1] = out[j]
			j--
		}
		out[j+1] = cur
	}
	return out
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
