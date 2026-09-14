package convert

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 出站（上游）方向的 OpenAI Responses 适配：站内通用语是 OpenAI Chat
// （见 api/protocols.go），渠道可以把上游声明成 Responses，于是这里要能做三件事：
//  1. 请求：Chat Completions -> Responses
//  2. 响应：Responses -> Chat Completions
//  3. 流式：Responses 事件流 -> Chat Completions SSE 分片
//
// 与入站方向的 responses.go 是**反向**的两件事，不要互相替代：
// 那边是把客户端发来的 Responses 请求译成 Chat 再发给上游，
// 这边是把站内的 Chat 译成 Responses 发给上游。
//
// 报文依据智谱 GLM Coding Plan 的 /api/v1/responses 实测（2026-09），关键差异：
//   - 系统提示在顶层 instructions，不在 messages 里
//   - input 是字符串或事件数组（message / function_call / function_call_output），
//     而 Chat 的 tool 结果是一条独立的 role=tool 消息
//   - 工具是扁平的 {type,name,parameters}，不像 Chat 要包一层 function
//   - 思维链在输出项 content[].type="reasoning_text"，不是入站方向那种 summary
//   - 没有 stop 字段，也不接受 stream_options

// 注意：max_output_tokens 刻意不做兜底。Responses 允许省略它（由服务端决定），
// 而站内的入站协议几乎总会给一个上限；只有来源确实没给时才不写，
// 避免擅自放大或缩小用户设定的预算（Anthropic 那边必须兜底是因为它必填）。

// OpenAIChatToResponsesRequest 把 OpenAI Chat 请求改写成 Responses 请求。
func OpenAIChatToResponsesRequest(body []byte) ([]byte, error) {
	var src map[string]any
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("OpenAI 请求体解析失败: %w", err)
	}

	out := map[string]any{}
	if v, ok := src["model"]; ok {
		out["model"] = v
	}
	if v, ok := src["stream"]; ok {
		out["stream"] = v
	}
	// max_tokens（老字段）与 max_completion_tokens（新字段）都映射到 max_output_tokens
	if v := asInt(src["max_tokens"]); v > 0 {
		out["max_output_tokens"] = v
	} else if v := asInt(src["max_completion_tokens"]); v > 0 {
		out["max_output_tokens"] = v
	}
	for _, k := range []string{"temperature", "top_p", "metadata", "user", "parallel_tool_calls"} {
		if v, ok := src[k]; ok {
			out[k] = v
		}
	}
	// Chat 的 response_format 在 Responses 里叫 text.format
	if rf, ok := src["response_format"]; ok {
		out["text"] = map[string]any{"format": rf}
	}
	if v, ok := src["tools"]; ok {
		if tools := openAIToolsToResponses(v); len(tools) > 0 {
			out["tools"] = tools
		}
	}
	if v, ok := src["tool_choice"]; ok {
		if tc := openAIToolChoiceToResponses(v); tc != nil {
			out["tool_choice"] = tc
		}
	}

	instructions, input := openAIMessagesToResponses(src["messages"])
	if instructions != "" {
		out["instructions"] = instructions
	}
	if len(input) > 0 {
		out["input"] = input
	} else {
		// input 不能为空，否则上游 400
		out["input"] = []any{}
	}
	return json.Marshal(out)
}

