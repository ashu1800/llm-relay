package convert

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// ---------- 请求转换 ----------

func TestResponsesRequestBasics(t *testing.T) {
	src := `{
		"model": "gpt-5",
		"instructions": "你是助手",
		"input": "你好",
		"max_output_tokens": 512,
		"tools": [{"type": "function", "name": "get_weather", "description": "查天气",
		           "parameters": {"type": "object", "properties": {}}}],
		"tool_choice": {"type": "function", "name": "get_weather"}
	}`

	got, err := ResponsesRequestToOpenAIChat([]byte(src))
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(got, &m)

	if m["max_tokens"] != float64(512) {
		t.Fatalf("max_output_tokens 应映射为 max_tokens，实际 %v", m["max_tokens"])
	}
	if _, ok := m["max_output_tokens"]; ok {
		t.Fatal("不应保留 max_output_tokens")
	}

	msgs := m["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("应为 system + user 两条，实际 %d", len(msgs))
	}
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("instructions 应转成 system，实际 %v", msgs[0])
	}
	if msgs[1].(map[string]any)["content"] != "你好" {
		t.Fatalf("字符串 input 应转成 user 消息，实际 %v", msgs[1])
	}

	// Responses 的工具是扁平结构，要包一层 function
	tools := m["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("工具名错误: %v", fn["name"])
	}
	if _, ok := fn["parameters"]; !ok {
		t.Fatal("parameters 应保留在 function 内")
	}
	tc := m["tool_choice"].(map[string]any)
	if tc["type"] != "function" {
		t.Fatalf("tool_choice 应转成 function 类型，实际 %v", tc)
	}
}

// Responses 的 input 数组可以混装消息、函数调用与函数结果。
func TestResponsesRequestMixedInput(t *testing.T) {
	src := `{
		"model": "m",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "北京天气"}]},
			{"type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": "{\"city\":\"北京\"}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "晴 25 度"},
			{"type": "reasoning", "id": "rs_1", "summary": []}
		]
	}`

	got, err := ResponsesRequestToOpenAIChat([]byte(src))
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(got, &m)
	msgs := m["messages"].([]any)

	// reasoning 项无法回传，应被跳过，因此是 3 条
	if len(msgs) != 3 {
		t.Fatalf("应为 3 条消息（user + assistant.tool_calls + tool），实际 %d: %v", len(msgs), msgs)
	}
	if msgs[0].(map[string]any)["content"] != "北京天气" {
		t.Fatalf("input_text 块应压平成文本，实际 %v", msgs[0])
	}
	asst := msgs[1].(map[string]any)
	calls := asst["tool_calls"].([]any)
	call := calls[0].(map[string]any)
	// Responses 的 call_id 才是 Chat 的工具调用 id
	if call["id"] != "call_1" {
		t.Fatalf("call_id 应作为 tool_call id，实际 %v", call["id"])
	}
	toolMsg := msgs[2].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_1" {
		t.Fatalf("函数结果应转成 role=tool，实际 %v", toolMsg)
	}
	if toolMsg["content"] != "晴 25 度" {
		t.Fatalf("函数结果内容错误: %v", toolMsg["content"])
	}
}

// ---------- 响应转换 ----------

