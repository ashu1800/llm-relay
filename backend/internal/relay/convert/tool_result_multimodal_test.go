package convert

import (
	"encoding/json"
	"testing"
)

// 工具结果里的多模态内容（Anthropic tool_result 支持 image 块）在
// 「入站 Anthropic -> 通用语 -> 出站 Anthropic」链路上的保真往返。
// 拍平成文本会让模型按「空结果」继续推理 —— 看起来正常、实际缺数据。

// TestToolResultImageRoundTrip 含图片的工具结果往返后还原出 image 块。
func TestToolResultImageRoundTrip(t *testing.T) {
	req := `{
		"model": "claude-x",
		"max_tokens": 1024,
		"messages": [
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "tu_1", "name": "screenshot", "input": {}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "tu_1", "content": [
					{"type": "text", "text": "页面截图如下"},
					{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "aW1n"}}
				]}
			]}
		]
	}`
	mid, err := AnthropicRequestToOpenAIChat([]byte(req))
	if err != nil {
		t.Fatalf("入站转换失败: %v", err)
	}
	// 通用语：tool 消息的 content 应是多模态数组（含 image_url）
	var midParsed struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(mid, &midParsed); err != nil {
		t.Fatal(err)
	}
	var sawImageURL bool
	for _, m := range midParsed.Messages {
		if m["role"] != "tool" {
			continue
		}
		arr, _ := m["content"].([]any)
		for _, item := range arr {
			blk, _ := item.(map[string]any)
			if blk != nil && blk["type"] == "image_url" {
				sawImageURL = true
			}
		}
	}
	if !sawImageURL {
		t.Fatalf("通用语 tool 消息应含 image_url 分片:\n%s", mid)
	}

	out, err := OpenAIChatToAnthropicRequest(mid)
	if err != nil {
		t.Fatalf("出站转换失败: %v", err)
	}
	msgs := messagesOf(t, out)
	// messages[0]=assistant(tool_use), [1]=user(tool_result)
	user, _ := msgs[1].(map[string]any)
	content, _ := user["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("user 消息应还原出 1 个 tool_result 块，实际 %d: %v", len(content), content)
	}
	tr, _ := content[0].(map[string]any)
	if tr["type"] != "tool_result" || tr["tool_use_id"] != "tu_1" {
		t.Fatalf("tool_result 块应还原，实际 %v", tr)
	}
	// content 应还原为块数组：text + image
	inner, _ := tr["content"].([]any)
	if len(inner) != 2 {
		t.Fatalf("tool_result 内容应还原为 2 块（text+image），实际 %d: %v", len(inner), tr["content"])
	}
	img, _ := inner[1].(map[string]any)
	if img["type"] != "image" {
		t.Fatalf("第二块应为 image，实际 %v", img)
	}
	src, _ := img["source"].(map[string]any)
	if src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] != "aW1n" {
		t.Fatalf("image source 应原样保真，实际 %v", src)
	}
}

// TestToolResultTextOnlyStaysString 纯文本工具结果仍是字符串（最小报文，零回归）。
func TestToolResultTextOnlyStaysString(t *testing.T) {
	req := `{
		"model": "claude-x",
		"max_tokens": 1024,
		"messages": [
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "tu_1", "name": "f", "input": {}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "tu_1", "content": "普通文本结果"}
			]}
		]
	}`
	mid, err := AnthropicRequestToOpenAIChat([]byte(req))
	if err != nil {
		t.Fatal(err)
	}
	var midParsed struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(mid, &midParsed); err != nil {
		t.Fatal(err)
	}
	for _, m := range midParsed.Messages {
		if m["role"] == "tool" {
			if s, ok := m["content"].(string); !ok || s != "普通文本结果" {
				t.Fatalf("纯文本工具结果应保持字符串，实际 %T %v", m["content"], m["content"])
			}
		}
	}

	out, err := OpenAIChatToAnthropicRequest(mid)
	if err != nil {
		t.Fatal(err)
	}
	msgs := messagesOf(t, out)
	user, _ := msgs[1].(map[string]any)
	content, _ := user["content"].([]any)
	tr, _ := content[0].(map[string]any)
	if s, ok := tr["content"].(string); !ok || s != "普通文本结果" {
		t.Fatalf("纯文本工具结果往返后应仍是字符串，实际 %T %v", tr["content"], tr["content"])
	}
}
