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
		// ---- anthropic 4.6+ 形状：adaptive + output_config.effort ----
		// Claude Code 的实际报文（真实抓包，省略 messages）：
		//   "max_tokens":32000,"thinking":{"type":"adaptive"},
		//   "context_management":{...},"output_config":{"effort":"max"},"stream":true
		// budget_tokens 在这一代已废弃（Opus 5 / Sonnet 5 上直接 400），
		// effort 取代它表达强度。
		{"Claude Code adaptive + effort max", protoAnthropic, `{"max_tokens":32000,"thinking":{"type":"adaptive"},"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"output_config":{"effort":"max"},"stream":true}`, "max"},
		{"anthropic effort 单独（没带 thinking）", protoAnthropic, `{"output_config":{"effort":"low"}}`, "low"},
		{"anthropic adaptive 单独", protoAnthropic, `{"thinking":{"type":"adaptive"}}`, "auto"},
		{"anthropic effort xhigh 原样透传", protoAnthropic, `{"output_config":{"effort":"xhigh"}}`, "xhigh"},
		{"anthropic effort 大写归一", protoAnthropic, `{"output_config":{"effort":"HIGH"}}`, "high"},
		// 「明确关掉」压过 effort：off 是「说了不要」，effort 在不想的时候没有意义
		{"anthropic disabled 优先于 effort", protoAnthropic, `{"thinking":{"type":"disabled"},"output_config":{"effort":"max"}}`, "off"},
		// 老形状不受影响（budget_tokens 仍是 4.6 之前的合法写法）
		{"anthropic enabled 仍走 budget 分档", protoAnthropic, `{"thinking":{"type":"enabled","budget_tokens":1024}}`, "low"},
		{"anthropic adaptive + enabled 缺 budget 回 on", protoAnthropic, `{"thinking":{"type":"enabled"},"context_management":{}}`, "on"},
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

// SupportedEfforts：行上显式声明优先，空声明回落到按协议推导。
//
// 两层缺一不可 —— 没有显式声明时，/v1/models 不能对所有模型一律回「八档全在」
// （OpenAI 兼容上游的 off/auto/minimal 发过去就是 400），也不能一律回空
// （客户端会以为这些模型不能用思考）。
func TestSupportedEfforts(t *testing.T) {
	cases := []struct {
		name     string
		declared []string
		protocol string
		want     []string
	}{
		{"没声明 + anthropic 渠道 → 全部八档", nil, "anthropic-messages",
			[]string{"off", "auto", "minimal", "low", "medium", "high", "xhigh", "max"}},
		{"没声明 + openai 渠道 → 五档", nil, "openai-chat",
			[]string{"low", "medium", "high", "xhigh", "max"}},
		{"没声明 + embeddings → 空", nil, "openai-embeddings", nil},
		// 声明原样生效，即使它比协议「能表达的」更宽：用户选的是上游的真实能力，
		// 中转后的协议不等于它直连时的表现（例如 openai-chat 渠道背后的网关
		// 其实认 off）。声明是承诺，不是提示。
		{"显式声明优先于协议推导", []string{"off", "max"}, "openai-chat", []string{"off", "max"}},
		{"声明里的重复与大小写会被归一", []string{"Max", "low", "max"}, "anthropic-messages", []string{"low", "max"}},
		// 全是无效条目 = 等于没配：不能因为「填了几个空格」就声明成空能力
		{"声明全无效 → 回落协议推导", []string{"", "  "}, "anthropic-messages",
			[]string{"off", "auto", "minimal", "low", "medium", "high", "xhigh", "max"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SupportedEfforts(c.declared, c.protocol)
			if len(got) != len(c.want) {
				t.Fatalf("得到 %v，期望 %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("得到 %v，期望 %v", got, c.want)
				}
			}
		})
	}
}

// MergeModelEfforts：同名模型挂在多条渠道上时取并集。
//
// 取并集而不是交集：请求最终只落在一条渠道上，只要有一条能表达这个档位，
// 这个档位就有意义。顺序必须是站内强度序（客户端按下标读会按它展示）。
func TestMergeModelEfforts(t *testing.T) {
	cases := []struct {
		name       string
		perChannel [][]string
		want       []string
	}{
		{"没有渠道 → nil", nil, nil},
		{"每条渠道都空 → nil", [][]string{nil, {}}, nil},
		{"单条渠道原样", [][]string{{"low", "high"}}, []string{"low", "high"}},
		{"两条渠道取并集并按强度序", [][]string{{"high", "max"}, {"low", "high"}}, []string{"low", "high", "max"}},
		{"一条空不影响另一条", [][]string{nil, {"medium"}}, []string{"medium"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MergeModelEfforts(c.perChannel)
			if len(got) != len(c.want) {
				t.Fatalf("得到 %v，期望 %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("得到 %v，期望 %v", got, c.want)
				}
			}
		})
	}
}
