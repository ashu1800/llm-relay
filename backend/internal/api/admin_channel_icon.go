package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/model"
	"llm-relay/internal/netguard"
)

// 图标抓取的公共部分（客户端、超时、SSRF 防护、data URI 口径、图标发现）
// 在 icon_fetch.go。
//
// 依次尝试的图标路径。顺序有讲究：先试站点自己的 /favicon.ico
// （绝大多数站点都有），再试 BaseURL 下的 —— 有些中转站把图标放在
// 自己的子路径里（例如 /v1/favicon.ico）。
var iconPaths = []string{"/favicon.ico", "/apple-touch-icon.png", "/apple-touch-icon-precomposed.png"}

// maxIconCandidates 是「最多尝试几个候选地址」的上界。
//
// 发现阶段来自站点自己的 HTML/manifest，数量不受我们控制（一个把几百个
// <link rel=icon> 写进页面的站点并不违法），而每个候选最长 8 秒、总预算
// 只有 iconFetchTimeout*3 —— 不设界的话预算会被前面几个慢候选吃光，
// 后面按惯例猜的 /favicon.ico（命中率最高的那个）反而永远轮不到。
//
// 12 的取法：典型站点首页 1~3 个声明 + manifest 1~5 个 + 6 个惯例路径
// 就够覆盖，再多基本是同一张图的不同尺寸。
const maxIconCandidates = 12

type channelIconPayload struct {
	// Icon 为空表示「去上游抓」，非空表示直接设置成这个值（自定义图标）
	Icon string `json:"icon"`
	// BaseURL 允许用「表单里当前填的地址」去抓，而不是库里那一份：
	// 用户刚改完上游地址就点抓取，期望的是从新地址抓
	BaseURL string `json:"base_url"`
	// ProxyID 预览接口用：抓取时走哪个出站代理。0 = 直连
	ProxyID uint `json:"proxy_id"`
}

// channelIconPreview 从表单里的上游地址抓图标，**不要求渠道已保存**。
//
// 这是「填了上游地址就能获取图标」的入口：新建渠道时还没有 ID，
// 而 /channels/:id/icon 必须先有记录。它只抓取并返回 data URI，
// 不写库 —— 保存由用户的「保存渠道」动作完成，抓取属于预览。
//
// 与设置接口一样永远返回 200 + 说明，让界面能显示失败原因
// （能不能拿到完全取决于上游有没有放图标）。
func (s *Server) channelIconPreview(c *gin.Context) {
	var p channelIconPayload
	_ = c.ShouldBindJSON(&p)

	baseURL := strings.TrimSpace(p.BaseURL)
	if baseURL == "" {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "先填上游地址，再获取图标"})
		return
	}
	// 自定义图标不需要抓取，直接回显（前端据此省掉一次往返）
	if icon := strings.TrimSpace(p.Icon); icon != "" {
		if len(icon) > maxIconBytes*2 {
			writeUpstreamError(c, http.StatusBadRequest, "图标太大了（上限 128KB）", "invalid_request_error")
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "icon": icon})
		return
	}

	ch := model.Channel{BaseURL: baseURL, ProxyID: p.ProxyID}
	icon, err := s.fetchUpstreamIcon(c.Request.Context(), ch)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "icon": icon})
}

// channelIcon 读取/抓取渠道图标。
//
// 抓取是「实验性」的：能不能拿到完全取决于上游站点有没有放 favicon，
// 所以它永远返回 200 + 一个说明，让界面把失败原因显示出来，
// 而不是用 4xx 让前端只能弹一句「请求失败」。
func (s *Server) channelIcon(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var p channelIconPayload
	_ = c.ShouldBindJSON(&p)

	db := s.deps.Store.DB()
	var ch model.Channel
	if err := db.First(&ch, id).Error; err != nil {
		writeUpstreamError(c, http.StatusNotFound, "渠道不存在", "not_found_error")
		return
	}

	if u := strings.TrimSpace(p.BaseURL); u != "" {
		ch.BaseURL = u
	}

	icon := strings.TrimSpace(p.Icon)
	if icon == "" {
		fetched, err := s.fetchUpstreamIcon(c.Request.Context(), ch)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": false, "icon": ch.Icon, "error": err.Error()})
			return
		}
		icon = fetched
	}
	// 手工填的图标同样要有上限：抓取路径限了 64KB，这里不限的话
	// 可以往 channels.icon（text）里塞任意大的内容，渠道列表与备份跟着膨胀
	if len(icon) > maxIconBytes*2 {
		writeUpstreamError(c, http.StatusBadRequest, "图标太大了（上限 128KB，抓取来的图标通常只有几 KB）", "invalid_request_error")
		return
	}
	if err := db.Model(&model.Channel{}).Where("id = ?", id).Update("icon", icon).Error; err != nil {
		writeInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "icon": icon})
}

