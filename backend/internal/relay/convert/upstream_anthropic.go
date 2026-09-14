package convert

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 出站（上游）方向的 Anthropic 适配：站内通用语是 OpenAI Chat（见 api/protocols.go），
// 渠道可以把上游声明成 Anthropic Messages，于是这里要能做三件事：
//   1. 请求：OpenAI Chat -> Anthropic Messages
//   2. 响应：Anthropic Messages -> OpenAI Chat
//   3. 流式：Anthropic 事件流 -> OpenAI Chat SSE 分片
//
// 响应为什么要转回来而不是直接透传给客户端：入站协议与上游协议是两件独立的事
// （客户端可能用 OpenAI / Anthropic / Gemini 任意一种），用量统计、定价快照与
// 请求日志也都建立在统一格式之上。先归一化再改写，才能保持 N 种入站 × M 种上游
// 只需要 N + M 个转换器。

// anthropicDefaultMaxTokens 是上游 Anthropic 的 max_tokens 兜底值。
//
// Anthropic 的 max_tokens 是**必填**，而 OpenAI 的客户端经常不传（OpenAI 侧它可选）。
// 不兜底的话，一个在 OpenAI 上跑得好好的客户端切到 Anthropic 渠道会直接 400
// invalid_request_error: max_tokens: Field required，而且报错来自上游、很难联想到是中转站的问题。
const anthropicDefaultMaxTokens = 4096

// OpenAIChatToAnthropicRequest 把 OpenAI Chat 请求改写成 Anthropic Messages 请求。
func OpenAIChatToAnthropicRequest(body []byte) ([]byte, error) {
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
	// Anthropic 对未知字段是直接 400，不做忽略处理，所以只能挑着抄字段：
	// stream_options / presence_penalty / frequency_penalty / n / logprobs /
	// response_format / user / seed 一律不发。
	maxTokens := asInt(src["max_tokens"])
	if maxTokens <= 0 {
		maxTokens = asInt(src["max_completion_tokens"])
	}
	if maxTokens <= 0 {
		maxTokens = anthropicDefaultMaxTokens
	}
	out["max_tokens"] = maxTokens

	for _, k := range []string{"temperature", "top_p"} {
		if v, ok := src[k]; ok {
			out[k] = v
		}
	}
	if v, ok := src["stop"]; ok {
		out["stop_sequences"] = openAIStopList(v)
	}
	if v, ok := src["tools"]; ok {
		out["tools"] = openAIToolsToAnthropic(v)
	}
	if v, ok := src["tool_choice"]; ok {
		if tc := openAIToolChoiceToAnthropic(v); tc != nil {
			out["tool_choice"] = tc
		}
	}

	system, messages, err := openAIMessagesToAnthropic(src["messages"])
	if err != nil {
		// 内容块无法转换（例如音频）时直接失败，不静默丢内容。
		// 调用方会把这个错误变成一条可读的 400，用户能立刻知道原因。
		return nil, err
	}
	// system 为空字符串或空数组时都不要写进请求体：
	// Anthropic 对 system: "" 是接受的，但对 system: [] 会报 400
	if !isEmptyContent(system) {
		out["system"] = system
	}
	if len(messages) > 0 {
		out["messages"] = messages
	}
	return json.Marshal(out)
}

// isEmptyContent 判断 system 之类的字段是否为空（空串或空数组）。
func isEmptyContent(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	default:
		return false
	}
}

// openAIStopList 把 stop（字符串或数组）统一成字符串数组。
// Anthropic 用它填 stop_sequences，Gemini 用它填 generationConfig.stopSequences，
// 所以名字里不带具体协议。
func openAIStopList(v any) []string {
	switch s := v.(type) {
	case string:
		if s == "" {
			return nil
		}
		return []string{s}
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if str := asString(item); str != "" {
				out = append(out, str)
			}
		}
		return out
	default:
		return nil
	}
}

