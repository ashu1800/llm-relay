package convert

import (
	"encoding/json"
	"io"
	"strings"
)

// ResponsesStreamTranslator 把 Chat Completions 的 SSE 流改写成 Responses 事件流。
//
// 事件顺序遵循 Responses 规范：
//
//	response.created → response.in_progress →
//	(output_item.added → 内容增量 → *.done)* → response.completed
//
// Chat 的单个 delta 流里可能交织思维链、正文与工具调用，必须切成多个独立 output item。
type ResponsesStreamTranslator struct {
	w        io.Writer
	splitter sseSplitter
	model    string

	respID  string
	created int
	started bool

	outputIndex int
	items       []any

	// 当前正在写入的输出项
	openKind string // "" | reasoning | message | function_call
	openID   string
	openIdx  int
	// openToolIdx 记录上游 tool_call 的 index。它与输出项序号不是一回事，混用会让
	// 第二个及之后的工具调用被误判成同一个。
	openToolIdx int
	hasOpenTool bool
	openText    strings.Builder
	openArgs    strings.Builder
	openName    string
	openCallID  string

	usage     map[string]any
	completed bool
}

// NewResponsesTranslator 按是否流式返回对应的改写器。
func NewResponsesTranslator(w io.Writer, model string, stream bool) Translator {
	if stream {
		return NewResponsesStreamTranslator(w, model)
	}
	return &bodyTranslator{w: w, model: model, conv: OpenAIChatToResponsesResponse}
}

// NewResponsesStreamTranslator 构造 Responses 流式改写器。
func NewResponsesStreamTranslator(w io.Writer, model string) *ResponsesStreamTranslator {
	return &ResponsesStreamTranslator{w: w, model: model}
}

