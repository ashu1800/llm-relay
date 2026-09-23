package convert

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// 出站（上游）方向的 Gemini 适配：OpenAI Chat <-> Gemini generateContent。
//
// 与 Anthropic 那条路最大的不同是**模型名在 URL 路径里**（/v1beta/models/{model}:generateContent），
// 请求体里没有 model 字段，所以转换函数要知道上游模型名；流式也靠方法名区分
// （:streamGenerateContent?alt=sse），而不是请求体里的 stream。

// GeminiUpstreamPath 拼出 Gemini 的上游路径。
// alt=sse 不能省：不加它 Gemini 返回的是一个 JSON 数组的分块流（chunked），
// 不是 SSE，站内的流式处理会读不出事件。
func GeminiUpstreamPath(model string, stream bool) string {
	escaped := url.PathEscape(strings.TrimSpace(model))
	if stream {
		return "/v1beta/models/" + escaped + ":streamGenerateContent?alt=sse"
	}
	return "/v1beta/models/" + escaped + ":generateContent"
}

// OpenAIChatToGeminiRequest 把 OpenAI Chat 请求改写成 Gemini generateContent 请求。
func OpenAIChatToGeminiRequest(body []byte) ([]byte, error) {
	var src map[string]any
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("OpenAI 请求体解析失败: %w", err)
	}

	out := map[string]any{}
	contents, system, err := openAIMessagesToGemini(src["messages"])
	if err != nil {
		return nil, err
	}
	if system != "" {
		out["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": system}}}
	}
	if len(contents) > 0 {
		out["contents"] = contents
	}

	gc := map[string]any{}
	if v := asInt(src["max_tokens"]); v > 0 {
		gc["maxOutputTokens"] = v
	} else if v := asInt(src["max_completion_tokens"]); v > 0 {
		gc["maxOutputTokens"] = v
	}
	for _, pair := range [][2]string{
		{"temperature", "temperature"}, {"top_p", "topP"},
	} {
		if v, ok := src[pair[0]]; ok {
			gc[pair[1]] = v
		}
	}
	if v, ok := src["stop"]; ok {
		if seq := openAIStopList(v); len(seq) > 0 {
			gc["stopSequences"] = seq
		}
	}
	// 思考强度 → generationConfig.thinkingConfig（数值预算，刻度见 thinking_map.go）。
	// 只在通用语里真有这个字段时才写：没有就完全交给上游默认行为，
	// 不凭空注入一个预算值去改用户的调用。
	if tc := geminiThinkingConfig(asString(src[reasoningEffortField])); tc != nil {
		gc["thinkingConfig"] = tc
	}
	// n / presence_penalty / frequency_penalty / logprobs / stream_options 等
	// Gemini 不认识的字段一律不发：它和 Anthropic 一样对多余字段是直接 400
	if len(gc) > 0 {
		out["generationConfig"] = gc
	}

	if v, ok := src["tools"]; ok {
		if decls := openAIToolsToGemini(v); len(decls) > 0 {
			out["tools"] = []any{map[string]any{"functionDeclarations": decls}}
		}
	}
	if v, ok := src["tool_choice"]; ok {
		if tc := openAIToolChoiceToGemini(v); tc != nil {
			out["toolConfig"] = map[string]any{"functionCallingConfig": tc}
		}
	}
	return json.Marshal(out)
}

