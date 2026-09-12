package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/model"
	"llm-relay/internal/secure"
)

const ctxAPIKey = "llm_relay_api_key"

// profileForPath 按请求路径推断入站协议。
// 鉴权失败发生在协议分发之前，若统一用 OpenAI 错误结构，
// Anthropic 客户端会因为解析不到 {"type":"error"} 而报出难懂的错。
func profileForPath(path string) *inboundProfile {
	if strings.HasPrefix(path, "/v1/messages") {
		return profileAnthropic
	}
	return profileOpenAIChat
}

// requireAPIKey 校验 Authorization: Bearer sk-xxx。
// 中转服务面向任意 OpenAI 兼容客户端，因此沿用标准 Bearer 约定。
func (s *Server) requireAPIKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		profile := profileForPath(c.Request.URL.Path)

		// Anthropic 客户端用 x-api-key，OpenAI 客户端用 Authorization: Bearer，两者都接受
		raw := extractBearer(c.GetHeader("Authorization"))
		if raw == "" {
			raw = c.GetHeader("x-api-key")
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
