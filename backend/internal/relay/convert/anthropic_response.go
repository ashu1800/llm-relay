package convert

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// mapFinishReason 把 OpenAI 的结束原因映射为 Anthropic 的 stop_reason。
func mapFinishReason(r string) string {
	switch r {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "stop", "content_filter", "":
		return "end_turn"
	default:
		return "end_turn"
	}
}

// usageToAnthropic 把 OpenAI 口径的用量换算成 Anthropic 口径。
// OpenAI 的 prompt_tokens 含缓存命中，Anthropic 的 input_tokens 不含，必须拆开。
func usageToAnthropic(usage map[string]any) map[string]any {
	if usage == nil {
		return map[string]any{"input_tokens": 0, "output_tokens": 0}
	}
	prompt := asInt(usage["prompt_tokens"])
	if prompt == 0 {
		prompt = asInt(usage["input_tokens"])
	}
	completion := asInt(usage["completion_tokens"])
	if completion == 0 {
		completion = asInt(usage["output_tokens"])
	}
	cached := 0
	if d := asMap(usage["prompt_tokens_details"]); d != nil {
		cached = asInt(d["cached_tokens"])
	}
	cached += asInt(usage["cache_read_input_tokens"])

	nonCached := prompt
	if cached > 0 && cached <= prompt {
		nonCached = prompt - cached
	}
	out := map[string]any{
		"input_tokens":  nonCached,
		"output_tokens": completion,
	}
	if cached > 0 {
		out["cache_read_input_tokens"] = cached
	}
	if creation := asInt(usage["cache_creation_input_tokens"]); creation > 0 {
		out["cache_creation_input_tokens"] = creation
	}
	return out
}

// OpenAIChatToAnthropicResponse 把非流式 OpenAI Chat 响应翻译成 Anthropic Messages 响应。
func OpenAIChatToAnthropicResponse(body []byte, fallbackModel string) ([]byte, error) {
	var src map[string]any
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("OpenAI 响应解析失败: %w", err)
	}

	model := asString(src["model"])
	if model == "" {
		model = fallbackModel
	}

	out := map[string]any{
		"id":    anthropicID(asString(src["id"])),
		"type":  "message",
		"role":  "assistant",
		"model": model,
	}
	if created := asInt(src["created"]); created > 0 {
		out["created"] = created
	}

	var content []any
	stopReason := "end_turn"
	if choices, ok := src["choices"].([]any); ok && len(choices) > 0 {
		choice := asMap(choices[0])
		if choice != nil {
			stopReason = mapFinishReason(asString(choice["finish_reason"]))
			msg := asMap(choice["message"])
			if msg != nil {
				// 部分上游把思维链放在 reasoning 字段，Anthropic 用 thinking 块
				if r := asString(msg["reasoning"]); r != "" {
					content = append(content, map[string]any{"type": "thinking", "thinking": r})
				}
				if t := asString(msg["content"]); t != "" {
					content = append(content, map[string]any{"type": "text", "text": t})
				}
				if calls, ok := msg["tool_calls"].([]any); ok {
					for _, c := range calls {
						call := asMap(c)
						if call == nil {
							continue
						}
						fn := asMap(call["function"])
						var input any = map[string]any{}
						if fn != nil {
							if args := asString(fn["arguments"]); args != "" {
								// 尽力解析参数，解析失败时退化为原始字符串
								if err := json.Unmarshal([]byte(args), &input); err != nil {
									input = map[string]any{"_raw": args}
								}
							}
						}
						block := map[string]any{
							"type":  "tool_use",
							"id":    asString(call["id"]),
							"input": input,
						}
						if fn != nil {
							block["name"] = asString(fn["name"])
						}
						content = append(content, block)
					}
					if len(content) > 0 {
						stopReason = "tool_use"
					}
				}
			}
		}
	}
	if content == nil {
		content = []any{}
	}
	out["content"] = content
	out["stop_reason"] = stopReason
	out["stop_sequence"] = nil
	out["usage"] = usageToAnthropic(asMap(src["usage"]))
	return json.Marshal(out)
}