// openAIMessagesToGemini 拆出 systemInstruction 与 contents。
//
// 结构性差异：
//   - assistant 在 Gemini 里叫 model；
//   - 工具调用是 model 轮次里的 functionCall 部件，工具结果是 user 轮次里的
//     functionResponse 部件，而 Gemini 的 functionResponse **只认函数名**、
//     没有调用 id 的概念 —— 所以要从发起调用的那一轮里记住 id -> name 的对应关系；
//   - 与 Anthropic 一样要求 user / model 严格交替，连续同角色必须合并：
//     OpenAI 侧「assistant 先输出 tool_calls，再补一条文本」、
//     「user 连发两条」都很常见，直接发过去会被上游 400 拒绝。
func openAIMessagesToGemini(v any) ([]any, string, error) {
	list, _ := v.([]any)
	var systemParts []string
	contents := make([]any, 0, len(list))
	callNames := map[string]string{}

	appendTurn := func(role string, parts []any) {
		if len(parts) == 0 {
			return
		}
		// 与上一条同角色就并进去，保持交替
		if n := len(contents); n > 0 {
			prev := asMap(contents[n-1])
			if prev != nil && asString(prev["role"]) == role {
				prevParts, _ := prev["parts"].([]any)
				prev["parts"] = append(prevParts, parts...)
				return
			}
		}
		contents = append(contents, map[string]any{"role": role, "parts": parts})
	}

	for _, item := range list {
		m := asMap(item)
		if m == nil {
			continue
		}
		role := asString(m["role"])
		switch role {
		case "system", "developer":
			if t := flattenTextContent(m["content"]); t != "" {
				systemParts = append(systemParts, t)
			}
			continue
		case "tool", "function":
			name := callNames[asString(m["tool_call_id"])]
			if name == "" {
				// 找不到对应关系时退回 id：Gemini 要求 name 必填，
				// 给一个明显的占位比整条请求被拒要好排查
				name = asString(m["tool_call_id"])
			}
			appendTurn("user", []any{map[string]any{
				"functionResponse": map[string]any{
					"name":     name,
					"response": parseToolResult(m["content"]),
				},
			}})
			continue
		}

		parts, err := openAIContentToGeminiParts(m["content"])
		if err != nil {
			return nil, "", err
		}
		if calls, ok := m["tool_calls"].([]any); ok {
			for _, c := range calls {
				call := asMap(c)
				if call == nil {
					continue
				}
				fn := asMap(call["function"])
				name := asString(fn["name"])
				callNames[asString(call["id"])] = name
				parts = append(parts, map[string]any{
					"functionCall": map[string]any{
						"name": name,
						"args": parseToolArguments(asString(fnArgs(fn))),
					},
				})
			}
		}
		grole := "user"
		if role == "assistant" {
			grole = "model"
		}
		appendTurn(grole, parts)
	}
	return contents, strings.Join(systemParts, "\n\n"), nil
}

// parseToolResult 把工具结果的文本包成 Gemini 要求的对象。
// Gemini 的 response 必须是对象（有的实现还要求里面的键是 output/content），
// 纯文本结果统一放进 content 键。
func parseToolResult(v any) map[string]any {
	text := flattenTextContent(v)
	if text == "" {
		return map[string]any{"content": ""}
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(text), &obj); err == nil && obj != nil {
		return obj
	}
	return map[string]any{"content": text}
}

// openAIContentToGeminiParts 转换消息内容为 Gemini 的 parts。
//
// 不认识的内容块宁可显式拒绝也不静默丢弃 —— 用户发了音频/文件、
// 模型"无视"后按剩余文本作答，用户会以为是提示词问题，正是
// Anthropic 出站注释里描述过的那类排查黑洞。
func openAIContentToGeminiParts(v any) ([]any, error) {
	switch c := v.(type) {
	case string:
		if c == "" {
			return nil, nil
		}
		return []any{map[string]any{"text": c}}, nil
	case []any:
		out := make([]any, 0, len(c))
		for _, item := range c {
			part := asMap(item)
			if part == nil {
				continue
			}
			switch asString(part["type"]) {
			case "text":
				out = append(out, map[string]any{"text": asString(part["text"])})
			case "image_url":
				if blk := openAIImageToGemini(asMap(part["image_url"])); blk != nil {
					out = append(out, blk)
				}
			case "input_audio":
				// Gemini 原生支持音频 inlineData，通用语的 input_audio 可以转过去
				if blk := openAIAudioToGemini(asMap(part["input_audio"])); blk != nil {
					out = append(out, blk)
				}
			default:
				return nil, errUnsupportedContent("内容块类型 " + asString(part["type"]) + " 无法转换为 Gemini 协议")
			}
		}
		return out, nil
	default:
		return nil, nil
	}
}

// openAIAudioToGemini 把通用语的 input_audio 转成 Gemini 的 inlineData。
func openAIAudioToGemini(a map[string]any) map[string]any {
	if a == nil {
		return nil
	}
	data := asString(a["data"])
	if data == "" {
		return nil
	}
	// 通用语的 format 只有 wav/mp3 两档，映射回标准 MIME
	mime := "audio/wav"
	if asString(a["format"]) == "mp3" {
		mime = "audio/mpeg"
	}
	return map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": data}}
}

