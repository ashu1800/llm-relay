package convert

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ResponsesRequestToOpenAIChat 把 OpenAI Responses 请求翻译成 Chat Completions 请求。
//
// 主要差异：
//   - Responses 用顶层 instructions 承载系统提示，Chat 要放进 messages
//   - Responses 的 input 既可以是字符串，也可以是混合了消息、函数调用、函数结果的事件数组
//   - Responses 的 tools 是扁平的 {type,name,parameters}，Chat 要包一层 function
//   - max_output_tokens 对应 max_tokens
func ResponsesRequestToOpenAIChat(body []byte) ([]byte, error) {
	var src map[string]any
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("Responses 请求体解析失败: %w", err)
	}

	out := map[string]any{}
	// previous_response_id 是 Responses 的**服务端会话延续**：客户端只发增量
	// input，历史在 OpenAI 那边。中继是无状态转发，这个引用转发过去也指向
	// 别人家的存储；而白名单不抄它，又会静默丢掉上下文 —— 模型"失忆"、
	// 答案接不上，且无任何报错，极难排查。所以明确拒绝并说明替代做法。
	if asString(src["previous_response_id"]) != "" {
		return nil, errUnsupportedContent("Responses 协议的 previous_response_id 依赖服务端会话状态，中继不支持转发；请在客户端关闭 store（用全量 input 发送完整对话）")
	}
	if v, ok := src["model"]; ok {
		out["model"] = v
	}
	if v, ok := src["stream"]; ok {
		out["stream"] = v
	}
	if v, ok := src["max_output_tokens"]; ok {
		out["max_tokens"] = v
	}
	for _, k := range []string{"temperature", "top_p", "parallel_tool_calls", "metadata", "user"} {
		if v, ok := src[k]; ok {
			out[k] = v
		}
	}
	// text.format 对应 Chat 的 response_format
	if txt := asMap(src["text"]); txt != nil {
		if f, ok := txt["format"]; ok {
			out["response_format"] = f
		}
	}
	if v, ok := src["tools"]; ok {
		out["tools"] = responsesToolsToOpenAI(v)
	}
	if v, ok := src["tool_choice"]; ok {
		out["tool_choice"] = responsesToolChoiceToOpenAI(v)
	}
	// 思考强度写进通用语顶层（reasoning.effort）：Responses 的 reasoning 对象
	// 在 Chat 里没有对应物，不记就整段丢失。取值原样保留（含 "none"）。
	if effort := responsesReasoningEffort(src); effort != "" {
		out[reasoningEffortField] = effort
	}

	var messages []any
	// instructions 在 Responses 规范里可以有两种形态：
	// 一个字符串，或者内容块数组（与 messages 的 content 同构）。
	// 只认字符串的话，数组形态会被静默丢掉 —— 系统提示承载的是人设、
	// 安全约束、输出格式要求，丢了以后表现为「模型不听话」，
	// 而不是任何一条报错。这里两种都收。
	if sys := responsesInstructionsToText(src["instructions"]); sys != "" {
		messages = append(messages, map[string]any{"role": "system", "content": sys})
	}
	messages = append(messages, responsesInputToMessages(src["input"])...)

	out["messages"] = messages
	return json.Marshal(out)
}

