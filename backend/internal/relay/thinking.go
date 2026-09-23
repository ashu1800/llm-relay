package relay

import (
	"encoding/json"
	"strings"

	"llm-relay/internal/relay/convert"
)

// 入站思考参数的提取：从客户端原始请求体里读「这次调用想要的思考强度」，
// 随日志落一份快照。只观察、不改写 —— 转发载荷（req.Body）怎么走与这里无关，
// 与 ExtractModel / ExtractStream 是同一种风格。
//
// 归一成有限档位，空串 = 请求没带思考参数（历史行与不带思考的模型自然是空）：
//
//	off · minimal · low · medium · high · xhigh · on · auto · max
//
// 档位词表跟着各家客户端长 —— 认不得的词原样透传（诚实优于猜测），
// 前端只为其中一部分配了颜色。xhigh / max 是 anthropic 4.6+ 的
// effort 档，minimal 来自 OpenAI 的 reasoning_effort。
//
// 协议名与 api/protocols.go 的 inboundProfile.Name 对齐（relay 不能反向
// import api，这里以常量钉住字面量；thinking_test 会用真实字面量逐个覆盖）。
const (
	protoOpenAIChat     = "openai-chat"
	protoOpenAIResponse = "openai-responses"
	protoAnthropic      = "anthropic-messages"
	protoGemini         = "gemini-generateContent"
)

// budget 分档是有意的粗档：列表要的是「思考强度的直觉」，不是精确保留
// 参数原值 —— 精确数值在详情报文留存里有。边界取各家常用档：
// Anthropic 常见 1024/4096/8192/16384/31999；Gemini Flash 预算上限 24576、Pro 32768。
//
// 数值不在这里写死，直接引用 convert 的刻度常量：那边是**转发方向**
// （档位 → 预算数值）用的同一把尺子。各写一份的话，改了一边就会出现
// 「列表显示 medium、上游收到 high」这类没人会立刻发现的偏差。
const (
	budgetMediumFrom  = convert.ThinkingBudgetMedium
	anthropicHighFrom = convert.ThinkingBudgetHigh
	geminiHighFrom    = convert.ThinkingBudgetXHigh
)