// fetchUpstreamIcon 从渠道的上游地址抓一个图标回来，转成 data URI。
//
// 存 data URI 而不是图标地址的理由见 iconFetcher.fetch 的注释。
//
// 抓取顺序刻意是「先发现、后猜路径」：
//  1. 读首页 HTML，按 <link rel="icon"> 等声明逐个试 —— 这是最准的，
//     站点把图标放 CDN、改名、甚至用 SVG 都只有这份声明知道；
//  2. 读 Web App Manifest（现代站点常把图标全放这里）；
//  3. 最后才按惯例猜 /favicon.ico、/apple-touch-icon.png。
//
// 顺序反过来会导致「站点明明声明了 512×512 的图标，却只拿到 16×16 的
// favicon.ico」—— 而且看起来是成功的，用户不会知道有更好的。
func (s *Server) fetchUpstreamIcon(ctx context.Context, ch model.Channel) (string, error) {
	base, err := url.Parse(strings.TrimSpace(ch.BaseURL))
	if err != nil || base.Host == "" {
		return "", errors.New("渠道的上游地址无法解析，取不到图标")
	}
	// 只支持 http(s)：file:// 之类会让这个接口变成任意文件读取
	if base.Scheme != "http" && base.Scheme != "https" {
		return "", errors.New("只支持从 http/https 的上游地址抓图标")
	}
	// SSRF 防护：这个接口会用请求体里的 base_url 发真实请求，
	// 而 base_url 是可以被调用者指定的，见 icon_fetch.go 的说明
	if _, err := netguard.CheckHost(base.Hostname()); err != nil {
		return "", err
	}

	// 总超时：先发现（2 个请求）再尝试若干候选，最坏情况十几次请求。
	// 用户点的是「从上游获取」，等 20 秒以上就是坏了。
	ctx, cancel := context.WithTimeout(ctx, iconFetchTimeout*3)
	defer cancel()

	// 与转发链路一致：渠道配了代理就必须走代理（理由见 newIconFetcher）
	fetcher, cleanup, err := s.newIconFetcher(ctx, ch.ProxyID)
	if err != nil {
		return "", err
	}
	defer cleanup()

	// 用 url.URL 拼而不是字符串拼接：BaseURL 带 query 时（如 /v1?k=1），
	// 字符串拼接会把图标路径塞进 query 里，请求就发到别的地方去了
	clean := *base
	clean.RawQuery = ""
	clean.Fragment = ""
	root := &url.URL{Scheme: clean.Scheme, Host: clean.Host}
	underBase := clean
	underBase.Path = strings.TrimRight(clean.Path, "/")

	// 候选地址按优先级收集，最后统一去重后逐个尝试。
	//
	// 用一个 slice 而不是「先发现、成功就返回」：发现阶段可能命中若干
	// 声明了但已经 404 的图标（站点改版没清理 HTML），这时候要继续往下
	// 试惯例路径，否则会白白失败。
	candidates := make([]string, 0, 16)

	// ---- 1. 发现：首页 HTML 里的 <link> 声明 ----
	//
	// 首页地址优先用 BaseURL 本身（用户填的那个就是入口），再补一个站点根。
	// 两者都读一遍，因为有一部分中转站把 HTML 放在 /v1 之类的子路径下。
	pageURLs := []*url.URL{&clean}
	if underBase.String() != root.String() {
		pageURLs = append(pageURLs, root)
	}
	var manifestHref string
	var manifestPage *url.URL
	// 页面队列：跳转目标会追加进来（最多跟两跳，见下）。
	// 用队列而不是「跟随跳转」：每个新地址都重新走一遍 SSRF 校验。
	queue := append([]*url.URL{}, pageURLs...)
	seenPage := map[string]bool{}
	for i := 0; i < len(queue) && i < 4; i++ {
		pageURL := queue[i]
		if pageURL == nil || seenPage[pageURL.String()] {
			continue
		}
		seenPage[pageURL.String()] = true

		page, _, redirect, htmlErr := fetcher.fetchHTML(pageURL.String())
		if htmlErr != nil {
			continue
		}
		// 站点给了 3xx（http→https、www→裸域 都极常见）：把跳转目标当作
		// 新页面再加进队列。它会在下一轮重新过 validateURL，
		// 所以「302 到内网」拿不到任何东西。
		if redirect != "" {
			if next, err := url.Parse(redirect); err == nil {
				queue = append(queue, next)
			}
			continue
		}
		links, baseHref := discoverIconLinks(page)
		for _, l := range links {
			if u := resolveIconURL(l.URL, pageURL, baseHref); u != "" {
				candidates = append(candidates, u)
			}
		}
		// 顺手找 manifest 声明（图标可能全在那里）
		if manifestHref == "" {
			if m := findManifestHref(string(page)); m != "" {
				manifestHref = resolveIconURL(m, pageURL, baseHref)
				manifestPage = pageURL
			}
		}
	}

	// ---- 2. 发现：Web App Manifest 里的 icons ----
	if manifestHref != "" && manifestPage != nil {
		if data, _, _, err := fetcher.fetchHTML(manifestHref); err == nil {
			for _, l := range parseManifestIcons(data) {
				// manifest 里的 src 可能是相对路径，以**声明它的那个页面**为基准
				if u := resolveIconURL(l.URL, manifestPage, ""); u != "" {
					candidates = append(candidates, u)
				}
			}
		}
	}

	// ---- 3. 兜底：按惯例猜路径 ----
	for _, p := range iconPaths {
		candidates = append(candidates, root.String()+p)
	}
	for _, p := range iconPaths {
		candidates = append(candidates, underBase.String()+p)
	}

	// 去重后逐个尝试：同一个地址在声明与惯例里重复出现是常态，
	// 不去重会让最坏情况多花一倍时间。
	//
	// 候选数量必须有上界。发现阶段来自站点自己的 HTML/manifest，数量不受
	// 我们控制（一个把几百个 <link rel=icon> 写进页面的站点并不违法），
	// 而每个候选最长 8 秒、总预算只有 iconFetchTimeout*3 —— 不设界的话
	// 预算会被前面几个慢候选吃光，后面按惯例猜的 /favicon.ico（命中率最高
	// 的那个）反而永远轮不到。
	//
	seen := map[string]bool{}
	var lastErr string
	// 预算耗尽时保留**第一条**错误：后面的候选只会重复「context deadline
	// exceeded」，那条信息对用户没有价值，而第一条（404 / 不是图片）
	// 才是站点真实的状况
	budgetHit := false
	tried := 0
	for _, u := range candidates {
		if seen[u] {
			continue
		}
		seen[u] = true
		// data: URI 直接可用，不必再请求
		if strings.HasPrefix(strings.ToLower(u), "data:") {
			if icon, err := readIconDataURI(u); err == nil {
				return icon, nil
			}
			continue
		}
		if tried >= maxIconCandidates {
			budgetHit = true
			break
		}
		tried++
		icon, err := fetcher.fetch(u)
		if err != nil {
			if lastErr == "" && ctx.Err() == nil {
				lastErr = err.Error()
			}
			if ctx.Err() != nil {
				budgetHit = true
				break
			}
			continue
		}
		return icon, nil
	}
	// 三种失败要分开说，否则用户不知道该改什么：
	//   1. 预算耗尽 → 站点太慢或候选太多，重试无用，换个地址；
	//   2. 试满上限 → 候选地址太多，实际是站点声明写得夸张；
	//   3. 都没试成 → 站点确实没有可用图标（此时 lastErr 是第一条真实原因）。
	if ctx.Err() != nil {
		return "", fmt.Errorf("拉取图标超时（上限 %s）：站点响应太慢，或者候选地址太多",
			iconFetchTimeout*3)
	}
	if budgetHit && lastErr != "" {
		return "", errors.New("拉取图标失败：" + lastErr)
	}
	if lastErr == "" {
		lastErr = "没有找到可用的图标"
	}
	return "", errors.New("拉取图标失败：" + lastErr)
}

