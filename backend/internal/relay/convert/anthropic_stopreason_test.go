package convert

import (
	"encoding/json"
	"testing"
)

// 截断必须优先于「有工具调用」的推断。
//
// 修复前：只要 content 非空就无条件置 tool_use，于是模型在输出工具调用时
// 撞上 max_tokens 会被报成 tool_use。客户端据此认为参数完整并拿去执行，
// 实际拿到的是被从中间截断的 JSON —— 静默的数据损坏。
func TestAnthropicStopReasonKeepsMaxTokensWithToolCalls(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-1","created":1700000000,"model":"m",
		"choices":[{"index":0,"finish_reason":"length","message":{
			"role":"assistant","content":null,
			"tool_calls":[{"id":"call_1","type":"function",
				"function":{"name":"write_file","arguments":"{\"path\":\"/tmp/a\",\"content\":\"half"}}]
		}}],
		"usage":{"prompt_tokens":10,"completion_tokens":20}
	}`)

	out, err := OpenAIChatToAnthropicResponse(body, "m")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	if got["stop_reason"] != "max_tokens" {
		t.Fatalf("截断必须报 max_tokens，实际 %v（原文：被 tool_use 覆盖）", got["stop_reason"])
	}
	// 工具块本身仍要保留，只是结束原因不能骗人
	content, _ := got["content"].([]any)
	foundToolUse := false
	for _, c := range content {
		if asString(asMap(c)["type"]) == "tool_use" {
			foundToolUse = true
		}
	}
	if !foundToolUse {
		t.Fatalf("tool_use 块不应因为截断而丢失: %v", content)
	}
}

// 正常结束的工具调用仍要报 tool_use。
func TestAnthropicStopReasonToolUseOnNormalFinish(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-1","created":1700000000,"model":"m",
		"choices":[{"index":0,"finish_reason":"tool_calls","message":{
			"role":"assistant","content":null,
			"tool_calls":[{"id":"call_1","type":"function",
				"function":{"name":"get_weather","arguments":"{\"city\":\"北京\"}"}}]
		}}],
		"usage":{"prompt_tokens":10,"completion_tokens":20}
	}`)

	out, err := OpenAIChatToAnthropicResponse(body, "m")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	if got["stop_reason"] != "tool_use" {
		t.Fatalf("正常结束的工具调用应报 tool_use，实际 %v", got["stop_reason"])
	}
}

// 没有工具调用、且上游报 length 时，仍应是 max_tokens（回归保护）。
func TestAnthropicStopReasonPlainTruncation(t *testing.T) {
	body := []byte(`{
		"id":"c","created":1,"model":"m",
		"choices":[{"index":0,"finish_reason":"length","message":{"role":"assistant","content":"半截"}}],
		"usage":{"prompt_tokens":1,"completion_tokens":2}
	}`)
	out, err := OpenAIChatToAnthropicResponse(body, "m")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	if got["stop_reason"] != "max_tokens" {
		t.Fatalf("期望 max_tokens，实际 %v", got["stop_reason"])
	}
}

// 空 tool_calls 数组不应把 end_turn 误判成 tool_use。
func TestAnthropicStopReasonEmptyToolCalls(t *testing.T) {
	body := []byte(`{
		"id":"c","created":1,"model":"m",
		"choices":[{"index":0,"finish_reason":"stop","message":{
			"role":"assistant","content":"好了","tool_calls":[]
		}}],
		"usage":{"prompt_tokens":1,"completion_tokens":2}
	}`)
	out, err := OpenAIChatToAnthropicResponse(body, "m")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	if got["stop_reason"] != "end_turn" {
		t.Fatalf("空 tool_calls 应为 end_turn，实际 %v", got["stop_reason"])
	}
}
