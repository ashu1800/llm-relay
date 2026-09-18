package relay

import (
	"encoding/json"
	"strings"
)

// 入站思考参数的提取：从客户端原始请求体里读「这次调用想要的思考强度」，
// 随日志落一份快照。只观察、不改写 —— 转发载荷（req.Body）怎么走与这里无关，
// 与 ExtractModel / ExtractStream 是同一种风格。
//
// 归一成有限档位，空串 = 请求没带思考参数（历史行与不带思考的模型自然是空）：
//
//	off · minimal · low · medium · high · on · auto
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
const (
	budgetMediumFrom  = 8192
	anthropicHighFrom = 16384
	geminiHighFrom    = 24576
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
		// Anthropic：thinking.type = enabled/disabled，enabled 必带 budget_tokens
		var p struct {
			Thinking *struct {
				Type         string `json:"type"`
				BudgetTokens int    `json:"budget_tokens"`
			} `json:"thinking"`
		}
		if json.Unmarshal(raw, &p) != nil || p.Thinking == nil {
			return ""
		}
		switch strings.ToLower(p.Thinking.Type) {
		case "disabled":
			return "off"
		case "enabled":
			return budgetBand(p.Thinking.BudgetTokens, budgetMediumFrom, anthropicHighFrom, "on")
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
