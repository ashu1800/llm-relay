package convert

import "testing"

// 三个出站转换器（Anthropic / Gemini / Responses）共用 usageCounts，
// 这里钉住它的取值规则 —— 尤其是缓存命中的两套方言：它们此前只被拷在
// usageToAnthropic 里，另两个转换器漏掉了并列型字段，于是同一份用量
// 在 Gemini / Responses 口径下会把命中记成 0。
func TestUsageCountsCacheDialects(t *testing.T) {
	cases := []struct {
		name       string
		usage      map[string]any
		wantPrompt int
		wantCached int
	}{
		{
			name: "子集型：prompt_tokens_details.cached_tokens",
			usage: map[string]any{
				"prompt_tokens":         float64(100),
				"completion_tokens":     float64(50),
				"prompt_tokens_details": map[string]any{"cached_tokens": float64(30)},
			},
			wantPrompt: 100,
			wantCached: 30,
		},
		{
			name: "并列型兜底：cache_read_input_tokens（子集字段缺位时）",
			usage: map[string]any{
				"prompt_tokens":           float64(100),
				"cache_read_input_tokens": float64(40),
				"completion_tokens":       float64(50),
			},
			wantPrompt: 100,
			wantCached: 40,
		},
		{
			name: "两套方言同发：只认子集型，绝不相加（相加会把命中翻倍）",
			usage: map[string]any{
				"prompt_tokens":           float64(100),
				"cache_read_input_tokens": float64(30),
				"prompt_tokens_details":   map[string]any{"cached_tokens": float64(30)},
			},
			wantPrompt: 100,
			wantCached: 30,
		},
		{
			name: "Anthropic 方言兜底：input_tokens / output_tokens",
			usage: map[string]any{
				"input_tokens":  float64(7),
				"output_tokens": float64(3),
			},
			wantPrompt: 7,
			wantCached: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prompt, _, cached, _ := usageCounts(c.usage)
			if prompt != c.wantPrompt {
				t.Errorf("prompt 期望 %d，实际 %d", c.wantPrompt, prompt)
			}
			if cached != c.wantCached {
				t.Errorf("cached 期望 %d，实际 %d", c.wantCached, cached)
			}
		})
	}
}

// usageNonCachedInput：Anthropic 口径的 input_tokens 不含命中，需要拆开；
// 但 cached > prompt 时不能扣成负数 —— 那说明两套字段描述的不是同一批 token。
func TestUsageNonCachedInput(t *testing.T) {
	cases := []struct{ prompt, cached, want int }{
		{100, 30, 70},
		{100, 0, 100},
		{100, 100, 0},
		{10, 99, 10}, // 超过 prompt：不扣减
	}
	for _, c := range cases {
		if got := usageNonCachedInput(c.prompt, c.cached); got != c.want {
			t.Errorf("usageNonCachedInput(%d, %d) = %d，期望 %d", c.prompt, c.cached, got, c.want)
		}
	}
}

// 三个转换器对同一条用量必须给出同一个命中数 —— 这是它们共用一个抽取器的意义。
func TestUsageConvertersAgreeOnCached(t *testing.T) {
	usage := map[string]any{
		"prompt_tokens":               float64(1000),
		"completion_tokens":           float64(200),
		"cache_creation_input_tokens": float64(50),
		"cache_read_input_tokens":     float64(400), // 并列型，此前 Gemini/Responses 会漏
	}

	if got := usageToAnthropic(usage)["cache_read_input_tokens"]; got != 400 {
		t.Errorf("Anthropic 命中期望 400，实际 %v", got)
	}
	if got := usageToGemini(usage)["cachedContentTokenCount"]; got != 400 {
		t.Errorf("Gemini 命中期望 400，实际 %v", got)
	}
	resp := usageToResponses(usage)
	details, ok := resp["input_tokens_details"].(map[string]any)
	if !ok {
		t.Fatalf("Responses 缺少 input_tokens_details：%v", resp)
	}
	if got := details["cached_tokens"]; got != 400 {
		t.Errorf("Responses 命中期望 400，实际 %v", got)
	}
}
