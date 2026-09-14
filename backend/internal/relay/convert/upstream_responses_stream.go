package convert

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// 上游 Responses 事件流 -> OpenAI Chat SSE 分片。
//
// 上游发的是 event+data 事件流（response.created / output_text.delta /
// function_call_arguments.delta / response.completed ...），站内统一以
// OpenAI 的 data-only 分片流转（客户端那一侧再由入站改写器转成它要的协议）。
// 这里实时逐事件改写，不缓冲整个响应。
//
// 报文字段依据智谱 /api/v1/responses 实测（2026-09）：
//   - 正文：response.output_text.delta（内容在 delta）
//   - 思维链：response.reasoning_text.delta（官方 OpenAI 用
//     response.reasoning_summary_text.delta，两种都收）
//   - 工具调用：response.output_item.added(item.type=function_call) 给
//     name/call_id/id，随后 response.function_call_arguments.delta 给参数片段
//   - 用量：大部分事件不带，只有 response.completed 的 response.usage 有

// NewResponsesStreamToOpenAIChat 包装上游的 Responses 事件流。
// 返回的 ReadCloser 读出的是 OpenAI Chat SSE 分片，Close 会关闭上游连接。
func NewResponsesStreamToOpenAIChat(src io.ReadCloser, model string) io.ReadCloser {
	return &responsesUpstreamStream{
		src:      src,
		model:    model,
		out:      make([]byte, 0, 4096),
		tools:    map[string]int{},
		toolArgs: map[int]string{},
	}
}

type responsesUpstreamStream struct {
	src   io.ReadCloser
	model string
	buf   []byte // 上游原始字节的未消费部分
	out   []byte // 已转换、等待被读走的字节

	started  bool
	finished bool

	id        string
	modelSeen string
	// hasTool 记录上游是否发过工具调用，收尾时决定 finish_reason
	hasTool bool
	// truncated 记录上游因 max_output_tokens 截断，收尾时 finish_reason 要给 length
	truncated bool
	usage     map[string]any
	// tools 把 Responses 的输出项 id / call_id 映射成 OpenAI 的 tool_call 序号
	tools map[string]int
	// toolArgs 记录每个 tool_call 已经发给客户端的参数，用于在 output_item.done
	// 时补齐上游没有以 delta 形式发出的部分
	toolArgs map[int]string
	// curEvent 记录最近一条 event: 行
	curEvent string
	readErr  error
	writeErr error
}

// Read 从转换结果里吐出字节，必要时继续从上游读并转换。
func (s *responsesUpstreamStream) Read(p []byte) (int, error) {
	for len(s.out) == 0 {
		if s.writeErr != nil {
			return 0, s.writeErr
		}
		if s.readErr != nil {
			return 0, s.readErr
		}
		buf := make([]byte, 16*1024)
		n, err := s.src.Read(buf)
		if n > 0 {
			s.feed(buf[:n])
		}
		if err != nil {
			// 上游断流时先冲刷已转换的内容，再收尾
			s.finish()
			s.readErr = err
			if err != io.EOF {
				return 0, err
			}
			break
		}
	}
	if len(s.out) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.out)
	s.out = s.out[n:]
	return n, nil
}

// Close 关闭上游连接。
func (s *responsesUpstreamStream) Close() error { return s.src.Close() }

// feed 逐行解析上游 SSE。
func (s *responsesUpstreamStream) feed(p []byte) {
	splitter := &sseSplitter{buf: s.buf}
	splitter.feed(p, func(line []byte) {
		if s.writeErr != nil {
			return
		}
		if err := s.handleLine(line); err != nil {
			s.writeErr = err
		}
	})
	s.buf = splitter.buf
}

