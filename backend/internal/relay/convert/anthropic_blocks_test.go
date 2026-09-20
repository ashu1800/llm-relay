package convert

import (
	"encoding/json"
	"testing"
)

// Anthropic 私有内容块（thinking / redacted_thinking / document）在
// 「入站 Anthropic -> 通用语 -> 出站 Anthropic」链路上的保真往返。
//
// 丢弃这些块的后果见 anthropicBlocksKey 的注释：thinking 丢了上游 400，
// document 丢了 PDF 输入被模型无视 —— 本组测试把「必须保真」钉死。

func decodeJSON(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("JSON 解析失败: %v\n%s", err, body)
	}
	return v
}

// messagesOf 从出站 Anthropic 请求里取 messages 数组。
func messagesOf(t *testing.T, body []byte) []any {
	t.Helper()
	out := decodeJSON(t, body)
	msgs, _ := out["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("出站请求里没有 messages")
	}
	return msgs
}

// TestAnthropicThinkingBlockRoundTrip 验证 assistant 轮的 thinking 块
// （含 signature）往返后原样还原，且排在 tool_use 之前。
func TestAnthropicThinkingBlockRoundTrip(t *testing.T) {
	req := `{
		"model": "claude-x",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "先想一下", "signature": "sig-abc"},
				{"type": "text", "text": "调用工具"},
				{"type": "tool_use", "id": "tu_1", "name": "get_weather", "input": {"city": "北京"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "tu_1", "content": "晴"}
			]}
		]
	}`
	mid, err := AnthropicRequestToOpenAIChat([]byte(req))
	if err != nil {
		t.Fatalf("入站转换失败: %v", err)
	}
	// 通用语里 assistant 消息必须带着私有块容器
	var midParsed struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(mid, &midParsed); err != nil {
		t.Fatal(err)
	}
	var foundContainer bool
	for _, m := range midParsed.Messages {
		if m["role"] == "assistant" {
			if _, ok := m["anthropic_blocks"]; ok {
				foundContainer = true
			}
		}
	}
	if !foundContainer {
		t.Fatal("通用语 assistant 消息没有携带 anthropic_blocks 容器")
	}

	out, err := OpenAIChatToAnthropicRequest(mid)
	if err != nil {
		t.Fatalf("出站转换失败: %v", err)
	}
	msgs := messagesOf(t, out)
	// messages[0]=user(hi), [1]=assistant(thinking+text+tool_use), [2]=user(tool_result)
	assistant, _ := msgs[1].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Fatalf("第二条消息应为 assistant，实际 %v", assistant["role"])
	}
	content, _ := assistant["content"].([]any)
	if len(content) != 3 {
		t.Fatalf("assistant 内容块应还原为 3 块（thinking/text/tool_use），实际 %d: %v", len(content), content)
	}
	first, _ := content[0].(map[string]any)
	if first["type"] != "thinking" || first["thinking"] != "先想一下" || first["signature"] != "sig-abc" {
		t.Fatalf("thinking 块应原样还原且排在最前，实际 %v", first)
	}
	tool, _ := content[2].(map[string]any)
	if tool["type"] != "tool_use" || tool["id"] != "tu_1" {
		t.Fatalf("tool_use 块应还原且排在 thinking 之后，实际 %v", tool)
	}
}

// TestAnthropicDocumentBlockRoundTrip 验证 user 轮的 PDF document 块
// 单独成条时（无 text/image）消息不丢、块还原。
func TestAnthropicDocumentBlockRoundTrip(t *testing.T) {
	req := `{
		"model": "claude-x",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": [
				{"type": "document", "source": {"type": "base64", "media_type": "application/pdf", "data": "JVBERi0="}}
			]}
		]
	}`
	mid, err := AnthropicRequestToOpenAIChat([]byte(req))
	if err != nil {
		t.Fatalf("入站转换失败: %v", err)
	}
	out, err := OpenAIChatToAnthropicRequest(mid)
	if err != nil {
		t.Fatalf("出站转换失败: %v", err)
	}
	msgs := messagesOf(t, out)
	if len(msgs) != 1 {
		t.Fatalf("只含 document 块的 user 消息不能在往返中丢失，实际剩 %d 条", len(msgs))
	}
	user, _ := msgs[0].(map[string]any)
	if user["role"] != "user" {
		t.Fatalf("应为 user 消息，实际 %v", user["role"])
	}
	content, _ := user["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("应还原出 1 个 document 块，实际 %d: %v", len(content), content)
	}
	doc, _ := content[0].(map[string]any)
	if doc["type"] != "document" {
		t.Fatalf("块类型应为 document，实际 %v", doc["type"])
	}
	src, _ := doc["source"].(map[string]any)
	if src["media_type"] != "application/pdf" || src["data"] != "JVBERi0=" {
		t.Fatalf("document source 应原样保真，实际 %v", src)
	}
}

// TestAnthropicBlocksStrippedForOtherUpstreams 验证出站非 Anthropic 时
// 私有块容器被整体剥掉（含子串粗筛路径：容器字段名必须命中 needle）。
func TestAnthropicBlocksStrippedForOtherUpstreams(t *testing.T) {
	req := `{
		"model": "claude-x",
		"max_tokens": 1024,
		"messages": [
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "s", "signature": "sig"}
			]}
		]
	}`
	mid, err := AnthropicRequestToOpenAIChat([]byte(req))
	if err != nil {
		t.Fatal(err)
	}
	// 注意：这条报文里没有 cache_control，粗筛必须靠 anthropic_blocks 自己的
	// needle 命中 —— 漏筛的话容器会原样发给 OpenAI 兼容上游
	stripped := stripCacheControlFromJSON(mid)
	var parsed struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(stripped, &parsed); err != nil {
		t.Fatal(err)
	}
	for i, m := range parsed.Messages {
		for k := range m {
			if k == anthropicBlocksKey {
				t.Fatalf("消息 %d 仍带私有块容器 %s，发给其它上游会被严格校验的实现 400", i, k)
			}
		}
	}
}
