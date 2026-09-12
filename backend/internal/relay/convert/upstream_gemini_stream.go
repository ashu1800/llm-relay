package convert

import (
	"encoding/json"
	"io"
)

// 上游 Gemini 的 SSE 流 -> OpenAI Chat 分片。
//
// 与 Anthropic 那套的区别：Gemini 没有事件名，每帧就是一个
// GenerateContentResponse 的片段；usageMetadata 是**累计值**（每帧都带，
// 数值只增不减），所以只能取最后一次，不能像 Anthropic 那样把
// message_start / message_delta 的用量合并起来 —— 累加会把 token 数乘上帧数。

// NewGeminiStreamToOpenAIChat 包装上游的 SSE 流。
func NewGeminiStreamToOpenAIChat(src io.ReadCloser, model string) io.ReadCloser {
	return &geminiUpstreamStream{
		src:   src,
		model: model,
		out:   make([]byte, 0, 4096),
	}
}

type geminiUpstreamStream struct {
	src   io.ReadCloser
	model string
	buf   []byte
	out   []byte

	id         string
	started    bool
	finished   bool
	stopReason string
	usage      map[string]any
	readErr    error
}

// Read 从转换结果里吐出字节。
func (s *geminiUpstreamStream) Read(p []byte) (int, error) {
	for len(s.out) == 0 {
		if s.readErr != nil {
			if len(s.out) == 0 {
				return 0, s.readErr
			}
			break
		}
		buf := make([]byte, 16*1024)
		n, err := s.src.Read(buf)
		if n > 0 {
			s.feed(buf[:n])
		}
		if err != nil {
			// Gemini 的流没有结束事件，靠连接关闭表示结束：
			// 正常收尾要自己补出 finish_reason 与 [DONE]
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
func (s *geminiUpstreamStream) Close() error { return s.src.Close() }

func (s *geminiUpstreamStream) feed(p []byte) {
	splitter := &sseSplitter{buf: s.buf}
	splitter.feed(p, func(line []byte) {
		payload := dataPayload(line)
		if payload == nil {
			return
		}
		s.handleChunk(payload)
	})
	s.buf = splitter.buf
}

func (s *geminiUpstreamStream) handleChunk(payload []byte) {
	var chunk map[string]any
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return // 忽略坏分片，不打断整个流
	}
	// 上游把错误塞在流里：转成一条错误分片再收尾，别让客户端一直等
	if e := asMap(chunk["error"]); e != nil {
		msg := asString(e["message"])
		if msg == "" {
			msg = "上游流式响应报错"
		}
		_ = s.appendSSE(map[string]any{
			"error": map[string]any{"message": msg, "type": "upstream_error"},
		})
		s.finish()
		return
	}
	if u := asMap(chunk["usageMetadata"]); u != nil {
		s.usage = u // 累计值，覆盖而不是合并
	}
	candidates, _ := chunk["candidates"].([]any)
	if len(candidates) == 0 {
		return
	}
	cand := asMap(candidates[0])
	if cand == nil {
		return
	}
	if fr := asString(cand["finishReason"]); fr != "" {
		s.stopReason = fr
	}
	content := asMap(cand["content"])
	if content == nil {
		return
	}
	parts, _ := content["parts"].([]any)
	for _, praw := range parts {
		p := asMap(praw)
		if p == nil {
			continue
		}
		if isThought, _ := p["thought"].(bool); isThought {
			if t := asString(p["text"]); t != "" {
				_ = s.appendChunk(map[string]any{"reasoning": t}, nil)
			}
			continue
		}
		if t := asString(p["text"]); t != "" {
			_ = s.appendChunk(map[string]any{"content": t}, nil)
		}
		if fc := asMap(p["functionCall"]); fc != nil {
			name := asString(fc["name"])
			args, _ := json.Marshal(fc["args"])
			_ = s.appendChunk(map[string]any{
				"tool_calls": []any{map[string]any{
					"index": 0, "id": "call_" + name, "type": "function",
					"function": map[string]any{"name": name, "arguments": string(args)},
				}},
			}, nil)
		}
	}
}

// finish 收尾：补 finish_reason、usage 分片与 [DONE]。
func (s *geminiUpstreamStream) finish() {
	if s.finished {
		return
	}
	s.finished = true
	if !s.started {
		s.started = true
		_ = s.appendChunk(map[string]any{"role": "assistant", "content": ""}, nil)
	}
	reason := geminiFinishReasonToOpenAI(s.stopReason)
	if s.stopReason == "" {
		// Gemini 正常结束时最后一帧通常带 STOP；没带也按正常收尾处理，
		// 否则客户端会以为被截断
		reason = "stop"
	}
	_ = s.appendChunk(map[string]any{}, &reason)
	if usage := geminiUsageToOpenAI(s.usage); usage != nil {
		_ = s.appendSSE(map[string]any{
			"id": s.id, "object": "chat.completion.chunk", "created": anthropicCreated(),
			"model": s.model, "choices": []any{}, "usage": usage,
		})
	}
	s.appendRaw("[DONE]")
}

func (s *geminiUpstreamStream) appendChunk(delta map[string]any, finishReason *string) error {
	if s.id == "" {
		s.id = newCompletionID()
	}
	choice := map[string]any{"index": 0, "delta": delta}
	if finishReason != nil {
		choice["finish_reason"] = *finishReason
	} else {
		choice["finish_reason"] = nil
	}
	return s.appendSSE(map[string]any{
		"id": s.id, "object": "chat.completion.chunk", "created": anthropicCreated(),
		"model": s.model, "choices": []any{choice},
	})
}

func (s *geminiUpstreamStream) appendSSE(payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	s.out = append(s.out, "data: "...)
	s.out = append(s.out, raw...)
	s.out = append(s.out, '\n', '\n')
	return nil
}

// appendRaw 写一条非 JSON 的 data 行（只有 [DONE] 用得上）。
func (s *geminiUpstreamStream) appendRaw(payload string) {
	s.out = append(s.out, "data: "...)
	s.out = append(s.out, payload...)
	s.out = append(s.out, '\n', '\n')
}
