package convert

import (
	"encoding/json"
	"strings"
	"testing"
)

// 思考强度的跨协议映射（第三轮审查 R-中1 / 设计文档
// docs/superpowers/specs/2026-09-19-thinking-effort-mapping-design.md）。
//
// 这一批用例盯的是同一条不变量：**入站说的思考强度，必须出现在出站请求里**
// （或按上游能力的边界显式降级，且降级方向只能是「无法表达」而不是「猜一个」）。
// 修复前的实际表现是静默丢弃 —— 客户端要了 high，上游按默认档跑，无任何信号。

// thinkingJSON 解析出参数字典。
func thinkingJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("解析失败: %v\n%s", err, b)
	}
	return m
}

// chatBodyWithEffort 造一个最小通用语请求，可带 reasoning_effort。
// effort 为空串时不带该字段（对应「客户端没说」）。
func chatBodyWithEffort(effort string) []byte {
	field := ""
	if effort != "" {
		field = `,"reasoning_effort":"` + effort + `"`
	}
	return []byte(`{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"}]` + field + `}`)
}

// ---------- 出站：通用语 → Anthropic ----------

func TestEffortToAnthropicUpstreamMapping(t *testing.T) {
	cases := []struct {
		effort     string
		wantType   string // thinking.type；空表示不该出现 thinking
		wantEffort string // output_config.effort；空表示不该出现 output_config
	}{
		{"off", "disabled", ""},
		{"none", "disabled", ""}, // none 与 off 同义
		{"auto", "adaptive", ""},
		{"minimal", "", "low"}, // Anthropic 没有 minimal：向上收敛到 low
		{"low", "", "low"},
		{"medium", "", "medium"},
		{"high", "", "high"},
		{"xhigh", "", "xhigh"},
		{"max", "", "max"},
	}
	for _, c := range cases {
		out, err := OpenAIChatToAnthropicRequest(chatBodyWithEffort(c.effort))
		if err != nil {
			t.Fatalf("%s: %v", c.effort, err)
		}
		got := thinkingJSON(t, out)
		th := asMap(got["thinking"])
		oc := asMap(got["output_config"])

		switch {
		case c.wantType == "" && th != nil:
			t.Fatalf("%s: 不该出现 thinking，实际 %v", c.effort, th)
		case c.wantType != "" && (th == nil || asString(th["type"]) != c.wantType):
			t.Fatalf("%s: thinking.type 应为 %s，实际 %v", c.effort, c.wantType, got["thinking"])
		}
		switch {
		case c.wantEffort == "" && oc != nil:
			t.Fatalf("%s: 不该出现 output_config，实际 %v", c.effort, oc)
		case c.wantEffort != "" && (oc == nil || asString(oc["effort"]) != c.wantEffort):
			t.Fatalf("%s: output_config.effort 应为 %s，实际 %v", c.effort, c.wantEffort, got["output_config"])
		}
		// budget_tokens 在 Opus 5 / Sonnet 5 这类新模型上会 400：
		// 只有「入站本来就是 Anthropic」的无损往返才允许带它
		if th != nil {
			if _, ok := th["budget_tokens"]; ok {
				t.Fatalf("%s: 跨协议映射不得发 budget_tokens，实际 %v", c.effort, th)
			}
		}
		// 通用语字段本身不得漏进上游请求体（Anthropic 对未知顶层字段是 400）
		if _, ok := got[reasoningEffortField]; ok {
			t.Fatalf("%s: reasoning_effort 不应出现在 Anthropic 请求体里", c.effort)
		}
	}
}

// 客户端没说思考强度时不得凭空注入：那会改掉用户本来没提的参数。
func TestNoEffortLeavesAnthropicRequestClean(t *testing.T) {
	out, err := OpenAIChatToAnthropicRequest(chatBodyWithEffort(""))
	if err != nil {
		t.Fatal(err)
	}
	got := thinkingJSON(t, out)
	if got["thinking"] != nil || got["output_config"] != nil {
		t.Fatalf("没有思考参数时不该注入任何思考字段，实际 %v", got)
	}
}

