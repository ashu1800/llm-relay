package convert

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// 入站 Gemini 流式：只回工具调用、没有正文的流，必须把 functionCall 发出去。
//
// 修复前这里写的是 parts: [] + finishReason: STOP —— 客户端收到一个
// 语法完全合法、状态为正常结束的空回复，而模型其实想调工具。
func TestGeminiStreamEmitsToolCalls(t *testing.T) {
	var buf bytes.Buffer
	tr := NewGeminiStreamTranslator(&buf, "gemini-2.5-pro")

	// Chat 分片：name 在第一片，arguments 分两片拼出完整 JSON
	feed := []string{
		`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"ci"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ty\":\"北京\"}"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":2}}` + "\n\n",
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

	if !strings.Contains(got, "functionCall") {
		t.Fatalf("工具调用必须出现在输出里，实际:\n%s", got)
	}
	if !strings.Contains(got, `"name":"get_weather"`) {
		t.Fatalf("函数名丢失:\n%s", got)
	}

	// 参数必须是从分片里拼出来的完整对象，而不是被丢弃
	var args map[string]any
	for _, line := range strings.Split(got, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) != nil {
			continue
		}
		cands, _ := ev["candidates"].([]any)
		if len(cands) == 0 {
			continue
		}
		content := asMap(asMap(cands[0])["content"])
		parts, _ := content["parts"].([]any)
		for _, p := range parts {
			fc := asMap(asMap(p)["functionCall"])
			if fc != nil && asString(fc["name"]) == "get_weather" {
				args = asMap(fc["args"])
			}
		}
	}
	if args == nil {
		t.Fatalf("没能解析出 functionCall 的 args:\n%s", got)
	}
	if asString(args["city"]) != "北京" {
		t.Fatalf("分片拼接的参数不正确: %v", args)
	}
}

// 纯文本流不应因为这次改动多出 functionCall。
func TestGeminiStreamTextOnlyUnaffected(t *testing.T) {
	var buf bytes.Buffer
	tr := NewGeminiStreamTranslator(&buf, "m")
	if _, err := tr.Write([]byte(`data: {"id":"c","choices":[{"index":0,"delta":{"content":"你好"},"finish_reason":null}]}` + "\n\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("收尾失败: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "functionCall") {
		t.Fatalf("纯文本流不该出现 functionCall:\n%s", got)
	}
	if !strings.Contains(got, `"text":"你好"`) {
		t.Fatalf("文本增量丢失:\n%s", got)
	}
}

// 多个工具调用的顺序必须稳定（按 index 排序，而非 map 随机序）。
func TestGeminiStreamMultipleToolCallsOrder(t *testing.T) {
	var buf bytes.Buffer
	tr := NewGeminiStreamTranslator(&buf, "m")
	frame := `data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[` +
		`{"index":0,"function":{"name":"first","arguments":"{}"}},` +
		`{"index":1,"function":{"name":"second","arguments":"{}"}}]}}]}` + "\n\n"
	if _, err := tr.Write([]byte(frame)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("收尾失败: %v", err)
	}
	got := buf.String()
	if strings.Index(got, "first") > strings.Index(got, "second") {
		t.Fatalf("多个调用应按 index 顺序发出:\n%s", got)
	}
}

// 参数不是合法 JSON 时退成空对象，绝不把半截 JSON 发出去。
func TestGeminiStreamTruncatedArgsFallBackToEmpty(t *testing.T) {
	var buf bytes.Buffer
	tr := NewGeminiStreamTranslator(&buf, "m")
	frame := `data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[` +
		`{"index":0,"function":{"name":"f","arguments":"{\"a\":1"}}]}}]}` + "\n\n"
	if _, err := tr.Write([]byte(frame)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("收尾失败: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, `"args":{}`) {
		t.Fatalf("截断的参数应退成空对象，实际:\n%s", got)
	}
	if strings.Contains(got, `{\"a\"`) {
		t.Fatalf("不应把半截 JSON 原样发出:\n%s", got)
	}
}
