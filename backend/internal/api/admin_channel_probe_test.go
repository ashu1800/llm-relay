package api

import (
	"strings"
	"testing"
)

// 「测试连通性」要在界面上给出一个「确实通了」的证据。
//
// 这里的坑：推理型模型（GLM / deepseek-reasoner 这一类）常把输出预算
// 全花在思考上，正文 content 是空串。如果只认 content，界面会显示
// 「通了但什么都没返回」—— 明明成功却看起来像失败。
//
// 站内统一把思维链放在 reasoning 字段（Anthropic / Responses / Gemini 三个
// 出站适配器都写这里），OpenAI 原生上游则用 reasoning_content，两个都要认。
// finish_reason 一并带出：正文仍为空时它能说明为什么（length = 预算耗尽）。
func TestExtractReplyFallsBackToReasoning(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		want       string
		wantFinish string
	}{
		{"正文优先", `{"choices":[{"message":{"content":"hello","reasoning":"think"}}]}`, "hello", ""},
		{"OpenAI 原生思维链", `{"choices":[{"message":{"content":"","reasoning_content":"thinking"}}]}`, "thinking", ""},
		{"站内统一 reasoning", `{"choices":[{"message":{"content":"","reasoning":"thinking"}}]}`, "thinking", ""},
		{"两者都有时优先 reasoning_content", `{"choices":[{"message":{"reasoning_content":"a","reasoning":"b"}}]}`, "a", ""},
		{"全空则返回空串", `{"choices":[{"message":{"content":"","reasoning":""}}]}`, "", ""},
		{"全空带 finish_reason", `{"choices":[{"finish_reason":"length","message":{"content":""}}]}`, "", "length"},
		{"没有 choices", `{"error":{"message":"boom"}}`, "", ""},
		{"非法 JSON", `not json`, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, finish := extractReply([]byte(c.body))
			if got != c.want {
				t.Errorf("期望 %q，实际 %q", c.want, got)
			}
			if finish != c.wantFinish {
				t.Errorf("finish_reason 期望 %q，实际 %q", c.wantFinish, finish)
			}
		})
	}
}

// 上游无视 stream:false 硬回 SSE 时，正文在流里。逐块拼接、思维链兜底、
// finish_reason 取最后一个非空值。
func TestReadReplyFromStream(t *testing.T) {
	cases := []struct {
		name       string
		sse        string
		want       string
		wantFinish string
	}{
		{
			"正文分块拼接",
			"data: {\"choices\":[{\"delta\":{\"content\":\"He\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"llo\"},\"finish_reason\":null}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n",
			"Hello", "stop",
		},
		{
			"思维链兜底（正文一个字都没有）",
			"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"思考中\"}}]}\n\ndata: [DONE]\n\n",
			"思考中", "",
		},
		{
			"站内 reasoning 字段兜底",
			"data: {\"choices\":[{\"delta\":{\"reasoning\":\"嗯\"}}]}\n\ndata: [DONE]\n\n",
			"嗯", "",
		},
		{
			"正文优先于思维链",
			"data: {\"choices\":[{\"delta\":{\"content\":\"答\",\"reasoning_content\":\"思\"}}]}\n\ndata: [DONE]\n\n",
			"答", "",
		},
		{
			"不讲理的上游：整块 chat.completion JSON 直接按 chunked 传输（无 data: 前缀）",
			"{\"id\":\"cmb-1\",\"model\":\"glm\",\"object\":\"chat.completion\",\"choices\":[{\"index\":0,\"message\":{\"role\":\"\",\"content\":\"完整正文\",\"reasoning_content\":\"思维链\"}}]}",
			"完整正文", "",
		},
		{
			"多行 JSON + SSE 混合也能拼",
			"data: {\"choices\":[{\"delta\":{\"content\":\"流式\"}}]}\n\n{\"choices\":[{\"message\":{\"content\":\"+整块\"}}]}\n\ndata: [DONE]\n\n",
			"流式+整块", "",
		},
		{"空流", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, finish := readReplyFromStream(strings.NewReader(c.sse))
			if got != c.want {
				t.Errorf("期望 %q，实际 %q", c.want, got)
			}
			if finish != c.wantFinish {
				t.Errorf("finish_reason 期望 %q，实际 %q", c.wantFinish, finish)
			}
		})
	}
}

// 空流：上游偶尔直接回一个空 body，正文与结束原因都是空串，
// 界面据此显示「上游没有返回内容」而不是误报成功。
func TestReadReplyFromStreamEmpty(t *testing.T) {
	got, finish := readReplyFromStream(strings.NewReader(""))
	if got != "" || finish != "" {
		t.Fatalf("空流期望空串，实际 %q/%q", got, finish)
	}
}

// 多协议响应形状：探测拿到的是上游原生形状，正文/结束原因的字段名各说各话。
func TestExtractReplyMultiProtocol(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		want       string
		wantFinish string
	}{
		{
			"Anthropic content 块",
			`{"content":[{"type":"thinking","text":"…"},{"type":"text","text":"你好"}],"stop_reason":"end_turn"}`,
			"你好", "end_turn",
		},
		{
			"Responses output_text",
			`{"output":[{"type":"message","content":[{"type":"output_text","text":"回复"}]}],"status":"completed"}`,
			"回复", "completed",
		},
		{
			"Gemini candidates parts",
			`{"candidates":[{"content":{"parts":[{"text":"答案"}]},"finishReason":"STOP"}]}`,
			"答案", "STOP",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, finish := extractReply([]byte(c.body))
			if got != c.want {
				t.Errorf("期望 %q，实际 %q", c.want, got)
			}
			if finish != c.wantFinish {
				t.Errorf("finish_reason 期望 %q，实际 %q", c.wantFinish, finish)
			}
		})
	}
}