// 认不得的档位原样透传：让上游回一条明确错误，好过在中转站里静默降级。
func TestUnknownEffortPassesThroughToAnthropic(t *testing.T) {
	out, err := OpenAIChatToAnthropicRequest(chatBodyWithEffort("turbo"))
	if err != nil {
		t.Fatal(err)
	}
	got := thinkingJSON(t, out)
	if oc := asMap(got["output_config"]); oc == nil || asString(oc["effort"]) != "turbo" {
		t.Fatalf("未知档位应原样透传给上游，实际 %v", got["output_config"])
	}
}

// ---------- 出站：通用语 → Gemini ----------

func TestEffortToGeminiUpstreamMapping(t *testing.T) {
	cases := []struct {
		effort     string
		wantBudget int
		wantNone   bool // true 表示不该出现 generationConfig.thinkingConfig
	}{
		{"off", 0, false},
		{"none", 0, false},
		{"auto", -1, false},
		{"minimal", ThinkingBudgetMinimal, false},
		{"low", ThinkingBudgetLow, false},
		{"medium", ThinkingBudgetMedium, false},
		{"high", ThinkingBudgetHigh, false},
		{"xhigh", ThinkingBudgetXHigh, false},
		{"max", ThinkingBudgetMax, false},
		{"turbo", 0, true}, // 认不得的档位不发：预算要数值，猜一个等于替用户改预算
		{"", 0, true},
	}
	for _, c := range cases {
		out, err := OpenAIChatToGeminiRequest(chatBodyWithEffort(c.effort))
		if err != nil {
			t.Fatalf("%s: %v", c.effort, err)
		}
		got := thinkingJSON(t, out)
		gc := asMap(got["generationConfig"])
		var tc map[string]any
		if gc != nil {
			tc = asMap(gc["thinkingConfig"])
		}
		switch {
		case c.wantNone && tc != nil:
			t.Fatalf("%s: 不该出现 thinkingConfig，实际 %v", c.effort, tc)
		case !c.wantNone && tc == nil:
			t.Fatalf("%s: 应有 thinkingConfig，实际 %v", c.effort, got)
		case !c.wantNone && asInt(tc["thinkingBudget"]) != c.wantBudget:
			t.Fatalf("%s: thinkingBudget 应为 %d，实际 %v", c.effort, c.wantBudget, tc)
		}
		if _, ok := got[reasoningEffortField]; ok {
			t.Fatalf("%s: reasoning_effort 不应出现在 Gemini 请求体里", c.effort)
		}
	}
}

// ---------- 出站：通用语 → Responses ----------

func TestEffortToResponsesUpstreamMapping(t *testing.T) {
	cases := []struct {
		effort string
		want   string // reasoning.effort；空表示不该出现 reasoning
	}{
		{"off", ""}, // Responses 侧没有「关」的表达，不发声
		{"auto", ""},
		{"none", "none"}, // 原生取值原样过去
		{"minimal", "minimal"},
		{"high", "high"},
	}
	for _, c := range cases {
		out, err := OpenAIChatToResponsesRequest(chatBodyWithEffort(c.effort))
		if err != nil {
			t.Fatalf("%s: %v", c.effort, err)
		}
		got := thinkingJSON(t, out)
		r := asMap(got["reasoning"])
		if c.want == "" {
			if r != nil {
				t.Fatalf("%s: 不该出现 reasoning，实际 %v", c.effort, r)
			}
			continue
		}
		if r == nil || asString(r["effort"]) != c.want {
			t.Fatalf("%s: reasoning.effort 应为 %s，实际 %v", c.effort, c.want, got["reasoning"])
		}
	}
}

// ---------- 出站：OpenAI 兼容（默认分支） ----------

