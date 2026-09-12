package convert

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Gemini 的模型名在 URL 路径里而不是请求体，因此转换时需要外部传入。
// 命中思维链时 Gemini 用 thought 标记，需单独处理以免混进正文。

// GeminiRequestToOpenAIChat 把 Gemini generateContent 请求翻译成 Chat Completions 请求。
func GeminiRequestToOpenAIChat(body []byte, model string, stream bool) ([]byte, error) {
	var src map[string]any
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("Gemini 请求体解析失败: %w", err)
	}

	out := map[string]any{"model": model}
	// Gemini 没有 stream 字段，流式与否只体现在 URL 的方法名上（:streamGenerateContent），
	// 必须显式注入。漏掉会让流式请求被降级成非流式，流式客户端一直等不到分片。
	if stream {
		out["stream"] = true
	}

	var messages []any
	// systemInstruction 是顶层字段，Chat 要求放进 messages 首条
	if sys := geminiPartsToText(asMap(src["systemInstruction"])["parts"]); sys != "" {
		messages = append(messages, map[string]any{"role": "system", "content": sys})
	}

	contents, _ := src["contents"].([]any)
	for _, raw := range contents {
		c := asMap(raw)
		if c == nil {
			continue
		}
		// Gemini 用 model 表示助手，其余一律按 user 处理
		role := "user"
		if asString(c["role"]) == "model" {
			role = "assistant"
		}

		parts, _ := c["parts"].([]any)
		var textParts []any
		var toolCalls []any
		var toolResults []any

		for _, praw := range parts {
			p := asMap(praw)
			if p == nil {
				continue
			}
			if t := asString(p["text"]); t != "" {
				textParts = append(textParts, map[string]any{"type": "text", "text": t})
			}
			if fc := asMap(p["functionCall"]); fc != nil {
				args, _ := json.Marshal(fc["args"])
				toolCalls = append(toolCalls, map[string]any{
					"id":   "call_" + asString(fc["name"]),
					"type": "function",
					"function": map[string]any{
						"name":      asString(fc["name"]),
						"arguments": string(args),
					},
				})
			}
			if fr := asMap(p["functionResponse"]); fr != nil {
				resp, _ := json.Marshal(fr["response"])
				toolResults = append(toolResults, map[string]any{
					"role":         "tool",
					"tool_call_id": "call_" + asString(fr["name"]),
					"content":      string(resp),
				})
			}
		}

		// 函数结果必须排在下一条用户消息之前，否则上游会拒绝
		messages = append(messages, toolResults...)
		if len(textParts) == 0 && len(toolCalls) == 0 {
			continue
		}
		msg := map[string]any{"role": role}
		msg["content"] = geminiFlattenText(textParts)
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		messages = append(messages, msg)
	}
	out["messages"] = messages

	// generationConfig 展开到 Chat 的顶层字段
	if gc := asMap(src["generationConfig"]); gc != nil {
		if v, ok := gc["maxOutputTokens"]; ok {
			out["max_tokens"] = v
		}
		for _, pair := range [][2]string{
			{"temperature", "temperature"}, {"topP", "top_p"}, {"stopSequences", "stop"},
		} {
			if v, ok := gc[pair[0]]; ok {
				out[pair[1]] = v
			}
		}
	}

	if tools, ok := src["tools"].([]any); ok {
		out["tools"] = geminiToolsToOpenAI(tools)
	}
	if tc := asMap(src["toolConfig"]); tc != nil {
		if fcc := asMap(tc["functionCallingConfig"]); fcc != nil {
			if v := geminiToolChoice(asString(fcc["mode"])); v != nil {
				out["tool_choice"] = v
			}
		}
	}

	return json.Marshal(out)
}

