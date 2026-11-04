package api

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/model"
	"llm-relay/internal/relay"
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
	// UpstreamPath 默认的上游路径。这是「上游是 OpenAI 形状」时的路径；
	// 渠道若声明了别的上游协议（Anthropic / Gemini），由 relay/convert 的
	// UpstreamRequest 换成对应的方法路径。
	UpstreamPath string
	// ContentType 响应内容类型
	ContentType string
	// TranslateRequest 把入站载荷转成 OpenAI Chat；nil 表示入站本身就是 OpenAI Chat
	TranslateRequest func([]byte) ([]byte, error)
	// TranslateRequestWithModel 供模型名不在请求体里的协议使用（如 Gemini，模型名在 URL 路径）
	TranslateRequestWithModel func(body []byte, model string, stream bool) ([]byte, error)
	// NewTranslator 构造响应改写器；nil 表示原样透传
	NewTranslator func(w io.Writer, model string, stream bool) convert.Translator
	// ErrorBody 生成错误响应体；nil 表示使用 OpenAI 错误结构
	ErrorBody func(status int, message string) []byte
	// Direct 标记直通端点（图像/音频，2026-11 P2）：请求体不做 OpenAI Chat
	// 归一化、响应不做协议转换，原样转发原样回传。候选渠道被限定为
	// OpenAI 兼容协议（directChannelProtocols），转换对它们本来就不适用。
	Direct bool
	// ExtractModel 从原始请求体里取模型名；nil 表示按 JSON 的 "model" 字段读
	// （relay.ExtractModel）。multipart 端点（语音转写）用 multipartFormModel。
	ExtractModel func(raw []byte, contentType string) (string, error)
}

// directChannelProtocols 是直通端点允许的渠道出站协议。
//
// 图像/音频请求不做协议转换，Anthropic / Gemini 协议渠道根本无法受理 ——
// 在路由层就排除（relay.CandidateQuery.RequireProtocols），而不是等转发失败。
// custom 是「自定义鉴权头」的 OpenAI 兼容渠道：鉴权方式不同、报文形状相同，
// 直通对它完全适用。
var directChannelProtocols = []string{
	model.ProtocolOpenAIChat,
	model.ProtocolOpenAIResponses,
	model.ProtocolEmbeddings,
	model.ProtocolCustom,
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
	profileGemini = &inboundProfile{
		Name:         "gemini-generateContent",
		UpstreamPath: "/v1/chat/completions",
		ContentType:  "application/json; charset=utf-8",
		// Gemini 把模型名和流式标记都放在 URL 里，请求体里没有
		TranslateRequestWithModel: convert.GeminiRequestToOpenAIChat,
		NewTranslator:             convert.NewGeminiTranslator,
		ErrorBody:                 convert.GeminiError,
	}
	profileEmbeddings = &inboundProfile{
		Name:         "openai-embeddings",
		UpstreamPath: "/v1/embeddings",
		ContentType:  "application/json; charset=utf-8",
	}

	// ---- 直通端点（2026-11 P2）：图像 / 音频 ----
	//
	// 这些端点没有「归一化成 OpenAI Chat」可言：请求体就是上游要的形状
	//（JSON 或 multipart），响应也是上游的形状（JSON 或音频二进制）。
	// Direct 让 relayRequest 跳过翻译、让转发层跳过协议转换。
	// 计费按非 token 维度（每张/每秒/每字符），见 directBillingKind。
	profileImageGenerations = &inboundProfile{
		Name:         "openai-images",
		UpstreamPath: "/v1/images/generations",
		ContentType:  "application/json; charset=utf-8",
		Direct:       true,
	}
	profileAudioSpeech = &inboundProfile{
		Name:         "openai-audio-speech",
		UpstreamPath: "/v1/audio/speech",
		ContentType:  "application/json; charset=utf-8",
		Direct:       true,
	}
	profileAudioTranscriptions = &inboundProfile{
		Name:         "openai-audio-transcriptions",
		UpstreamPath: "/v1/audio/transcriptions",
		ContentType:  "application/json; charset=utf-8",
		Direct:       true,
		ExtractModel: multipartModel,
	}
	profileAudioTranslations = &inboundProfile{
		Name:         "openai-audio-translations",
		UpstreamPath: "/v1/audio/translations",
		ContentType:  "application/json; charset=utf-8",
		Direct:       true,
		ExtractModel: multipartModel,
	}
)

// multipartModel 从 multipart 报文里取模型名（语音转写的 model 在表单字段里）。
func multipartModel(raw []byte, contentType string) (string, error) {
	return relay.MultipartModelField(raw, contentType), nil
}

// translator 构造本次响应要用的改写器，默认透传。
func (p *inboundProfile) translator(w io.Writer, model string, stream bool) convert.Translator {
	if p.NewTranslator == nil {
		return convert.NewPassthrough(w)
	}
	return p.NewTranslator(w, model, stream)
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
func (p *inboundProfile) translateBody(body []byte, model string, stream bool) ([]byte, error) {
	if p.TranslateRequestWithModel != nil {
		return p.TranslateRequestWithModel(body, model, stream)
	}
	if p.TranslateRequest == nil {
		return body, nil
	}
	return p.TranslateRequest(body)
}
