package convert

import (
	"bytes"
	"encoding/json"
	"strings"
)

// 思考强度的跨协议映射。
//
// 站内通用语（OpenAI Chat）里 reasoning_effort 是思考强度的**唯一权威字段**：
// 入站转换器写它，出站转换器读它并译成自己协议的形状。设计见
// docs/superpowers/specs/2026-09-19-thinking-effort-mapping-design.md。
//
// 为什么必须做：思考强度此前从未跨协议传递 —— 入站 Anthropic 的
// thinking / output_config 在出站非 Anthropic 时被清掉，入站 OpenAI 的
// reasoning_effort 在出站 Anthropic / Gemini 时被忽略。表现是「思考开着、
// 强度丢失」，而强度既改变模型行为也改变计费（reasoning token 量级），
// 客户端却拿不到任何信号。
//
// 档位词表与 relay/thinking.go 的观察层同源（那边读「这次请求要多强的思考」，
// 这边写「转发给上游要多强的思考」）：
//
//	off · auto · minimal · low · medium · high · xhigh · max
const (
	// reasoningEffortField 是通用语里的思考强度字段（顶层）。
	reasoningEffortField = "reasoning_effort"

	effortOff     = "off"
	effortAuto    = "auto"
	effortMinimal = "minimal"
	effortLow     = "low"
	effortMedium  = "medium"
	effortHigh    = "high"
	effortXHigh   = "xhigh"
	effortMax     = "max"
)

// thinkingDisabled 是 Anthropic thinking.type 的「关」取值。
const thinkingDisabled = "disabled"

// 预算刻度：Anthropic 的 budget_tokens 与 Gemini 的 thinkingBudget 共用这套数值，
// 也就是列表里「思考」列分档用的那几个边界 —— relay/thinking.go 的
// budgetMediumFrom / anthropicHighFrom / geminiHighFrom 直接引用这里的常量。
// 读与写共用一把尺子，才不会出现「列表显示 medium、上游收到 high」。
//
// 数值出处：Anthropic 常见 1024/4096/8192/16384；xhigh 取 24576；
// max 取 Gemini Pro 的预算上限 32768。**Gemini 侧的具体数值待真实上游定标**
// （见设计文档「已知不等价」），有真机后按实测回来改这一处即可。
const (
	ThinkingBudgetMinimal = 1024
	ThinkingBudgetLow     = 4096
	ThinkingBudgetMedium  = 8192
	ThinkingBudgetHigh    = 16384
	ThinkingBudgetXHigh   = 24576
	ThinkingBudgetMax     = 32768
)

// normalizeEffort 归一档位：小写、去空白。
//
// "none" 归到 "off"：OpenAI 的 reasoning_effort:"none" 与 Anthropic 的
// thinking.disabled 是同一件事（明确关闭），通用语里只留一个取值。
// 这与 relay/thinking.go 的 normalizeEffort 同口径。
func normalizeEffort(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "none" {
		return effortOff
	}
	return v
}

// budgetOf 返回档位对应的预算刻度；不在词表内的档位返回 0,false。
func budgetOf(effort string) (int, bool) {
	switch effort {
	case effortMinimal:
		return ThinkingBudgetMinimal, true
	case effortLow:
		return ThinkingBudgetLow, true
	case effortMedium:
		return ThinkingBudgetMedium, true
	case effortHigh:
		return ThinkingBudgetHigh, true
	case effortXHigh:
		return ThinkingBudgetXHigh, true
	case effortMax:
		return ThinkingBudgetMax, true
	}
	return 0, false
}

// budgetBand 把预算数值分回档位（入站方向用）：
// n < mediumFrom → low，n < highFrom → medium，否则 high。
//
// 入站把数值收敛成档位是**有损**的（budget 5000 会记成 low），但跨协议时
// 上游协议本来就只认自己的形状，精确原值仍在报文留存里。
// 调用方保证 n > 0。
func budgetBand(n, mediumFrom, highFrom int) string {
	switch {
	case n < mediumFrom:
		return effortLow
	case n < highFrom:
		return effortMedium
	default:
		return effortHigh
	}
}

