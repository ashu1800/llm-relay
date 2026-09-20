package convert

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"llm-relay/internal/model"
)

// ErrUnsupportedContent 表示请求体本身含有目标上游协议表达不了的内容。
//
// 这是**客户端**的问题，不是渠道的问题 —— 换一条渠道结果一样。
// 调用方（relay 包）据此跳过重试、不记渠道失败，直接回 400。
// 若不区分，发一次音频会让每个渠道都白试一遍，并把健康渠道记成故障。
var ErrUnsupportedContent = errors.New("请求内容无法转换为上游协议")

// unsupportedContentError 带上具体原因，便于回一条能读懂的错误。
type unsupportedContentError struct {
	reason string
}

func (e *unsupportedContentError) Error() string { return e.reason }

// Unwrap 让 errors.Is(err, ErrUnsupportedContent) 成立，
// 调用方分类时不必关心具体是哪种内容块。
func (e *unsupportedContentError) Unwrap() error { return ErrUnsupportedContent }

func errUnsupportedContent(reason string) error {
	return &unsupportedContentError{reason: reason}
}

// 出站协议适配的入口。
//
// 站内以 OpenAI Chat Completions 为通用语（入站协议先在 api/protocols.go 归一化），
// 渠道上的 protocol 声明的是**上游**协议。这里把通用语转成上游协议，
// 再把上游的响应转回通用语 —— 后面的用量统计、定价快照、日志与入站改写器
// 都只认通用语，不必知道上游说的是哪种协议。

// UpstreamRequest 按上游协议改写请求路径与请求体。
//
// path 是入站协议对应的默认上游路径（见 api/protocols.go）；
// upstreamModel 是白名单里的上游模型名 —— Gemini 把它放在路径里，
// 转换时必须拿到。只有「对话」类请求才做协议转换：embeddings 没有
// Anthropic / Gemini 的对应物，遇到这类渠道时保持原样，让上游自己报错，
// 比在中转站里造一个假的成功响应好。
func UpstreamRequest(protocol, path string, body []byte, upstreamModel string) (string, []byte, error) {
	if !isChatPath(path) {
		return path, body, nil
	}
	switch protocol {
	case model.ProtocolAnthropic:
		out, err := OpenAIChatToAnthropicRequest(body)
		if err != nil {
			return "", nil, err
		}
		return "/v1/messages", out, nil
	case model.ProtocolOpenAIResponses:
		// Responses 是**另一套端点**（POST /v1/responses），报文也与 Chat 不同：
		// 不能只换路径，必须整体改写（instructions / input 事件数组 / 扁平 tools）。
		out, err := OpenAIChatToResponsesRequest(body)
		if err != nil {
			return "", nil, err
		}
		return "/v1/responses", stripCacheControlFromJSON(out), nil
	case model.ProtocolGemini:
		out, err := OpenAIChatToGeminiRequest(body)
		if err != nil {
			return "", nil, err
		}
		return GeminiUpstreamPath(upstreamModel, requestIsStream(body)), stripCacheControlFromJSON(out), nil
	default:
		// openai-chat / openai-responses / openai-embeddings / custom：
		// 上游本来就是 OpenAI 形状（responses 与 embeddings 也走 OpenAI 端点），
		// 不需要改写。
		//
		// 但要清掉通用语里可能残留的 cache_control 附加字段：它是为
		// 「入站 Anthropic -> 出站 Anthropic」暂存断点用的，OpenAI 兼容端点
		// 不认识它。严格校验的实现（如 Azure OpenAI）会直接 400。
		return path, stripCacheControlFromJSON(body), nil
	}
}

// stripCacheControlFromJSON 解析 JSON、递归清掉 cache_control 等
// Anthropic 私有附加字段后再序列化。
//
// 解析失败时原样返回：这个函数只负责去除附加字段，
// 不该因为请求体不是合法 JSON 就让整次转发失败（上游会给出更准确的错误）。
func stripCacheControlFromJSON(body []byte) []byte {
	// 先做子串粗筛再解析：这些字段只可能来自 Anthropic 入站，绝大多数请求
	// 根本没有它们 —— 但下面那个 containsKey 的短路在 Unmarshal **之后**，
	// 全量解析的代价照样付了。数 MB 的长对话每个候选渠道都要再来一遍。
	if !bytes.Contains(body, privateFieldNeedles[0]) && !bytes.Contains(body, privateFieldNeedles[1]) {
		return body
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return body
	}
	if !containsKey(v, cacheControlKey) && !containsKey(v, anthropicBlocksKey) {
		// 子串命中但不是这两个键（比如正文里恰好写了 "cache_control"）：
		// 直接返回原文避免无谓的重新序列化
		return body
	}
	stripCacheControl(v)
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return out
}

