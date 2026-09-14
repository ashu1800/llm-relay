package convert

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"llm-relay/internal/model"
)

// 出站 Responses 适配（Chat 通用语 -> 上游 Responses）。
//
// 这套断言的报文依据智谱 /api/v1/responses 的实测结构（2026-09），
// 三方差异都在这里钉住，避免日后被「顺手简化」掉。

func TestOpenAIChatToResponsesRequestBasics(t *testing.T) {
	src := `{
		"model":"glm-5.3",
		"messages":[
			{"role":"system","content":"be brief"},
			{"role":"user","content":"hi"}
		],
		"max_tokens":64,
		"stream":false
	}`
	out, err := OpenAIChatToResponsesRequest([]byte(src))
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
	// 系统提示必须是顶层 instructions，不能留在 input 里
	if asString(got["instructions"]) != "be brief" {
		t.Errorf("instructions 应为 be brief，实际 %v", got["instructions"])
	}
	if _, exists := got["messages"]; exists {
		t.Error("Responses 请求不应带 messages 字段")
	}
	if asInt(got["max_output_tokens"]) != 64 {
		t.Errorf("max_tokens 应映射成 max_output_tokens=64，实际 %v", got["max_output_tokens"])
	}
	input, _ := got["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("input 应只有 1 条（system 不进 input），实际 %d", len(input))
	}
	msg := asMap(input[0])
	if asString(msg["type"]) != "message" || asString(msg["role"]) != "user" {
		t.Errorf("input[0] 应是 user message，实际 %v", msg)
	}
	if asString(msg["content"]) != "hi" {
		t.Errorf("input[0].content 应为 hi，实际 %v", msg["content"])
	}
}

// 工具往返：Chat 的 role=tool 消息与 tool_calls 要变成 Responses 的事件。
func TestOpenAIChatToResponsesRequestToolRoundTrip(t *testing.T) {
	src := `{
		"model":"glm-5.3",
		"messages":[
			{"role":"user","content":"weather?"},
			{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"BJ\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"sunny"}
		],
		"tools":[{"type":"function","function":{"name":"get_weather","description":"w","parameters":{"type":"object"}}}]
	}`
	out, err := OpenAIChatToResponsesRequest([]byte(src))
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)

	// tools 要拍平成 {type,name,parameters}，不能再包 function
	tools, _ := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools 应有 1 条，实际 %d", len(tools))
	}
	tool := asMap(tools[0])
	if asString(tool["name"]) != "get_weather" {
		t.Errorf("工具名应在顶层，实际 %v", tool)
	}
	if _, exists := tool["function"]; exists {
		t.Error("Responses 的工具定义不应再包一层 function")
	}

	input, _ := got["input"].([]any)
	var sawCall, sawOutput bool
	for _, raw := range input {
		item := asMap(raw)
		switch asString(item["type"]) {
		case "function_call":
			sawCall = true
			if asString(item["call_id"]) != "call_1" {
				t.Errorf("function_call.call_id 应为 call_1，实际 %v", item["call_id"])
			}
			if asString(item["name"]) != "get_weather" {
				t.Errorf("function_call.name 错误: %v", item["name"])
			}
		case "function_call_output":
			sawOutput = true
			if asString(item["call_id"]) != "call_1" {
				t.Errorf("function_call_output.call_id 应为 call_1，实际 %v", item["call_id"])
			}
			if asString(item["output"]) != "sunny" {
				t.Errorf("function_call_output.output 错误: %v", item["output"])
			}
		}
	}
	if !sawCall {
		t.Error("输入里缺少 function_call 事件（tool_calls 没被拆出来）")
	}
	if !sawOutput {
		t.Error("输入里缺少 function_call_output 事件（role=tool 没被转换）")
	}
	// 纯工具调用的 assistant 消息不应产生空 message 事件
	for _, raw := range input {
		item := asMap(raw)
		if asString(item["type"]) == "message" && asString(item["role"]) == "assistant" {
			t.Errorf("空正文的 assistant 不应产生 message 事件: %v", item)
		}
	}
}