// findManifestHref 从 HTML 里找 <link rel="manifest" href="…"> 的地址。
func findManifestHref(page string) string {
	for _, m := range linkTagRe.FindAllStringSubmatch(page, -1) {
		if strings.ToLower(m[1]) != "link" {
			continue
		}
		attrs := parseLinkAttrs(m[2])
		if strings.Contains(strings.ToLower(attrs["rel"]), "manifest") {
			return strings.TrimSpace(attrs["href"])
		}
	}
	return ""
}

// maxDataURIBytes 是接受内联 data URI 图标的上限。
//
// 与 maxIconBytes 分开：data URI 是 base64，同样内容体积约为原文的 4/3，
// 这里按解码后的原始字节判断，与 file 路径的上限口径一致。
const maxDataURIBytes = maxIconBytes

// readIconDataURI 校验页面里内联的 data: 图标并原样返回。
//
// 为什么要支持 data URI：一小部分站点（尤其用构建工具打包的文档站）
// 把图标直接内联进 HTML，requests 里根本不存在对应的图标 URL。
// 不做这一步的话这类站点只能落到「猜 /favicon.ico → 404」。
func readIconDataURI(raw string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return "", errors.New("不是 data URI")
	}
	comma := strings.Index(raw, ",")
	if comma < 0 {
		return "", errors.New("data URI 格式不正确")
	}
	meta := raw[len("data:"):comma]
	payload := raw[comma+1:]
	if strings.TrimSpace(payload) == "" {
		// 有些站点（example.com 就是）声明 <link rel=icon href="data:,">，
		// 这是合法 HTML 但图标是空的 —— 必须说清楚，否则用户会以为是
		// 抓取坏了，而实际上这个站点根本没有图标可抓
		return "", errors.New("站点声明的图标是空的（data:,）")
	}
	if !strings.Contains(strings.ToLower(meta), "base64") {
		return "", errors.New("只支持 base64 编码的内联图标")
	}
	// 允许超限一点点再报错，避免把刚好等于上限的内容误判
	if len(payload) > maxDataURIBytes*4/3+4 {
		return "", fmt.Errorf("内联图标超过 %dKB", maxDataURIBytes>>10)
	}
	if meta == "" {
		return "", errors.New("data URI 缺少媒体类型")
	}
	return raw, nil
}
