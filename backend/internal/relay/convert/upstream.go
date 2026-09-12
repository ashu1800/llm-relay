package convert

import (
	"encoding/json"
	"io"
	"strings"

	"llm-relay/internal/model"
)

// 出站协议适配的入口。
//
// 站内以 OpenAI Chat Completions 为通用语（入站协议先在 api/protocols.go 归一化），
// 渠道上的 protocol 声明的是**上游**协议。这里把通用语转成上游协议，
// 再把上游的响应转回通用语 —— 后面的用量统计、定价快照、日志与入站改写器
// 都只认通用语，不必知道上游说的是哪种协议。

// UpstreamRequest 按上游协议改写请求路径与请求体。
//
// path 是入站协议对应的默认上游路径（见 api/protocols.go）。只有「对话」
// 类请求才做协议转换：embeddings 没有 Anthropic / Gemini 的对应物，
// 遇到这类渠道时保持原样，让上游自己报错，比在中转站里造一个假的成功响应好。
func UpstreamRequest(protocol, path string, body []byte) (string, []byte, error) {
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
	default:
		// openai-chat / openai-responses / openai-embeddings / custom：
		// 上游本来就是 OpenAI 形状（responses 与 embeddings 也走 OpenAI 端点），
		// 不需要改写
		return path, body, nil
	}
}

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
	default:
		return body
	}
}

// UpstreamStream 把流式响应体转回 OpenAI Chat 的 SSE 流。
func UpstreamStream(protocol string, r io.ReadCloser, upstreamModel string) io.ReadCloser {
	switch protocol {
	case model.ProtocolAnthropic:
		return NewAnthropicStreamToOpenAIChat(r, upstreamModel)
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
