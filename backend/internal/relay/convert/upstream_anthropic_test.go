package convert

import (
	"encoding/json"
	"strings"
	"testing"
)

// OpenAI Chat 请求 -> Anthropic 请求。
//
// 这几条断言对应 Anthropic 会直接 400 的硬性要求：
// max_tokens 必填、system 在顶层、tool_result 必须在 user 消息里、
// 未知字段一律拒绝（所以 stream_options 这类必须丢掉）。
func TestOpenAIChatToAnthropicRequest(t *testing.T) {
	body := []byte(`{
		"model": "deepseek-v4-flash",
		"messages": [
			{"role": "system", "content": "你是助手"},
			{"role": "user", "content": "北京天气"},
			{"role": "assistant", "content": null, "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"北京\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "晴"}
		],
		"stream": true,
		"stream_options": {"include_usage": true},
		"temperature": 0.3,
		"stop": ["END"],
		"tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {"type": "object"}}}],
		"frequency_penalty": 1.0
	}`)
	out, err := OpenAIChatToAnthropicRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}

	if got["max_tokens"] == nil {
		t.Error("max_tokens 必须补齐：Anthropic 侧它是必填")
	}
	if got["system"] != "你是助手" {
		t.Errorf("system 应提到顶层，实际 %v", got["system"])
	}
	// 未知字段对 Anthropic 是 400，不能透传
	for _, k := range []string{"stream_options", "frequency_penalty"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s 不能发给 Anthropic（未知字段会 400）", k)
		}
	}
	if got["stop_sequences"] == nil {
		t.Error("OpenAI 的 stop 要映射成 stop_sequences")
	}

	msgs, _ := got["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("应剩 3 条消息（system 已提到顶层），实际 %d", len(msgs))
	}
	// assistant 的 tool_calls 要变成 tool_use 块
	asst := asMap(msgs[1])
	blocks, _ := asst["content"].([]any)
	var sawToolUse bool
	for _, b := range blocks {
		if asString(asMap(b)["type"]) == "tool_use" {
			sawToolUse = true
			if asMap(asMap(b)["input"])["city"] != "北京" {
				t.Errorf("工具参数应被解析成对象，实际 %v", asMap(b)["input"])
			}
		}
	}
	if !sawToolUse {
		t.Error("assistant 的 tool_calls 应转成 tool_use 内容块")
	}
	// tool 消息要变成 user 里的 tool_result
	last := asMap(msgs[2])
	if asString(last["role"]) != "user" {
		t.Errorf("tool 结果必须挂在 user 消息上，实际 %s", asString(last["role"]))
	}
	lastBlocks, _ := last["content"].([]any)
	if len(lastBlocks) == 0 || asString(asMap(lastBlocks[0])["type"]) != "tool_result" {
		t.Errorf("tool 消息应转成 tool_result 块，实际 %v", last["content"])
	}
}

// 连续的同角色消息要合并：Anthropic 要求 user / assistant 严格交替。
func TestOpenAIChatToAnthropicMergesSameRole(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"一"},
		{"role":"user","content":"二"},
		{"role":"assistant","content":"好"}
	]}`)
	out, _ := OpenAIChatToAnthropicRequest(body)
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("两条连续的 user 应合并成一条，实际 %d 条", len(msgs))
	}
	blocks, _ := asMap(msgs[0])["content"].([]any)
	if len(blocks) != 2 {
		t.Errorf("合并后应保留两个内容块，实际 %d", len(blocks))
	}
}

func TestOpenAIChatToAnthropicImage(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[
		{"type":"text","text":"看图"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}
	]}]}`)
	out, _ := OpenAIChatToAnthropicRequest(body)
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	msgs, _ := got["messages"].([]any)
	blocks, _ := asMap(msgs[0])["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("应有文本与图片两个块，实际 %d", len(blocks))
	}
	img := asMap(blocks[1])
	if asString(img["type"]) != "image" {
		t.Fatalf("第二个块应是 image，实际 %v", img["type"])
	}
	src := asMap(img["source"])
	if asString(src["type"]) != "base64" || asString(src["media_type"]) != "image/png" {
		t.Errorf("data URI 应拆成 base64 source，实际 %v", src)
	}
}