// openAIImageToGemini 把 image_url 转成 Gemini 的 inlineData（data URI）或 fileData（URL）。
func openAIImageToGemini(img map[string]any) map[string]any {
	if img == nil {
		return nil
	}
	raw := asString(img["url"])
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "data:") {
		rest := strings.TrimPrefix(raw, "data:")
		semi := strings.Index(rest, ";base64,")
		if semi < 0 {
			return nil
		}
		return map[string]any{"inlineData": map[string]any{
			"mimeType": rest[:semi],
			"data":     rest[semi+len(";base64,"):],
		}}
	}
	return map[string]any{"fileData": map[string]any{"fileUri": raw}}
}

// openAIToolsToGemini 把 tools 转成 functionDeclarations。
func openAIToolsToGemini(v any) []any {
	list, _ := v.([]any)
	out := make([]any, 0, len(list))
	for _, item := range list {
		t := asMap(item)
		if t == nil {
			continue
		}
		fn := asMap(t["function"])
		if fn == nil {
			// 已经是 Gemini 形状（functionDeclarations）时原样带走
			if name := asString(t["name"]); name != "" {
				out = append(out, geminiDeclaration(name, asString(t["description"]), t["parameters"]))
			}
			continue
		}
		if name := asString(fn["name"]); name != "" {
			out = append(out, geminiDeclaration(name, asString(fn["description"]), fn["parameters"]))
		}
	}
	return out
}

func geminiDeclaration(name, description string, params any) map[string]any {
	decl := map[string]any{"name": name}
	if description != "" {
		decl["description"] = description
	}
	schema := sanitizeGeminiSchema(params)
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	decl["parameters"] = schema
	return decl
}

// sanitizeGeminiSchema 只保留 Gemini 认识的 JSON Schema 字段。
//
// OpenAI 的函数定义里常见 additionalProperties / $schema / strict 等，
// Gemini 对不认识的关键字会直接 400，而这类字段是 SDK 自动加的、
// 用户根本不知道它的存在 —— 所以这里主动过滤，而不是原样透传。
func sanitizeGeminiSchema(v any) map[string]any {
	m := asMap(v)
	if m == nil {
		return nil
	}
	out := map[string]any{}
	for _, k := range []string{"type", "description", "format", "nullable", "enum", "title"} {
		if val, ok := m[k]; ok {
			out[k] = val
		}
	}
	if props := asMap(m["properties"]); props != nil {
		clean := map[string]any{}
		for name, raw := range props {
			if sub := sanitizeGeminiSchema(raw); sub != nil {
				clean[name] = sub
			}
		}
		out["properties"] = clean
		// required 只保留真的还在的属性：属性被过滤掉却仍写在 required 里，
		// Gemini 会报「required 里的字段不存在」，整条请求失败
		if req, ok := m["required"].([]any); ok {
			kept := make([]any, 0, len(req))
			for _, item := range req {
				name := asString(item)
				if _, exists := clean[name]; exists {
					kept = append(kept, item)
				}
			}
			if len(kept) > 0 {
				out["required"] = kept
			}
		}
	} else if val, ok := m["required"]; ok {
		out["required"] = val
	}
	if items, ok := m["items"]; ok {
		if sub := sanitizeGeminiSchema(items); sub != nil {
			out["items"] = sub
		}
	}
	if out["type"] == nil && out["properties"] != nil {
		out["type"] = "object"
	}
	return out
}

// openAIToolChoiceToGemini 映射 tool_choice 到 functionCallingConfig。
func openAIToolChoiceToGemini(v any) map[string]any {
	if s, ok := v.(string); ok {
		switch s {
		case "auto":
			return map[string]any{"mode": "AUTO"}
		case "required":
			return map[string]any{"mode": "ANY"}
		case "none":
			return map[string]any{"mode": "NONE"}
		default:
			return nil
		}
	}
	m := asMap(v)
	if m == nil {
		return nil
	}
	name := ""
	if fn := asMap(m["function"]); fn != nil {
		name = asString(fn["name"])
	}
	if name == "" {
		name = asString(m["name"])
	}
	if name == "" {
		return nil
	}
	// OpenAI 的「指定某个函数」在 Gemini 里是 ANY + 白名单
	return map[string]any{"mode": "ANY", "allowedFunctionNames": []any{name}}
}

