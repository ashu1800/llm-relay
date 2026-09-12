package convert

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// ---------- 请求转换 ----------

func TestAnthropicRequestSystemAndTools(t *testing.T) {
	src := `{
		"model": "claude-sonnet-4",
		"max_tokens": 1024,
		"system": [{"type": "text", "text": "你是助手"}, {"type": "text", "text": "回答要简短"}],
		"messages": [{"role": "user", "content": "北京天气"}],
		"tools": [{"name": "get_weather", "description": "查天气", "input_schema": {"type": "object"}}],
		"tool_choice": {"type": "any"},
		"stop_sequences": ["END"]
	}`

	got, err := AnthropicRequestToOpenAIChat([]byte(src))
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}

	msgs, _ := m["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("应转成 2 条消息（system + user），实际 %d", len(msgs))
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("首条应为 system，实际 %v", first["role"])
	}
	// 多个 system 文本块要按换行拼接
	if !strings.Contains(first["content"].(string), "你是助手") ||
		!strings.Contains(first["content"].(string), "回答要简短") {
		t.Fatalf("system 未正确拼接: %v", first["content"])
	}

	// tools 要包一层 function，input_schema 改名为 parameters
	tools, _ := m["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("应保留 1 个工具，实际 %d", len(tools))
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Fatalf("工具应包成 function 类型，实际 %v", tool["type"])
	}
	fn := tool["function"].(map[string]any)
	if _, ok := fn["parameters"]; !ok {
		t.Fatal("input_schema 应改名为 parameters")
	}
	// tool_choice any -> required
	if m["tool_choice"] != "required" {
		t.Fatalf("tool_choice any 应映射为 required，实际 %v", m["tool_choice"])
	}
	// stop_sequences -> stop
	if _, ok := m["stop"]; !ok {
		t.Fatal("stop_sequences 应改名为 stop")
	}
	if _, ok := m["stop_sequences"]; ok {
		t.Fatal("不应保留 stop_sequences")
	}
}

// Anthropic 把工具结果放在 user 消息的内容块里，OpenAI 要求独立的 role=tool 消息，
// 且必须紧跟触发它的 assistant 消息之后，否则上游会报消息顺序错误。
func TestAnthropicRequestToolResultBecomesSeparateMessage(t *testing.T) {
	src := `{
		"model": "m", "max_tokens": 100,
		"messages": [
			{"role": "assistant", "content": [
				{"type": "text", "text": "我来查一下"},
				{"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {"city": "北京"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_1", "content": [{"type": "text", "text": "晴 25 度"}]},
				{"type": "text", "text": "谢谢"}
			]}
		]
	}`

	got, err := AnthropicRequestToOpenAIChat([]byte(src))
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(got, &m)
	msgs, _ := m["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("应转成 3 条消息（assistant + tool + user），实际 %d", len(msgs))
	}

	asst := msgs[0].(map[string]any)
	calls, _ := asst["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("assistant 应带 1 个 tool_call，实际 %d", len(calls))
	}
	call := calls[0].(map[string]any)
	fn := call["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("工具名错误: %v", fn["name"])
	}
	// input 对象要序列化成 arguments 字符串
	args, _ := fn["arguments"].(string)
	if !strings.Contains(args, "北京") {
		t.Fatalf("arguments 应包含原始参数，实际 %s", args)
	}

	toolMsg := msgs[1].(map[string]any)
	if toolMsg["role"] != "tool" {
		t.Fatalf("第二条应为 role=tool，实际 %v", toolMsg["role"])
	}
	if toolMsg["tool_call_id"] != "toolu_1" {
		t.Fatalf("tool_call_id 未正确传递: %v", toolMsg["tool_call_id"])
	}
	if toolMsg["content"] != "晴 25 度" {
		t.Fatalf("工具结果内容错误: %v", toolMsg["content"])
	}
}

// ---------- 非流式响应转换 ----------