// anthropicThinkingEffort 从 Anthropic 请求里读思考强度（入站方向）。
//
// 两种代际的表达都要认（与 relay.ExtractThinkingLevel 同一套判断）：
//   - 4.6 之前：thinking.type = enabled/disabled，强度写在 budget_tokens 里
//   - 4.6 起：  thinking.type = adaptive，强度改由 output_config.effort 表达
//
// 返回空串表示「这次请求没说要思考」—— 出站侧据此不发任何思考参数，
// 由上游按自己的默认行为走。
func anthropicThinkingEffort(src map[string]any) string {
	if src == nil {
		return ""
	}
	th := asMap(src["thinking"])
	// 明确关掉优先于 effort：off 是「说了不要」，此时再按 effort 报一个档位
	// 等于把这次请求说成在思考（与观察层同一条优先级）。
	if th != nil && strings.EqualFold(asString(th["type"]), thinkingDisabled) {
		return effortOff
	}
	// effort 比 type 更精确，先看它；空串不算一个档位
	if oc := asMap(src["output_config"]); oc != nil {
		if v := normalizeEffort(asString(oc["effort"])); v != "" {
			return v
		}
	}
	if th == nil {
		return ""
	}
	switch strings.ToLower(asString(th["type"])) {
	case "enabled":
		// budget_tokens 是 enabled 的必填项，正常都在；真缺失时不写档位 ——
		// 「开着但强度未知」在通用语里没有可表达的值，凭空造一个不如留给上游默认。
		if n := asInt(th["budget_tokens"]); n > 0 {
			return budgetBand(n, ThinkingBudgetMedium, ThinkingBudgetHigh)
		}
		return ""
	case "adaptive":
		// 自适应：思考开着、强度交给模型定，与 Gemini 的 budget -1 同义
		return effortAuto
	}
	return ""
}

// geminiThinkingEffort 从 Gemini 请求里读思考强度（入站方向）。
// thinkingLevel 优先（Gemini 3），否则看 thinkingBudget（0=关，-1=动态，正数=预算）。
func geminiThinkingEffort(src map[string]any) string {
	gc := asMap(src["generationConfig"])
	if gc == nil {
		return ""
	}
	tc := asMap(gc["thinkingConfig"])
	if tc == nil {
		return ""
	}
	if lv := asString(tc["thinkingLevel"]); lv != "" {
		return normalizeEffort(lv)
	}
	raw, ok := tc["thinkingBudget"]
	if !ok {
		return ""
	}
	n := asInt(raw)
	switch {
	case n == 0:
		return effortOff
	case n < 0:
		return effortAuto
	default:
		return budgetBand(n, ThinkingBudgetMedium, ThinkingBudgetXHigh)
	}
}

// responsesReasoningEffort 从 Responses 请求里读思考强度（入站方向）。
//
// 取值原样小写保留（**不**把 "none" 归成 "off"）：Responses 属 OpenAI 生态，
// "none" 在那里是合法的「关」，而归一后再发回同生态上游只能落得删字段。
// 需要通用语取值的消费点（Anthropic / Gemini 出站）自己会归一。
func responsesReasoningEffort(src map[string]any) string {
	r := asMap(src["reasoning"])
	if r == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(asString(r["effort"])))
}