func TestUpstreamRequestStripsOffAutoForOpenAICompat(t *testing.T) {
	// 跨协议归一出来的 off / auto 会被 OpenAI 兼容上游 400 拒绝：剔除。
	for _, effort := range []string{"off", "auto"} {
		_, out, err := UpstreamRequest("openai-chat", "/v1/chat/completions", chatBodyWithEffort(effort), "")
		if err != nil {
			t.Fatalf("%s: %v", effort, err)
		}
		if strings.Contains(string(out), reasoningEffortField) {
			t.Fatalf("%s: 应剔除 reasoning_effort，实际 %s", effort, out)
		}
	}
	// 原生取值一律原样透传 —— 不替 OpenAI 生态的客户端做取舍
	for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
		_, out, err := UpstreamRequest("openai-chat", "/v1/chat/completions", chatBodyWithEffort(effort), "")
		if err != nil {
			t.Fatalf("%s: %v", effort, err)
		}
		if !strings.Contains(string(out), `"reasoning_effort":"`+effort+`"`) {
			t.Fatalf("%s: 原生档位应原样透传，实际 %s", effort, out)
		}
	}
}

// ---------- 入站：Anthropic → 通用语 ----------

func TestAnthropicInboundCarriesEffort(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"旧代高预算", `"thinking":{"type":"enabled","budget_tokens":20000}`, "high"},
		{"旧代中预算", `"thinking":{"type":"enabled","budget_tokens":8192}`, "medium"},
		{"旧代低预算", `"thinking":{"type":"enabled","budget_tokens":512}`, "low"},
		{"新代 effort", `"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}`, "max"},
		{"自适应无 effort", `"thinking":{"type":"adaptive"}`, "auto"},
		{"明确关闭", `"thinking":{"type":"disabled"}`, "off"},
		{"没提思考", "", ""},
	}
	for _, c := range cases {
		fields := ""
		if c.body != "" {
			fields = "," + c.body
		}
		src := []byte(`{"model":"claude-3","max_tokens":100,"messages":[{"role":"user","content":"hi"}]` + fields + `}`)
		mid, err := AnthropicRequestToOpenAIChat(src)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := thinkingJSON(t, mid)
		if c.want == "" {
			if _, ok := got[reasoningEffortField]; ok {
				t.Fatalf("%s: 不该写 reasoning_effort，实际 %v", c.name, got[reasoningEffortField])
			}
			continue
		}
		if asString(got[reasoningEffortField]) != c.want {
			t.Fatalf("%s: 通用语 reasoning_effort 应为 %s，实际 %v", c.name, c.want, got[reasoningEffortField])
		}
	}
}

// ---------- 入站：Gemini → 通用语 ----------

func TestGeminiInboundCarriesEffort(t *testing.T) {
	cases := []struct {
		name string
		tc   string
		want string
	}{
		{"预算低档", `"thinkingConfig":{"thinkingBudget":4096}`, "low"},
		{"预算中档", `"thinkingConfig":{"thinkingBudget":8192}`, "medium"},
		{"预算高档", `"thinkingConfig":{"thinkingBudget":32768}`, "high"},
		{"关闭", `"thinkingConfig":{"thinkingBudget":0}`, "off"},
		{"动态", `"thinkingConfig":{"thinkingBudget":-1}`, "auto"},
		{"thinkingLevel 优先", `"thinkingConfig":{"thinkingLevel":"high","thinkingBudget":0}`, "high"},
	}
	for _, c := range cases {
		src := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":64,` + c.tc + `}}`)
		mid, err := GeminiRequestToOpenAIChat(src, "gemini-2.5-pro", false)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := thinkingJSON(t, mid)
		if asString(got[reasoningEffortField]) != c.want {
			t.Fatalf("%s: 通用语 reasoning_effort 应为 %s，实际 %v", c.name, c.want, got[reasoningEffortField])
		}
	}
}

