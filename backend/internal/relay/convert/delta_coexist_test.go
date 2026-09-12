package convert

import (
	"bytes"
	"strings"
	"testing"
)

// 同一个 delta 里同时出现正文与工具调用时，两者都必须发出去。
//
// 上游把 content 和 tool_calls 放在同一个 delta 里是完全合法的（有些网关和模型
// 就是这么发的）。原来每个分支各自 return，一个 delta 里只有第一个字段能活下来 ——
// 工具调用被静默丢弃，客户端（Claude Code 等）于是认为模型没打算调用工具，
// 一次本该继续的对话就此中断，而且没有任何错误提示。
func TestDeltaKeepsContentAndToolCallsTogether(t *testing.T) {
	// 一个 delta 同时带正文和工具调用
	chunk := "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{" +
		"\"content\":\"我来查一下天气\"," +
		"\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\"," +
		"\"function\":{\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"北京\\\"}\"}}]" +
		"},\"finish_reason\":null}]}\n\n"

	t.Run("Anthropic", func(t *testing.T) {
		var buf bytes.Buffer
		tr := NewAnthropicStreamTranslator(&buf, "m")
		if _, err := tr.Write([]byte(chunk)); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
		got := buf.String()
		if !strings.Contains(got, "text_delta") || !strings.Contains(got, "我来查一下天气") {
			t.Errorf("正文被丢弃了。实际输出:\n%s", got)
		}
		if !strings.Contains(got, "tool_use") || !strings.Contains(got, "get_weather") {
			t.Errorf("工具调用被丢弃了 —— 客户端会以为模型没要调工具。实际输出:\n%s", got)
		}
		if !strings.Contains(got, "call_1") {
			t.Errorf("工具调用 id 应一并下发。实际输出:\n%s", got)
		}
	})

	t.Run("OpenAI Responses", func(t *testing.T) {
		var buf bytes.Buffer
		tr := NewResponsesStreamTranslator(&buf, "m")
		if _, err := tr.Write([]byte(chunk)); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
		got := buf.String()
		if !strings.Contains(got, "response.output_text.delta") {
			t.Errorf("正文被丢弃了。实际输出:\n%s", got)
		}
		if !strings.Contains(got, "function_call") || !strings.Contains(got, "get_weather") {
			t.Errorf("工具调用被丢弃了。实际输出:\n%s", got)
		}
	})
}

// 思维链与正文同处一个 delta 时同样不能丢。
func TestDeltaKeepsReasoningAndContentTogether(t *testing.T) {
	chunk := "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{" +
		"\"reasoning\":\"先想一下\",\"content\":\"答案是 42\"},\"finish_reason\":null}]}\n\n"

	t.Run("Anthropic", func(t *testing.T) {
		var buf bytes.Buffer
		tr := NewAnthropicStreamTranslator(&buf, "m")
		if _, err := tr.Write([]byte(chunk)); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
		got := buf.String()
		if !strings.Contains(got, "thinking_delta") || !strings.Contains(got, "先想一下") {
			t.Errorf("思维链被丢弃。实际输出:\n%s", got)
		}
		if !strings.Contains(got, "text_delta") || !strings.Contains(got, "答案是 42") {
			t.Errorf("正文被丢弃。实际输出:\n%s", got)
		}
		if strings.Index(got, "先想一下") > strings.Index(got, "答案是 42") {
			t.Errorf("思维链应在正文之前发出。实际输出:\n%s", got)
		}
	})

	t.Run("OpenAI Responses", func(t *testing.T) {
		var buf bytes.Buffer
		tr := NewResponsesStreamTranslator(&buf, "m")
		if _, err := tr.Write([]byte(chunk)); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
		got := buf.String()
		if !strings.Contains(got, "response.reasoning_summary_text.delta") {
			t.Errorf("思维链被丢弃。实际输出:\n%s", got)
		}
		if !strings.Contains(got, "response.output_text.delta") {
			t.Errorf("正文被丢弃。实际输出:\n%s", got)
		}
	})
}