func (s *responsesUpstreamStream) handleLine(line []byte) error {
	if len(line) == 0 {
		return nil
	}
	if bytes.HasPrefix(line, []byte("event:")) {
		s.curEvent = string(bytes.TrimSpace(line[len("event:"):]))
		return nil
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	if len(payload) == 0 || payload[0] != '{' {
		return nil
	}
	var evt map[string]any
	if err := json.Unmarshal(payload, &evt); err != nil {
		// 解析不了的分片直接跳过：一条坏事件不该打断整个流
		return nil
	}
	kind := asString(evt["type"])
	if kind == "" {
		kind = s.curEvent
	}
	switch kind {
	case "response.created", "response.in_progress":
		return s.onStart(asMap(evt["response"]))
	case "response.output_item.added":
		return s.onItemAdded(asInt(evt["output_index"]), asMap(evt["item"]))
	case "response.output_text.delta", "response.refusal.delta":
		text := asString(evt["delta"])
		// 有的实现把它放在 part.text 里
		if text == "" {
			text = asString(asMap(evt["part"])["text"])
		}
		if text == "" {
			return nil
		}
		return s.appendChunk(map[string]any{"content": text}, nil)
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		text := asString(evt["delta"])
		if text == "" {
			return nil
		}
		return s.appendChunk(map[string]any{"reasoning": text}, nil)
	case "response.function_call_arguments.delta":
		return s.onToolDelta(evt)
	case "response.output_item.done":
		// 工具调用的参数可能只在 done 事件里给全（上游没发 delta 时），
		// 这里补上差值，避免参数丢失
		return s.onItemDone(asMap(evt["item"]))
	case "response.completed", "response.incomplete", "response.failed":
		if r := asMap(evt["response"]); r != nil {
			if u := asMap(r["usage"]); u != nil {
				s.usage = u
			}
			if asString(r["status"]) == "incomplete" {
				s.truncated = true
			}
		}
		// response.completed 之后通常还有一条同名事件，重复收尾是幂等的
		s.finish()
		return nil
	case "error":
		msg := ""
		if e := asMap(evt["error"]); e != nil {
			msg = asString(e["message"])
		}
		if msg == "" {
			msg = asString(evt["message"])
		}
		if msg == "" {
			msg = "上游流式响应报错"
		}
		s.appendSSE(map[string]any{
			"error": map[string]any{"message": msg, "type": "upstream_error"},
		})
		s.finish()
		return nil
	default:
		// response.content_part.* / *.done / ping 等无需转换
		return nil
	}
}

// onStart 收到 response.created：发第一条分片（带 role）。
func (s *responsesUpstreamStream) onStart(resp map[string]any) error {
	if resp == nil {
		return nil
	}
	if id := asString(resp["id"]); id != "" {
		s.id = responsesToOpenAIID(id)
	}
	if m := asString(resp["model"]); m != "" {
		s.modelSeen = m
	}
	if s.started {
		return nil
	}
	s.started = true
	return s.appendChunk(map[string]any{"role": "assistant", "content": ""}, nil)
}

// onItemAdded 只关心 function_call：它要立刻把头分片发出去
// （OpenAI 的 tool_call 分片里带 id 与函数名，后续分片只补 arguments）。
func (s *responsesUpstreamStream) onItemAdded(_ int, item map[string]any) error {
	if item == nil || asString(item["type"]) != "function_call" {
		return nil
	}
	// 确保已经发过带 role 的首片
	if !s.started {
		s.started = true
		if err := s.appendChunk(map[string]any{"role": "assistant", "content": ""}, nil); err != nil {
			return err
		}
	}
	key := responsesToolKey(item)
	toolIndex := len(s.tools)
	s.tools[key] = toolIndex
	s.hasTool = true
	return s.appendChunk(map[string]any{
		"tool_calls": []any{map[string]any{
			"index": toolIndex,
			"id":    responsesCallID(item),
			"type":  "function",
			"function": map[string]any{
				"name":      asString(item["name"]),
				"arguments": "",
			},
		}},
	}, nil)
}

// onToolDelta 转换工具参数增量。
func (s *responsesUpstreamStream) onToolDelta(evt map[string]any) error {
	delta := asString(evt["delta"])
	if delta == "" {
		return nil
	}
	toolIndex := s.toolIndexFor(evt)
	s.toolArgs[toolIndex] += delta
	return s.appendChunk(map[string]any{
		"tool_calls": []any{map[string]any{
			"index":    toolIndex,
			"function": map[string]any{"arguments": delta},
		}},
	}, nil)
}

// onItemDone 在工具项结束时补齐上游没以 delta 形式发出的参数。
//
// 只在「已发出去的是完整参数的前缀」时才补差值：两者不一致说明上游的 delta 与
// done 给的不是同一份内容，此时硬补会把参数拼坏（客户端拿到的 JSON 直接解析失败）。
func (s *responsesUpstreamStream) onItemDone(item map[string]any) error {
	if item == nil || asString(item["type"]) != "function_call" {
		return nil
	}
	args := asString(item["arguments"])
	if args == "" {
		return nil
	}
	idx, ok := s.tools[responsesToolKey(item)]
	if !ok {
		return nil
	}
	sent := s.toolArgs[idx]
	if sent == args {
		return nil
	}
	if !strings.HasPrefix(args, sent) {
		return nil
	}
	missing := args[len(sent):]
	if missing == "" {
		return nil
	}
	s.toolArgs[idx] = args
	return s.appendChunk(map[string]any{
		"tool_calls": []any{map[string]any{
			"index":    idx,
			"function": map[string]any{"arguments": missing},
		}},
	}, nil)
}

// toolIndexFor 找出该事件对应的 tool_call 序号。
//
// 上游把 item_id 放在 item_id 字段，call_id 有的实现放在 call_id、有的只在
// output_item.added 里出现过，所以三个键都试一遍；都找不到时退回上一个工具项
// （单工具调用场景下这是对的，多工具时也不会串到别的序号上）。
func (s *responsesUpstreamStream) toolIndexFor(evt map[string]any) int {
	for _, k := range []string{"item_id", "call_id", "output_index"} {
		if key := asString(evt[k]); key != "" {
			if idx, ok := s.tools[key]; ok {
				return idx
			}
		}
	}
	if idx, ok := s.lastTool(); ok {
		return idx
	}
	return 0
}

func (s *responsesUpstreamStream) lastTool() (int, bool) {
	if len(s.tools) == 0 {
		return 0, false
	}
	max := 0
	for _, idx := range s.tools {
		if idx > max {
			max = idx
		}
	}
	return max, true
}

// responsesToolKey 为工具项生成稳定的键：优先 call_id，其次 item id。
func responsesToolKey(item map[string]any) string {
	if v := asString(item["call_id"]); v != "" {
		return v
	}
	if v := asString(item["id"]); v != "" {
		return v
	}
	return "tool"
}

// responsesCallID 取 OpenAI 侧要用的 tool_call id：优先 call_id，退回 id。
func responsesCallID(item map[string]any) string {
	if v := asString(item["call_id"]); v != "" {
		return v
	}
	return asString(item["id"])
}

// finish 收尾：补一条带 finish_reason 的分片、一条带 usage 的分片，再写 [DONE]。
func (s *responsesUpstreamStream) finish() {
	if s.finished {
		return
	}
	s.finished = true
	// 上游一条事件都没发出来时也要给出结构完整的分片，
	// 否则客户端拿到的是空响应体，连「回复为空」都判断不了
	if !s.started {
		s.started = true
		_ = s.appendChunk(map[string]any{"role": "assistant", "content": ""}, nil)
	}
	reason := "stop"
	switch {
	case s.truncated:
		reason = "length"
	case s.hasTool:
		reason = "tool_calls"
	}
	_ = s.appendChunk(map[string]any{}, &reason)
	// usage 单独一条、choices 为空：与 OpenAI 的 include_usage 行为一致，
	// 站内的用量抓取（UsageTee）正是按这个位置取数
	if usage := responsesUsageToOpenAI(s.usage); usage != nil {
		s.appendSSE(map[string]any{
			"id": s.id, "object": "chat.completion.chunk", "created": anthropicCreated(),
			"model": s.modelName(), "choices": []any{}, "usage": usage,
		})
	}
	s.appendRaw("[DONE]")
}

func (s *responsesUpstreamStream) modelName() string {
	if s.modelSeen != "" {
		return s.modelSeen
	}
	return s.model
}

// appendChunk 写一条对话分片。
func (s *responsesUpstreamStream) appendChunk(delta map[string]any, finishReason *string) error {
	choice := map[string]any{"index": 0, "delta": delta}
	if finishReason != nil {
		choice["finish_reason"] = *finishReason
	} else {
		choice["finish_reason"] = nil
	}
	if s.id == "" {
		s.id = newCompletionID()
	}
	return s.appendSSE(map[string]any{
		"id": s.id, "object": "chat.completion.chunk", "created": anthropicCreated(),
		"model": s.modelName(), "choices": []any{choice},
	})
}

// appendSSE 写出 data 分片。
func (s *responsesUpstreamStream) appendSSE(payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	s.out = append(s.out, "data: "...)
	s.out = append(s.out, raw...)
	s.out = append(s.out, '\n', '\n')
	return nil
}

func (s *responsesUpstreamStream) appendRaw(payload string) {
	s.out = append(s.out, "data: "...)
	s.out = append(s.out, payload...)
	s.out = append(s.out, '\n', '\n')
}