// 端到端：Anthropic 客户端 → Gemini 上游，强度必须落地。
func TestAnthropicInboundToGeminiUpstreamKeepsEffort(t *testing.T) {
	src := []byte(`{"model":"claude-3","max_tokens":100,"thinking":{"type":"enabled","budget_tokens":20000},"messages":[{"role":"user","content":"hi"}]}`)
	mid, err := AnthropicRequestToOpenAIChat(src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := OpenAIChatToGeminiRequest(mid)
	if err != nil {
		t.Fatal(err)
	}
	got := thinkingJSON(t, out)
	tc := asMap(asMap(got["generationConfig"])["thinkingConfig"])
	if tc == nil || asInt(tc["thinkingBudget"]) != ThinkingBudgetHigh {
		t.Fatalf("Anthropic 的 high 应落成 Gemini thinkingBudget=%d，实际 %v", ThinkingBudgetHigh, got["generationConfig"])
	}
}

// 端到端：OpenAI 客户端 → Anthropic 上游，强度必须落地（丙的原始场景）。
func TestOpenAIInboundToAnthropicUpstreamKeepsEffort(t *testing.T) {
	out, err := OpenAIChatToAnthropicRequest(chatBodyWithEffort("high"))
	if err != nil {
		t.Fatal(err)
	}
	got := thinkingJSON(t, out)
	if oc := asMap(got["output_config"]); oc == nil || asString(oc["effort"]) != "high" {
		t.Fatalf("reasoning_effort=high 应落成 output_config.effort=high，实际 %v", got["output_config"])
	}
}

// ---------- Anthropic → Anthropic 仍是无损往返 ----------

// 入站 Anthropic 自己带的 thinking 必须原样过去：按档位重写会把
// budget_tokens 精确值抹成档位值（还可能触发新模型的 400）。
func TestAnthropicRoundTripPrefersOriginalParams(t *testing.T) {
	src := []byte(`{"model":"claude-3","max_tokens":100,"thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":"hi"}]}`)
	mid, err := AnthropicRequestToOpenAIChat(src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := OpenAIChatToAnthropicRequest(mid)
	if err != nil {
		t.Fatal(err)
	}
	got := thinkingJSON(t, out)
	th := asMap(got["thinking"])
	if th == nil || asString(th["type"]) != "enabled" || asInt(th["budget_tokens"]) != 2048 {
		t.Fatalf("thinking 应原样往返，实际 %v", got["thinking"])
	}
	if got["output_config"] != nil {
		t.Fatalf("无损往返时不该再按档位补 output_config，实际 %v", got["output_config"])
	}
}

// 4.6 代客户端（Claude Code）的 output_config 必须往返存活：
// 它早先不在白名单里，整类请求的思考强度被静默丢弃。
func TestAnthropicOutputConfigSurvivesRoundTrip(t *testing.T) {
	src := []byte(`{"model":"claude-3","max_tokens":100,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"},"context_management":{"edits":[]},"messages":[{"role":"user","content":"hi"}]}`)
	mid, err := AnthropicRequestToOpenAIChat(src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := OpenAIChatToAnthropicRequest(mid)
	if err != nil {
		t.Fatal(err)
	}
	got := thinkingJSON(t, out)
	oc := asMap(got["output_config"])
	if oc == nil || asString(oc["effort"]) != "max" {
		t.Fatalf("output_config.effort 应原样往返，实际 %v", got["output_config"])
	}
	if asMap(got["thinking"]) == nil {
		t.Fatalf("thinking 应原样往返，实际 %v", got["thinking"])
	}
	if asMap(got["context_management"]) == nil {
		t.Fatalf("context_management 应原样往返，实际 %v", got["context_management"])
	}
}

// 发往非 Anthropic 上游时 output_config / context_management 同样要清掉
// （它们是 Anthropic 私有概念，严格校验的上游会 400）。
func TestAnthropicOutputConfigStrippedForGeminiUpstream(t *testing.T) {
	src := []byte(`{"model":"claude-3","max_tokens":100,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"},"messages":[{"role":"user","content":"hi"}]}`)
	mid, err := AnthropicRequestToOpenAIChat(src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := OpenAIChatToGeminiRequest(mid)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "output_config") || strings.Contains(string(out), "anthropic_params") {
		t.Fatalf("Anthropic 私有参数不应漏给 Gemini:\n%s", out)
	}
	// 强度本身要按档位传过去（max → 32768）
	got := thinkingJSON(t, out)
	tc := asMap(asMap(got["generationConfig"])["thinkingConfig"])
	if tc == nil || asInt(tc["thinkingBudget"]) != ThinkingBudgetMax {
		t.Fatalf("max 应落成 Gemini thinkingBudget=%d，实际 %v", ThinkingBudgetMax, got["generationConfig"])
	}
}
