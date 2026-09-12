package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/model"
	"llm-relay/internal/secure"
)

// sameOriginOnly 拦掉来自其它网页的跨站请求。
//
// 管理接口是本地自用、没有登录态的，于是浏览器里的**任何**网页都能对 localhost
// 发起请求。一个
//
//	<form method="post" action="http://127.0.0.1:8888/api/admin/settings/cleanup">
//
// 就足以触发清库这类操作：表单提交属于「简单请求」，不触发预检，
// 浏览器不会拦，服务端收到的报文与正常请求也没有区别。
// 这不是推测 —— 实测用 application/x-www-form-urlencoded 提交确实返回 200 并执行了。
//
// 判断依据用 Origin 头，理由：
//   - 浏览器对所有跨站请求都会带上 Origin，页面脚本改不了它
//   - 同源请求也带，值与本服务地址一致，放行
//   - 命令行客户端（curl、各家 SDK）通常不带 Origin，放行，
//     免得把「本地自用」最常见的调试方式一并挡掉
//
// 覆盖不到的情况：不带 Origin 的请求无法区分（例如 DNS rebinding）。
// 但把「随便打开一个网页就能打管理接口」这条最现实的路径堵上了。
func sameOriginOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}
		u, err := url.Parse(origin)
		// Origin: null（沙箱 iframe、file:// 页面）解析出来 Host 为空，会走拒绝分支
		if err != nil || !strings.EqualFold(u.Host, c.Request.Host) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{
				"code":    http.StatusForbidden,
				"message": "拒绝跨站请求：管理接口只接受来自本服务页面的调用",
				"type":    "forbidden_error",
			}})
			return
		}
		c.Next()
	}
}

const ctxAPIKey = "llm_relay_api_key"

// profileForPath 按请求路径推断入站协议。
// 鉴权失败发生在协议分发之前，若统一用 OpenAI 错误结构，
// Anthropic 客户端会因为解析不到 {"type":"error"} 而报出难懂的错。
func profileForPath(path string) *inboundProfile {
	switch {
	case strings.HasPrefix(path, "/v1/messages"):
		return profileAnthropic
	case strings.HasPrefix(path, "/v1/responses"):
		return profileOpenAIResponses
	case strings.HasPrefix(path, "/v1beta/models"):
		return profileGemini
	default:
		return profileOpenAIChat
	}
}

// requireAPIKey 校验 Authorization: Bearer sk-xxx。
// 中转服务面向任意 OpenAI 兼容客户端，因此沿用标准 Bearer 约定。
func (s *Server) requireAPIKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		profile := profileForPath(c.Request.URL.Path)

		// 各家的密钥传递方式不同，逐个尝试：
		//   OpenAI    Authorization: Bearer sk-...
		//   Anthropic x-api-key: sk-...
		//   Gemini    x-goog-api-key: ... 或 ?key=...
		raw := extractBearer(c.GetHeader("Authorization"))
		if raw == "" {
			raw = c.GetHeader("x-api-key")
		}
		if raw == "" {
			raw = c.GetHeader("x-goog-api-key")
		}
		if raw == "" {
			raw = c.Query("key")
		}
		if raw == "" {
			profile.writeError(c, http.StatusUnauthorized, "缺少 API Key", "invalid_request_error")
			c.Abort()
			return
		}

		var key model.APIKey
		err := s.deps.Store.DB().
			Where("key_hash = ? AND enabled = ?", secure.HashKey(raw), true).
			First(&key).Error
		if err != nil {
			profile.writeError(c, http.StatusUnauthorized, "API Key 无效或已禁用", "invalid_request_error")
			c.Abort()
			return
		}

		// 密钥级限流：先看该密钥自己的额度，没配就用全局默认值。
		// 负值表示这把密钥不限流，便于本地脚本或压测单独放行。
		limit := key.RateLimitRPM
		if limit == 0 {
			limit = s.deps.Config.Relay.DefaultRPM
		}
		if ok, wait := s.deps.RateLimiter.Allow(key.ID, limit, time.Now()); !ok {
			secs := int(wait.Seconds())
			if secs < 1 {
				secs = 1
			}
			c.Header("Retry-After", strconv.Itoa(secs))
			profile.writeError(c, http.StatusTooManyRequests,
				"请求过于频繁，密钥 "+key.Name+" 的限制为每分钟 "+strconv.Itoa(limit)+" 次", "rate_limit_error")
			c.Abort()
			return
		}

		c.Set(ctxAPIKey, &key)
		s.touchAPIKey(key.ID)
		c.Next()
	}
}

func extractBearer(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return header
}

// touchAPIKey 异步刷新最后使用时间，失败不影响请求。
func (s *Server) touchAPIKey(id uint) {
	go func() {
		_ = s.deps.Store.DB().Model(&model.APIKey{}).
			Where("id = ?", id).
			Update("last_used_at", nowUTC()).Error
	}()
}

func apiKeyFromContext(c *gin.Context) *model.APIKey {
	if v, ok := c.Get(ctxAPIKey); ok {
		if k, ok := v.(*model.APIKey); ok {
			return k
		}
	}
	return nil
}