// input 不能为空，否则上游直接 400。
func TestOpenAIChatToResponsesRequestAlwaysHasInput(t *testing.T) {
	out, err := OpenAIChatToResponsesRequest([]byte(`{"model":"m","messages":[]}`))
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if _, ok := got["input"]; !ok {
		t.Error("input 缺失时也必须补一个空数组")
	}
}

func TestResponsesResponseToOpenAIChat(t *testing.T) {
	// 实测结构的精简版：reasoning 在 content[].type=reasoning_text，正文在 output_text
	body := `{
		"id":"resp_abc","object":"response","created_at":1789357440,"model":"glm-5.3","status":"completed",
		"output":[
			{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"thinking..."}]},
			{"type":"message","id":"msg_1","role":"assistant","status":"completed",
			 "content":[{"type":"output_text","text":"Hi","annotations":[]}]}
		],
		"usage":{"input_tokens":17,"input_tokens_details":{"cached_tokens":4},
		         "output_tokens":25,"output_tokens_details":{"reasoning_tokens":22},"total_tokens":42}
	}`
	out, err := ResponsesResponseToOpenAIChat([]byte(body), "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
	choices, _ := got["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices 应有 1 条，实际 %d", len(choices))
	}
	choice := asMap(choices[0])
	msg := asMap(choice["message"])
	if asString(msg["content"]) != "Hi" {
		t.Errorf("content 应为 Hi，实际 %v", msg["content"])
	}
	if asString(msg["reasoning"]) != "thinking..." {
		t.Errorf("reasoning 应取 reasoning_text，实际 %v", msg["reasoning"])
	}
	if asString(choice["finish_reason"]) != "stop" {
		t.Errorf("finish_reason 应为 stop，实际 %v", choice["finish_reason"])
	}
	if !strings.HasPrefix(asString(got["id"]), "chatcmpl-") {
		t.Errorf("id 应换算成 chatcmpl- 前缀，实际 %v", got["id"])
	}
	// usage：缓存命中放在子集型字段里，交给 NormalizeUsage 归一化
	usage := asMap(got["usage"])
	if asInt(usage["prompt_tokens"]) != 17 {
		t.Errorf("prompt_tokens 应为 17，实际 %v", usage["prompt_tokens"])
	}
	if asInt(usage["completion_tokens"]) != 25 {
		t.Errorf("completion_tokens 应为 25，实际 %v", usage["completion_tokens"])
	}
	if details := asMap(usage["prompt_tokens_details"]); asInt(details["cached_tokens"]) != 4 {
		t.Errorf("缓存命中应放在 prompt_tokens_details.cached_tokens，实际 %v", usage)
	}
	if details := asMap(usage["completion_tokens_details"]); asInt(details["reasoning_tokens"]) != 22 {
		t.Errorf("推理 token 应放在 completion_tokens_details，实际 %v", usage)
	}
}

func TestResponsesFinishReasonLength(t *testing.T) {
	body := `{"id":"r","model":"m","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},
	          "output":[{"type":"message","content":[{"type":"output_text","text":"x"}]}]}`
	out, _ := ResponsesResponseToOpenAIChat([]byte(body), "")
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	choice := asMap(asAnyList(got["choices"])[0])
	if asString(choice["finish_reason"]) != "length" {
		t.Errorf("截断时应为 length，实际 %v", choice["finish_reason"])
	}
}