// Anthropic 响应 -> OpenAI 响应：正文、思维链、工具调用、结束原因、用量都要搬过来。
func TestAnthropicResponseToOpenAIChat(t *testing.T) {
	body := []byte(`{
		"id": "msg_abc", "type": "message", "role": "assistant", "model": "claude-x",
		"content": [
			{"type": "thinking", "thinking": "先想一下"},
			{"type": "text", "text": "答案是 42"},
			{"type": "tool_use", "id": "toolu_1", "name": "calc", "input": {"expr": "6*7"}}
		],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 10, "output_tokens": 20, "cache_read_input_tokens": 5, "cache_creation_input_tokens": 3}
	}`)
	out, err := AnthropicResponseToOpenAIChat(body, "")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)

	if got["object"] != "chat.completion" {
		t.Errorf("object 应为 chat.completion，实际 %v", got["object"])
	}
	if !strings.HasPrefix(asString(got["id"]), "chatcmpl-") {
		t.Errorf("id 应转成 chatcmpl- 前缀，实际 %v", got["id"])
	}
	choices, _ := got["choices"].([]any)
	choice := asMap(choices[0])
	if fr := asString(choice["finish_reason"]); fr != "tool_calls" {
		t.Errorf("tool_use 应映射成 tool_calls，实际 %s", fr)
	}
	msg := asMap(choice["message"])
	if asString(msg["content"]) != "答案是 42" {
		t.Errorf("正文应拼起来，实际 %v", msg["content"])
	}
	if asString(msg["reasoning"]) != "先想一下" {
		t.Errorf("thinking 应搬到 reasoning，实际 %v", msg["reasoning"])
	}
	calls, _ := msg["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("应有一个 tool_call，实际 %d", len(calls))
	}
	args := asString(asMap(asMap(calls[0])["function"])["arguments"])
	if !strings.Contains(args, "6*7") {
		t.Errorf("工具参数应序列化回字符串，实际 %s", args)
	}

	// 用量口径：prompt = input + cache_read，cache_read 走子集字段，
	// cache_creation 原样保留 —— 归一化后正好是 Anthropic 的并列口径
	usage := asMap(got["usage"])
	if asInt(usage["prompt_tokens"]) != 15 {
		t.Errorf("prompt_tokens 应为 input+cached=15，实际 %v", usage["prompt_tokens"])
	}
	if asInt(asMap(usage["prompt_tokens_details"])["cached_tokens"]) != 5 {
		t.Errorf("cached_tokens 应为 5，实际 %v", usage["prompt_tokens_details"])
	}
	if asInt(usage["cache_creation_input_tokens"]) != 3 {
		t.Errorf("cache_creation_input_tokens 应为 3，实际 %v", usage["cache_creation_input_tokens"])
	}
}

// 流式：Anthropic 事件流 -> OpenAI 分片，正文、工具调用参数、结束原因、用量一个都不能少。
func TestAnthropicStreamToOpenAIChat(t *testing.T) {
	upstream := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-x","usage":{"input_tokens":11,"cache_read_input_tokens":4}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"calc","input":{}}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"a\":1}"}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	stream := NewAnthropicStreamToOpenAIChat(ioNopCloser(strings.NewReader(upstream)), "fallback-model")
	raw, err := readAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)

	// 参数分片在 JSON 里是转义过的，用反引号原样写出期望值
	for _, want := range []string{"\"role\":\"assistant\"", "你好", "toolu_9", "calc", `{\"a\":1}`, "\"finish_reason\":\"tool_calls\"", "[DONE]"} {
		if !strings.Contains(text, want) {
			t.Errorf("转换后的流里缺少 %s\n实际内容:\n%s", want, text)
		}
	}
	// 用量分片必须存在，否则日志里的 token 会退化成按长度估算
	if !strings.Contains(text, "\"prompt_tokens\":15") || !strings.Contains(text, "\"completion_tokens\":7") {
		t.Errorf("缺少用量分片（prompt=11+4, completion=7）：\n%s", text)
	}
	// Anthropic 的事件名不该出现在下游流里
	if strings.Contains(text, "event:") {
		t.Errorf("下游流里不应再有 event: 行：\n%s", text)
	}
}

// 上游一条事件都没发就断了：也必须给出结构完整的分片，客户端才能判断「回复为空」。
func TestAnthropicStreamEmptyUpstream(t *testing.T) {
	stream := NewAnthropicStreamToOpenAIChat(ioNopCloser(strings.NewReader("")), "m")
	raw, err := readAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "\"role\":\"assistant\"") || !strings.Contains(text, "\"finish_reason\":\"stop\"") {
		t.Errorf("空流也要补齐起止分片，实际:\n%s", text)
	}
}

func TestUpstreamRequestSkipsNonChatPaths(t *testing.T) {
	body := []byte(`{"model":"m","input":"hi"}`)
	path, out, err := UpstreamRequest("anthropic-messages", "/v1/embeddings", body)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/embeddings" {
		t.Errorf("embeddings 不该被改成 /v1/messages，实际 %s", path)
	}
	if string(out) != string(body) {
		t.Error("embeddings 的载荷不该被改写")
	}
}

func TestUpstreamErrorMessage(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: Field required"}}`)
	got := UpstreamErrorMessage("anthropic-messages", body)
	if !strings.Contains(got, "max_tokens") || !strings.Contains(got, "invalid_request_error") {
		t.Errorf("应抽出类型与原因，实际 %q", got)
	}
}
