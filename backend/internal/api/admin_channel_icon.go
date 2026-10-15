package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/model"
	"llm-relay/internal/netguard"
)

// 图标抓取的公共部分（客户端、超时、SSRF 防护、data URI 口径）在
// icon_fetch.go —— 客户端图标（用户填的图片地址）复用同一套。
//
// 依次尝试的图标路径。顺序有讲究：先试站点自己的 /favicon.ico
// （绝大多数站点都有），再试 BaseURL 下的 —— 有些中转站把图标放在
// 自己的子路径里（例如 /v1/favicon.ico）。
var iconPaths = []string{"/favicon.ico", "/apple-touch-icon.png", "/apple-touch-icon-precomposed.png"}

type channelIconPayload struct {
	// Icon 为空表示「去上游抓」，非空表示直接设置成这个值（自定义图标）
	Icon string `json:"icon"`
	// BaseURL 允许用「表单里当前填的地址」去抓，而不是库里那一份：
	// 用户刚改完上游地址就点抓取，期望的是从新地址抓
	BaseURL string `json:"base_url"`
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

	// 总超时：6 个候选地址各 8 秒，最坏情况要跑将近一分钟，
	// 而用户点的是「从上游获取」，等 10 秒以上就是坏了
	ctx, cancel := context.WithTimeout(ctx, iconFetchTimeout*3)
	defer cancel()

	// 与转发链路一致：渠道配了代理就必须走代理（理由见 newIconFetcher）
	fetcher, cleanup, err := s.newIconFetcher(ctx, ch.ProxyID)
	if err != nil {
		return "", err
	}
	defer cleanup()

	// 候选地址：站点根目录下的常见图标，以及 BaseURL 自己那一层。
	// 用 url.URL 拼而不是字符串拼接：BaseURL 带 query 时（如 /v1?k=1），
	// 字符串拼接会把图标路径塞进 query 里，请求就发到别的地方去了
	clean := *base
	clean.RawQuery = ""
	clean.Fragment = ""
	root := &url.URL{Scheme: clean.Scheme, Host: clean.Host}
	underBase := clean
	underBase.Path = strings.TrimRight(clean.Path, "/")
	candidates := make([]string, 0, len(iconPaths)*2)
	for _, p := range iconPaths {
		candidates = append(candidates, root.String()+p)
	}
	for _, p := range iconPaths {
		candidates = append(candidates, underBase.String()+p)
	}

	var lastErr string
	for _, u := range candidates {
		icon, err := fetcher.fetch(u)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		return icon, nil
	}
	if lastErr == "" {
		lastErr = "没有找到可用的图标"
	}
	return "", errors.New("拉取图标失败：" + lastErr)
}
