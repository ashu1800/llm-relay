package relay

import "testing"

// total_tokens 必须是四项之和。
//
// 修复前 Anthropic 原生并列报文被记成 150（正确 1350）：总量在缓存换算
// 之前就算好了，末尾那句「为 0 时兜底」永远轮不到。
func TestNormalizeUsageTotalTokens(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
		want int
	}{
		{
			name: "Anthropic 原生并列（含缓存写入）",
			raw: map[string]any{
				"input_tokens": 100, "output_tokens": 50,
				"cache_read_input_tokens": 1000, "cache_creation_input_tokens": 200,
			},
			want: 1350,
		},
		{
			name: "原生 Gemini 无 totalTokenCount",
			// cachedContentTokenCount 是 promptTokenCount 的子集（60 含在 100 里）：
			// 归一化成并列语义后 Prompt=40、Cached=60，总量 120 而不是 180 ——
			// 旧的 180 把缓存读双计了一次
			raw: map[string]any{
				"promptTokenCount": 100, "candidatesTokenCount": 20, "cachedContentTokenCount": 60,
			},
			want: 120,
		},
		{
			name: "OpenAI 形状 + 缓存写入",
			raw: map[string]any{
				"prompt_tokens": 820, "completion_tokens": 15,
				"prompt_tokens_details":       map[string]any{"cached_tokens": 800},
				"cache_creation_input_tokens": 300,
			},
			want: 1135,
		},
		{
			name: "OpenAI 标准（上游给了 total_tokens）",
			raw: map[string]any{
				"prompt_tokens": 150, "completion_tokens": 50, "total_tokens": 200,
			},
			want: 200,
		},
		{
			name: "真实 DeepSeek 原生",
			raw: map[string]any{
				"prompt_tokens": 820, "completion_tokens": 15, "total_tokens": 835,
				"prompt_cache_hit_tokens": 800, "prompt_cache_miss_tokens": 20,
			},
			want: 835,
		},
		{
			name: "Gemini 思维链独立于候选（上游总量更大时采用上游值）",
			raw: map[string]any{
				"promptTokenCount": 10, "candidatesTokenCount": 5,
				"thoughtsTokenCount": 100, "totalTokenCount": 115,
			},
			want: 115,
		},
		{
			name: "空 usage",
			raw:  map[string]any{},
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := NormalizeUsage(tc.raw)
			if u.TotalTokens != tc.want {
				t.Errorf("total_tokens = %d，期望 %d（归一化后 Prompt=%d Compl=%d Cached=%d Create=%d）",
					u.TotalTokens, tc.want, u.PromptTokens, u.CompletionTokens, u.CachedTokens, u.CacheCreationTokens)
			}
		})
	}
}

// TPM 记账用的必须是同一个总量口径。
func TestBillableTokensMatchesTotal(t *testing.T) {
	u := NormalizeUsage(map[string]any{
		"input_tokens": 100, "output_tokens": 50,
		"cache_read_input_tokens": 1000, "cache_creation_input_tokens": 200,
	})
	if got := u.BillableTokens(); got != 1350 {
		t.Errorf("BillableTokens = %d，期望 1350（TPM 曾因此偏小 9 倍）", got)
	}
}

// 总量恒等于四项之和（上游总量一致或更小时）。
func TestTotalIsSumOfParts(t *testing.T) {
	raw := map[string]any{
		"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150,
		"prompt_cache_hit_tokens": 80, "prompt_cache_miss_tokens": 20,
	}
	u := NormalizeUsage(raw)
	sum := UsageTotal(u.PromptTokens, u.CompletionTokens, u.CachedTokens, u.CacheCreationTokens)
	if u.TotalTokens != sum {
		t.Errorf("总量 %d 与四项之和 %d 不一致", u.TotalTokens, sum)
	}
}

// 缓存命中率的分母是全部输入（未命中 + 命中 + 缓存写入）。
func TestCacheHitRateDenominator(t *testing.T) {
	// 未命中 20 + 命中 800 = 820 输入，命中率 800/820
	if got := CacheHitRateOf(20, 800, 0); got < 0.9756 || got > 0.9757 {
		t.Errorf("命中率 = %v，期望 800/820 ≈ 0.9756", got)
	}
	// 缓存写入也算进分母：20 + 800 + 180 = 1000，命中率 0.8
	if got := CacheHitRateOf(20, 800, 180); got != 0.8 {
		t.Errorf("命中率 = %v，期望 0.8", got)
	}
	// 全零不能除零
	if got := CacheHitRateOf(0, 0, 0); got != 0 {
		t.Errorf("全零时命中率应为 0，实际 %v", got)
	}
}

// 方法与自由函数必须同源。
func TestUsageCacheHitRateMethodMatchesHelper(t *testing.T) {
	u := Usage{PromptTokens: 20, CachedTokens: 800, CacheCreationTokens: 180}
	if u.CacheHitRate() != CacheHitRateOf(20, 800, 180) {
		t.Errorf("Usage.CacheHitRate 与 CacheHitRateOf 不一致：%v vs %v",
			u.CacheHitRate(), CacheHitRateOf(20, 800, 180))
	}
}