func geminiToolsToOpenAI(tools []any) []any {
	var out []any
	for _, raw := range tools {
		t := asMap(raw)
		if t == nil {
			continue
		}
		decls, _ := t["functionDeclarations"].([]any)
		for _, draw := range decls {
			d := asMap(draw)
			if d == nil {
				continue
			}
			fn := map[string]any{"name": asString(d["name"])}
			if desc := asString(d["description"]); desc != "" {
				fn["description"] = desc
			}
			if p, ok := d["parameters"]; ok {
				fn["parameters"] = p
			} else {
				fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			out = append(out, map[string]any{"type": "function", "function": fn})
		}
	}
	return out
}

func geminiToolChoice(mode string) any {
	switch mode {
	case "AUTO":
		return "auto"
	case "ANY":
		return "required"
	case "NONE":
		return "none"
	default:
		return nil
	}
}

func geminiPartsToText(v any) string {
	parts, _ := v.([]any)
	var sb strings.Builder
	for _, raw := range parts {
		if p := asMap(raw); p != nil {
			sb.WriteString(asString(p["text"]))
		}
	}
	return sb.String()
}

func geminiFlattenText(parts []any) any {
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return asString(asMap(parts[0])["text"])
	}
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString(asString(asMap(p)["text"]))
	}
	return sb.String()
}

// ============================ 响应转换 ============================

// OpenAIChatToGeminiResponse 把非流式 Chat 响应翻译成 Gemini 响应。
func OpenAIChatToGeminiResponse(body []byte, fallbackModel string) ([]byte, error) {
	var src map[string]any
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("OpenAI 响应解析失败: %w", err)
	}

	var parts []any
	finish := "STOP"
	if choices, ok := src["choices"].([]any); ok && len(choices) > 0 {
		choice := asMap(choices[0])
		if choice != nil {
			finish = geminiFinishReason(asString(choice["finish_reason"]))
			msg := asMap(choice["message"])
			if msg != nil {
				// Gemini 用 thought 标记思维链部件，丢掉会让推理模型的行为看起来不完整
				if r := asString(msg["reasoning"]); r != "" {
					parts = append(parts, map[string]any{"text": r, "thought": true})
				}
				if t := asString(msg["content"]); t != "" {
					parts = append(parts, map[string]any{"text": t})
				}
				if calls, ok := msg["tool_calls"].([]any); ok {
					for _, c := range calls {
						call := asMap(c)
						if call == nil {
							continue
						}
						fn := asMap(call["function"])
						var args any = map[string]any{}
						if fn != nil {
							if raw := asString(fn["arguments"]); raw != "" {
								if err := json.Unmarshal([]byte(raw), &args); err != nil {
									args = map[string]any{}
								}
							}
						}
						part := map[string]any{"functionCall": map[string]any{"args": args}}
						if fn != nil {
							part["functionCall"].(map[string]any)["name"] = asString(fn["name"])
						}
						parts = append(parts, part)
					}
				}
			}
		}
	}
	if parts == nil {
		parts = []any{}
	}

	return json.Marshal(map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": finish,
			"index":        0,
		}},
		"modelVersion":  fallbackModel,
		"usageMetadata": usageToGemini(asMap(src["usage"])),
	})
}

// usageToGemini 把 Chat 用量换算成 Gemini 口径。
func usageToGemini(usage map[string]any) map[string]any {
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
	meta := map[string]any{
		"promptTokenCount":     in,
		"candidatesTokenCount": outTokens,
		"totalTokenCount":      in + outTokens,
	}
	if cached > 0 {
		meta["cachedContentTokenCount"] = cached
	}
	return meta
}

func geminiFinishReason(r string) string {
	switch r {
	case "length":
		return "MAX_TOKENS"
	case "tool_calls", "function_call":
		return "STOP"
	case "content_filter":
		return "SAFETY"
	case "stop", "":
		return "STOP"
	default:
		return "STOP"
	}
}

// GeminiError 生成 Gemini 结构的错误体。
func GeminiError(status int, message string) []byte {
	code := 500
	switch status {
	case 400:
		code = 400
	case 401, 403:
		code = 401
	case 404:
		code = 404
	case 429:
		code = 429
	case 501:
		code = 501
	case 502, 503:
		code = 503
	}
	raw, _ := json.Marshal(map[string]any{
		"error": map[string]any{"code": code, "message": message, "status": geminiStatus(code)},
	})
	return raw
}

func geminiStatus(code int) string {
	switch code {
	case 400:
		return "INVALID_ARGUMENT"
	case 401:
		return "UNAUTHENTICATED"
	case 404:
		return "NOT_FOUND"
	case 429:
		return "RESOURCE_EXHAUSTED"
	case 501:
		return "UNIMPLEMENTED"
	case 503:
		return "UNAVAILABLE"
	default:
		return "INTERNAL"
	}
}

