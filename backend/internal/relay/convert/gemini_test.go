package convert

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestGeminiRequestBasics(t *testing.T) {
	src := `{
		"systemInstruction": {"parts": [{"text": "你是助手"}]},
		"contents": [{"role": "user", "parts": [{"text": "北京天气"}]}],
		"generationConfig": {"maxOutputTokens": 256, "temperature": 0.7, "stopSequences": ["END"]},
		"tools": [{"functionDeclarations": [{"name": "get_weather", "description": "查天气",
		           "parameters": {"type": "object"}}]}],
		"toolConfig": {"functionCallingConfig": {"mode": "ANY"}}
	}`

	got, err := GeminiRequestToOpenAIChat([]byte(src), "gemini-2.5-pro", false)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(got, &m)

	// 模型名来自 URL，必须由转换函数注入
	if m["model"] != "gemini-2.5-pro" {
		t.Fatalf("模型名未注入: %v", m["model"])
	}
	// 非流式时不应带 stream
	if _, ok := m["stream"]; ok {
		t.Fatal("非流式请求不应带 stream 字段")
	}
	if m["max_tokens"] != float64(256) {
		t.Fatalf("maxOutputTokens 应映射为 max_tokens，实际 %v", m["max_tokens"])
	}
	if m["temperature"] != 0.7 {
		t.Fatalf("temperature 未透传: %v", m["temperature"])
	}
	if _, ok := m["stop"]; !ok {
		t.Fatal("stopSequences 应改名为 stop")
	}

	msgs := m["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("应为 system + user 两条，实际 %d", len(msgs))
	}
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("systemInstruction 应转成 system，实际 %v", msgs[0])
	}
	if msgs[1].(map[string]any)["content"] != "北京天气" {
		t.Fatalf("用户消息内容错误: %v", msgs[1])
	}

	tools := m["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("functionDeclarations 未正确展开: %v", fn)
	}
	if m["tool_choice"] != "required" {
		t.Fatalf("mode ANY 应映射为 required，实际 %v", m["tool_choice"])
	}
}

// Gemini 用 model 表示助手角色，函数结果放在 parts 里，需要拆成独立消息。
func TestGeminiRequestToolRoundTrip(t *testing.T) {
	src := `{
		"contents": [
			{"role": "user", "parts": [{"text": "北京天气"}]},
			{"role": "model", "parts": [{"functionCall": {"name": "get_weather", "args": {"city": "北京"}}}]},
			{"role": "user", "parts": [{"functionResponse": {"name": "get_weather", "response": {"temp": 25}}}]}
		]
	}`

	got, err := GeminiRequestToOpenAIChat([]byte(src), "m", true)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(got, &m)
	msgs := m["messages"].([]any)

	if len(msgs) != 3 {
		t.Fatalf("应为 3 条消息（user + assistant.tool_calls + tool），实际 %d", len(msgs))
	}
	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" {
		t.Fatalf("Gemini 的 model 角色应转成 assistant，实际 %v", asst["role"])
	}
	calls := asst["tool_calls"].([]any)
	call := calls[0].(map[string]any)
	if call["id"] != "call_get_weather" {
		t.Fatalf("工具调用 id 生成错误: %v", call["id"])
	}
	args := call["function"].(map[string]any)["arguments"].(string)
	if !strings.Contains(args, "北京") {
		t.Fatalf("参数未序列化: %v", args)
	}

	toolMsg := msgs[2].(map[string]any)
	if toolMsg["role"] != "tool" {
		t.Fatalf("函数结果应转成 role=tool，实际 %v", toolMsg["role"])
	}
	// id 必须与前面的 tool_call 对齐，否则上游会拒绝
	if toolMsg["tool_call_id"] != call["id"] {
		t.Fatalf("tool_call_id 未对齐: %v vs %v", toolMsg["tool_call_id"], call["id"])
	}
}

// 流式标记只存在于 URL，漏注入会让上游返回非流式报文，流式客户端一直等不到分片。
func TestGeminiRequestInjectsStreamFlag(t *testing.T) {
	got, err := GeminiRequestToOpenAIChat([]byte(`{"contents":[{"parts":[{"text":"hi"}]}]}`), "m", true)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(got, &m)
	if m["stream"] != true {
		t.Fatalf("流式请求必须注入 stream=true，实际 %v", m["stream"])
	}
}

