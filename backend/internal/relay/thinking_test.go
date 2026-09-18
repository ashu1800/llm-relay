package relay

import "testing"

// 各入站协议的思考等级提取：样例取自真实客户端请求体的关键字段形状。
// 协议名是 api/protocols.go 里 inboundProfile.Name 的字面量，改协议名时
// 这里会先红。
func TestExtractThinkingLevel(t *testing.T) {
	cases := []struct {
		name  string
		proto string
		body  string
		want  string
	}{
		// ---- openai-chat ----
		{"openai effort low", protoOpenAIChat, `{"model":"gpt-5","reasoning_effort":"low"}`, "low"},
		{"openai effort high 大写归一", protoOpenAIChat, `{"reasoning_effort":"HIGH"}`, "high"},
		{"openai effort none 即关闭", protoOpenAIChat, `{"reasoning_effort":"none"}`, "off"},
		{"openai effort minimal", protoOpenAIChat, `{"reasoning_effort":"minimal"}`, "minimal"},
		{"GLM thinking enabled", protoOpenAIChat, `{"model":"glm-5.3","thinking":{"type":"enabled"}}`, "on"},
		{"GLM thinking disabled", protoOpenAIChat, `{"thinking":{"type":"disabled"}}`, "off"},
		{"openai 两者都有时 effort 优先", protoOpenAIChat, `{"reasoning_effort":"low","thinking":{"type":"enabled"}}`, "low"},
		{"openai 什么都没有", protoOpenAIChat, `{"model":"deepseek-chat"}`, ""},
		// ---- openai-responses ----
		{"responses reasoning.effort", protoOpenAIResponse, `{"model":"o4","reasoning":{"effort":"medium","summary":"auto"}}`, "medium"},
		{"responses 没有 reasoning", protoOpenAIResponse, `{"model":"o4"}`, ""},
		// ---- anthropic-messages ----
		{"anthropic budget 1024 低档", protoAnthropic, `{"thinking":{"type":"enabled","budget_tokens":1024}}`, "low"},
		{"anthropic budget 8192 中档（边界含）", protoAnthropic, `{"thinking":{"type":"enabled","budget_tokens":8192}}`, "medium"},
		{"anthropic budget 16384 高档（边界含）", protoAnthropic, `{"thinking":{"type":"enabled","budget_tokens":16384}}`, "high"},
		{"anthropic enabled 缺 budget 回 on", protoAnthropic, `{"thinking":{"type":"enabled"}}`, "on"},
		{"anthropic disabled", protoAnthropic, `{"thinking":{"type":"disabled"}}`, "off"},
		{"anthropic 没有 thinking", protoAnthropic, `{"model":"claude-x","max_tokens":16}`, ""},
		// ---- gemini-generateContent ----
		{"gemini thinkingLevel 优先", protoGemini, `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high","thinkingBudget":1024}}}`, "high"},
		{"gemini budget 0 关", protoGemini, `{"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`, "off"},
		{"gemini budget -1 动态", protoGemini, `{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`, "auto"},
		{"gemini budget 4096 低", protoGemini, `{"generationConfig":{"thinkingConfig":{"thinkingBudget":4096}}}`, "low"},
		{"gemini budget 24576 高", protoGemini, `{"generationConfig":{"thinkingConfig":{"thinkingBudget":24576}}}`, "high"},
		// ---- 其他 ----
		{"embeddings 协议不提取", "openai-embeddings", `{"model":"e","input":"x"}`, ""},
		{"未知协议", "something-else", `{"reasoning_effort":"low"}`, ""},
		{"空请求体", protoOpenAIChat, ``, ""},
		{"坏 JSON 不炸", protoAnthropic, `{"thinking":`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractThinkingLevel(tc.proto, []byte(tc.body)); got != tc.want {
				t.Fatalf("ExtractThinkingLevel(%q) = %q, want %q", tc.proto, got, tc.want)
			}
		})
	}
}
