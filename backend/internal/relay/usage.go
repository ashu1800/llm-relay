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

// CacheHitRate 返回缓存命中率，分母为总输入 token。
func (u Usage) CacheHitRate() float64 {
	denom := u.PromptTokens + u.CachedTokens
	if denom <= 0 {
		return 0
	}
	return float64(u.CachedTokens) / float64(denom)
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

	// ---- 缓存读取（子集型字段）----
	if d, ok := raw["prompt_tokens_details"].(map[string]any); ok {
		u.CachedTokens += getInt(d, "cached_tokens")
	}
	if d, ok := raw["input_tokens_details"].(map[string]any); ok {
		u.CachedTokens += getInt(d, "cached_tokens")
	}

	// ---- 缓存读取（并列型字段）----
	u.CachedTokens += getInt(raw, "cache_read_input_tokens")
	u.CachedTokens += getInt(raw, "prompt_cache_hit_tokens")
	u.CachedTokens += getInt(raw, "cachedContentTokenCount")

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

	if u.TotalTokens == 0 {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens + u.CachedTokens + u.CacheCreationTokens
	}
	return u
}

// hasSubsetCacheField 判断 raw 是否含有「子集型」缓存字段。
func hasSubsetCacheField(raw map[string]any) bool {
	for _, k := range []string{"prompt_tokens_details", "input_tokens_details"} {
		if d, ok := raw[k].(map[string]any); ok {
			if getInt(d, "cached_tokens") > 0 {
				return true
			}
		}
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
	u.TotalTokens = u.PromptTokens + u.CompletionTokens
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