func TestOpenAIToGeminiResponse(t *testing.T) {
	src := `{
		"id": "c1", "model": "m",
		"choices": [{"index": 0, "finish_reason": "stop",
			"message": {"role": "assistant", "content": "你好"}}],
		"usage": {"prompt_tokens": 100, "completion_tokens": 20,
		          "prompt_tokens_details": {"cached_tokens": 30}}
	}`

	got, err := OpenAIChatToGeminiResponse([]byte(src), "gemini-2.5-pro")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(got, &m)

	cands := m["candidates"].([]any)
	if len(cands) != 1 {
		t.Fatalf("应有 1 个 candidate，实际 %d", len(cands))
	}
	cand := cands[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Fatalf("finish_reason stop 应映射为 STOP，实际 %v", cand["finishReason"])
	}
	content := cand["content"].(map[string]any)
	if content["role"] != "model" {
		t.Fatalf("角色应为 model，实际 %v", content["role"])
	}
	parts := content["parts"].([]any)
	if parts[0].(map[string]any)["text"] != "你好" {
		t.Fatalf("文本未转成 parts: %v", parts)
	}

	u := m["usageMetadata"].(map[string]any)
	if u["promptTokenCount"] != float64(100) || u["candidatesTokenCount"] != float64(20) {
		t.Fatalf("用量映射错误: %v", u)
	}
	if u["totalTokenCount"] != float64(120) {
		t.Fatalf("totalTokenCount 应为 120，实际 %v", u["totalTokenCount"])
	}
	if u["cachedContentTokenCount"] != float64(30) {
		t.Fatalf("缓存命中未映射: %v", u["cachedContentTokenCount"])
	}
}

func TestGeminiFinishReason(t *testing.T) {
	cases := map[string]string{
		"stop": "STOP", "length": "MAX_TOKENS",
		"tool_calls": "STOP", "content_filter": "SAFETY", "": "STOP",
	}
	for in, want := range cases {
		if got := geminiFinishReason(in); got != want {
			t.Errorf("geminiFinishReason(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// Gemini 的 SSE 只有 data 行，没有 event 行，且靠 finishReason 判断结束。
func TestGeminiStreamTranslator(t *testing.T) {
	var buf bytes.Buffer
	tr := NewGeminiStreamTranslator(&buf, "gemini-2.5-pro")

	feed := []string{
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"你\"},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"好\"},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2}}\n\n",
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
	// Gemini 不应出现 event 行
	if strings.Contains(got, "event:") {
		t.Fatalf("Gemini 的 SSE 不应带 event 行:\n%s", got)
	}
	if !strings.Contains(got, "\"text\":\"你\"") || !strings.Contains(got, "\"text\":\"好\"") {
		t.Fatalf("文本增量丢失:\n%s", got)
	}
	if !strings.Contains(got, "\"role\":\"model\"") {
		t.Fatalf("角色应为 model:\n%s", got)
	}
	// 收尾必须有 finishReason，否则客户端一直等
	if !strings.Contains(got, "\"finishReason\":\"STOP\"") {
		t.Fatalf("收尾缺少 finishReason:\n%s", got)
	}
	if !strings.Contains(got, "\"candidatesTokenCount\":2") {
		t.Fatalf("收尾未带用量:\n%s", got)
	}
}

func TestGeminiStreamEmptyUpstream(t *testing.T) {
	var buf bytes.Buffer
	tr := NewGeminiStreamTranslator(&buf, "m")
	if _, err := tr.Write([]byte("")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("收尾失败: %v", err)
	}
	if !strings.Contains(buf.String(), "finishReason") {
		t.Fatalf("空流也应补出终止分片:\n%s", buf.String())
	}
}

func TestGeminiErrorShape(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal(GeminiError(429, "too many"), &m); err != nil {
		t.Fatalf("错误体不是合法 JSON: %v", err)
	}
	inner := m["error"].(map[string]any)
	if inner["status"] != "RESOURCE_EXHAUSTED" {
		t.Fatalf("429 应映射为 RESOURCE_EXHAUSTED，实际 %v", inner["status"])
	}
}