// anthropicID 把 OpenAI 的 id 改写成 Anthropic 的 msg_ 前缀，便于客户端识别。
func anthropicID(id string) string {
	if id == "" {
		return "msg_relay"
	}
	if strings.HasPrefix(id, "msg_") {
		return id
	}
	return "msg_" + id
}

// AnthropicError 把上游错误包装成 Anthropic 的错误结构。
func AnthropicError(status int, message string) []byte {
	kind := "api_error"
	switch status {
	case 400:
		kind = "invalid_request_error"
	case 401:
		kind = "authentication_error"
	case 403:
		kind = "permission_error"
	case 404:
		kind = "not_found_error"
	case 413:
		kind = "request_too_large"
	case 429:
		kind = "rate_limit_error"
	case 529:
		kind = "overloaded_error"
	}
	raw, _ := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    kind,
			"message": message,
		},
	})
	return raw
}

// AnthropicStreamTranslator 把 OpenAI Chat 的 SSE 流实时改写成 Anthropic 的事件流。
//
// 事件顺序遵循 Anthropic 规范：message_start → (content_block_start → deltas → content_block_stop)*
// → message_delta → message_stop。工具调用在 OpenAI 里是零散的 arguments 分片，
// 必须映射成独立的 tool_use 内容块并按 input_json_delta 输出。
type AnthropicStreamTranslator struct {
	w        io.Writer
	splitter sseSplitter
	model    string

	started    bool
	blockOpen  bool
	blockIndex int
	blockKind  string
	// toolBlocks 记录 OpenAI 的 tool_call index 对应的 Anthropic 内容块序号
	toolBlocks map[int]int
	stopReason string
	usage      map[string]any
}

// NewAnthropicStreamTranslator 构造流式改写器。
func NewAnthropicStreamTranslator(w io.Writer, model string) *AnthropicStreamTranslator {
	return &AnthropicStreamTranslator{
		w:          w,
		model:      model,
		blockIndex: -1,
		toolBlocks: map[int]int{},
	}
}

