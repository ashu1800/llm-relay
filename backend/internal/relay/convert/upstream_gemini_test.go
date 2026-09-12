package convert

import (
	"encoding/json"
	"strings"
	"testing"
)

// Gemini 的上游路径：模型名在路径里，流式靠方法名区分。
func TestGeminiUpstreamPath(t *testing.T) {
	if got := GeminiUpstreamPath("gemini-2.0-flash", false); got != "/v1beta/models/gemini-2.0-flash:generateContent" {
		t.Errorf("非流式路径不对: %s", got)
	}
	// alt=sse 不能少：不加它回来的不是 SSE，站内读不出事件
	if got := GeminiUpstreamPath("gemini-2.0-flash", true); got != "/v1beta/models/gemini-2.0-flash:streamGenerateContent?alt=sse" {
		t.Errorf("流式路径不对: %s", got)
	}
}

// OpenAI Chat -> Gemini generateContent。
func TestOpenAIChatToGeminiRequest(t *testing.T) {
	body := []byte(`{
		"model": "public-name",
		"messages": [
			{"role": "system", "content": "你是助手"},
			{"role": "user", "content": "北京天气"},
			{"role": "assistant", "content": null, "tool_calls": [
				{"id": "call_get_weather", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"北京\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_get_weather", "content": "晴"}
		],
		"max_tokens": 128,
		"temperature": 0.5,
		"top_p": 0.9,
		"stop": ["END"],
		"tools": [{"type": "function", "function": {
			"name": "get_weather",
			"description": "查天气",
			"parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"], "additionalProperties": false}
		}}],
		"tool_choice": "auto",
		"stream_options": {"include_usage": true}
	}`)
	out, err := OpenAIChatToGeminiRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}

	if asString(asMap(got["systemInstruction"])["parts"].([]any)[0].(map[string]any)["text"]) != "你是助手" {
		t.Errorf("systemInstruction 不对: %v", got["systemInstruction"])
	}
	// 未知字段不能透传：Gemini 对多余字段同样是 400
	if _, ok := got["stream_options"]; ok {
		t.Error("stream_options 不能发给 Gemini")
	}
	if _, ok := got["messages"]; ok {
		t.Error("messages 不是 Gemini 的字段")
	}

	gc := asMap(got["generationConfig"])
	if asInt(gc["maxOutputTokens"]) != 128 {
		t.Errorf("max_tokens 应映射成 maxOutputTokens: %v", gc)
	}
	// topP 是小数，取整会变成 0，直接看原值
	if topP, _ := gc["topP"].(float64); topP != 0.9 {
		t.Errorf("top_p 应映射成 topP: %v", gc["topP"])
	}
	if seq, _ := gc["stopSequences"].([]any); len(seq) != 1 || seq[0] != "END" {
		t.Errorf("stop 应映射成 stopSequences: %v", gc["stopSequences"])
	}

	contents, _ := got["contents"].([]any)
	if len(contents) != 3 {
		t.Fatalf("应有 3 个 contents（user/model/user），实际 %d", len(contents))
	}
	if asString(asMap(contents[1])["role"]) != "model" {
		t.Errorf("assistant 应映射成 model，实际 %s", asMap(contents[1])["role"])
	}
	modelParts, _ := asMap(contents[1])["parts"].([]any)
	if asMap(asMap(modelParts[0])["functionCall"])["name"] != "get_weather" {
		t.Errorf("工具调用应转成 functionCall，实际 %v", modelParts[0])
	}
	userParts, _ := asMap(contents[2])["parts"].([]any)
	fr := asMap(asMap(userParts[0])["functionResponse"])
	if asString(fr["name"]) != "get_weather" {
		t.Errorf("functionResponse 必须用函数名（Gemini 没有调用 id），实际 %v", fr)
	}
	if asString(asMap(fr["response"])["content"]) != "晴" {
		t.Errorf("工具结果应放进 response.content，实际 %v", fr["response"])
	}

	// 函数声明的 schema 要清掉 Gemini 不认识的关键字
	decls, _ := asMap(got["tools"].([]any)[0])["functionDeclarations"].([]any)
	schema := asMap(asMap(decls[0])["parameters"])
	if _, ok := schema["additionalProperties"]; ok {
		t.Error("additionalProperties 必须过滤掉，否则 Gemini 直接 400")
	}
	if asMap(schema["properties"])["city"] == nil {
		t.Errorf("properties 应保留，实际 %v", schema)
	}
}