// GeminiResponseToOpenAIChat 把非流式 Gemini 响应翻译成 OpenAI Chat 响应。
func GeminiResponseToOpenAIChat(body []byte, fallbackModel string) ([]byte, error) {
	var src map[string]any
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("Gemini 响应解析失败: %w", err)
	}

	message := map[string]any{"role": "assistant"}
	var texts []string
	var thinking []string
	var toolCalls []any
	finish := "stop"
	candidates, _ := src["candidates"].([]any)
	if len(candidates) > 0 {
		cand := asMap(candidates[0])
		if cand != nil {
			finish = geminiFinishReasonToOpenAI(asString(cand["finishReason"]))
			content := asMap(cand["content"])
			if content != nil {
				parts, _ := content["parts"].([]any)
				for _, praw := range parts {
					p := asMap(praw)
					if p == nil {
						continue
					}
					// thought 部件是思维链，不能混进正文
					if isThought, _ := p["thought"].(bool); isThought {
						thinking = append(thinking, asString(p["text"]))
						continue
					}
					if t := asString(p["text"]); t != "" {
						texts = append(texts, t)
					}
					if fc := asMap(p["functionCall"]); fc != nil {
						name := asString(fc["name"])
						args, _ := json.Marshal(fc["args"])
						toolCalls = append(toolCalls, map[string]any{
							"id":   "call_" + name,
							"type": "function",
							"function": map[string]any{
								"name":      name,
								"arguments": string(args),
							},
						})
					}
				}
			}
		}
	}
	// Gemini 用 STOP 表示「模型要求调用函数」，客户端要的是 tool_calls
	if len(toolCalls) > 0 {
		finish = "tool_calls"
	}
	message["content"] = strings.Join(texts, "")
	if len(thinking) > 0 {
		message["reasoning"] = strings.Join(thinking, "")
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}

	model := asString(src["modelVersion"])
	if model == "" {
		model = fallbackModel
	}
	out := map[string]any{
		"id":      newCompletionID(),
		"object":  "chat.completion",
		"created": anthropicCreated(),
		"model":   model,
		"choices": []any{map[string]any{
			"index": 0, "message": message, "finish_reason": finish,
		}},
	}
	if usage := geminiUsageToOpenAI(asMap(src["usageMetadata"])); usage != nil {
		out["usage"] = usage
	}
	return json.Marshal(out)
}

// geminiFinishReasonToOpenAI 是 geminiFinishReason 的逆映射。
func geminiFinishReasonToOpenAI(r string) string {
	switch r {
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "BLOCKLIST", "SPII":
		return "content_filter"
	case "MALFORMED_FUNCTION_CALL":
		return "tool_calls"
	case "STOP", "":
		return "stop"
	default:
		return "stop"
	}
}

// geminiUsageToOpenAI 把 Gemini 口径的用量换算成 OpenAI 口径。
//
// Gemini 的 cachedContentTokenCount 是 promptTokenCount 的**子集**（与 OpenAI 同构），
// 所以直接搬过去即可；站内归一化见到子集字段会减掉它，得到并列口径的输入量。
// thoughtsTokenCount 单独上报，作为推理 token 计量。
func geminiUsageToOpenAI(meta map[string]any) map[string]any {
	if meta == nil {
		return nil
	}
	prompt := asInt(meta["promptTokenCount"])
	completion := asInt(meta["candidatesTokenCount"])
	cached := asInt(meta["cachedContentTokenCount"])
	thoughts := asInt(meta["thoughtsTokenCount"])
	out := map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      asInt(meta["totalTokenCount"]),
	}
	if asInt(out["total_tokens"]) == 0 {
		out["total_tokens"] = prompt + completion
	}
	if cached > 0 {
		out["prompt_tokens_details"] = map[string]any{"cached_tokens": cached}
	}
	if thoughts > 0 {
		out["completion_tokens_details"] = map[string]any{"reasoning_tokens": thoughts}
	}
	return out
}

// GeminiErrorMessage 从 Gemini 的错误体里取出可读信息。
func GeminiErrorMessage(body []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return string(body)
	}
	if e := asMap(payload["error"]); e != nil {
		msg := asString(e["message"])
		status := asString(e["status"])
		switch {
		case msg != "" && status != "":
			return status + ": " + msg
		case msg != "":
			return msg
		}
	}
	return string(body)
}