// responsesInstructionsToText 把 instructions 归一成纯文本。
//
// 数组形态形如 [{"type":"input_text","text":"..."}, ...]，
// 也可能混入 {"type":"text","text":"..."}（两种叫法都出现过）。
// 非文本块（图片等）在这里没有意义，跳过而不是让整段失效。
func responsesInstructionsToText(v any) string {
	switch ins := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(ins)
	case []any:
		var parts []string
		for _, raw := range ins {
			switch item := raw.(type) {
			case string:
				if s := strings.TrimSpace(item); s != "" {
					parts = append(parts, s)
				}
			case map[string]any:
				// 明确跳过非文本块（图片等），避免把 base64 塞进 system
				if t, _ := item["type"].(string); t != "" && !isTextBlockType(t) {
					continue
				}
				if s := strings.TrimSpace(asString(item["text"])); s != "" {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		// 单个内容块
		if t, _ := ins["type"].(string); t != "" && !isTextBlockType(t) {
			return ""
		}
		return strings.TrimSpace(asString(ins["text"]))
	default:
		return ""
	}
}

// isTextBlockType 判断内容块类型是不是文本。
func isTextBlockType(t string) bool {
	switch t {
	case "text", "input_text", "output_text":
		return true
	default:
		return false
	}
}

// responsesInputToMessages 把 input 展开成 Chat 的 messages。
// input 可以是纯字符串，也可以是消息 / 函数调用 / 函数结果混合的事件数组。
func responsesInputToMessages(v any) []any {
	switch input := v.(type) {
	case string:
		if input == "" {
			return nil
		}
		return []any{map[string]any{"role": "user", "content": input}}
	case []any:
		var messages []any
		for _, raw := range input {
			item := asMap(raw)
			if item == nil {
				// 数组里也可能直接是字符串
				if s, ok := raw.(string); ok && s != "" {
					messages = append(messages, map[string]any{"role": "user", "content": s})
				}
				continue
			}
			switch asString(item["type"]) {
			case "function_call":
				// Responses 的 call_id 才是 Chat 里的 tool_call id，id 字段是响应侧标识
				callID := asString(item["call_id"])
				if callID == "" {
					callID = asString(item["id"])
				}
				messages = append(messages, map[string]any{
					"role":    "assistant",
					"content": "",
					"tool_calls": []any{map[string]any{
						"id": callID, "type": "function",
						"function": map[string]any{
							"name":      asString(item["name"]),
							"arguments": asString(item["arguments"]),
						},
					}},
				})
			case "function_call_output":
				messages = append(messages, map[string]any{
					"role":         "tool",
					"tool_call_id": asString(item["call_id"]),
					"content":      asString(item["output"]),
				})
			case "reasoning":
				// 思维链无法回传给 Chat 上游，跳过而不是报错
				continue
			default:
				role := asString(item["role"])
				if role == "" {
					role = "user"
				}
				messages = append(messages, map[string]any{
					"role":    role,
					"content": responsesContentToOpenAI(item["content"]),
				})
			}
		}
		return messages
	default:
		return nil
	}
}

// responsesContentToOpenAI 把 Responses 的内容块转成 Chat 格式。
// input_text/output_text 是 Responses 特有的块类型，要还原成 Chat 的 text。
func responsesContentToOpenAI(v any) any {
	if s, ok := v.(string); ok {
		return s
	}
	blocks, ok := v.([]any)
	if !ok {
		return ""
	}
	var parts []any
	var plain []string
	for _, raw := range blocks {
		blk := asMap(raw)
		if blk == nil {
			if s, ok := raw.(string); ok {
				plain = append(plain, s)
			}
			continue
		}
		switch asString(blk["type"]) {
		case "input_text", "output_text", "text", "summary_text":
			parts = append(parts, map[string]any{"type": "text", "text": asString(blk["text"])})
		case "input_image":
			url := asString(blk["image_url"])
			if url == "" {
				url = asString(blk["file_id"])
			}
			if url != "" {
				parts = append(parts, map[string]any{
					"type": "image_url", "image_url": map[string]any{"url": url},
				})
			}
		}
	}
	// 全是纯文本时压成字符串，兼容性最好
	if len(parts) > 0 {
		allText := true
		for _, p := range parts {
			if asString(asMap(p)["type"]) != "text" {
				allText = false
				break
			}
		}
		if allText {
			for _, p := range parts {
				plain = append(plain, asString(asMap(p)["text"]))
			}
			return strings.Join(plain, "")
		}
	}
	if len(parts) > 0 {
		return parts
	}
	return strings.Join(plain, "")
}

func responsesToolsToOpenAI(v any) []any {
	list, _ := v.([]any)
	out := make([]any, 0, len(list))
	for _, raw := range list {
		t := asMap(raw)
		if t == nil {
			continue
		}
		// Responses 的 function 工具是扁平结构，直接读顶层字段
		if asString(t["type"]) != "function" && asString(t["name"]) == "" {
			continue
		}
		fn := map[string]any{"name": asString(t["name"])}
		if d := asString(t["description"]); d != "" {
			fn["description"] = d
		}
		if p, ok := t["parameters"]; ok {
			fn["parameters"] = p
		} else {
			fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

func responsesToolChoiceToOpenAI(v any) any {
	if s, ok := v.(string); ok {
		switch s {
		case "auto", "none", "required":
			return s
		default:
			return nil
		}
	}
	m := asMap(v)
	if m == nil {
		return nil
	}
	if asString(m["type"]) == "function" {
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": asString(m["name"])},
		}
	}
	return nil
}

// ============================ 响应转换 ============================

// OpenAIChatToResponsesResponse 把非流式 Chat 响应翻译成 Responses 响应。
func OpenAIChatToResponsesResponse(body []byte, fallbackModel string) ([]byte, error) {
	var src map[string]any
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("OpenAI 响应解析失败: %w", err)
	}

	model := asString(src["model"])
	if model == "" {
		model = fallbackModel
	}
	created := asInt(src["created"])
	if created == 0 {
		created = int(time.Now().Unix())
	}
	respID := responsesID(asString(src["id"]))

	output := []any{}
	if choices, ok := src["choices"].([]any); ok && len(choices) > 0 {
		choice := asMap(choices[0])
		if choice != nil {
			msg := asMap(choice["message"])
			if msg != nil {
				if r := asString(msg["reasoning"]); r != "" {
					output = append(output, map[string]any{
						"type": "reasoning", "id": "rs_" + respID,
						"summary": []any{map[string]any{"type": "summary_text", "text": r}},
					})
				}
				if t := asString(msg["content"]); t != "" {
					output = append(output, map[string]any{
						"type": "message", "id": "msg_" + respID,
						"role": "assistant", "status": "completed",
						"content": []any{map[string]any{
							"type": "output_text", "text": t, "annotations": []any{},
						}},
					})
				}
				if calls, ok := msg["tool_calls"].([]any); ok {
					for i, raw := range calls {
						call := asMap(raw)
						if call == nil {
							continue
						}
						fn := asMap(call["function"])
						item := map[string]any{
							"type":    "function_call",
							"id":      "fc_" + respID + "_" + itoa(i),
							"call_id": asString(call["id"]),
							"status":  "completed",
						}
						if fn != nil {
							item["name"] = asString(fn["name"])
							item["arguments"] = asString(fn["arguments"])
						}
						output = append(output, item)
					}
				}
			}
		}
	}

	res := map[string]any{
		"id":          respID,
		"object":      "response",
		"created_at":  created,
		"status":      "completed",
		"model":       model,
		"output":      output,
		"output_text": collectOutputText(output),
		"usage":       usageToResponses(asMap(src["usage"])),
	}
	if meta, ok := src["metadata"]; ok {
		res["metadata"] = meta
	}
	return json.Marshal(res)
}

// usageToResponses 把 Chat 的用量换算成 Responses 口径。
// 两者的 input_tokens 都包含缓存命中，语义一致，直接映射即可。
func usageToResponses(usage map[string]any) map[string]any {
	in := asInt(usage["prompt_tokens"])
	if in == 0 {
		in = asInt(usage["input_tokens"])
	}
	outTokens := asInt(usage["completion_tokens"])
	if outTokens == 0 {
		outTokens = asInt(usage["output_tokens"])
	}
	cached := 0
	if d := asMap(usage["prompt_tokens_details"]); d != nil {
		cached = asInt(d["cached_tokens"])
	}
	reasoning := 0
	if d := asMap(usage["completion_tokens_details"]); d != nil {
		reasoning = asInt(d["reasoning_tokens"])
	}
	return map[string]any{
		"input_tokens":          in,
		"output_tokens":         outTokens,
		"total_tokens":          in + outTokens,
		"input_tokens_details":  map[string]any{"cached_tokens": cached},
		"output_tokens_details": map[string]any{"reasoning_tokens": reasoning},
	}
}

func collectOutputText(output []any) string {
	var sb strings.Builder
	for _, raw := range output {
		item := asMap(raw)
		if item == nil || asString(item["type"]) != "message" {
			continue
		}
		parts, _ := item["content"].([]any)
		for _, p := range parts {
			if blk := asMap(p); blk != nil {
				sb.WriteString(asString(blk["text"]))
			}
		}
	}
	return sb.String()
}

// responsesID 统一成 resp_ 前缀。
func responsesID(id string) string {
	if id == "" {
		return "resp_relay"
	}
	if strings.HasPrefix(id, "resp_") {
		return id
	}
	return "resp_" + id
}

// ResponsesError 生成 Responses 结构的错误体。
func ResponsesError(status int, message string) []byte {
	code := "server_error"
	switch status {
	case 400:
		code = "invalid_request_error"
	case 401:
		code = "invalid_api_key"
	case 404:
		code = "not_found"
	case 429:
		code = "rate_limit_exceeded"
	}
	raw, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": message, "type": code, "code": code},
	})
	return raw
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf []byte
	for i > 0 {
		buf = append([]byte{byte('0' + i%10)}, buf...)
		i /= 10
	}
	if neg {
		return "-" + string(buf)
	}
	return string(buf)
}
