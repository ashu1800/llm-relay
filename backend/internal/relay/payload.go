package relay

import (
	"net/http"
	"strings"
	"time"

	"llm-relay/internal/model"
)

// 报文留存模式的取值。
const (
	PayloadStoreAll    = "all"
	PayloadStoreErrors = "errors"
	PayloadStoreNone   = "none"
)

// DefaultPayloadMaxBytes 单条报文留存上限，防止长流或大请求把库撑爆。
const DefaultPayloadMaxBytes = 256 << 10

// sensitiveHeaders 这些头携带凭据。留存前必须抹掉，
// 否则等于把上游密钥和客户端密钥明文写进库，页面上还能直接看到。
var sensitiveHeaders = map[string]bool{
	"authorization":       true,
	"x-api-key":           true,
	"x-goog-api-key":      true,
	"api-key":             true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
}

// ShouldStorePayload 判断本次调用是否需要留存报文。
// errors 模式只留出错的调用，是日常最实用的档位。
func ShouldStorePayload(mode string, status int) bool {
	switch mode {
	case PayloadStoreAll:
		return true
	case PayloadStoreErrors:
		return status >= 400
	default:
		return false
	}
}

// Truncate 按上限截断报文。
// 结尾会明确标注被截断，避免看到半截 JSON 误判成上游返回了非法内容。
func Truncate(b []byte, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = DefaultPayloadMaxBytes
	}
	if len(b) <= maxBytes {
		return string(b)
	}
	return string(b[:maxBytes]) + "\n…（内容超过留存上限，已截断）"
}

// BuildPayload 组装一条待留存的报文；当前模式不需要留存时返回 nil。
func BuildPayload(mode string, status int, reqBody, respBody []byte,
	reqHeader, respHeader http.Header, maxBytes int,
) *model.RequestPayload {
	if !ShouldStorePayload(mode, status) {
		return nil
	}
	return &model.RequestPayload{
		RequestBody:     Truncate(reqBody, maxBytes),
		ResponseBody:    Truncate(respBody, maxBytes),
		RequestHeaders:  sanitizeHeader(reqHeader),
		ResponseHeaders: sanitizeHeader(respHeader),
		CreatedAt:       time.Now().UTC(),
	}
}

// sanitizeHeader 复制请求头并抹掉凭据字段。
func sanitizeHeader(h http.Header) model.JSONMap {
	if h == nil {
		return nil
	}
	out := make(model.JSONMap, len(h))
	for k, v := range h {
		if sensitiveHeaders[strings.ToLower(k)] {
			out[k] = "[已隐藏]"
			continue
		}
		out[k] = strings.Join(v, ", ")
	}
	return out
}
