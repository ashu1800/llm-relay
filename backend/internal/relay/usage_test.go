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

// 双方言报文：智谱 GLM（实测 2026-09）在同一段 usage 里同时给出 DeepSeek
// 方言与 OpenAI 方言的缓存命中字段，且值相同 —— 它们描述的是**同一个**
// 命中数，绝不能相加。修复前 cached 被记成 2×59712=119424（翻倍），
// 用户看到的缓存命中直接比上游面板高出一倍。
func TestNormalizeUsageDualDialectCache(t *testing.T) {
	// 2026-09 从智谱（D1 渠道）真实报文原样摘录：hit 与 cached_tokens 同值，
	// prompt_tokens = hit + miss，total_tokens 自洽。
	u := NormalizeUsage(mustJSON(t, `{
		"cache_creation_input_tokens": 0,
		"cache_read_input_tokens": 0,
		"cached_tokens": 0,
		"completion_tokens": 89,
		"prompt_cache_hit_tokens": 59712,
		"prompt_cache_miss_tokens": 819,
		"prompt_tokens": 60531,
		"prompt_tokens_details": {"cached_tokens": 59712},
		"total_tokens": 60620
	}`))

	if u.CachedTokens != 59712 {
		t.Fatalf("命中应为 59712（只认一套口径），实际 %d（双方言相加会翻倍）", u.CachedTokens)
	}
	if u.PromptTokens != 819 {
		t.Fatalf("未命中输入应为 819，实际 %d", u.PromptTokens)
	}
	if u.TotalTokens != 60620 {
		t.Fatalf("总量应为 60620，实际 %d", u.TotalTokens)
	}
}

// 中转站混用方言：子集字段与 Anthropic 的并列 cache_read 并存且同值 ——
// 同一命中的两种描述，只认与 prompt_tokens 自洽的子集口径。
func TestNormalizeUsageMixedDialectCache(t *testing.T) {
	u := NormalizeUsage(mustJSON(t, `{
		"prompt_tokens": 1000, "completion_tokens": 50, "total_tokens": 1050,
		"prompt_tokens_details": {"cached_tokens": 800},
		"cache_read_input_tokens": 800
	}`))

	if u.CachedTokens != 800 {
		t.Fatalf("命中应为 800（子集与并列同值时取子集），实际 %d", u.CachedTokens)
	}
	if u.PromptTokens != 200 {
		t.Fatalf("未命中输入应为 200（1000-800），实际 %d", u.PromptTokens)
	}
	if u.TotalTokens != 1050 {
		t.Fatalf("总量应为 1050，实际 %d", u.TotalTokens)
	}
}

// 上游只报「未命中」、完全不报「命中」时，命中数只能从上游总量反推。
//
// 报文原样取自 2026-10-07 生产 trace 564b83d907a4ab6c80a38466（渠道
// WorkBuddy美模 → 本机 workbuddy2api → global:gpt-6-luna）。修复前：
// ↑输入只剩未命中的 3、缓存 0、命中率 0.00%，总量却写着 15293 ——
// 四项互相打架，差出来的 15075 个缓存读 token 既不计费也不统计。
func TestNormalizeUsageMissOnlyDerivesCacheHit(t *testing.T) {
	u := NormalizeUsage(mustJSON(t, `{
		"cache_creation_input_tokens": 0,
		"cache_read_input_tokens": 0,
		"cached_tokens": 0,
		"completion_thinking_tokens": 64,
		"completion_tokens": 215,
		"completion_tokens_details": {"cached_tokens": 0, "reasoning_tokens": 64},
		"prompt_cache_hit_tokens": 0,
		"prompt_cache_miss_tokens": 3,
		"prompt_tokens": 15078,
		"prompt_tokens_details": {"cached_tokens": 0, "reasoning_tokens": 0},
		"total_tokens": 15293
	}`))

	if u.PromptTokens != 3 {
		t.Fatalf("未命中输入应为 3，实际 %d", u.PromptTokens)
	}
	if u.CachedTokens != 15075 {
		t.Fatalf("缓存命中应反推为 15075，实际 %d（记 0 会让命中率与费用系统性偏低）", u.CachedTokens)
	}
	if u.CompletionTokens != 215 || u.ReasoningTokens != 64 {
		t.Fatalf("输出/推理应为 215/64，实际 %d/%d", u.CompletionTokens, u.ReasoningTokens)
	}
	if u.TotalTokens != 15293 {
		t.Fatalf("总量应沿用上游的 15293，实际 %d", u.TotalTokens)
	}
	// 四项之和必须与上游总量相等：对不上就是日志里 ↑/↓/缓存 三个数在打架
	if got := UsageTotal(u.PromptTokens, u.CompletionTokens, u.CachedTokens, u.CacheCreationTokens); got != 15293 {
		t.Fatalf("四项之和应为 15293，实际 %d", got)
	}
	if r := u.CacheHitRate(); r < 0.999 || r > 1 {
		t.Fatalf("命中率应约为 0.9998，实际 %f", r)
	}
}

// 冷请求（未命中就是全部输入）不能反推出任何缓存。
func TestNormalizeUsageMissOnlyColdRequestKeepsZeroCache(t *testing.T) {
	u := NormalizeUsage(mustJSON(t, `{
		"cache_read_input_tokens": 0, "cached_tokens": 0,
		"completion_tokens": 16,
		"prompt_cache_hit_tokens": 0, "prompt_cache_miss_tokens": 21,
		"prompt_tokens": 21,
		"prompt_tokens_details": {"cached_tokens": 0},
		"total_tokens": 37
	}`))

	if u.CachedTokens != 0 {
		t.Fatalf("冷请求命中应为 0，实际 %d（反推不该凭空造缓存）", u.CachedTokens)
	}
	if u.PromptTokens != 21 || u.TotalTokens != 37 {
		t.Fatalf("输入/总量应为 21/37，实际 %d/%d", u.PromptTokens, u.TotalTokens)
	}
}

// 没有上游总量时无从反推：宁可记 0，也不臆造缓存读。
func TestNormalizeUsageMissOnlyWithoutTotalKeepsZeroCache(t *testing.T) {
	u := NormalizeUsage(mustJSON(t, `{
		"prompt_tokens": 800, "prompt_cache_miss_tokens": 160, "completion_tokens": 20
	}`))

	if u.CachedTokens != 0 {
		t.Fatalf("无总量时命中应为 0，实际 %d", u.CachedTokens)
	}
	if u.PromptTokens != 160 || u.TotalTokens != 180 {
		t.Fatalf("输入/总量应为 160/180，实际 %d/%d", u.PromptTokens, u.TotalTokens)
	}
}

// 命中字段在场时按原口径，反推必须让路 —— 否则同一笔命中会被记两遍。
func TestNormalizeUsageMissOnlyIgnoresExplicitHit(t *testing.T) {
	u := NormalizeUsage(mustJSON(t, `{
		"prompt_tokens": 15078, "completion_tokens": 215, "total_tokens": 15293,
		"prompt_cache_hit_tokens": 15075, "prompt_cache_miss_tokens": 3,
		"prompt_tokens_details": {"cached_tokens": 15075}
	}`))

	if u.CachedTokens != 15075 {
		t.Fatalf("命中应为 15075（只认一套口径），实际 %d", u.CachedTokens)
	}
	if u.PromptTokens != 3 || u.TotalTokens != 15293 {
		t.Fatalf("输入/总量应为 3/15293，实际 %d/%d", u.PromptTokens, u.TotalTokens)
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
