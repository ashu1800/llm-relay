package convert

// 出站用量口径的三个转换器（usageToAnthropic / usageToGemini / usageToResponses）
// 各自把规范 OpenAI 形状的 usage 渲染成目标协议的字段名。三者的**输入**是同一份
// 规范形状，因此「哪些字段算输入 / 输出 / 缓存命中」的取值规则必须只有一处 ——
// 原先每个转换器各抄了一遍，三份都只读 prompt_tokens_details.cached_tokens。
//
// 缓存命中尤其危险：Anthropic 把一个请求的命中拆在「子集型」与「并列型」两套
// 方言里（前者与 prompt_tokens 重叠、后者平行），中转站可能两套同发。少读一套
// 就会把这条日志的缓存读记成 0。relay.NormalizeUsage 已经踩全了这些方言，
// 但方向是 relay → convert，这里不能反向引用它，所以本文件承载 convert 侧的
// 唯一取值规则，三个转换器都走它。

// usageCounts 从规范 OpenAI 形状的 usage 里取出四个有效值。
//
//   - prompt / completion 允许 Anthropic 方言字段兜底：部分调用点直接把
//     上游语言模型返回的报文喂进来（见 anthropic_response.go 的非流式入口），
//     那里 input_tokens / output_tokens 才是本名。
//   - cached 是「缓存命中」的**全部**来源。子集型字段
//     （prompt_tokens_details.cached_tokens）优先；它缺位时退回并列型字段
//     （cache_read_input_tokens）。两套同时非零时只认子集型 —— 它与
//     prompt_tokens 自洽，后面的 nonCached 扣减才站得住；两者相加会把
//     同一段命中记两遍（正是 f68a428 修掉的那类翻倍）。
func usageCounts(usage map[string]any) (prompt, completion, cached, creation int) {
	prompt = asInt(usage["prompt_tokens"])
	if prompt == 0 {
		prompt = asInt(usage["input_tokens"])
	}
	completion = asInt(usage["completion_tokens"])
	if completion == 0 {
		completion = asInt(usage["output_tokens"])
	}
	if d := asMap(usage["prompt_tokens_details"]); d != nil {
		cached = asInt(d["cached_tokens"])
	}
	if cached == 0 {
		if parallel := asInt(usage["cache_read_input_tokens"]); parallel > 0 {
			cached = parallel
		}
	}
	creation = asInt(usage["cache_creation_input_tokens"])
	return prompt, completion, cached, creation
}

// usageNonCachedInput 返回「未命中缓存的输入」。
//
// Anthropic 口径的 input_tokens 不含缓存命中，而 OpenAI 的 prompt_tokens 含，
// 换算时必须拆开。cached > prompt 时说明两套字段描述的不是同一批 token
// （上游混发方言），此时不扣减 —— 宁可把输入报大，也不把命中扣成负数。
func usageNonCachedInput(prompt, cached int) int {
	if cached > 0 && cached <= prompt {
		return prompt - cached
	}
	return prompt
}
