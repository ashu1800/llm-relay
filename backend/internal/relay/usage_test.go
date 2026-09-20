package relay

import "testing"

// 下面两段是 2026-09 从真实上游抓到的 usage 原样载荷，用来锁住归一化口径。
const openaiStyleUsage = `{
  "prompt_tokens": 360, "completion_tokens": 10, "total_tokens": 370,
  "prompt_tokens_details": {"cached_tokens": 294, "text_tokens": 0},
  "completion_tokens_details": {"reasoning_tokens": 0},
  "input_tokens": 360, "output_tokens": 10,
  "claude_cache_creation_5_m_tokens": 0, "claude_cache_creation_1_h_tokens": 0
}`

const deepseekStyleUsage = `{
  "prompt_tokens": 33, "completion_tokens": 30, "total_tokens": 63,
  "prompt_tokens_details": {"cached_tokens": 0, "audio_tokens": 0},
  "completion_tokens_details": {"reasoning_tokens": 30},
  "cache_creation_input_tokens": 0
}`

// 子集型缓存字段必须从输入里扣除，否则输入会被重复计费、命中率被算低。
func TestNormalizeUsageOpenAISubsetCache(t *testing.T) {
	u := NormalizeUsage(mustJSON(t, openaiStyleUsage))

	if u.CachedTokens != 294 {
		t.Fatalf("缓存命中应为 294，实际 %d", u.CachedTokens)
	}
	if u.PromptTokens != 66 {
		t.Fatalf("未命中输入应为 66（360-294），实际 %d", u.PromptTokens)
	}
	if u.CompletionTokens != 10 {
		t.Fatalf("输出应为 10，实际 %d", u.CompletionTokens)
	}
	// 66 + 294 = 360，说明输入没有被重复计算
	if got := u.PromptTokens + u.CachedTokens; got != 360 {
		t.Fatalf("输入总量应为 360，实际 %d", got)
	}
}

func TestNormalizeUsageDeepSeekReasoning(t *testing.T) {
	u := NormalizeUsage(mustJSON(t, deepseekStyleUsage))

	if u.PromptTokens != 33 || u.CompletionTokens != 30 {
		t.Fatalf("输入/输出应为 33/30，实际 %d/%d", u.PromptTokens, u.CompletionTokens)
	}
	if u.ReasoningTokens != 30 {
		t.Fatalf("推理 token 应为 30，实际 %d", u.ReasoningTokens)
	}
	if u.CachedTokens != 0 {
		t.Fatalf("缓存命中应为 0，实际 %d", u.CachedTokens)
	}
}

// 并列型字段（Anthropic 口径）不应触发扣减。
func TestNormalizeUsageParallelCache(t *testing.T) {
	u := NormalizeUsage(mustJSON(t, `{
		"input_tokens": 100, "output_tokens": 50,
		"cache_read_input_tokens": 800, "cache_creation_input_tokens": 200
	}`))

	if u.PromptTokens != 100 {
		t.Fatalf("并列型输入应保持 100，实际 %d", u.PromptTokens)
	}
	if u.CachedTokens != 800 {
		t.Fatalf("缓存读取应为 800，实际 %d", u.CachedTokens)
	}
	if u.CacheCreationTokens != 200 {
		t.Fatalf("缓存写入应为 200，实际 %d", u.CacheCreationTokens)
	}
}

// DeepSeek 原生把输入拆成命中/未命中两个字段。
func TestNormalizeUsageDeepSeekNativeCache(t *testing.T) {
	u := NormalizeUsage(mustJSON(t, `{
		"prompt_cache_hit_tokens": 640, "prompt_cache_miss_tokens": 160,
		"completion_tokens": 20
	}`))

	if u.CachedTokens != 640 {
		t.Fatalf("命中应为 640，实际 %d", u.CachedTokens)
	}
	if u.PromptTokens != 160 {
		t.Fatalf("未命中应为 160，实际 %d", u.PromptTokens)
	}
}

func TestCacheHitRate(t *testing.T) {
	u := Usage{PromptTokens: 66, CachedTokens: 294}
	got := u.CacheHitRate()
	if got < 0.81 || got > 0.82 {
		t.Fatalf("命中率应约为 0.8165，实际 %f", got)
	}
	if (Usage{}).CacheHitRate() != 0 {
		t.Fatal("空用量命中率应为 0")
	}
}

func TestSlotAvailableCrossMidnight(t *testing.T) {
	// 跨午夜窗口：22:00 - 02:00
	slots := mustSlots(t, `[{"days": [], "start": "22:00", "end": "02:00"}]`)

	if !SlotAvailable(slots, mustTime(t, "2026-09-12T23:30:00Z")) {
		t.Fatal("23:30 应落在 22:00-02:00 窗口内")
	}
	if !SlotAvailable(slots, mustTime(t, "2026-09-12T01:00:00Z")) {
		t.Fatal("01:00 应落在 22:00-02:00 窗口内")
	}
	if SlotAvailable(slots, mustTime(t, "2026-09-12T12:00:00Z")) {
		t.Fatal("12:00 不应落在窗口内")
	}
	if !SlotAvailable(nil, mustTime(t, "2026-09-12T12:00:00Z")) {
		t.Fatal("空时段配置应视为全天可用")
	}
}

// Gemini 原生 usageMetadata 直达 NormalizeUsage（上游响应体转换失败、
// 原样透传的回退路径）时，cachedContentTokenCount 必须按子集型扣减 ——
// 漏认会让缓存读双计（一次含在 prompt 里、一次记在 cached 里）。
func TestNormalizeUsageGeminiNativeIsSubset(t *testing.T) {
	u := NormalizeUsage(map[string]any{
		"promptTokenCount": 100, "candidatesTokenCount": 20, "cachedContentTokenCount": 60,
	})
	if u.PromptTokens != 40 {
		t.Fatalf("prompt 应扣减缓存部分为 40，实际 %d（双计）", u.PromptTokens)
	}
	if u.CachedTokens != 60 {
		t.Fatalf("cached 应为 60，实际 %d", u.CachedTokens)
	}
}