// ExtractThinkingLevel 从入站请求体提取思考等级。
// proto 是入站协议名（同 InboundProto / inboundProfile.Name），raw 是原始请求体。
func ExtractThinkingLevel(proto string, raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	switch proto {
	case protoOpenAIChat:
		// OpenAI 系：顶层 reasoning_effort（o 系列 / gpt-5，"none" 表示关闭）。
		// GLM 走的也是 openai-chat 协议，思考开关是顶层 thinking.type。
		var p struct {
			ReasoningEffort string `json:"reasoning_effort"`
			Thinking        *struct {
				Type string `json:"type"`
			} `json:"thinking"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return ""
		}
		if p.ReasoningEffort != "" {
			return normalizeEffort(p.ReasoningEffort)
		}
		if p.Thinking != nil {
			return normalizeSwitch(p.Thinking.Type)
		}
		return ""
	case protoOpenAIResponse:
		// Responses API：reasoning.effort
		var p struct {
			Reasoning *struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		if json.Unmarshal(raw, &p) != nil || p.Reasoning == nil {
			return ""
		}
		return normalizeEffort(p.Reasoning.Effort)
	case protoAnthropic:
		// Anthropic 有两种「开思考」的表达，按代际分的：
		//
		//  4.6 之前：thinking.type = enabled/disabled，强度写在 budget_tokens 里
		//  4.6 起：  thinking.type = adaptive，强度改由 output_config.effort
		//            表达（low/medium/high/xhigh/max）。
		//            budget_tokens 在这一代已废弃 —— 在 Opus 5 / Sonnet 5
		//            这类新模型上直接 400，所以新版客户端不会再发它。
		//
		// Claude Code 走的是后者，且它发的 effort 是 "max"。只认 enabled
		// 会让这一整类请求静默落成空档（列表里那一格什么都没有），
		// 而请求其实一直在正常思考 —— 观察值与该列存在的意义正好相反。
		var p struct {
			Thinking *struct {
				Type         string `json:"type"`
				BudgetTokens int    `json:"budget_tokens"`
			} `json:"thinking"`
			OutputConfig *struct {
				// 指针：区分「没传 effort」与「传了空串」，
				// 后者不该被当成一个未知档位透传上去
				Effort *string `json:"effort"`
			} `json:"output_config"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return ""
		}
		// 明确关掉优先于 effort：off 是「说了不要」，
		// 此时再按 effort 报一个档位等于把这次请求说成在思考。
		if p.Thinking != nil && strings.EqualFold(p.Thinking.Type, "disabled") {
			return "off"
		}
		// effort 是比 type 更精确的意图，先看它。
		// "max" 原样透传 —— 前端 THINKING_COLORS 里有这一档
		// （注释记着「线上观测到的客户端自定义最高档」就是它）。
		if p.OutputConfig != nil && p.OutputConfig.Effort != nil {
			if v := normalizeEffort(*p.OutputConfig.Effort); v != "" {
				return v
			}
		}
		if p.Thinking == nil {
			return ""
		}
		switch strings.ToLower(p.Thinking.Type) {
		case "disabled":
			return "off"
		case "enabled":
			return budgetBand(p.Thinking.BudgetTokens, budgetMediumFrom, anthropicHighFrom, "on")
		case "adaptive":
			// 自适应但没有 effort：思考开着、强度交给模型自己定，
			// 与 Gemini 的 budget -1 是同一个语义，共用 "auto"。
			return "auto"
		}
		return ""
	case protoGemini:
		// Gemini：generationConfig.thinkingConfig，thinkingLevel 优先（Gemini 3），
		// 否则 thinkingBudget（0=关，-1=动态，正数=预算）
		var p struct {
			GenerationConfig *struct {
				ThinkingConfig *struct {
					ThinkingLevel  string `json:"thinkingLevel"`
					ThinkingBudget *int   `json:"thinkingBudget"`
				} `json:"thinkingConfig"`
			} `json:"generationConfig"`
		}
		if json.Unmarshal(raw, &p) != nil || p.GenerationConfig == nil || p.GenerationConfig.ThinkingConfig == nil {
			return ""
		}
		tc := p.GenerationConfig.ThinkingConfig
		if tc.ThinkingLevel != "" {
			return normalizeEffort(tc.ThinkingLevel)
		}
		if tc.ThinkingBudget == nil {
			return ""
		}
		switch {
		case *tc.ThinkingBudget == 0:
			return "off"
		case *tc.ThinkingBudget < 0:
			return "auto"
		default:
			return budgetBand(*tc.ThinkingBudget, budgetMediumFrom, geminiHighFrom, "on")
		}
	default:
		// embeddings 等没有思考概念的协议
		return ""
	}
}

// budgetBand 把预算数值映射到档位：n < mediumFrom 是 low，
// mediumFrom ≤ n < highFrom 是 medium，n ≥ highFrom 是 high；
// n ≤ 0（没有有效预算）回 fallback（如 enabled 但没带 budget_tokens）。
func budgetBand(n, mediumFrom, highFrom int, fallback string) string {
	switch {
	case n <= 0:
		return fallback
	case n < mediumFrom:
		return "low"
	case n < highFrom:
		return "medium"
	default:
		return "high"
	}
}

// normalizeEffort 归一 effort 风格的等级词：小写；"none" 是「明确关闭」。
// 认不得的词原样小写透传 —— 上游自定义的档位诚实展示，优于猜测。
func normalizeEffort(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "none" {
		return "off"
	}
	return v
}

// normalizeSwitch 归一「开/关」风格的思考开关（GLM thinking.type）。
func normalizeSwitch(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "enabled":
		return "on"
	case "disabled":
		return "off"
	}
	return ""
}