// writeGeminiSSE 写出 Gemini 的 SSE 帧。与 Anthropic/Responses 不同，它没有 event 行。
func writeGeminiSSE(w io.Writer, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, "data: "); err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n\n")
	return err
}

// GeminiStreamTranslator 把 Chat 的 SSE 流改写成 Gemini 的 SSE 流。
type GeminiStreamTranslator struct {
	w        io.Writer
	splitter sseSplitter
	model    string

	toolSeen   map[string]bool
	usage      map[string]any
	finishSent bool
	anySent    bool
}

// NewGeminiTranslator 按是否流式返回对应的改写器。
func NewGeminiTranslator(w io.Writer, model string, stream bool) Translator {
	if stream {
		return NewGeminiStreamTranslator(w, model)
	}
	return &bodyTranslator{w: w, model: model, conv: OpenAIChatToGeminiResponse}
}

// NewGeminiStreamTranslator 构造 Gemini 流式改写器。
func NewGeminiStreamTranslator(w io.Writer, model string) *GeminiStreamTranslator {
	return &GeminiStreamTranslator{w: w, model: model, toolSeen: map[string]bool{}}
}

// Write 接收上游原始字节并输出 Gemini 事件流。
func (t *GeminiStreamTranslator) Write(p []byte) (int, error) {
	var werr error
	t.splitter.feed(p, func(line []byte) {
		if werr != nil {
			return
		}
		if payload := dataPayload(line); payload != nil {
			werr = t.handleChunk(payload)
		}
	})
	if werr != nil {
		return 0, werr
	}
	return len(p), nil
}

func (t *GeminiStreamTranslator) handleChunk(payload []byte) error {
	var chunk map[string]any
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return nil
	}
	if u := asMap(chunk["usage"]); u != nil {
		t.usage = u
	}
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return nil
	}
	choice := asMap(choices[0])
	if choice == nil {
		return nil
	}

	var parts []any
	if delta := asMap(choice["delta"]); delta != nil {
		if r := asString(delta["reasoning"]); r != "" {
			parts = append(parts, map[string]any{"text": r, "thought": true})
		}
		if txt := asString(delta["content"]); txt != "" {
			parts = append(parts, map[string]any{"text": txt})
		}
		if calls, ok := delta["tool_calls"].([]any); ok {
			for _, craw := range calls {
				call := asMap(craw)
				if call == nil {
					continue
				}
				fn := asMap(call["function"])
				name := asString(fnName(fn))
				// Gemini 的函数调用是整体对象，无法增量拼接，累积到收尾时一次性发出
				if name == "" {
					continue
				}
				t.toolSeen[name] = true
			}
		}
	}

	if len(parts) == 0 {
		return nil
	}
	t.anySent = true
	return writeGeminiSSE(t.w, map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"role": "model", "parts": parts},
			"index":   0,
		}},
		"modelVersion": t.model,
	})
}

// Abort 在上游流中断时收尾。
//
// Gemini 的流式协议没有专门的错误事件，官方在 HTTP 层用 {"error":{...}} 表达失败，
// 这里沿用同一结构。原来中断时走的是 Close，会补一个带 finishReason 的终止分片，
// 客户端据此认为生成正常结束 —— 截断被伪装成成功。
func (t *GeminiStreamTranslator) Abort(reason string) error {
	return writeGeminiSSE(t.w, map[string]any{
		"error": map[string]any{
			"code": 502, "message": reason, "status": "UNAVAILABLE",
		},
	})
}

// Close 收尾并补出终止分片。
// Gemini 客户端靠 finishReason 判断结束，缺了它会一直等。
// 仅用于上游**正常**结束的场合；中断请走 Abort。
func (t *GeminiStreamTranslator) Close() error {
	var flushErr error
	t.splitter.flush(func(line []byte) {
		if flushErr != nil {
			return
		}
		if payload := dataPayload(line); payload != nil {
			flushErr = t.handleChunk(payload)
		}
	})
	if flushErr != nil {
		return flushErr
	}
	if t.finishSent {
		return nil
	}
	t.finishSent = true

	return writeGeminiSSE(t.w, map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": []any{}},
			"finishReason": "STOP",
			"index":        0,
		}},
		"modelVersion":  t.model,
		"usageMetadata": usageToGemini(t.usage),
	})
}
