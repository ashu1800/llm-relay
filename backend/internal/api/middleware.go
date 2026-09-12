package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/model"
	"llm-relay/internal/secure"
)

const ctxAPIKey = "llm_relay_api_key"

// requireAPIKey 校验 Authorization: Bearer sk-xxx。
// 中转服务面向任意 OpenAI 兼容客户端，因此沿用标准 Bearer 约定。
func (s *Server) requireAPIKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := extractBearer(c.GetHeader("Authorization"))
		if raw == "" {
			raw = c.GetHeader("x-api-key")
		}
		if raw == "" {
			writeUpstreamError(c, http.StatusUnauthorized, "缺少 API Key", "invalid_request_error")
			c.Abort()
			return
		}

		var key model.APIKey
		err := s.deps.Store.DB().
			Where("key_hash = ? AND enabled = ?", secure.HashKey(raw), true).
			First(&key).Error
		if err != nil {
			writeUpstreamError(c, http.StatusUnauthorized, "API Key 无效或已禁用", "invalid_request_error")
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
