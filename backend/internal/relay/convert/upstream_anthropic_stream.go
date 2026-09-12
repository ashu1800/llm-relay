package convert

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"
)

// 上游 Anthropic 事件流 -> OpenAI Chat SSE 分片。
//
// 上游发的是 event+data 的事件流（message_start / content_block_delta / message_delta ...），
// 而站内统一以 OpenAI 的 data-only 分片流转（客户端那一侧再由入站改写器转成
// 它要的协议）。这里做的是实时逐事件改写，不缓冲整个响应。

// NewAnthropicStreamToOpenAIChat 包装上游的事件流。
// 返回的 ReadCloser 读出的是 OpenAI Chat SSE 分片，Close 会关闭上游连接。
func NewAnthropicStreamToOpenAIChat(src io.ReadCloser, model string) io.ReadCloser {
	return &anthropicUpstreamStream{
		src:    src,
		model:  model,
		out:    make([]byte, 0, 4096),
		blocks: map[int]string{},
		tools:  map[int]int{},
	}
}

type anthropicUpstreamStream struct {
	src   io.ReadCloser
	model string
	buf   []byte // 上游原始字节的未消费部分
	out   []byte // 已转换、等待被读走的字节

	started  bool
	finished bool
	done     bool // 已写出 [DONE]

	id         string
	modelSeen  string
	stopReason string
	usage      map[string]any
	// blocks 记录每个内容块的类型（text / thinking / tool_use）
	blocks map[int]string
	// tools 把 Anthropic 的内容块序号映射成 OpenAI 的 tool_call 序号
	tools map[int]int
	// curEvent 记录最近一条 event: 行，Anthropic 的事件类型在这里而不是 data 里
	curEvent string
	readErr  error
}