// openAIToolsToResponses 把 Chat 的工具定义拍平成 Responses 的扁平结构。
func openAIToolsToResponses(v any) []any {
	list, _ := v.([]any)
	out := make([]any, 0, len(list))
	for _, raw := range list {
		t := asMap(raw)
		if t == nil {
			continue
		}
		fn := asMap(t["function"])
		if fn == nil {
			// 已经是扁平形状（入站就是 Responses 时会被转成 Chat 再转回来）
			if name := asString(t["name"]); name != "" {
				item := map[string]any{"type": "function", "name": name}
				if d := asString(t["description"]); d != "" {
					item["description"] = d
				}
				if p, ok := t["parameters"]; ok && p != nil {
					item["parameters"] = p
				} else {
					item["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
				}
				out = append(out, item)
			}
			continue
		}
		item := map[string]any{"type": "function", "name": asString(fn["name"])}
		if d := asString(fn["description"]); d != "" {
			item["description"] = d
		}
		if p, ok := fn["parameters"]; ok && p != nil {
			item["parameters"] = p
		} else {
			item["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, item)
	}
	return out
}

// openAIToolChoiceToResponses 映射 tool_choice。
func openAIToolChoiceToResponses(v any) any {
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
	// Chat 的 {"type":"function","function":{"name":...}} -> Responses 的 {"type":"function","name":...}
	if fn := asMap(m["function"]); fn != nil {
		if name := asString(fn["name"]); name != "" {
			return map[string]any{"type": "function", "name": name}
		}
	}
	if name := asString(m["name"]); name != "" {
		return map[string]any{"type": "function", "name": name}
	}
	return nil
}

// openAIMessagesToResponses 拆分 system 提示与 input 事件数组。
//
// 三处结构性差异：
//   - system / developer 消息在 Responses 里是顶层 instructions，不能留在 input 里
//   - role=tool 的消息要变成 function_call_output 事件，且 call_id 必须与
//     之前 function_call 的 call_id 一致
//   - assistant 的 tool_calls 要拆成独立的 function_call 事件
func openAIMessagesToResponses(v any) (string, []any) {
	list, _ := v.([]any)
	var systemParts []string
	input := make([]any, 0, len(list))

	for _, raw := range list {
		m := asMap(raw)
		if m == nil {
			continue
		}
		switch asString(m["role"]) {
		case "system", "developer":
			if t := flattenTextContent(m["content"]); t != "" {
				systemParts = append(systemParts, t)
			}
			continue
		case "tool", "function":
			// 工具结果必须作为 function_call_output 事件回传
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": asString(m["tool_call_id"]),
				"output":  flattenTextContent(m["content"]),
			})
			continue
		}

		role := asString(m["role"])
		if role == "" {
			role = "user"
		}
		// 正文可能为空（纯工具调用的 assistant 消息），此时不产生 message 事件
		content := openAIContentToResponses(m["content"])
		hasContent := false
		if s, ok := content.(string); ok {
			hasContent = strings.TrimSpace(s) != ""
		} else if arr, ok := content.([]any); ok {
			hasContent = len(arr) > 0
		}
		if hasContent {
			input = append(input, map[string]any{
				"type":    "message",
				"role":    role,
				"content": content,
			})
		}
		// assistant 发起的工具调用要拆成 function_call 事件，紧跟在该消息之后
		if calls, ok := m["tool_calls"].([]any); ok {
			for _, c := range calls {
				call := asMap(c)
				if call == nil {
					continue
				}
				fn := asMap(call["function"])
				item := map[string]any{
					"type":    "function_call",
					"call_id": asString(call["id"]),
					"name":    asString(fnName(fn)),
				}
				// call_id 为空时上游会拒，退回用 id 兜底
				if asString(item["call_id"]) == "" {
					item["call_id"] = asString(call["id"])
				}
				args := asString(fnArgs(fn))
				if args == "" {
					args = "{}"
				}
				item["arguments"] = args
				input = append(input, item)
			}
		}
	}
	return strings.Join(systemParts, "\n\n"), input
}

// openAIContentToResponses 把 Chat 的内容转成 Responses 的内容块。
// 纯文本用字符串（上游两种都收，字符串最省事），含图片时用块数组。
func openAIContentToResponses(v any) any {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []any
		allText := true
		for _, item := range c {
			part := asMap(item)
			if part == nil {
				continue
			}
			switch asString(part["type"]) {
			case "text":
				parts = append(parts, map[string]any{"type": "input_text", "text": asString(part["text"])})
			case "image_url":
				url := asString(asMap(part["image_url"])["url"])
				if url != "" {
					parts = append(parts, map[string]any{"type": "input_image", "image_url": url})
					allText = false
				}
			}
		}
		if allText && len(parts) > 0 {
			var sb strings.Builder
			for _, p := range parts {
				sb.WriteString(asString(asMap(p)["text"]))
			}
			return sb.String()
		}
		return parts
	default:
		return ""
	}
}

// ============================ 响应转换 ============================