func TestResponsesResponseToolCalls(t *testing.T) {
	body := `{"id":"r","model":"m","status":"completed","output":[
		{"type":"function_call","id":"fc_1","call_id":"call_9","name":"get_weather","arguments":"{\"city\":\"BJ\"}","status":"completed"}
	]}`
	out, _ := ResponsesResponseToOpenAIChat([]byte(body), "")
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	choice := asMap(asAnyList(got["choices"])[0])
	msg := asMap(choice["message"])
	calls, _ := msg["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("tool_calls 应有 1 条，实际 %d", len(calls))
	}
	call := asMap(calls[0])
	// OpenAI 侧要用的 id 是 call_id，不是响应的 item id
	if asString(call["id"]) != "call_9" {
		t.Errorf("tool_call.id 应取 call_id，实际 %v", call["id"])
	}
	if fn := asMap(call["function"]); asString(fn["name"]) != "get_weather" {
		t.Errorf("工具名错误: %v", call)
	}
	if asString(choice["finish_reason"]) != "tool_calls" {
		t.Errorf("有工具调用时 finish_reason 应为 tool_calls，实际 %v", choice["finish_reason"])
	}
	// content 不能是 null
	if _, ok := msg["content"]; !ok {
		t.Error("message.content 必须存在（哪怕为空串）")
	}
}

// 流式：事件序列要还原成 Chat 分片，usage 单独一条且 choices 为空。
func TestResponsesStreamToOpenAIChat(t *testing.T) {
	raw := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_s1","model":"glm-5.3","status":"in_progress"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","status":"in_progress"}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"He"}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"llo"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_s1","status":"completed","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`,
		"",
	}, "\n")

	out := readAllConverted(t, raw, "glm-5.3")
	var content strings.Builder
	var finish string
	var sawUsage bool
	for _, chunk := range sseChunks(t, out) {
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			if asMap(chunk["usage"]) != nil {
				sawUsage = true
				if asInt(asMap(chunk["usage"])["prompt_tokens"]) != 5 {
					t.Errorf("usage.prompt_tokens 应为 5，实际 %v", chunk["usage"])
				}
			}
			continue
		}
		choice := asMap(choices[0])
		if delta := asMap(choice["delta"]); delta != nil {
			content.WriteString(asString(delta["content"]))
		}
		if fr := asString(choice["finish_reason"]); fr != "" {
			finish = fr
		}
	}
	if content.String() != "Hello" {
		t.Errorf("正文应为 Hello，实际 %q", content.String())
	}
	if finish != "stop" {
		t.Errorf("finish_reason 应为 stop，实际 %q", finish)
	}
	if !sawUsage {
		t.Error("缺少带 usage 的收尾分片")
	}
	if !strings.Contains(out, "[DONE]") {
		t.Error("缺少 [DONE] 结束标记")
	}
}

// 流式思维链：reasoning_text.delta 要映射成站内的 reasoning 字段。
func TestResponsesStreamReasoningAndTools(t *testing.T) {
	raw := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_s2","model":"glm-5.3"}}`,
		"",
		"event: response.reasoning_text.delta",
		`data: {"type":"response.reasoning_text.delta","delta":"think"}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_7","name":"get_weather"}}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"city\":"}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"\"BJ\"}"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_s2","status":"completed"}}`,
		"",
	}, "\n")

	out := readAllConverted(t, raw, "glm-5.3")
	var reasoning, args strings.Builder
	var toolName, toolID, finish string
	for _, chunk := range sseChunks(t, out) {
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice := asMap(choices[0])
		if delta := asMap(choice["delta"]); delta != nil {
			reasoning.WriteString(asString(delta["reasoning"]))
			for _, rawCall := range asAnyList(delta["tool_calls"]) {
				call := asMap(rawCall)
				fn := asMap(call["function"])
				if n := asString(fn["name"]); n != "" {
					toolName = n
				}
				if id := asString(call["id"]); id != "" {
					toolID = id
				}
				args.WriteString(asString(fn["arguments"]))
			}
		}
		if fr := asString(choice["finish_reason"]); fr != "" {
			finish = fr
		}
	}
	if reasoning.String() != "think" {
		t.Errorf("思维链应为 think，实际 %q", reasoning.String())
	}
	if toolName != "get_weather" {
		t.Errorf("工具名应为 get_weather，实际 %q", toolName)
	}
	// tool_call 的头分片里带的 id 必须是 call_id（客户端要用它回传 function_call_output）
	if toolID != "call_7" {
		t.Errorf("tool_call.id 应取 call_id，实际 %q", toolID)
	}
	if args.String() != `{"city":"BJ"}` {
		t.Errorf("工具参数应完整拼接，实际 %q", args.String())
	}
	if finish != "tool_calls" {
		t.Errorf("有工具调用时 finish_reason 应为 tool_calls，实际 %q", finish)
	}
}

