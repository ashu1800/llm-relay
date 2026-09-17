package convert

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
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
		// contentParts 装这条消息的所有内容块（文本 + 多模态）。
		// 原来叫 textParts 且只认文本：inlineData（图片/音频）与 fileData
		// 被无声跳过，模型收不到图按纯文本作答，无任何报错 —— 静默数据损坏
		var contentParts []any
		var toolCalls []any
		var toolResults []any

		for _, praw := range parts {
			p := asMap(praw)
			if p == nil {
				continue
			}
			if t := asString(p["text"]); t != "" {
				contentParts = append(contentParts, map[string]any{"type": "text", "text": t})
			}
			// inlineData 是 Gemini SDK 的标准多模态用法（:generateContent 带图），
			// 映射进 OpenAI 通用语；不认识的类型宁可显式拒绝也不静默丢弃
			if inline := asMap(p["inlineData"]); inline != nil {
				mime := asString(inline["mimeType"])
				data := asString(inline["data"])
				switch {
				case strings.HasPrefix(mime, "image/"):
					contentParts = append(contentParts, map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": "data:" + mime + ";base64," + data},
					})
				case strings.HasPrefix(mime, "audio/"):
					format := geminiAudioFormat(mime)
					if format == "" {
						return nil, errUnsupportedContent("Gemini 协议的音频 " + mime + " 无法转发（通用语仅支持 wav/mp3）")
					}
					contentParts = append(contentParts, map[string]any{
						"type":        "input_audio",
						"input_audio": map[string]any{"data": data, "format": format},
					})
				default:
					return nil, errUnsupportedContent("Gemini 协议的 inlineData 仅支持图片与音频，收到 " + mime)
				}
			}
			// fileData 是 Files API 的文件引用：中继无状态转发、没有那份文件，
			// 通用语里也没有对应物。明确拒绝比静默丢掉好 —— 客户端至少知道要改
			if asMap(p["fileData"]) != nil {
				return nil, errUnsupportedContent("Gemini 协议的 fileData（Files API 引用）不支持转发，请把文件内容以内联方式发送")
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
		if len(contentParts) == 0 && len(toolCalls) == 0 {
			continue
		}
		msg := map[string]any{"role": role}
		// 全是文本块时拍平成纯字符串（与纯文本请求的通用语形状一致，
		// 对各类上游兼容面最广）；带图/音频时保留块数组 —— 那才是
		// OpenAI 通用语的多模态形状，拍平会把非文本块丢掉
		if onlyTextBlocks(contentParts) {
			msg["content"] = geminiFlattenText(contentParts)
		} else {
			msg["content"] = contentParts
		}
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

// onlyTextBlocks 判断内容块是否全是 text 类型（可安全拍平成字符串）。
func onlyTextBlocks(parts []any) bool {
	for _, p := range parts {
		if asString(asMap(p)["type"]) != "text" {
			return false
		}
	}
	return true
}

// geminiAudioFormat 把音频 MIME 映射成 OpenAI input_audio 认的格式串。
func geminiAudioFormat(mime string) string {
	switch mime {
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav"
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	default:
		return ""
	}
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

	// toolCalls 累积本次流里出现的工具调用。
	//
	// Gemini 的 functionCall 是一个完整对象（name + args），没法像 Chat 那样
	// 先给 name、再一片片追加 arguments 字符串，所以只能攒起来在收尾时一次性发出。
	// 按 index 归并：Chat 的分片带 index，同一个调用的 name 只会出现在第一片里。
	toolCalls map[int]*pendingToolCall
	usage     map[string]any
	// emitted 记录已经写出的调用下标，避免收尾时重复发。
	emitted    map[int]bool
	finishSent bool
	// stopReason 暂存上游最后一帧的 finish_reason：收尾分片要按它映射，
	// 不能硬编码 STOP —— 撞 max_tokens 时客户端要靠 MAX_TOKENS 才知道
	// 回复被截断了
	stopReason string
}

// pendingToolCall 是一个正在累积的工具调用。
type pendingToolCall struct {
	name string
	args strings.Builder
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
	return &GeminiStreamTranslator{
		w: w, model: model,
		toolCalls: map[int]*pendingToolCall{},
		emitted:   map[int]bool{},
	}
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
	if fr := asString(choice["finish_reason"]); fr != "" {
		t.stopReason = fr
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
			for i, craw := range calls {
				call := asMap(craw)
				if call == nil {
					continue
				}
				// Chat 的分片带 index；缺省时用数组下标兜底（单调用场景两者一致）
				idx := asInt(call["index"])
				if _, has := call["index"]; !has {
					idx = i
				}
				pend := t.toolCalls[idx]
				if pend == nil {
					pend = &pendingToolCall{}
					t.toolCalls[idx] = pend
				}
				if fn := asMap(call["function"]); fn != nil {
					// name 只在第一片出现；后续分片为空串，不能覆盖已有值
					if name := asString(fnName(fn)); name != "" {
						pend.name = name
					}
					if frag := asString(fn["arguments"]); frag != "" {
						pend.args.WriteString(frag)
					}
				}
			}
		}
	}

	if len(parts) == 0 {
		return nil
	}
	return writeGeminiSSE(t.w, map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"role": "model", "parts": parts},
			"index":   0,
		}},
		"modelVersion": t.model,
	})
}

// flushToolCalls 把累积到的工具调用补发出去。
//
// 没有这一步时，凡是「只回工具调用、没有正文」的流会在收尾处变成
// parts: [] + finishReason: STOP —— 客户端收到一个语法完全合法、
// 状态为正常结束的空回复，而模型其实想调工具。对话就此中断且没有任何报错。
func (t *GeminiStreamTranslator) flushToolCalls() error {
	if len(t.toolCalls) == 0 {
		return nil
	}
	toolParts := make([]any, 0, len(t.toolCalls))
	// 按 index 排序，保证同一轮里多个调用的顺序稳定
	idxs := make([]int, 0, len(t.toolCalls))
	for idx := range t.toolCalls {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)
	for _, idx := range idxs {
		pend := t.toolCalls[idx]
		if pend.name == "" || t.emitted[idx] {
			continue
		}
		t.emitted[idx] = true
		// 参数是分片拼起来的 JSON 字符串，解析失败时退成空对象：
		// 宁可给出「无参数调用」也不要把半截 JSON 发出去。
		var args any = map[string]any{}
		if raw := strings.TrimSpace(pend.args.String()); raw != "" {
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				args = map[string]any{}
			}
		}
		toolParts = append(toolParts, map[string]any{
			"functionCall": map[string]any{"name": pend.name, "args": args},
		})
	}
	if len(toolParts) == 0 {
		return nil
	}
	return writeGeminiSSE(t.w, map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"role": "model", "parts": toolParts},
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
	if err := t.flushToolCalls(); err != nil {
		return err
	}
	if t.finishSent {
		return nil
	}
	t.finishSent = true

	return writeGeminiSSE(t.w, map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"role": "model", "parts": []any{}},
			// finish_reason 透传（与非流式同一映射）：length -> MAX_TOKENS、
			// content_filter -> SAFETY；原来一律 STOP，截断被呈现成完整结束
			"finishReason": geminiFinishReason(t.stopReason),
			"index":        0,
		}},
		"modelVersion":  t.model,
		"usageMetadata": usageToGemini(t.usage),
	})
}
