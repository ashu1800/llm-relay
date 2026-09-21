package relay

import (
	"encoding/json"
	"math"
)

// Usage 是跨协议归一化后的统一计量结构。
//
// 缓存口径必须区分两类，否则命中率会算错：
//   - 子集型：OpenAI 的 prompt_tokens_details.cached_tokens 已包含在 prompt_tokens 内；
//   - 并列型：Anthropic / DeepSeek 原生 / 部分中转站的 cache_read 与 cache_creation
//     与 input_tokens 平行，不重叠。
//
// 归一化后统一采用「并列」语义：PromptTokens 表示未命中缓存的输入。
type Usage struct {
	PromptTokens        int  `json:"prompt_tokens"`
	CompletionTokens    int  `json:"completion_tokens"`
	TotalTokens         int  `json:"total_tokens"`
	CachedTokens        int  `json:"cached_tokens"`
	CacheCreationTokens int  `json:"cache_creation_tokens"`
	ReasoningTokens     int  `json:"reasoning_tokens"`
	Estimated           bool `json:"estimated"`
}

// BillableTokens 返回本次请求应计入配额的 token 总数（分组 TPM 记账用）。
//
// UsageTotal 是「总 token」的唯一公式：四项之和。
//
// 全项目只有这一处定义。此前同样的概念在六处各写了一遍且互不一致
// （有的漏 cache_creation、有的漏 cached、有的漏 reasoning），
// 结果是总览卡、日志页、热力图、排行榜、导出 CSV 的口径都对不上，
// 而且改动时很容易只改其中一两处。新增代码请一律调用它。
func UsageTotal(prompt, completion, cached, cacheCreation int) int {
	return prompt + completion + cached + cacheCreation
}

// CacheHitRateOf 是缓存命中率的唯一公式。
//
// 分母是「全部输入」＝未命中 + 命中 + 缓存写入。前端 LogsView 与
// DashboardView 用的是同一口径，改这里要同步改前端。
func CacheHitRateOf(prompt, cached, cacheCreation int) float64 {
	denom := UsageTotal(prompt, 0, cached, cacheCreation)
	if denom <= 0 {
		return 0
	}
	return float64(cached) / float64(denom)
}

// BillableTokens 返回计入 TPM 配额的量。
//
// 口径与落库的 total_tokens 一致：上游明确给了总数就用它，
// 否则按四项之和累加。单独抽出来是为了让「TPM 记的是哪个数」
// 只有一处定义，改口径时不会漏。
func (u Usage) BillableTokens() int {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	total := UsageTotal(u.PromptTokens, u.CompletionTokens, u.CachedTokens, u.CacheCreationTokens)
	if total == 0 {
		// 有些上游只报 reasoning_tokens，至少别把它漏掉
		total = u.ReasoningTokens
	}
	return total
}

// CacheHitRate 返回缓存命中率，分母为全部输入 token。
//
// 与 CacheHitRateOf 同源，保留方法形式是为了让调用方读起来更自然。
func (u Usage) CacheHitRate() float64 {
	return CacheHitRateOf(u.PromptTokens, u.CachedTokens, u.CacheCreationTokens)
}