// Write 接收上游原始字节并输出改写后的事件流。
func (t *ResponsesStreamTranslator) Write(p []byte) (int, error) {
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

func (t *ResponsesStreamTranslator) handleChunk(payload []byte) error {
	var chunk map[string]any
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return nil // 无法解析的分片直接忽略，不要打断整个流
	}
	if !t.started {
		if err := t.emitCreated(chunk); err != nil {
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
	delta := asMap(choice["delta"])
	if delta == nil {
		return nil
	}

	// 一个 delta 里可能同时带思维链、正文和工具调用，三者都要发出去。
	// 原来每个分支各自 return，同一 delta 里只有第一个字段能活下来，
	// 上游把 content 和 tool_calls 放在一起时工具调用会被静默丢弃。
	if r := asString(delta["reasoning"]); r != "" {
		if err := t.ensureReasoning(); err != nil {
			return err
		}
		t.openText.WriteString(r)
		if err := writeSSE(t.w, "response.reasoning_summary_text.delta", map[string]any{
			"type":    "response.reasoning_summary_text.delta",
			"item_id": t.openID, "output_index": t.openIdx,
			"summary_index": 0, "delta": r,
		}); err != nil {
			return err
		}
	}

	if c := asString(delta["content"]); c != "" {
		if err := t.ensureMessage(); err != nil {
			return err
		}
		t.openText.WriteString(c)
		if err := writeSSE(t.w, "response.output_text.delta", map[string]any{
			"type":    "response.output_text.delta",
			"item_id": t.openID, "output_index": t.openIdx,
			"content_index": 0, "delta": c,
		}); err != nil {
			return err
		}
	}

	if calls, ok := delta["tool_calls"].([]any); ok {
		for _, raw := range calls {
			if err := t.handleToolCall(asMap(raw)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *ResponsesStreamTranslator) handleToolCall(call map[string]any) error {
	if call == nil {
		return nil
	}
	fn := asMap(call["function"])
	idx := asInt(call["index"])

	// 新的工具调用：换一个 output item
	if t.openKind != "function_call" || !t.hasOpenTool || idx != t.openToolIdx {
		if err := t.closeCurrent(); err != nil {
			return err
		}
		t.openKind = "function_call"
		t.openToolIdx = idx
		t.hasOpenTool = true
		t.openIdx = t.outputIndex
		t.outputIndex++
		t.openArgs.Reset()
		t.openText.Reset()
		t.openID = "fc_" + t.respID + "_" + itoa(idx)
		t.openCallID = asString(call["id"])
		t.openName = asString(fnName(fn))

		item := map[string]any{
			"type": "function_call", "id": t.openID,
			"call_id": t.openCallID, "name": t.openName,
			"arguments": "", "status": "in_progress",
		}
		if err := writeSSE(t.w, "response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"output_index": t.openIdx, "item": item,
		}); err != nil {
			return err
		}
	}
	args := asString(fnArgs(fn))
	if args == "" {
		return nil
	}
	t.openArgs.WriteString(args)
	return writeSSE(t.w, "response.function_call_arguments.delta", map[string]any{
		"type":    "response.function_call_arguments.delta",
		"item_id": t.openID, "output_index": t.openIdx, "delta": args,
	})
}

// ensureReasoning 保证当前输出项是思维链项。
func (t *ResponsesStreamTranslator) ensureReasoning() error {
	if t.openKind == "reasoning" {
		return nil
	}
	if err := t.closeCurrent(); err != nil {
		return err
	}
	t.openKind = "reasoning"
	t.hasOpenTool = false
	t.openIdx = t.outputIndex
	t.outputIndex++
	t.openText.Reset()
	t.openArgs.Reset()
	t.openID = "rs_" + t.respID

	return writeSSE(t.w, "response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": t.openIdx,
		"item": map[string]any{
			"type": "reasoning", "id": t.openID, "summary": []any{},
		},
	})
}

// ensureMessage 保证当前输出项是正文项。
func (t *ResponsesStreamTranslator) ensureMessage() error {
	if t.openKind == "message" {
		return nil
	}
	if err := t.closeCurrent(); err != nil {
		return err
	}
	t.openKind = "message"
	t.hasOpenTool = false
	t.openIdx = t.outputIndex
	t.outputIndex++
	t.openText.Reset()
	t.openArgs.Reset()
	t.openID = "msg_" + t.respID

	if err := writeSSE(t.w, "response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": t.openIdx,
		"item": map[string]any{
			"type": "message", "id": t.openID, "role": "assistant",
			"status": "in_progress", "content": []any{},
		},
	}); err != nil {
		return err
	}
	return writeSSE(t.w, "response.content_part.added", map[string]any{
		"type":    "response.content_part.added",
		"item_id": t.openID, "output_index": t.openIdx, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
	})
}

// closeCurrent 收尾当前输出项，把它归档进 items 供 response.completed 使用。
func (t *ResponsesStreamTranslator) closeCurrent() error {
	switch t.openKind {
	case "reasoning":
		text := t.openText.String()
		if err := writeSSE(t.w, "response.reasoning_summary_text.done", map[string]any{
			"type":    "response.reasoning_summary_text.done",
			"item_id": t.openID, "output_index": t.openIdx, "summary_index": 0, "text": text,
		}); err != nil {
			return err
		}
		item := map[string]any{
			"type": "reasoning", "id": t.openID, "summary": []any{},
		}
		if text != "" {
			item["summary"] = []any{map[string]any{"type": "summary_text", "text": text}}
		}
		if err := t.emitItemDone(item); err != nil {
			return err
		}
	case "message":
		text := t.openText.String()
		if err := writeSSE(t.w, "response.output_text.done", map[string]any{
			"type":    "response.output_text.done",
			"item_id": t.openID, "output_index": t.openIdx, "content_index": 0, "text": text,
		}); err != nil {
			return err
		}
		part := map[string]any{"type": "output_text", "text": text, "annotations": []any{}}
		if err := writeSSE(t.w, "response.content_part.done", map[string]any{
			"type":    "response.content_part.done",
			"item_id": t.openID, "output_index": t.openIdx, "content_index": 0, "part": part,
		}); err != nil {
			return err
		}
		if err := t.emitItemDone(map[string]any{
			"type": "message", "id": t.openID, "role": "assistant",
			"status": "completed", "content": []any{part},
		}); err != nil {
			return err
		}
	case "function_call":
		args := t.openArgs.String()
		if err := writeSSE(t.w, "response.function_call_arguments.done", map[string]any{
			"type":    "response.function_call_arguments.done",
			"item_id": t.openID, "output_index": t.openIdx, "arguments": args,
		}); err != nil {
			return err
		}
		if err := t.emitItemDone(map[string]any{
			"type": "function_call", "id": t.openID, "call_id": t.openCallID,
			"name": t.openName, "arguments": args, "status": "completed",
		}); err != nil {
			return err
		}
	}
	t.openKind = ""
	t.hasOpenTool = false
	return nil
}

func (t *ResponsesStreamTranslator) emitItemDone(item map[string]any) error {
	t.items = append(t.items, item)
	return writeSSE(t.w, "response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"output_index": len(t.items) - 1, "item": item,
	})
}

func (t *ResponsesStreamTranslator) emitCreated(chunk map[string]any) error {
	t.started = true
	model := asString(chunk["model"])
	if model == "" {
		model = t.model
	}
	t.respID = responsesID(asString(chunk["id"]))
	t.created = asInt(chunk["created"])

	placeholder := map[string]any{
		"id": t.respID, "object": "response", "created_at": t.created,
		"status": "in_progress", "model": model, "output": []any{},
		"usage": usageToResponses(nil),
	}
	if err := writeSSE(t.w, "response.created", map[string]any{
		"type": "response.created", "response": placeholder,
	}); err != nil {
		return err
	}
	return writeSSE(t.w, "response.in_progress", map[string]any{
		"type": "response.in_progress", "response": placeholder,
	})
}

// Abort 在上游流中断时收尾：发 error 事件，而不是 response.completed。
//
// 原来中断时走的是 Close，会补出 response.completed，
// 客户端据此认为回复完整 —— 截断被伪装成成功。
func (t *ResponsesStreamTranslator) Abort(reason string) error {
	return writeSSE(t.w, "error", map[string]any{
		"type":    "error",
		"code":    "upstream_error",
		"message": reason,
		"param":   nil,
	})
}

// Close 收尾并补出 response.completed。
// 仅用于上游**正常**结束的场合；中断请走 Abort。
func (t *ResponsesStreamTranslator) Close() error {
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
		if err := t.emitCreated(map[string]any{"model": t.model}); err != nil {
			return err
		}
	}
	if err := t.closeCurrent(); err != nil {
		return err
	}
	if t.completed {
		return nil
	}
	t.completed = true

	output := t.items
	if output == nil {
		output = []any{}
	}
	return writeSSE(t.w, "response.completed", map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id": t.respID, "object": "response", "created_at": t.created,
			"status": "completed", "model": t.model,
			"output":      output,
			"output_text": collectOutputText(output),
			"usage":       usageToResponses(t.usage),
		},
	})
}