// 上游把参数只在 output_item.done 里给全时，要在收尾前补齐。
func TestResponsesStreamFillsToolArgsFromItemDone(t *testing.T) {
	raw := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_s3","model":"m"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_9","call_id":"call_9","name":"f"}}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_9","call_id":"call_9","name":"f","arguments":"{\"a\":1}"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_s3","status":"completed"}}`,
		"",
	}, "\n")

	out := readAllConverted(t, raw, "m")
	var args strings.Builder
	for _, chunk := range sseChunks(t, out) {
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		delta := asMap(asMap(choices[0])["delta"])
		for _, rawCall := range asAnyList(delta["tool_calls"]) {
			args.WriteString(asString(asMap(asMap(rawCall)["function"])["arguments"]))
		}
	}
	if args.String() != `{"a":1}` {
		t.Errorf("参数应被补齐为 {\"a\":1}，实际 %q", args.String())
	}
}

// 上游什么都没发就断流时，也要给出结构完整的分片。
func TestResponsesStreamEmptyUpstream(t *testing.T) {
	out := readAllConverted(t, "", "m")
	chunks := sseChunks(t, out)
	if len(chunks) == 0 {
		t.Fatal("空上游也必须产出分片，否则客户端连「回复为空」都判断不了")
	}
	if !strings.Contains(out, "[DONE]") {
		t.Error("缺少 [DONE]")
	}
}

// 路径：Responses 渠道必须打到 /v1/responses，且 base 带版本段时不重复插入。
func TestUpstreamRequestResponsesPath(t *testing.T) {
	body := []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	path, out, err := UpstreamRequest(model.ProtocolOpenAIResponses, "/v1/chat/completions", body, "glm-5.3")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	if path != "/v1/responses" {
		t.Errorf("路径应为 /v1/responses，实际 %s", path)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("请求体不是合法 JSON: %v", err)
	}
	if _, exists := got["messages"]; exists {
		t.Error("发往 Responses 的报文不该带 messages")
	}
	// 站内通用语没有 instructions，input 必须存在
	if _, exists := got["input"]; !exists {
		t.Error("input 必须存在")
	}
}

// 端到端拼 URL：智谱 Responses 的 base 是 /api/v1，不能拼成 /api/v1/v1/responses
func TestResponsesUpstreamURL(t *testing.T) {
	// 这条断言在 relay 包（BuildUpstreamURL）里，这里只固定 path 的取值
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	path, _, err := UpstreamRequest(model.ProtocolOpenAIResponses, "/v1/chat/completions", body, "m")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	if path != "/v1/responses" {
		t.Errorf("Responses 的探测/转发路径应为 /v1/responses，实际 %s", path)
	}
}

// ---------- 测试辅助 ----------

// readAllConverted 把上游字节流喂进流式适配器，读出全部转换结果。
func readAllConverted(t *testing.T, upstream, model string) string {
	t.Helper()
	conv := NewResponsesStreamToOpenAIChat(io.NopCloser(strings.NewReader(upstream)), model)
	raw, err := io.ReadAll(conv)
	if err != nil {
		t.Fatalf("读取转换结果失败: %v", err)
	}
	return string(raw)
}

// sseChunks 解析出转换结果里的 JSON 分片。
func sseChunks(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" || payload[0] != '{' {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatalf("分片不是合法 JSON: %v (%s)", err, payload)
		}
		out = append(out, m)
	}
	return out
}