// privateFieldNeedles 是子串粗筛用的 needle（见 stripCacheControlFromJSON 注释）。
// cache_control 是块级断点，anthropic_blocks 是消息级私有块容器
// （thinking / document 等），两者互不包含、都要筛。
var privateFieldNeedles = [][]byte{
	[]byte(cacheControlKey),
	[]byte(anthropicBlocksKey),
}

// containsKey 递归判断结构里是否存在某个键。
func containsKey(v any, key string) bool {
	switch t := v.(type) {
	case map[string]any:
		if _, ok := t[key]; ok {
			return true
		}
		for _, sub := range t {
			if containsKey(sub, key) {
				return true
			}
		}
	case []any:
		for _, sub := range t {
			if containsKey(sub, key) {
				return true
			}
		}
	}
	return false
}

// requestIsStream 读请求体里的 stream 标记。
// Gemini 的流式与否体现在方法名（:streamGenerateContent）上，路径必须与它一致。
func requestIsStream(body []byte) bool {
	// 子串粗筛：没有 "stream" 字样必然 false，免去一次全量 JSON 解析
	if !bytes.Contains(body, streamNeedle) {
		return false
	}
	var payload struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return payload.Stream
}

var streamNeedle = []byte(`"stream"`)

// UpstreamResponseBody 把非流式响应体转回 OpenAI Chat。
// 解析失败时原样返回：宁可让客户端看到上游的原文，也不要吞掉一次成功的响应。
func UpstreamResponseBody(protocol string, body []byte, upstreamModel string) []byte {
	switch protocol {
	case model.ProtocolAnthropic:
		out, err := AnthropicResponseToOpenAIChat(body, upstreamModel)
		if err != nil {
			return body
		}
		return out
	case model.ProtocolGemini:
		out, err := GeminiResponseToOpenAIChat(body, upstreamModel)
		if err != nil {
			return body
		}
		return out
	case model.ProtocolOpenAIResponses:
		out, err := ResponsesResponseToOpenAIChat(body, upstreamModel)
		if err != nil {
			return body
		}
		return out
	default:
		return body
	}
}

// UpstreamStream 把流式响应体转回 OpenAI Chat 的 SSE 流。
func UpstreamStream(protocol string, r io.ReadCloser, upstreamModel string) io.ReadCloser {
	switch protocol {
	case model.ProtocolAnthropic:
		return NewAnthropicStreamToOpenAIChat(r, upstreamModel)
	case model.ProtocolGemini:
		return NewGeminiStreamToOpenAIChat(r, upstreamModel)
	case model.ProtocolOpenAIResponses:
		return NewResponsesStreamToOpenAIChat(r, upstreamModel)
	default:
		return r
	}
}

// UpstreamErrorMessage 从上游的错误体里抽出可读信息。
//
// 上游报错时报文结构是它自己的协议（Anthropic 是 {"type":"error","error":{...}}），
// 直接把整段 JSON 当 message 回给客户端会套好几层，排障时很难看清是哪一步拒绝了。
func UpstreamErrorMessage(protocol string, body []byte) string {
	if len(body) == 0 {
		return "上游返回了空错误体"
	}
	switch protocol {
	case model.ProtocolAnthropic:
		return AnthropicErrorMessage(body)
	case model.ProtocolGemini:
		return GeminiErrorMessage(body)
	case model.ProtocolOpenAIResponses:
		return ResponsesUpstreamErrorMessage(body)
	default:
		// OpenAI 形状的上游同样把原因放在 error.message 里
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err == nil {
			if e := asMap(payload["error"]); e != nil {
				if msg := asString(e["message"]); msg != "" {
					return msg
				}
			}
		}
		return string(body)
	}
}

// isChatPath 判断这次请求是不是「对话」类。
func isChatPath(path string) bool {
	return strings.HasPrefix(path, "/v1/chat/completions")
}
