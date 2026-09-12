package convert

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AnthropicRequestToOpenAIChat 把 Anthropic Messages 请求翻译成 OpenAI Chat Completions 请求。
//
// 主要差异：
//   - Anthropic 的 system 是顶层字段，OpenAI 要求放进 messages 的第一条
//   - Anthropic 的 tool_result 是 user 消息里的内容块，OpenAI 要拆成独立的 role=tool 消息
//   - Anthropic 的 tools 用 input_schema，OpenAI 包一层 {type:function, function:{parameters}}
func AnthropicRequestToOpenAIChat(body []byte) ([]byte, error) {
	var src map[string]any
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("Anthropic 请求体解析失败: %w", err)
	}

	out := map[string]any{}
	if v, ok := src["model"]; ok {
		out["model"] = v
	}
	if v, ok := src["stream"]; ok {
		out["stream"] = v
	}
	// max_tokens 在 Anthropic 是必填，在 OpenAI 可选，直接透传
	if v, ok := src["max_tokens"]; ok {
		out["max_tokens"] = v
	}
	for _, k := range []string{"temperature", "top_p"} {
		if v, ok := src[k]; ok {
			out[k] = v
		}
	}
	if v, ok := src["stop_sequences"]; ok {
		out["stop"] = v
	}
	if v, ok := src["tools"]; ok {
		out["tools"] = anthropicToolsToOpenAI(v)
	}
	if v, ok := src["tool_choice"]; ok {
		out["tool_choice"] = anthropicToolChoiceToOpenAI(v)
	}

	var messages []any
	if sys := flattenAnthropicSystem(src["system"]); sys != "" {
		messages = append(messages, map[string]any{"role": "system", "content": sys})
	}

	rawMsgs, _ := src["messages"].([]any)
	for _, item := range rawMsgs {
		m := asMap(item)
		if m == nil {
			continue
		}
		role := asString(m["role"])
		content := m["content"]

		// content 为纯字符串时直接透传，避免不必要的结构改写
		if s, ok := content.(string); ok {
			messages = append(messages, map[string]any{"role": role, "content": s})
			continue
		}

		blocks, _ := content.([]any)
		var textParts []any
		var toolCalls []any
		var toolResults []any

		for _, b := range blocks {
			blk := asMap(b)
			if blk == nil {
				continue
			}
			switch asString(blk["type"]) {
			case "text":
				textParts = append(textParts, map[string]any{
					"type": "text", "text": asString(blk["text"]),
				})
			case "image":
				if part := anthropicImageToOpenAI(blk); part != nil {
					textParts = append(textParts, part)
				}
			case "tool_use":
				args, _ := json.Marshal(blk["input"])
				toolCalls = append(toolCalls, map[string]any{
					"id":   asString(blk["id"]),
					"type": "function",
					"function": map[string]any{
						"name":      asString(blk["name"]),
						"arguments": string(args),
					},
				})
			case "tool_result":
				// Anthropic 把工具结果塞在 user 消息里，OpenAI 要求独立的 tool 消息
				toolResults = append(toolResults, map[string]any{
					"role":         "tool",
					"tool_call_id": asString(blk["tool_use_id"]),
					"content":      flattenToolResult(blk["content"]),
				})
			}
		}

		// Anthropic 把 tool_result 放在 user 回合的开头，而 OpenAI 要求 role=tool
		// 的消息紧跟在触发它的 assistant 消息之后、下一条 user 消息之前。
		// 顺序反了上游会直接报「tool 消息必须回应前面的 tool_calls」。
		messages = append(messages, toolResults...)

		// 纯 tool_result 的回合不应再产生一条空的 user 消息
		if len(textParts) == 0 && len(toolCalls) == 0 {
			continue
		}

		msg := map[string]any{"role": role}
		if len(textParts) == 1 {
			if t, ok := textParts[0].(map[string]any); ok && asString(t["type"]) == "text" {
				msg["content"] = asString(t["text"])
			} else {
				msg["content"] = textParts
			}
		} else if len(textParts) > 1 {
			msg["content"] = textParts
		} else {
			msg["content"] = ""
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		messages = append(messages, msg)
	}

	out["messages"] = messages
	return json.Marshal(out)
}

// flattenAnthropicSystem 把 system 字段（字符串或内容块数组）拍平成纯文本。
func flattenAnthropicSystem(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []any:
		var parts []string
		for _, item := range s {
			if blk := asMap(item); blk != nil {
				if t := asString(blk["text"]); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

// flattenToolResult 把工具结果的内容块拍平成文本。
func flattenToolResult(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, item := range c {
			if blk := asMap(item); blk != nil {
				if asString(blk["type"]) == "text" {
					parts = append(parts, asString(blk["text"]))
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

// anthropicImageToOpenAI 转换图片块。Anthropic 用 base64/url 的 source，OpenAI 用 data URI。
func anthropicImageToOpenAI(blk map[string]any) map[string]any {
	src := asMap(blk["source"])
	if src == nil {
		return nil
	}
	var url string
	switch asString(src["type"]) {
	case "base64":
		url = "data:" + asString(src["media_type"]) + ";base64," + asString(src["data"])
	case "url":
		url = asString(src["url"])
	default:
		return nil
	}
	if url == "" {
		return nil
	}
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}
}

func anthropicToolsToOpenAI(v any) []any {
	list, _ := v.([]any)
	outTools := make([]any, 0, len(list))
	for _, item := range list {
		t := asMap(item)
		if t == nil {
			continue
		}
		fn := map[string]any{"name": asString(t["name"])}
		if d := asString(t["description"]); d != "" {
			fn["description"] = d
		}
		if s, ok := t["input_schema"]; ok {
			fn["parameters"] = s
		}
		outTools = append(outTools, map[string]any{"type": "function", "function": fn})
	}
	return outTools
}

func anthropicToolChoiceToOpenAI(v any) any {
	m := asMap(v)
	if m == nil {
		if s, ok := v.(string); ok {
			return s
		}
		return nil
	}
	switch asString(m["type"]) {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": asString(m["name"])},
		}
	default:
		return nil
	}
}
