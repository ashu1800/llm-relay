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

	"github.com/gin-gonic/gin"

	"llm-relay/internal/model"
)

// 图标大小与超时。上游的 favicon 通常 1-5KB；
// 给到 64KB 是留出 apple-touch-icon 这类大图的余量，
// 再大就不是图标了，拉回来只会把备份撑肥。
const maxIconBytes = 64 << 10
const iconFetchTimeout = 8 * time.Second

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
	if err := db.Model(&model.Channel{}).Where("id = ?", id).Update("icon", icon).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "icon": icon})
}

// fetchUpstreamIcon 从渠道的上游地址抓一个图标回来，转成 data URI。
//
// 存 data URI 而不是图标地址：上游随时可能把 favicon 换掉或加上鉴权，
// 那种时候界面上的图标会莫名其妙变成裂图；而且备份要能带着图标走。
func (s *Server) fetchUpstreamIcon(ctx context.Context, ch model.Channel) (string, error) {
	base, err := url.Parse(strings.TrimSpace(ch.BaseURL))
	if err != nil || base.Host == "" {
		return "", errors.New("渠道的上游地址无法解析，取不到图标")
	}
	// 只支持 http(s)：file:// 之类会让这个接口变成任意文件读取
	if base.Scheme != "http" && base.Scheme != "https" {
		return "", errors.New("只支持从 http/https 的上游地址抓图标")
	}

	client := &http.Client{Timeout: iconFetchTimeout}
	// 候选地址：站点根目录下的常见图标，以及 BaseURL 自己那一层
	candidates := make([]string, 0, len(iconPaths)*2)
	root := &url.URL{Scheme: base.Scheme, Host: base.Host}
	for _, p := range iconPaths {
		candidates = append(candidates, root.String()+p)
	}
	for _, p := range iconPaths {
		candidates = append(candidates, strings.TrimRight(base.String(), "/")+p)
	}

	var lastErr string
	for _, u := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		ct := resp.Header.Get("Content-Type")
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			lastErr = fmt.Sprintf("%s 返回 HTTP %d", u, resp.StatusCode)
			continue
		}
		// 有些站点会把 404 页面配上 image/png 的 Content-Type 发出来，
		// 所以还要看内容类型对不对得上
		if !strings.HasPrefix(ct, "image/") {
			_ = resp.Body.Close()
			lastErr = fmt.Sprintf("%s 返回的不是图片（%s）", u, ct)
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes+1))
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err.Error()
			continue
		}
		if len(data) == 0 {
			lastErr = u + " 返回了空内容"
			continue
		}
		if len(data) > maxIconBytes {
			lastErr = fmt.Sprintf("%s 的图标超过 %dKB", u, maxIconBytes>>10)
			continue
		}
		return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(data), nil
	}
	if lastErr == "" {
		lastErr = "没有找到可用的图标"
	}
	return "", errors.New("拉取图标失败：" + lastErr)
}