// Write 接收上游原始字节并输出改写后的事件流。
func (t *AnthropicStreamTranslator) Write(p []byte) (int, error) {
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

func (t *AnthropicStreamTranslator) handleChunk(payload []byte) error {
	var chunk map[string]any
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return nil // 忽略无法解析的分片，不要打断整个流
	}
	if !t.started {
		if err := t.emitMessageStart(chunk); err != nil {
			return err
		}
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
		t.stopReason = mapFinishReason(fr)
	}

	delta := asMap(choice["delta"])
	if delta == nil {
		return nil
	}
	// 思维链增量以 thinking 块输出
	if r := asString(delta["reasoning"]); r != "" {
		if err := t.ensureBlock("thinking"); err != nil {
			return err
		}
		return writeSSE(t.w, "content_block_delta", map[string]any{
			"type": "content_block_delta", "index": t.blockIndex,
			"delta": map[string]any{"type": "thinking_delta", "thinking": r},
		})
	}
	if c := asString(delta["content"]); c != "" {
		if err := t.ensureBlock("text"); err != nil {
			return err
		}
		return writeSSE(t.w, "content_block_delta", map[string]any{
			"type": "content_block_delta", "index": t.blockIndex,
			"delta": map[string]any{"type": "text_delta", "text": c},
		})
	}
	if calls, ok := delta["tool_calls"].([]any); ok {
		for _, c := range calls {
			if err := t.handleToolCall(asMap(c)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *AnthropicStreamTranslator) handleToolCall(call map[string]any) error {
	if call == nil {
		return nil
	}
	idx := asInt(call["index"])
	fn := asMap(call["function"])

	block, seen := t.toolBlocks[idx]
	if !seen {
		// 新的工具调用：先收掉正在写的块，再开一个 tool_use 块
		if err := t.closeBlock(); err != nil {
			return err
		}
		t.blockIndex++
		block = t.blockIndex
		t.toolBlocks[idx] = block
		t.blockKind = "tool_use"
		t.blockOpen = true
		if err := writeSSE(t.w, "content_block_start", map[string]any{
			"type": "content_block_start", "index": block,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    asString(call["id"]),
				"name":  asString(fnName(fn)),
				"input": map[string]any{},
			},
		}); err != nil {
			return err
		}
	}
	args := asString(fnArgs(fn))
	if args == "" {
		return nil
	}
	return writeSSE(t.w, "content_block_delta", map[string]any{
		"type": "content_block_delta", "index": block,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": args},
	})
}

func fnName(fn map[string]any) any {
	if fn == nil {
		return ""
	}
	return fn["name"]
}

func fnArgs(fn map[string]any) any {
	if fn == nil {
		return ""
	}
	return fn["arguments"]
}

// ensureBlock 保证当前有一个指定类型的内容块，类型不同则切换。
func (t *AnthropicStreamTranslator) ensureBlock(kind string) error {
	if t.blockOpen && t.blockKind == kind {
		return nil
	}
	if err := t.closeBlock(); err != nil {
		return err
	}
	t.blockIndex++
	t.blockOpen = true
	t.blockKind = kind

	block := map[string]any{"type": kind}
	if kind == "text" {
		block["text"] = ""
	}
	if kind == "thinking" {
		block["thinking"] = ""
	}
	return writeSSE(t.w, "content_block_start", map[string]any{
		"type": "content_block_start", "index": t.blockIndex, "content_block": block,
	})
}

func (t *AnthropicStreamTranslator) closeBlock() error {
	if !t.blockOpen {
		return nil
	}
	t.blockOpen = false
	return writeSSE(t.w, "content_block_stop", map[string]any{
		"type": "content_block_stop", "index": t.blockIndex,
	})
}

func (t *AnthropicStreamTranslator) emitMessageStart(chunk map[string]any) error {
	t.started = true
	model := asString(chunk["model"])
	if model == "" {
		model = t.model
	}
	return writeSSE(t.w, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            anthropicID(asString(chunk["id"])),
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	})
}

// Abort 在上游流中断时收尾：关掉未闭合的内容块，再发一个 error 事件。
//
// 原来中断时走的是 Close，会补上 message_delta + message_stop，
// 客户端（Claude Code 等）据此认为回复完整 —— 截断被伪装成成功。
func (t *AnthropicStreamTranslator) Abort(reason string) error {
	if t.started && t.blockOpen {
		// 内容块没关就发 error，部分客户端的状态机会卡在块内
		_ = writeSSE(t.w, "content_block_stop", map[string]any{
			"type": "content_block_stop", "index": t.blockIndex,
		})
		t.blockOpen = false
	}
	return writeSSE(t.w, "error", map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "api_error", "message": reason},
	})
}

// Close 收尾：补上 message_delta 与 message_stop。
// 仅用于上游**正常**结束的场合；中断请走 Abort。
func (t *AnthropicStreamTranslator) Close() error {
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
	if !t.started {
		// 上游一个事件都没发，也要补一个完整的空消息
		if err := t.emitMessageStart(map[string]any{"model": t.model}); err != nil {
			return err
		}
	}
	if err := t.closeBlock(); err != nil {
		return err
	}
	if t.stopReason == "" {
		t.stopReason = "end_turn"
	}
	usage := usageToAnthropic(t.usage)
	if err := writeSSE(t.w, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": t.stopReason, "stop_sequence": nil},
		"usage": usage,
	}); err != nil {
		return err
	}
	return writeSSE(t.w, "message_stop", map[string]any{"type": "message_stop"})
}