// openAIToolsToAnthropic 把 tools 从 {type:function, function:{...}} 拆平成 Anthropic 的
// {name, description, input_schema}。
func openAIToolsToAnthropic(v any) []any {
	list, _ := v.([]any)
	out := make([]any, 0, len(list))
	for _, item := range list {
		t := asMap(item)
		if t == nil {
			continue
		}
		fn := asMap(t["function"])
		if fn == nil {
			// 已经是 Anthropic 形状（入站就是 Anthropic 时会把工具转成 OpenAI 再转回来，
			// 这里兜住非 function 形状的条目，避免整条请求被丢掉）
			if name := asString(t["name"]); name != "" {
				block := map[string]any{"name": name}
				if d := asString(t["description"]); d != "" {
					block["description"] = d
				}
				if s, ok := t["input_schema"]; ok {
					block["input_schema"] = s
				} else {
					block["input_schema"] = map[string]any{"type": "object", "properties": map[string]any{}}
				}
				out = append(out, block)
			}
			continue
		}
		block := map[string]any{"name": asString(fn["name"])}
		if d := asString(fn["description"]); d != "" {
			block["description"] = d
		}
		if s, ok := fn["parameters"]; ok && s != nil {
			block["input_schema"] = s
		} else {
			// input_schema 必填：没有参数的函数也得给一个空对象结构
			block["input_schema"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, block)
	}
	return out
}

// openAIToolChoiceToAnthropic 映射 tool_choice。Anthropic 的取值只有
// auto / any / tool / none 四种。
func openAIToolChoiceToAnthropic(v any) any {
	if s, ok := v.(string); ok {
		switch s {
		case "auto":
			return map[string]any{"type": "auto"}
		case "required":
			return map[string]any{"type": "any"}
		case "none":
			return map[string]any{"type": "none"}
		default:
			return nil
		}
	}
	m := asMap(v)
	if m == nil {
		return nil
	}
	if fn := asMap(m["function"]); fn != nil {
		if name := asString(fn["name"]); name != "" {
			return map[string]any{"type": "tool", "name": name}
		}
	}
	if name := asString(m["name"]); name != "" && asString(m["type"]) == "tool" {
		return map[string]any{"type": "tool", "name": name}
	}
	return nil
}

// openAIMessagesToAnthropic 拆出顶层 system 与 messages。
//
// 两处结构性差异必须处理，否则上游会直接报 400：
//   - system 在 Anthropic 是顶层字段，OpenAI 把它放在 messages 里；
//   - 工具结果在 OpenAI 是独立的 role=tool 消息，在 Anthropic 必须作为
//     user 消息里的 tool_result 内容块，且必须紧跟在发起调用的 assistant 之后。
//
// 另外 Anthropic 要求 user / assistant 严格交替，所以连续的同角色消息要合并
// （OpenAI 允许连着两条 user，Anthropic 不允许）。
func openAIMessagesToAnthropic(v any) (any, []any, error) {
	list, _ := v.([]any)
	// system 可能是纯文本（最常见），也可能是带 cache_control 的块数组
	var systemParts []any
	var systemTexts []string
	systemHasBreakpoint := false
	messages := make([]any, 0, len(list))

	appendMessage := func(role string, blocks []any) {
		if len(blocks) == 0 {
			return
		}
		if n := len(messages); n > 0 {
			prev := asMap(messages[n-1])
			if prev != nil && asString(prev["role"]) == role {
				// 合并到上一条，保持严格交替
				prevBlocks, _ := prev["content"].([]any)
				prev["content"] = append(prevBlocks, blocks...)
				return
			}
		}
		messages = append(messages, map[string]any{"role": role, "content": blocks})
	}

	for _, item := range list {
		m := asMap(item)
		if m == nil {
			continue
		}
		role := asString(m["role"])
		switch role {
		case "system", "developer":
			// system 带断点时保留块结构，否则拍平成文本（与原来一致的最简形态）
			if blocks, ok := m["content"].([]any); ok && contentHasCacheControl(blocks) {
				for _, item := range blocks {
					blk := asMap(item)
					if blk == nil {
						continue
					}
					if t := asString(blk["text"]); t != "" {
						part := map[string]any{"type": "text", "text": t}
						if cc := takeCacheControl(blk); cc != nil {
							part["cache_control"] = cc
							systemHasBreakpoint = true
						}
						systemParts = append(systemParts, part)
					}
				}
			} else if t := flattenTextContent(m["content"]); t != "" {
				systemTexts = append(systemTexts, t)
			}
			continue
		case "tool", "function":
			blk := map[string]any{
				"type":        "tool_result",
				"tool_use_id": asString(m["tool_call_id"]),
				"content":     flattenTextContent(m["content"]),
			}
			// tool 消息上的断点也要带上：多轮工具调用的场景里，
			// 断点通常就落在最后一条 tool_result 上
			if cc := takeCacheControl(m); cc != nil {
				blk["cache_control"] = cc
			}
			appendMessage("user", []any{blk})
			continue
		}

		blocks, err := openAIContentToAnthropic(m["content"])
		if err != nil {
			return nil, nil, err
		}
		if calls, ok := m["tool_calls"].([]any); ok {
			for _, c := range calls {
				call := asMap(c)
				if call == nil {
					continue
				}
				fn := asMap(call["function"])
				block := map[string]any{
					"type":  "tool_use",
					"id":    asString(call["id"]),
					"input": parseToolArguments(asString(fnArgs(fn))),
				}
				if fn != nil {
					block["name"] = asString(fn["name"])
				}
				if cc := takeCacheControl(call); cc != nil {
					block["cache_control"] = cc
				}
				blocks = append(blocks, block)
			}
		}
		if role == "assistant" {
			appendMessage("assistant", blocks)
		} else {
			appendMessage("user", blocks)
		}
	}
	// 有断点时返回块数组，否则返回原来的纯文本
	if systemHasBreakpoint {
		return systemParts, messages, nil
	}
	if len(systemParts) > 0 {
		for _, p := range systemParts {
			systemTexts = append(systemTexts, asString(asMap(p)["text"]))
		}
	}
	return strings.Join(systemTexts, "\n\n"), messages, nil
}

// contentHasCacheControl 判断内容块数组里是否带了缓存断点。
func contentHasCacheControl(blocks []any) bool {
	for _, item := range blocks {
		if takeCacheControl(asMap(item)) != nil {
			return true
		}
	}
	return false
}

// parseToolArguments 解析工具参数。Anthropic 的 input 必须是对象，
// 解析失败时退回空对象（参数是模型生成的，不保证是合法 JSON）。
func parseToolArguments(args string) any {
	args = strings.TrimSpace(args)
	if args == "" {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(args), &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// openAIContentToAnthropic 转换消息内容：字符串与多模态数组都要支持。
func openAIContentToAnthropic(v any) ([]any, error) {
	switch c := v.(type) {
	case string:
		if c == "" {
			return nil, nil
		}
		return []any{map[string]any{"type": "text", "text": c}}, nil
	case []any:
		out := make([]any, 0, len(c))
		for _, item := range c {
			part := asMap(item)
			if part == nil {
				continue
			}
			switch asString(part["type"]) {
			case "text":
				blk := map[string]any{"type": "text", "text": asString(part["text"])}
				// 恢复入站时暂存的提示缓存断点。丢掉它就等于关掉了 Anthropic 的
				// 提示缓存 —— 多轮对话里每一轮都会按全量输入计费。
				if cc := takeCacheControl(part); cc != nil {
					blk["cache_control"] = cc
				}
				out = append(out, blk)
			case "image_url":
				if blk := openAIImageToAnthropic(asMap(part["image_url"])); blk != nil {
					if cc := takeCacheControl(part); cc != nil {
						blk["cache_control"] = cc
					}
					out = append(out, blk)
				}
			case "input_audio":
				// 显式拒绝而不是静默丢弃。
				//
				// Anthropic 的 Messages API 不接受音频块。原来这里没有 default
				// 分支，音频会被无声吃掉 —— 用户看到的是「模型忽略了我的语音」，
				// 而不是「这个协议不支持音频」，会花很多时间去查提示词。
				// 换成一条可读的错误，把原因说清楚。
				return nil, errUnsupportedContent("Anthropic 协议不支持音频输入（input_audio），请改用文本或图片")
			case "file":
				return nil, errUnsupportedContent("Anthropic 协议不支持文件输入（file），请改用文本或图片")
			default:
				// 其它未知类型同样拒绝：宁可报错也不要静默丢内容。
				// 类型名带上，便于判断是哪个客户端发了什么。
				if t := asString(part["type"]); t != "" {
					return nil, errUnsupportedContent("内容块类型 " + t + " 无法转换为 Anthropic 协议")
				}
			}
		}
		return out, nil
	default:
		return nil, nil
	}
}

// openAIImageToAnthropic 把 image_url（data URI 或普通 URL）转成 Anthropic 的图片块。
func openAIImageToAnthropic(img map[string]any) map[string]any {
	if img == nil {
		return nil
	}
	url := asString(img["url"])
	if url == "" {
		return nil
	}
	// data:image/png;base64,xxxx
	if strings.HasPrefix(url, "data:") {
		rest := strings.TrimPrefix(url, "data:")
		semi := strings.Index(rest, ";base64,")
		if semi < 0 {
			return nil
		}
		return map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": rest[:semi],
				"data":       rest[semi+len(";base64,"):],
			},
		}
	}
	return map[string]any{
		"type":   "image",
		"source": map[string]any{"type": "url", "url": url},
	}
}

// flattenTextContent 把字符串或内容块数组拍平成纯文本（system 与工具结果用）。
func flattenTextContent(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, item := range c {
			blk := asMap(item)
			if blk == nil {
				continue
			}
			if t := asString(blk["text"]); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

// anthropicStopReasonToOpenAI 是 mapFinishReason 的逆映射。
func anthropicStopReasonToOpenAI(r string) string {
	switch r {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	case "stop_sequence", "end_turn", "pause_turn", "":
		return "stop"
	default:
		return "stop"
	}
}

// AnthropicResponseToOpenAIChat 把非流式 Anthropic 响应翻译成 OpenAI Chat 响应。
func AnthropicResponseToOpenAIChat(body []byte, fallbackModel string) ([]byte, error) {
	var src map[string]any
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("Anthropic 响应解析失败: %w", err)
	}
	model := asString(src["model"])
	if model == "" {
		model = fallbackModel
	}

	message := map[string]any{"role": "assistant"}
	var texts []string
	var thinking []string
	var toolCalls []any
	blocks, _ := src["content"].([]any)
	for _, item := range blocks {
		blk := asMap(item)
		if blk == nil {
			continue
		}
		switch asString(blk["type"]) {
		case "text":
			texts = append(texts, asString(blk["text"]))
		case "thinking":
			thinking = append(thinking, asString(blk["thinking"]))
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
		}
	}
	message["content"] = strings.Join(texts, "")
	if len(thinking) > 0 {
		// 站内统一用 reasoning 承载思维链，与入站方向（OpenAIChatToAnthropicResponse）对称
		message["reasoning"] = strings.Join(thinking, "")
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}

	out := map[string]any{
		"id":      openAIID(asString(src["id"])),
		"object":  "chat.completion",
		"created": anthropicCreated(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": anthropicStopReasonToOpenAI(asString(src["stop_reason"])),
		}},
	}
	if usage := anthropicUsageToOpenAI(asMap(src["usage"])); usage != nil {
		out["usage"] = usage
	}
	return json.Marshal(out)
}

// anthropicUsageToOpenAI 把 Anthropic 口径的用量换算成 OpenAI 口径。
//
// 这里的算术比看上去绕，写清楚免得日后被「顺手简化」弄错：
//   - Anthropic 是**并列**口径：input_tokens 不含缓存命中，cache_read 与
//     cache_creation 各自独立上报；
//   - OpenAI 是**子集**口径：prompt_tokens 含缓存命中，命中数另放
//     prompt_tokens_details.cached_tokens。
//
// 站内的 NormalizeUsage 见到子集字段会把 cached 从 prompt 里减掉，换算成并列语义
// （见 relay/usage.go）。所以这里必须让减法正好落回 input_tokens：
// prompt_tokens = input + cached，减掉 cached 后就是 input。
//
// cache_creation 刻意**不计入** prompt_tokens，而是原样保留
// cache_creation_input_tokens：它既不是「输入正文」也不是「缓存命中」，
// 计进 prompt 的话归一化后会同时出现在输入和缓存写入两处，费用翻倍。
// total_tokens 按四者之和给出，与归一化后的口径一致。
func anthropicUsageToOpenAI(usage map[string]any) map[string]any {
	if usage == nil {
		return nil
	}
	in := asInt(usage["input_tokens"])
	cached := asInt(usage["cache_read_input_tokens"])
	created := asInt(usage["cache_creation_input_tokens"])
	outTokens := asInt(usage["output_tokens"])
	prompt := in + cached
	out := map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": outTokens,
		"total_tokens":      prompt + outTokens + created,
	}
	if cached > 0 {
		out["prompt_tokens_details"] = map[string]any{"cached_tokens": cached}
	}
	if created > 0 {
		out["cache_creation_input_tokens"] = created
	}
	return out
}

// openAIID 把 Anthropic 的 msg_xxx 形式的 id 转成 OpenAI 的 chatcmpl_xxx。
func openAIID(id string) string {
	if id == "" {
		return newCompletionID()
	}
	if strings.HasPrefix(id, "msg_") {
		return "chatcmpl-" + strings.TrimPrefix(id, "msg_")
	}
	return "chatcmpl-" + id
}

// AnthropicErrorMessage 从 Anthropic 的错误体里取出可读信息。
// 直接把整个 JSON 当 message 回给客户端的话，一层套一层很难看懂。
func AnthropicErrorMessage(body []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return string(body)
	}
	if e := asMap(payload["error"]); e != nil {
		if msg := asString(e["message"]); msg != "" {
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