// 图片：data URI -> inlineData。
func TestOpenAIChatToGeminiImage(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[
		{"type":"text","text":"看图"},
		{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,BBBB"}}
	]}]}`)
	out, _ := OpenAIChatToGeminiRequest(body)
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	contents, _ := got["contents"].([]any)
	parts, _ := asMap(contents[0])["parts"].([]any)
	inline := asMap(asMap(parts[1])["inlineData"])
	if asString(inline["mimeType"]) != "image/jpeg" || asString(inline["data"]) != "BBBB" {
		t.Errorf("inlineData 不对: %v", inline)
	}
}

// Gemini 响应 -> OpenAI 响应。
func TestGeminiResponseToOpenAIChat(t *testing.T) {
	body := []byte(`{
		"candidates": [{"content": {"role": "model", "parts": [
			{"text": "先想一下", "thought": true},
			{"text": "答案是 42"},
			{"functionCall": {"name": "calc", "args": {"expr": "6*7"}}}
		]}, "finishReason": "STOP", "index": 0}],
		"usageMetadata": {"promptTokenCount": 20, "candidatesTokenCount": 8, "totalTokenCount": 28, "cachedContentTokenCount": 5, "thoughtsTokenCount": 3},
		"modelVersion": "gemini-2.0-flash-001"
	}`)
	out, err := GeminiResponseToOpenAIChat(body, "")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["object"] != "chat.completion" {
		t.Errorf("object 不对: %v", got["object"])
	}
	if got["model"] != "gemini-2.0-flash-001" {
		t.Errorf("model 应取 modelVersion，实际 %v", got["model"])
	}
	choice := asMap(got["choices"].([]any)[0])
	msg := asMap(choice["message"])
	if asString(msg["content"]) != "答案是 42" {
		t.Errorf("thought 部件不能混进正文，实际 %v", msg["content"])
	}
	if asString(msg["reasoning"]) != "先想一下" {
		t.Errorf("thought 应放进 reasoning，实际 %v", msg["reasoning"])
	}
	// Gemini 用 STOP 表示要调函数，客户端要的是 tool_calls
	if asString(choice["finish_reason"]) != "tool_calls" {
		t.Errorf("有 functionCall 时 finish_reason 应是 tool_calls，实际 %s", choice["finish_reason"])
	}
	calls, _ := msg["tool_calls"].([]any)
	if len(calls) != 1 || asString(asMap(asMap(calls[0])["function"])["name"]) != "calc" {
		t.Errorf("functionCall 应转成 tool_calls，实际 %v", msg["tool_calls"])
	}
	usage := asMap(got["usage"])
	if asInt(usage["prompt_tokens"]) != 20 {
		t.Errorf("prompt_tokens 应为 20（Gemini 的 promptTokenCount 已含缓存），实际 %v", usage["prompt_tokens"])
	}
	if asInt(asMap(usage["prompt_tokens_details"])["cached_tokens"]) != 5 {
		t.Errorf("cachedContentTokenCount 应放进子集字段，实际 %v", usage["prompt_tokens_details"])
	}
	if asInt(asMap(usage["completion_tokens_details"])["reasoning_tokens"]) != 3 {
		t.Errorf("thoughtsTokenCount 应作为推理 token，实际 %v", usage["completion_tokens_details"])
	}
}

// 流式：Gemini 的 SSE 片段 -> OpenAI 分片。
// 重点是 usageMetadata 是累计值，只能取最后一帧。
func TestGeminiStreamToOpenAIChat(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"你好"}]},"index":0}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":1,"totalTokenCount":10}}`,
		"",
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"，世界"}]},"index":0}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":4,"totalTokenCount":13}}`,
		"",
		`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":4,"totalTokenCount":13}}`,
		"",
	}, "\n")

	stream := NewGeminiStreamToOpenAIChat(ioNopCloser(strings.NewReader(upstream)), "gemini-upstream")
	raw, err := readAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"你好", "，世界", "\"finish_reason\":\"stop\"", "[DONE]", "\"model\":\"gemini-upstream\""} {
		if !strings.Contains(text, want) {
			t.Errorf("缺少 %s\n实际:\n%s", want, text)
		}
	}
	// 累计的用量只能取最后一次：4 而不是 1+4=5
	if !strings.Contains(text, "\"completion_tokens\":4") {
		t.Errorf("用量应取最后一帧的累计值（4），实际:\n%s", text)
	}
	if strings.Contains(text, "event:") {
		t.Errorf("下游不应出现 event: 行:\n%s", text)
	}
}

// 上游流里带 error（Gemini 在 HTTP 200 的流里报错的情况）时，要把原因传下去。
func TestGeminiStreamError(t *testing.T) {
	upstream := "data: {\"error\":{\"code\":429,\"message\":\"quota exceeded\",\"status\":\"RESOURCE_EXHAUSTED\"}}\n\n"
	stream := NewGeminiStreamToOpenAIChat(ioNopCloser(strings.NewReader(upstream)), "m")
	raw, _ := readAll(stream)
	text := string(raw)
	if !strings.Contains(text, "quota exceeded") || !strings.Contains(text, "\"finish_reason\":\"stop\"") {
		t.Errorf("错误原因要下发且流要正常收尾，实际:\n%s", text)
	}
}

// 上游路径分派：Gemini 渠道的对话请求要换成带模型名的方法路径。
func TestUpstreamRequestGemini(t *testing.T) {
	path, body, err := UpstreamRequest("gemini-generateContent", "/v1/chat/completions",
		[]byte(`{"model":"x","stream":true,"messages":[{"role":"user","content":"hi"}]}`), "gemini-2.0-flash")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1beta/models/gemini-2.0-flash:streamGenerateContent?alt=sse" {
		t.Errorf("流式路径不对: %s", path)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if _, ok := got["contents"]; !ok {
		t.Errorf("请求体应已转成 Gemini 结构，实际 %s", string(body))
	}
}
