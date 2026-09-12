package convert

import (
	"encoding/json"
	"strings"
	"testing"
)

// Gemini 与 Anthropic 一样要求 user / model 交替，连续同角色必须合并 ——
// OpenAI 侧「assistant 先输出 tool_calls 再补一条文本」「user 连发两条」都很常见，
// 直接发过去会被上游 400 拒绝。
func TestOpenAIChatToGeminiMergesSameRole(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role": "user", "content": "一"},
			{"role": "user", "content": "二"},
			{"role": "assistant", "content": null, "tool_calls": [
				{"id": "call_a", "type": "function", "function": {"name": "f", "arguments": "{}"}}
			]},
			{"role": "assistant", "content": "补充说明"}
		]
	}`)
	out, err := OpenAIChatToGeminiRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	contents, _ := got["contents"].([]any)
	if len(contents) != 2 {
		t.Fatalf("连续同角色应合并成 2 个轮次（user / model），实际 %d 个: %v", len(contents), contents)
	}
	if role := asString(asMap(contents[1])["role"]); role != "model" {
		t.Errorf("第二个轮次应是 model，实际 %s", role)
	}
	userParts, _ := asMap(contents[0])["parts"].([]any)
	if len(userParts) != 2 {
		t.Errorf("两条 user 合并后应有两个部件，实际 %d", len(userParts))
	}
	// 工具调用与后续文本都落在同一个 model 轮次里
	modelParts, _ := asMap(contents[1])["parts"].([]any)
	var hasCall, hasText bool
	for _, p := range modelParts {
		if asMap(p)["functionCall"] != nil {
			hasCall = true
		}
		if asString(asMap(p)["text"]) == "补充说明" {
			hasText = true
		}
	}
	if !hasCall || !hasText {
		t.Errorf("functionCall 与后续文本应在同一个 model 轮次里，实际 %v", modelParts)
	}
}

// 一次回复里有两个工具调用时，OpenAI 分片的 index 必须递增：
// 都写 0 的话下游改写器会把两个调用叠进同一个内容块，只剩最后一个。
//
// 注意 SSE 的每个 data 行必须是**单行** JSON：
// 这里若换成多行美化格式，切分器只会把第一行当载荷（解析失败即跳过），
// 断言就会变成「什么都没转出来」。
func TestGeminiStreamMultipleToolCalls(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"f1","args":{"a":1}}},{"functionCall":{"name":"f2","args":{"b":2}}}]},"index":0}]}`,
		"",
		`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`,
		"",
	}, "\n")

	stream := NewGeminiStreamToOpenAIChat(ioNopCloser(strings.NewReader(upstream)), "m")
	raw, err := readAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `"index":0`) || !strings.Contains(text, `"index":1`) {
		t.Errorf("两个工具调用的 index 应为 0 与 1，实际:\n%s", text)
	}
	for _, want := range []string{"f1", "f2", "call_f1", "call_f2"} {
		if !strings.Contains(text, want) {
			t.Errorf("缺少 %s:\n%s", want, text)
		}
	}
}