// OpenAI 的 prompt_tokens 含缓存命中，Anthropic 的 input_tokens 不含，
// 混用会让客户端把缓存部分重复计入成本。
func TestOpenAIToAnthropicResponseSplitsCachedTokens(t *testing.T) {
	src := `{
		"id": "chatcmpl-abc", "model": "gpt-5",
		"choices": [{"index": 0, "message": {"role": "assistant", "content": "你好"}, "finish_reason": "stop"}],
		"usage": {"prompt_tokens": 1000, "completion_tokens": 50, "total_tokens": 1050,
		          "prompt_tokens_details": {"cached_tokens": 800}}
	}`

	got, err := OpenAIChatToAnthropicResponse([]byte(src), "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(got, &m)

	if m["type"] != "message" || m["role"] != "assistant" {
		t.Fatalf("响应骨架错误: type=%v role=%v", m["type"], m["role"])
	}
	if !strings.HasPrefix(m["id"].(string), "msg_") {
		t.Fatalf("id 应带 msg_ 前缀，实际 %v", m["id"])
	}
	if m["stop_reason"] != "end_turn" {
		t.Fatalf("finish_reason stop 应映射为 end_turn，实际 %v", m["stop_reason"])
	}

	u := m["usage"].(map[string]any)
	if u["input_tokens"] != float64(200) {
		t.Fatalf("input_tokens 应为 1000-800=200，实际 %v", u["input_tokens"])
	}
	if u["output_tokens"] != float64(50) {
		t.Fatalf("output_tokens 应为 50，实际 %v", u["output_tokens"])
	}
	if u["cache_read_input_tokens"] != float64(800) {
		t.Fatalf("cache_read_input_tokens 应为 800，实际 %v", u["cache_read_input_tokens"])
	}

	content := m["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("应有 1 个内容块，实际 %d", len(content))
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "你好" {
		t.Fatalf("文本块错误: %v", block)
	}
}

func TestOpenAIToAnthropicResponseToolCalls(t *testing.T) {
	src := `{
		"id": "c1", "model": "m",
		"choices": [{"index": 0, "finish_reason": "tool_calls", "message": {
			"role": "assistant", "content": null,
			"tool_calls": [{"id": "call_1", "type": "function",
				"function": {"name": "f", "arguments": "{\"a\":1}"}}]}}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 2}
	}`

	got, _ := OpenAIChatToAnthropicResponse([]byte(src), "")
	var m map[string]any
	_ = json.Unmarshal(got, &m)

	if m["stop_reason"] != "tool_use" {
		t.Fatalf("finish_reason tool_calls 应映射为 tool_use，实际 %v", m["stop_reason"])
	}
	content := m["content"].([]any)
	block := content[0].(map[string]any)
	if block["type"] != "tool_use" {
		t.Fatalf("应为 tool_use 块，实际 %v", block["type"])
	}
	// arguments 字符串要还原成对象
	input := block["input"].(map[string]any)
	if input["a"] != float64(1) {
		t.Fatalf("arguments 未正确还原: %v", input)
	}
}

func TestMapFinishReason(t *testing.T) {
	cases := map[string]string{
		"stop": "end_turn", "length": "max_tokens",
		"tool_calls": "tool_use", "content_filter": "end_turn", "": "end_turn",
	}
	for in, want := range cases {
		if got := mapFinishReason(in); got != want {
			t.Errorf("mapFinishReason(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// ---------- 流式转换 ----------

func TestAnthropicStreamTranslatorTextFlow(t *testing.T) {
	var buf bytes.Buffer
	tr := NewAnthropicStreamTranslator(&buf, "gpt-4o")

	feed := []string{
		"data: {\"id\":\"c1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"你好\"},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"c1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"世界\"},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"c1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":7,\"total_tokens\":107}}\n\n",
		"data: [DONE]\n\n",
	}
	for _, s := range feed {
		if _, err := tr.Write([]byte(s)); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("收尾失败: %v", err)
	}

	got := buf.String()
	// 事件必须按 Anthropic 规范排序，否则客户端会解析失败
	order := []string{"event: message_start", "event: content_block_start",
		"event: content_block_delta", "event: content_block_stop",
		"event: message_delta", "event: message_stop"}
	last := -1
	for _, ev := range order {
		idx := strings.Index(got, ev)
		if idx < 0 {
			t.Fatalf("缺少事件 %q，实际输出:\n%s", ev, got)
		}
		if idx < last {
			t.Fatalf("事件顺序错误：%q 出现在期望位置之前", ev)
		}
		last = idx
	}
	if !strings.Contains(got, "你好") || !strings.Contains(got, "世界") {
		t.Fatalf("文本增量丢失:\n%s", got)
	}
	if !strings.Contains(got, "\"text_delta\"") {
		t.Fatalf("应使用 text_delta 类型:\n%s", got)
	}
	// 收尾的 usage 要带上游统计
	if !strings.Contains(got, "\"output_tokens\":7") {
		t.Fatalf("message_delta 未带上 output_tokens:\n%s", got)
	}
	if !strings.Contains(got, "\"input_tokens\":100") {
		t.Fatalf("message_delta 未带上 input_tokens:\n%s", got)
	}
	if !strings.Contains(got, "\"stop_reason\":\"end_turn\"") {
		t.Fatalf("stop_reason 未正确映射:\n%s", got)
	}
}

// OpenAI 的工具参数是零散分片，必须映射成独立的 tool_use 块与 input_json_delta。
func TestAnthropicStreamTranslatorToolCalls(t *testing.T) {
	var buf bytes.Buffer
	tr := NewAnthropicStreamTranslator(&buf, "m")

	feed := []string{
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"city\\\":\"}}]},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"北京\\\"}\"}}]},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n",
	}
	for _, s := range feed {
		if _, err := tr.Write([]byte(s)); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("收尾失败: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "\"type\":\"tool_use\"") {
		t.Fatalf("未生成 tool_use 块:\n%s", got)
	}
	if !strings.Contains(got, "\"name\":\"get_weather\"") {
		t.Fatalf("工具名丢失:\n%s", got)
	}
	if !strings.Contains(got, "\"id\":\"call_1\"") {
		t.Fatalf("工具调用 id 丢失:\n%s", got)
	}
	if !strings.Contains(got, "input_json_delta") {
		t.Fatalf("应使用 input_json_delta:\n%s", got)
	}
	if !strings.Contains(got, "\"stop_reason\":\"tool_use\"") {
		t.Fatalf("stop_reason 应为 tool_use:\n%s", got)
	}
	// 参数分片要完整拼回
	if !strings.Contains(got, "city") || !strings.Contains(got, "北京") {
		t.Fatalf("参数分片丢失:\n%s", got)
	}
}

// 上游一个事件都没发就断开时，也要补齐事件，否则客户端会一直等。
func TestAnthropicStreamTranslatorEmptyUpstream(t *testing.T) {
	var buf bytes.Buffer
	tr := NewAnthropicStreamTranslator(&buf, "m")
	if _, err := tr.Write([]byte("")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("收尾失败: %v", err)
	}
	got := buf.String()
	for _, ev := range []string{"message_start", "message_delta", "message_stop"} {
		if !strings.Contains(got, ev) {
			t.Fatalf("空流也应补出 %s:\n%s", ev, got)
		}
	}
}

// 分片边界与事件边界无关，任意切分都要能正确解析。
func TestAnthropicStreamTranslatorSplitAcrossWrites(t *testing.T) {
	var buf bytes.Buffer
	tr := NewAnthropicStreamTranslator(&buf, "m")
	full := "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"abc\"},\"finish_reason\":\"stop\"}]}\n\n"

	// 每 7 个字节切一次，刻意切在 JSON 中间
	for i := 0; i < len(full); i += 7 {
		end := i + 7
		if end > len(full) {
			end = len(full)
		}
		if _, err := tr.Write([]byte(full[i:end])); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("收尾失败: %v", err)
	}
	if !strings.Contains(buf.String(), "abc") {
		t.Fatalf("跨分片内容丢失:\n%s", buf.String())
	}
}

func TestAnthropicErrorShape(t *testing.T) {
	raw := AnthropicError(429, "限流了")
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("错误体不是合法 JSON: %v", err)
	}
	if m["type"] != "error" {
		t.Fatalf("type 应为 error，实际 %v", m["type"])
	}
	inner := m["error"].(map[string]any)
	if inner["type"] != "rate_limit_error" {
		t.Fatalf("429 应映射为 rate_limit_error，实际 %v", inner["type"])
	}
}