// applyEffortToAnthropicRequest 把通用语的思考强度写进 Anthropic 请求。
//
// 两条路径，优先级明确：
//  1. 入站就是 Anthropic（extra 里带着原样的 thinking / output_config）：
//     已经在调用处无损搬过去了，这里不再插手 —— 再按档位补一份只会与它冲突。
//  2. 其余入站（OpenAI / Gemini / Responses）：按档位映射。
//
// 第 2 条路径**只发 output_config.effort，不发 budget_tokens**：
// budget_tokens 在 Opus 5 / Sonnet 5 这类新模型上会直接 400，而
// output_config.effort 正是新代表达强度的方式（设计文档「出站 anthropic 的
// 分代处理」）。代价是旧代模型可能不认识 output_config —— 那时上游会回一条
// 明确的 400，比静默按默认强度跑要好排查。
func applyEffortToAnthropicRequest(out, src map[string]any, extra map[string]any) {
	if out == nil || src == nil {
		return
	}
	if extra != nil {
		if _, ok := extra["thinking"]; ok {
			return
		}
		if _, ok := extra["output_config"]; ok {
			return
		}
	}
	switch effort := normalizeEffort(asString(src[reasoningEffortField])); effort {
	case "":
		return
	case effortOff:
		out["thinking"] = map[string]any{"type": thinkingDisabled}
	case effortAuto:
		out["thinking"] = map[string]any{"type": "adaptive"}
	case effortMinimal:
		// Anthropic 没有 minimal 档：向上收敛到 low。
		// 收敛（而不是丢弃）保留了「用户要了最低档」这个意图，代价只是略高于所要求；
		// 用任何档位冒充 off 才是语义反转（见设计文档「已知不等价」）。
		out["output_config"] = map[string]any{"effort": effortLow}
	default:
		// low/medium/high/xhigh/max 原样，认不得的档位也原样透传：
		// 让它到上游 400 并带回明确错误，好过在中转站里静默降级。
		out["output_config"] = map[string]any{"effort": effort}
	}
}

// geminiThinkingConfig 把通用语的思考强度译成 Gemini 的 thinkingConfig。
//
// 用 thinkingBudget（数值）而不是 thinkingLevel（枚举）：数值是老中青三代
// 都认的形状，而 thinkingLevel 只有 Gemini 3 认。数值刻度见文件顶部的常量表。
func geminiThinkingConfig(effortRaw string) map[string]any {
	switch effort := normalizeEffort(effortRaw); effort {
	case "":
		return nil
	case effortOff:
		return map[string]any{"thinkingBudget": 0}
	case effortAuto:
		return map[string]any{"thinkingBudget": -1}
	default:
		if n, ok := budgetOf(effort); ok {
			return map[string]any{"thinkingBudget": n}
		}
		// 认不得的档位不发：Gemini 的预算是数值，为它猜一个数字等于替用户
		// 改预算；不发则退回上游默认行为。
		return nil
	}
}

// responsesReasoningFromEffort 把通用语的思考强度译成 Responses 的 reasoning。
//
// "off" / "auto" 不发声：Responses 侧没有「关」与「自适应」的表达。用字面
// 判断而不是归一后的值，是为了让原生的 "none" 原样过去（它在 OpenAI 生态里
// 是合法的关闭）。
func responsesReasoningFromEffort(effortRaw string) map[string]any {
	raw := strings.ToLower(strings.TrimSpace(effortRaw))
	switch raw {
	case "", effortOff, effortAuto:
		return nil
	}
	return map[string]any{"effort": raw}
}

// reasoningEffortNeedle 是粗筛用的子串：绝大多数请求没有这个字段，
// 先做子串判断就不必为每次转发付一次全量 JSON 解析。
var reasoningEffortNeedle = []byte(`"` + reasoningEffortField + `"`)

// stripUnsupportedEffort 从发往 OpenAI 兼容上游的请求体里剔除它表达不了的档位。
//
// 通用语里的 "off" / "auto" 是跨协议归一出来的值（来自 Anthropic 的
// thinking.disabled、Gemini 的 budget 0/-1），实测发给 OpenAI 兼容上游一律 400。
// 删字段让上游按自己的默认行为走 —— 代价是用户说「不要思考」时上游可能仍然思考，
// 这是上游能力的边界（设计文档「已知不等价」记录在案）。**不得**改用某个正档位
// 来假装关闭：那会让「关」变成「低」，是更隐蔽的欺骗。
//
// 只剔除字面的 off / auto：原生的 none / minimal / low… 原样透传，
// 不替 OpenAI 生态的客户端做取舍。
func stripUnsupportedEffort(body []byte) []byte {
	if !bytes.Contains(body, reasoningEffortNeedle) {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	raw, ok := m[reasoningEffortField]
	if !ok {
		return body
	}
	switch strings.ToLower(strings.TrimSpace(asString(raw))) {
	case effortOff, effortAuto:
		delete(m, reasoningEffortField)
	default:
		return body
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