// ResponsesResponseToOpenAIChat 把非流式 Responses 响应翻译成 Chat 响应。
func ResponsesResponseToOpenAIChat(body []byte, fallbackModel string) ([]byte, error) {
	var src map[string]any
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("Responses 响应解析失败: %w", err)
	}

	model := asString(src["model"])
	if model == "" {
		model = fallbackModel
	}
	created := asInt(src["created_at"])
	if created == 0 {
		created = int(anthropicCreated())
	}

	message := map[string]any{"role": "assistant"}
	var texts []string
	var thinking []string
	var toolCalls []any

	for _, raw := range asAnyList(src["output"]) {
		item := asMap(raw)
		if item == nil {
			continue
		}
		switch asString(item["type"]) {
		case "message":
			for _, p := range asAnyList(item["content"]) {
				blk := asMap(p)
				if blk == nil {
					continue
				}
				switch asString(blk["type"]) {
				case "output_text", "text":
					texts = append(texts, asString(blk["text"]))
				case "refusal":
					if r := asString(blk["refusal"]); r != "" {
						texts = append(texts, r)
					}
				}
			}
		case "reasoning":
			// 智谱把思维链放在 content[].type=reasoning_text；
			// 官方 OpenAI 用 summary[].type=summary_text。两种都要收。
			for _, p := range asAnyList(item["content"]) {
				if blk := asMap(p); blk != nil {
					if t := asString(blk["text"]); t != "" {
						thinking = append(thinking, t)
					}
				}
			}
			for _, p := range asAnyList(item["summary"]) {
				if blk := asMap(p); blk != nil {
					if t := asString(blk["text"]); t != "" {
						thinking = append(thinking, t)
					}
				}
			}
		case "function_call":
			args := asString(item["arguments"])
			if args == "" {
				args = "{}"
			}
			callID := asString(item["call_id"])
			if callID == "" {
				callID = asString(item["id"])
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   callID,
				"type": "function",
				"function": map[string]any{
					"name":      asString(item["name"]),
					"arguments": args,
				},
			})
		}
	}

	message["content"] = strings.Join(texts, "")
	if len(thinking) > 0 {
		message["reasoning"] = strings.Join(thinking, "")
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	// 只调工具、没有正文时 content 必须是空字符串而不是 null
	if _, ok := message["content"]; !ok {
		message["content"] = ""
	}

	out := map[string]any{
		"id":      responsesToOpenAIID(asString(src["id"])),
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": responsesStatusToFinishReason(src, len(toolCalls) > 0),
		}},
	}
	if u := asMap(src["usage"]); u != nil {
		out["usage"] = responsesUsageToOpenAI(u)
	}
	return json.Marshal(out)
}

// responsesStatusToFinishReason 把 Responses 的 status / incomplete_details
// 映射成 Chat 的 finish_reason。
func responsesStatusToFinishReason(src map[string]any, hasTools bool) string {
	if d := asMap(src["incomplete_details"]); d != nil {
		if asString(d["reason"]) == "max_output_tokens" {
			return "length"
		}
	}
	// 输出被截断时上游也可能只给 status=incomplete
	if asString(src["status"]) == "incomplete" {
		return "length"
	}
	if hasTools {
		return "tool_calls"
	}
	return "stop"
}

// responsesUsageToOpenAI 把 Responses 口径的用量换算成 Chat 口径。
//
// input_tokens 与 Chat 的 prompt_tokens 都是「包含缓存命中」的子集口径，
// 因此直接映射；缓存命中数放到 prompt_tokens_details.cached_tokens，
// 由站内的 NormalizeUsage 统一换算成并列语义。
func responsesUsageToOpenAI(usage map[string]any) map[string]any {
	if usage == nil {
		return nil
	}
	in := asInt(usage["input_tokens"])
	outTokens := asInt(usage["output_tokens"])
	total := asInt(usage["total_tokens"])
	if total == 0 {
		total = in + outTokens
	}
	res := map[string]any{
		"prompt_tokens":     in,
		"completion_tokens": outTokens,
		"total_tokens":      total,
	}
	if d := asMap(usage["input_tokens_details"]); d != nil {
		if c := asInt(d["cached_tokens"]); c > 0 {
			res["prompt_tokens_details"] = map[string]any{"cached_tokens": c}
		}
	}
	if d := asMap(usage["output_tokens_details"]); d != nil {
		if r := asInt(d["reasoning_tokens"]); r > 0 {
			res["completion_tokens_details"] = map[string]any{"reasoning_tokens": r}
		}
	}
	return res
}

// responsesToOpenAIID 把 resp_xxx 转成 Chat 的 chatcmpl-xxx。
func responsesToOpenAIID(id string) string {
	if id == "" {
		return newCompletionID()
	}
	return "chatcmpl-" + strings.TrimPrefix(id, "resp_")
}

// ResponsesUpstreamErrorMessage 从 Responses 的错误体里取出可读信息。
func ResponsesUpstreamErrorMessage(body []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return string(body)
	}
	if e := asMap(payload["error"]); e != nil {
		msg := asString(e["message"])
		if msg != "" {
			if t := asString(e["type"]); t != "" {
				return t + ": " + msg
			}
			return msg
		}
	}
	if msg := asString(payload["message"]); msg != "" {
		return msg
	}
	return string(body)
}

// asAnyList 安全地把任意值转成数组。
func asAnyList(v any) []any {
	list, _ := v.([]any)
	return list
}