// Read 从转换结果里吐出字节，必要时继续从上游读并转换。
func (s *anthropicUpstreamStream) Read(p []byte) (int, error) {
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
func (s *anthropicUpstreamStream) Close() error { return s.src.Close() }

// feed 逐行解析上游 SSE。
func (s *anthropicUpstreamStream) feed(p []byte) {
	var werr error
	splitter := &sseSplitter{buf: s.buf}
	splitter.feed(p, func(line []byte) {
		if werr != nil {
			return
		}
		werr = s.handleLine(line)
	})
	s.buf = splitter.buf
	_ = werr
}

func (s *anthropicUpstreamStream) handleLine(line []byte) error {
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
	case "message_start":
		return s.onMessageStart(asMap(evt["message"]))
	case "content_block_start":
		return s.onBlockStart(asInt(evt["index"]), asMap(evt["content_block"]))
	case "content_block_delta":
		return s.onBlockDelta(asInt(evt["index"]), asMap(evt["delta"]))
	case "message_delta":
		if d := asMap(evt["delta"]); d != nil {
			if fr := asString(d["stop_reason"]); fr != "" {
				s.stopReason = fr
			}
		}
		if u := asMap(evt["usage"]); u != nil {
			s.mergeUsage(u)
		}
		return nil
	case "message_stop":
		s.finish()
		return nil
	case "error":
		// 上游中途报错：把它作为一条 data 分片发出去（OpenAI 的流里没有错误事件的
		// 标准结构），再正常收尾，客户端至少能看到原因而不是一直等
		msg := ""
		if e := asMap(evt["error"]); e != nil {
			msg = asString(e["message"])
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
		// ping / content_block_stop 等无需转换
		return nil
	}
}

// onMessageStart 收到 message_start：发第一条分片（带 role），并记下令牌用量。
func (s *anthropicUpstreamStream) onMessageStart(message map[string]any) error {
	if message == nil {
		return nil
	}
	s.id = openAIID(asString(message["id"]))
	s.modelSeen = asString(message["model"])
	if u := asMap(message["usage"]); u != nil {
		s.mergeUsage(u)
	}
	if s.started {
		return nil
	}
	s.started = true
	return s.appendChunk(map[string]any{"role": "assistant", "content": ""}, nil)
}

// onBlockStart 记录内容块类型；工具调用要立刻把头分片发出去
// （OpenAI 的 tool_call 分片里带 id 与函数名，后续分片只补 arguments）。
func (s *anthropicUpstreamStream) onBlockStart(index int, block map[string]any) error {
	if block == nil {
		return nil
	}
	kind := asString(block["type"])
	s.blocks[index] = kind
	if kind != "tool_use" {
		return nil
	}
	toolIndex := len(s.tools)
	s.tools[index] = toolIndex
	return s.appendChunk(map[string]any{
		"tool_calls": []any{map[string]any{
			"index": toolIndex,
			"id":    asString(block["id"]),
			"type":  "function",
			"function": map[string]any{
				"name":      asString(block["name"]),
				"arguments": "",
			},
		}},
	}, nil)
}

// onBlockDelta 转换内容块增量。
func (s *anthropicUpstreamStream) onBlockDelta(index int, delta map[string]any) error {
	if delta == nil {
		return nil
	}
	switch asString(delta["type"]) {
	case "text_delta":
		return s.appendChunk(map[string]any{"content": asString(delta["text"])}, nil)
	case "thinking_delta":
		// 站内统一用 reasoning 承载思维链，与入站方向对称
		return s.appendChunk(map[string]any{"reasoning": asString(delta["thinking"])}, nil)
	case "input_json_delta":
		toolIndex, ok := s.tools[index]
		if !ok {
			toolIndex = 0
		}
		return s.appendChunk(map[string]any{
			"tool_calls": []any{map[string]any{
				"index":    toolIndex,
				"function": map[string]any{"arguments": asString(delta["partial_json"])},
			}},
		}, nil)
	default:
		return nil
	}
}

// mergeUsage 合并用量：message_start 给输入，message_delta 给输出。
func (s *anthropicUpstreamStream) mergeUsage(u map[string]any) {
	if s.usage == nil {
		s.usage = map[string]any{}
	}
	for k, v := range u {
		s.usage[k] = v
	}
}

// finish 收尾：补一条带 finish_reason 的分片、一条带 usage 的分片，再写 [DONE]。
func (s *anthropicUpstreamStream) finish() {
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
	reason := anthropicStopReasonToOpenAI(s.stopReason)
	_ = s.appendChunk(map[string]any{}, &reason)
	// usage 单独一条、choices 为空：与 OpenAI 的 include_usage 行为一致，
	// 站内的用量抓取（UsageTee）正是按这个位置取数
	if usage := anthropicUsageToOpenAI(s.usage); usage != nil {
		s.appendSSE(map[string]any{
			"id": s.id, "object": "chat.completion.chunk", "created": anthropicCreated(),
			"model": s.modelName(), "choices": []any{}, "usage": usage,
		})
	}
	s.appendRaw("[DONE]")
}

func (s *anthropicUpstreamStream) modelName() string {
	if s.modelSeen != "" {
		return s.modelSeen
	}
	return s.model
}

// appendChunk 写一条对话分片。
func (s *anthropicUpstreamStream) appendChunk(delta map[string]any, finishReason *string) error {
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
func (s *anthropicUpstreamStream) appendSSE(payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	s.out = append(s.out, "data: "...)
	s.out = append(s.out, raw...)
	s.out = append(s.out, '\n', '\n')
	return nil
}

func (s *anthropicUpstreamStream) appendRaw(payload string) {
	s.out = append(s.out, "data: "...)
	s.out = append(s.out, payload...)
	s.out = append(s.out, '\n', '\n')
}

// ---------- 小工具 ----------

// anthropicCreated 生成 OpenAI 的 created 字段（秒级时间戳）。
func anthropicCreated() int64 { return time.Now().Unix() }

// newCompletionID 生成一个 OpenAI 形状的补全 id。
// 上游（Anthropic）不给 OpenAI 形状的 id，自己造一个比伪造上游 id 更诚实。
func newCompletionID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "chatcmpl-relay"
	}
	return "chatcmpl-" + hex.EncodeToString(buf)
}