func TestOpenAIToResponsesResponse(t *testing.T) {
	src := `{
		"id": "chatcmpl-1", "model": "gpt-5", "created": 1700000000,
		"choices": [{"index": 0, "finish_reason": "stop",
			"message": {"role": "assistant", "content": "你好"}}],
		"usage": {"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120,
		          "prompt_tokens_details": {"cached_tokens": 40},
		          "completion_tokens_details": {"reasoning_tokens": 5}}
	}`

	got, err := OpenAIChatToResponsesResponse([]byte(src), "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(got, &m)

	if m["object"] != "response" || m["status"] != "completed" {
		t.Fatalf("响应骨架错误: object=%v status=%v", m["object"], m["status"])
	}
	if !strings.HasPrefix(m["id"].(string), "resp_") {
		t.Fatalf("id 应带 resp_ 前缀，实际 %v", m["id"])
	}
	if m["output_text"] != "你好" {
		t.Fatalf("output_text 便捷字段应填充，实际 %v", m["output_text"])
	}

	output := m["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("应有 1 个输出项，实际 %d", len(output))
	}
	item := output[0].(map[string]any)
	if item["type"] != "message" || item["role"] != "assistant" {
		t.Fatalf("输出项类型错误: %v", item)
	}
	part := item["content"].([]any)[0].(map[string]any)
	if part["type"] != "output_text" || part["text"] != "你好" {
		t.Fatalf("内容块错误: %v", part)
	}

	u := m["usage"].(map[string]any)
	if u["input_tokens"] != float64(100) || u["output_tokens"] != float64(20) {
		t.Fatalf("用量映射错误: %v", u)
	}
	// Responses 的 input_tokens 同样含缓存命中，直接映射不做拆分
	if u["input_tokens_details"].(map[string]any)["cached_tokens"] != float64(40) {
		t.Fatalf("缓存明细错误: %v", u["input_tokens_details"])
	}
	if u["output_tokens_details"].(map[string]any)["reasoning_tokens"] != float64(5) {
		t.Fatalf("推理明细错误: %v", u["output_tokens_details"])
	}
}

func TestOpenAIToResponsesToolCalls(t *testing.T) {
	src := `{
		"id": "c1", "model": "m",
		"choices": [{"index": 0, "finish_reason": "tool_calls", "message": {
			"role": "assistant", "content": null,
			"tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "f1", "arguments": "{\"a\":1}"}},
				{"id": "call_2", "type": "function", "function": {"name": "f2", "arguments": "{}"}}
			]}}],
		"usage": {"prompt_tokens": 5, "completion_tokens": 3}
	}`

	got, _ := OpenAIChatToResponsesResponse([]byte(src), "")
	var m map[string]any
	_ = json.Unmarshal(got, &m)

	output := m["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("应有 2 个 function_call 输出项，实际 %d", len(output))
	}
	first := output[0].(map[string]any)
	if first["type"] != "function_call" || first["name"] != "f1" || first["call_id"] != "call_1" {
		t.Fatalf("函数调用项错误: %v", first)
	}
	// 两个调用必须有各自独立的 id，否则客户端无法区分
	if output[0].(map[string]any)["id"] == output[1].(map[string]any)["id"] {
		t.Fatal("多个工具调用的 id 不应重复")
	}
}

// ---------- 流式转换 ----------

func TestResponsesStreamTextFlow(t *testing.T) {
	var buf bytes.Buffer
	tr := NewResponsesStreamTranslator(&buf, "gpt-5")

	feed := []string{
		"data: {\"id\":\"c1\",\"model\":\"gpt-5\",\"created\":1700000000,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"你\"},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"好\"},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\n",
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
	order := []string{"response.created", "response.in_progress", "response.output_item.added",
		"response.content_part.added", "response.output_text.delta",
		"response.output_text.done", "response.content_part.done",
		"response.output_item.done", "response.completed"}
	last := -1
	for _, ev := range order {
		idx := strings.Index(got, ev)
		if idx < 0 {
			t.Fatalf("缺少事件 %q，实际输出:\n%s", ev, got)
		}
		if idx < last {
			t.Fatalf("事件顺序错误：%q 位置不对", ev)
		}
		last = idx
	}
	if !strings.Contains(got, "\"delta\":\"你\"") || !strings.Contains(got, "\"delta\":\"好\"") {
		t.Fatalf("文本增量丢失:\n%s", got)
	}
	if !strings.Contains(got, "\"text\":\"你好\"") {
		t.Fatalf("收尾时拼接的完整文本错误:\n%s", got)
	}
	if !strings.Contains(got, "\"output_tokens\":2") {
		t.Fatalf("response.completed 未带用量:\n%s", got)
	}
}

// 多个工具调用必须各自生成独立的 output item 与 delta。
// 判重若误用输出项序号代替上游 tool index，第二个调用会被并进第一个。
func TestResponsesStreamMultipleToolCalls(t *testing.T) {
	var buf bytes.Buffer
	tr := NewResponsesStreamTranslator(&buf, "m")

	feed := []string{
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_a\",\"type\":\"function\",\"function\":{\"name\":\"f1\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"x\\\":1}\"}}]},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"call_b\",\"type\":\"function\",\"function\":{\"name\":\"f2\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"function\":{\"arguments\":\"{\\\"y\\\":2}\"}}]},\"finish_reason\":null}]}\n\n",
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
	// 两个调用各自应有 output_item.added。
	// 注意只数 event: 行——事件名在 data 的 type 字段里还会再出现一次。
	if n := strings.Count(got, "event: response.output_item.added"); n != 2 {
		t.Fatalf("应生成 2 个输出项，实际 %d:\n%s", n, got)
	}
	if n := strings.Count(got, "event: response.output_item.done"); n != 2 {
		t.Fatalf("应有 2 个输出项收尾，实际 %d", n)
	}
	if !strings.Contains(got, "\"name\":\"f1\"") || !strings.Contains(got, "\"name\":\"f2\"") {
		t.Fatalf("两个工具名都应出现:\n%s", got)
	}
	if !strings.Contains(got, "call_a") || !strings.Contains(got, "call_b") {
		t.Fatalf("两个 call_id 都应出现:\n%s", got)
	}
	// 参数在事件里是 JSON 转义过的，用原始字符串匹配转义后的形态
	if !strings.Contains(got, `{\"x\":1}`) || !strings.Contains(got, `{\"y\":2}`) {
		t.Fatalf("两个调用的参数都应完整拼回:\n%s", got)
	}
}

func TestResponsesStreamReasoningAndEmptyUpstream(t *testing.T) {
	var buf bytes.Buffer
	tr := NewResponsesStreamTranslator(&buf, "m")
	if _, err := tr.Write([]byte("")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("收尾失败: %v", err)
	}
	got := buf.String()
	// 上游一个事件都没发时也要补齐，否则客户端一直等
	for _, ev := range []string{"response.created", "response.completed"} {
		if !strings.Contains(got, ev) {
			t.Fatalf("空流也应补出 %s:\n%s", ev, got)
		}
	}
}

func TestResponsesErrorShape(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal(ResponsesError(429, "too many"), &m); err != nil {
		t.Fatalf("错误体不是合法 JSON: %v", err)
	}
	inner := m["error"].(map[string]any)
	if inner["type"] != "rate_limit_exceeded" {
		t.Fatalf("429 应映射为 rate_limit_exceeded，实际 %v", inner["type"])
	}
}
