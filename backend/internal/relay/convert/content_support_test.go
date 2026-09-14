package convert

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"llm-relay/internal/model"
)

// M14c：instructions 是数组时要收下，不能静默丢系统提示。
//
// 系统提示承载人设、安全约束、输出格式要求。原来只认字符串，
// 数组形态被静默丢掉，表现为「模型不听话」而不是任何一条报错 ——
// 这类 bug 用户几乎不可能自己定位。
func TestResponsesInstructionsArrayFormIsKept(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"instructions": [
			{"type": "input_text", "text": "你是一个只回答中文的助手。"},
			{"type": "text", "text": "不要编造事实。"}
		],
		"input": "你好"
	}`)
	out, err := ResponsesRequestToOpenAIChat(body)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("messages 为空：%s", out)
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("第一条应为 system，实际 %v", first["role"])
	}
	content, _ := first["content"].(string)
	if !strings.Contains(content, "只回答中文") || !strings.Contains(content, "不要编造事实") {
		t.Errorf("系统提示的两段都应保留，实际 %q", content)
	}
}

// 字符串形态不能被这次改动破坏。
func TestResponsesInstructionsStringFormStillWorks(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","instructions":"你是助手","input":"嗨"}`)
	out, err := ResponsesRequestToOpenAIChat(body)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	if !strings.Contains(string(out), "你是助手") {
		t.Fatalf("字符串形态的系统提示丢了：%s", out)
	}
}

// 非文本块不应被塞进 system（例如图片的 base64）。
func TestResponsesInstructionsSkipsNonTextBlocks(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"instructions": [
			{"type": "input_image", "image_url": "data:image/png;base64,AAAA"},
			{"type": "input_text", "text": "只要文字"}
		],
		"input": "x"
	}`)
	out, err := ResponsesRequestToOpenAIChat(body)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "AAAA") {
		t.Errorf("图片数据不该进 system：%s", s)
	}
	if !strings.Contains(s, "只要文字") {
		t.Errorf("同一数组里的文本块应保留：%s", s)
	}
}

// instructions 缺失或为 null 时不应产生空的 system 消息。
func TestResponsesInstructionsAbsentProducesNoSystemMessage(t *testing.T) {
	for _, body := range []string{
		`{"model":"gpt-4o","input":"嗨"}`,
		`{"model":"gpt-4o","instructions":null,"input":"嗨"}`,
		`{"model":"gpt-4o","instructions":"","input":"嗨"}`,
		`{"model":"gpt-4o","instructions":[],"input":"嗨"}`,
	} {
		out, err := ResponsesRequestToOpenAIChat([]byte(body))
		if err != nil {
			t.Fatalf("转换失败 (%s): %v", body, err)
		}
		var got map[string]any
		_ = json.Unmarshal(out, &got)
		msgs, _ := got["messages"].([]any)
		for _, m := range msgs {
			if mm, ok := m.(map[string]any); ok && mm["role"] == "system" {
				t.Errorf("不该产生 system 消息（%s）：%s", body, out)
			}
		}
	}
}

// M14d：asInt 要认 json.Number 与数字字符串。
//
// 现在不触发（包内没有 UseNumber），但 relay.getInt 专门处理了 json.Number，
// 说明这个形态在本项目里是预期会出现的。不认它会让 max_tokens 静默变 0。
func TestAsIntHandlesJsonNumberAndStrings(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{float64(100), 100},
		{int(7), 7},
		{int64(9), 9},
		{json.Number("42"), 42},
		{json.Number("3.7"), 3}, // 非整数退回浮点再截断，不丢整段
		{json.Number("abc"), 0},
		{"128", 128},
		{" 256 ", 256},
		{"1.9", 1},
		{"", 0},
		{nil, 0},
		{true, 0},
	}
	for _, c := range cases {
		if got := asInt(c.in); got != c.want {
			t.Errorf("asInt(%#v) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

// UseNumber 解码后 max_tokens 仍要生效 —— 这是 M14d 真正要防的回归。
func TestMaxTokensSurvivesUseNumberDecoding(t *testing.T) {
	dec := json.NewDecoder(strings.NewReader(`{"model":"m","max_tokens":321,"messages":[]}`))
	dec.UseNumber()
	var src map[string]any
	if err := dec.Decode(&src); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if got := asInt(src["max_tokens"]); got != 321 {
		t.Fatalf("max_tokens 应解出 321，实际 %d", got)
	}
}

// M14e：内容块无法表达时要报错，而不是静默丢掉。
func TestAnthropicContentBlocksRejectUnsupportedTypes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // 错误里应出现的关键字
	}{
		{
			name: "音频",
			body: `{"model":"m","messages":[{"role":"user","content":[
				{"type":"text","text":"听听这个"},
				{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}]}]}`,
			want: "音频",
		},
		{
			name: "文件",
			body: `{"model":"m","messages":[{"role":"user","content":[
				{"type":"file","file":{"filename":"a.pdf"}}]}]}`,
			want: "文件",
		},
		{
			name: "未知类型",
			body: `{"model":"m","messages":[{"role":"user","content":[
				{"type":"video_url","video_url":{"url":"http://x/y.mp4"}}]}]}`,
			want: "video_url",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := OpenAIChatToAnthropicRequest([]byte(c.body))
			if err == nil {
				t.Fatal("应报错而不是静默丢内容")
			}
			if !errors.Is(err, ErrUnsupportedContent) {
				t.Errorf("错误应可被 errors.Is(ErrUnsupportedContent) 识别，实际 %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误信息应提到 %q，实际 %q", c.want, err.Error())
			}
		})
	}
}

// 正常内容不能被这次改动误伤。
func TestAnthropicNormalContentStillConverts(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[
		{"type":"text","text":"你好"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]}]}`)
	out, err := OpenAIChatToAnthropicRequest(body)
	if err != nil {
		t.Fatalf("文本+图片的组合不该报错: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "你好") || !strings.Contains(s, "image") {
		t.Errorf("内容丢了：%s", s)
	}
}

// 纯字符串 content 的常见形态也要照常工作。
func TestAnthropicStringContentStillConverts(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"一句话"}]}`)
	out, err := OpenAIChatToAnthropicRequest(body)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if !strings.Contains(string(out), "一句话") {
		t.Errorf("内容丢了：%s", out)
	}
}

// 转换错误必须能被 relay 层识别为「客户端问题」，否则会被当成渠道故障重试。
func TestUnsupportedContentIsClassifiedForCaller(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[
		{"type":"input_audio","input_audio":{"data":"AA","format":"wav"}}]}]}`)
	_, _, err := UpstreamRequest(model.ProtocolAnthropic, "/v1/chat/completions", body, "claude")
	if err == nil {
		t.Fatal("应报错")
	}
	if !errors.Is(err, ErrUnsupportedContent) {
		t.Fatalf("必须能被 errors.Is 识别，否则 relay 会重试所有渠道并记渠道失败：%v", err)
	}
}

// embeddings 这类非对话路径不做协议转换，也就不该被内容块检查影响。
func TestNonChatPathUnaffectedByContentCheck(t *testing.T) {
	body := []byte(`{"model":"m","input":"文本"}`)
	path, out, err := UpstreamRequest(model.ProtocolAnthropic, "/v1/embeddings", body, "")
	if err != nil {
		t.Fatalf("embeddings 不该走内容块转换: %v", err)
	}
	if path != "/v1/embeddings" {
		t.Errorf("路径不该被改写，实际 %s", path)
	}
	if string(out) != string(body) {
		t.Errorf("请求体不该被改动：%s", out)
	}
}