// NormalizeUsage 从任意上游的 usage 对象归一化。
// 同时兼容 OpenAI、Anthropic、Gemini 以及中转站混用的字段。
func NormalizeUsage(raw map[string]any) Usage {
	var u Usage
	if raw == nil {
		return u
	}

	// ---- 输入 / 输出 ----
	u.PromptTokens = firstNonZero(
		getInt(raw, "prompt_tokens"),
		getInt(raw, "input_tokens"),
		getInt(raw, "promptTokenCount"),
	)
	u.CompletionTokens = firstNonZero(
		getInt(raw, "completion_tokens"),
		getInt(raw, "output_tokens"),
		getInt(raw, "candidatesTokenCount"),
	)
	u.TotalTokens = firstNonZero(
		getInt(raw, "total_tokens"),
		getInt(raw, "totalTokenCount"),
		u.PromptTokens+u.CompletionTokens,
	)

	// ---- 收集缓存命中的来源字段 ----
	//
	// 必须先收集、再决定认哪一套。有些上游（智谱 GLM 实测，2026-09）在
	// 同一段 usage 里同时给出多套方言字段，且描述的是**同一个**命中数：
	// prompt_cache_hit_tokens = prompt_tokens_details.cached_tokens = 59712。
	// 两套字段是同一个数的两种写法，无脑累加会把命中数翻倍（59712 记成
	// 119424），命中率虚高一倍。见 TestNormalizeUsageDualDialectCache。
	subsetSum := 0 // 子集型字段之和：cached 已含在 prompt_tokens 里
	subsetN := 0   // 非零的子集型字段个数
	if d, ok := raw["prompt_tokens_details"].(map[string]any); ok {
		if v := getInt(d, "cached_tokens"); v > 0 {
			subsetSum += v
			subsetN++
		}
	}
	if d, ok := raw["input_tokens_details"].(map[string]any); ok {
		if v := getInt(d, "cached_tokens"); v > 0 {
			subsetSum += v
			subsetN++
		}
	}
	if v := getInt(raw, "cachedContentTokenCount"); v > 0 {
		subsetSum += v
		subsetN++
	}

	parallelSum := 0 // 并列型字段之和：与 input_tokens 平行、不重叠
	parallelN := 0
	if v := getInt(raw, "cache_read_input_tokens"); v > 0 {
		parallelSum += v
		parallelN++
	}
	if v := getInt(raw, "prompt_cache_hit_tokens"); v > 0 {
		parallelSum += v
		parallelN++
	}

	// ---- 缓存读取 ----
	//
	// 原则：命中数只认**一套**口径，绝不把描述同一个命中的多套方言相加。
	// 同一套内多个字段仍相加（它们描述互不重叠的命中段）。
	// 方言并存时（并行字段与子集字段同时非零）的取舍：
	//   1. 优先 DeepSeek 拆分对 —— 它与 prompt_cache_miss_tokens 自洽
	//      （hit+miss 恰为 prompt_tokens），是唯一自带校验的口径；
	//   2. 否则取子集字段 —— 它与 prompt_tokens 自洽，可被安全扣减，
	//      并列字段（如中转站附加的 cache_read）按重复描述舍弃。
	// 代价：若某上游真用两套字段描述**两段不同的**命中，这里会少记一段
	// —— 相比翻倍虚报，宁可保守。
	switch {
	case parallelN > 0 && subsetN > 0:
		if v := getInt(raw, "prompt_cache_hit_tokens"); v > 0 {
			u.CachedTokens = v
		} else {
			u.CachedTokens = subsetSum
		}
	case parallelN > 0:
		u.CachedTokens = parallelSum
	default:
		u.CachedTokens = subsetSum
	}

	// ---- 缓存写入 ----
	u.CacheCreationTokens += getInt(raw, "cache_creation_input_tokens")
	u.CacheCreationTokens += getInt(raw, "claude_cache_creation_5_m_tokens")
	u.CacheCreationTokens += getInt(raw, "claude_cache_creation_1_h_tokens")
	if d, ok := raw["cache_creation"].(map[string]any); ok {
		u.CacheCreationTokens += getInt(d, "ephemeral_5m_input_tokens")
		u.CacheCreationTokens += getInt(d, "ephemeral_1h_input_tokens")
	}

	// ---- 推理 token ----
	if d, ok := raw["completion_tokens_details"].(map[string]any); ok {
		u.ReasoningTokens += getInt(d, "reasoning_tokens")
	}
	u.ReasoningTokens += getInt(raw, "thoughtsTokenCount")

	// 子集型缓存字段已计入 PromptTokens，这里换算成并列语义
	if u.CachedTokens > 0 && u.CachedTokens <= u.PromptTokens {
		if hasSubsetCacheField(raw) {
			u.PromptTokens -= u.CachedTokens
		}
	}

	// DeepSeek 原生会把输入拆成命中/未命中两个字段，未命中即 PromptTokens
	if miss := getInt(raw, "prompt_cache_miss_tokens"); miss > 0 {
		u.PromptTokens = miss
	}

	// ---- 总量 ----
	//
	// 必须在上面所有调整**做完之后**才算。归一化会把「子集型」缓存字段换算成
	// 并列语义（上面的扣减与 DeepSeek miss 覆盖），总量只有在这之后才反映
	// 真实的四项之和。原来的写法在调整之前就取好了值，于是
	// Anthropic 原生并列报文 {input:100, output:50, cache_read:1000, cache_creation:200}
	// 被记成 150（正确是 1350）；而末尾那句「总量为 0 时兜底」永远轮不到。
	//
	// 上游总量更大时采用上游值：Gemini 原生的 thoughtsTokenCount 是**独立于**
	// candidatesTokenCount 的（不同于 OpenAI 的 reasoning_tokens 已含在
	// completion_tokens 里），此时总量确实大于四项之和，上游值才是对的。
	sum := u.PromptTokens + u.CompletionTokens + u.CachedTokens + u.CacheCreationTokens
	if upstream := firstNonZero(getInt(raw, "total_tokens"), getInt(raw, "totalTokenCount")); upstream > sum {
		u.TotalTokens = upstream
	} else {
		u.TotalTokens = sum
	}
	return u
}

// hasSubsetCacheField 判断 raw 是否含有「子集型」缓存字段。
// cachedContentTokenCount 也在列：Gemini 原生报文里它是 promptTokenCount
// 的子集 —— 正常链路已被 geminiUsageToOpenAI 预转成 OpenAI 形态，
// 但上游响应体转换失败、原样透传时会直接进这里，漏认会把缓存读双计
// （一次含在 prompt 里、一次记在 cached 里）。
func hasSubsetCacheField(raw map[string]any) bool {
	for _, k := range []string{"prompt_tokens_details", "input_tokens_details"} {
		if d, ok := raw[k].(map[string]any); ok {
			if getInt(d, "cached_tokens") > 0 {
				return true
			}
		}
	}
	if getInt(raw, "cachedContentTokenCount") > 0 {
		return true
	}
	return false
}

func getInt(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0
		}
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0
		}
		return int(i)
	default:
		return 0
	}
}

// EstimateUsage 在上游没有回传 usage 时按文本长度粗略估算，避免统计全为零。
// 结果标记 Estimated，界面据此提示这是估算值而非上游真实计量。
func EstimateUsage(promptChars, completionChars int) Usage {
	// 中英混排下约 3 字符折合 1 token
	toTokens := func(n int) int {
		if n <= 0 {
			return 0
		}
		if t := n / 3; t > 0 {
			return t
		}
		return 1
	}
	u := Usage{
		PromptTokens:     toTokens(promptChars),
		CompletionTokens: toTokens(completionChars),
		Estimated:        true,
	}
	u.TotalTokens = UsageTotal(u.PromptTokens, u.CompletionTokens, 0, 0)
	return u
}

func firstNonZero(vals ...int) int {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}
