package api

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/relay/convert"
)

// inboundProfile 描述一个入站端点如何映射到上游。
//
// 站内以 OpenAI Chat Completions 作为「通用语」：入站协议若不是它，
// 由 TranslateRequest 归一化后再发给上游；响应则通过 Translator 改回原协议。
// 这样新增一个上游协议只需要写一个出站适配器，不必组合 N x M 个转换器。
type inboundProfile struct {
	// Name 写入请求日志的入站协议名
	Name string
	// UpstreamPath 上游路径。当前所有协议都收敛到 OpenAI 兼容端点
	UpstreamPath string
	// ContentType 响应内容类型
	ContentType string
	// TranslateRequest 把入站载荷转成 OpenAI Chat；nil 表示入站本身就是 OpenAI Chat
	TranslateRequest func([]byte) ([]byte, error)
	// NewTranslator 构造响应改写器；nil 表示原样透传
	NewTranslator func(w io.Writer, model string, stream bool) convert.Translator
	// ErrorBody 生成错误响应体；nil 表示使用 OpenAI 错误结构
	ErrorBody func(status int, message string) []byte
}

var (
	profileOpenAIChat = &inboundProfile{
		Name:         "openai-chat",
		UpstreamPath: "/v1/chat/completions",
		ContentType:  "application/json; charset=utf-8",
	}
	profileOpenAIResponses = &inboundProfile{
		Name:             "openai-responses",
		UpstreamPath:     "/v1/chat/completions",
		ContentType:      "application/json; charset=utf-8",
		TranslateRequest: convert.ResponsesRequestToOpenAIChat,
		NewTranslator:    convert.NewResponsesTranslator,
		ErrorBody:        convert.ResponsesError,
	}
	profileAnthropic = &inboundProfile{
		Name:             "anthropic-messages",
		UpstreamPath:     "/v1/chat/completions",
		ContentType:      "application/json",
		TranslateRequest: convert.AnthropicRequestToOpenAIChat,
		NewTranslator:    convert.NewAnthropicTranslator,
		ErrorBody:        convert.AnthropicError,
	}
	profileEmbeddings = &inboundProfile{
		Name:         "openai-embeddings",
		UpstreamPath: "/v1/embeddings",
		ContentType:  "application/json; charset=utf-8",
	}
)

// translator 构造本次响应要用的改写器，默认透传。
func (p *inboundProfile) translator(w io.Writer, model string, stream bool) convert.Translator {
	if p.NewTranslator == nil {
		return convert.NewPassthrough(w)
	}
	return p.NewTranslator(w, model, stream)
}

// errorBody 生成错误响应体，默认用 OpenAI 结构。
func (p *inboundProfile) errorBody(status int, message string) []byte {
	if p.ErrorBody != nil {
		return p.ErrorBody(status, message)
	}
	return defaultErrorBody(status, message)
}

// writeError 按入站协议的错误结构返回。
func (p *inboundProfile) writeError(c *gin.Context, status int, message, errType string) {
	if status <= 0 {
		status = http.StatusBadGateway
	}
	if p.ErrorBody != nil {
		c.Data(status, p.ContentType, p.ErrorBody(status, message))
		return
	}
	c.JSON(status, map[string]any{
		"error": map[string]any{"message": message, "type": errType, "code": status},
	})
}

// translateBody 归一化请求载荷。转换失败时返回错误，由调用方以 400 回给客户端。
func (p *inboundProfile) translateBody(body []byte) ([]byte, error) {
	if p.TranslateRequest == nil {
		return body, nil
	}
	return p.TranslateRequest(body)
}
