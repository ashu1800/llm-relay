package api

import "testing"

// 「测试连通性」要在界面上给出一个「确实通了」的证据。
//
// 这里的坑：推理型模型（GLM / deepseek-reasoner 这一类）常把 64 个 token 的预算
// 全花在思考上，正文 content 是空串。如果只认 content，界面会显示
// 「通了但什么都没返回」—— 明明成功却看起来像失败。
//
// 站内统一把思维链放在 reasoning 字段（Anthropic / Responses / Gemini 三个
// 出站适配器都写这里），OpenAI 原生上游则用 reasoning_content，两个都要认。
func TestExtractReplyFallsBackToReasoning(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"正文优先", `{"choices":[{"message":{"content":"hello","reasoning":"think"}}]}`, "hello"},
		{"OpenAI 原生思维链", `{"choices":[{"message":{"content":"","reasoning_content":"thinking"}}]}`, "thinking"},
		{"站内统一 reasoning", `{"choices":[{"message":{"content":"","reasoning":"thinking"}}]}`, "thinking"},
		{"两者都有时优先 reasoning_content", `{"choices":[{"message":{"reasoning_content":"a","reasoning":"b"}}]}`, "a"},
		{"全空则返回空串", `{"choices":[{"message":{"content":"","reasoning":""}}]}`, ""},
		{"没有 choices", `{"error":{"message":"boom"}}`, ""},
		{"非法 JSON", `not json`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractReply([]byte(c.body)); got != c.want {
				t.Errorf("期望 %q，实际 %q", c.want, got)
			}
		})
	}
}
