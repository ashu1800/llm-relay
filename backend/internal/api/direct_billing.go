package api

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"llm-relay/internal/pricing"
	"llm-relay/internal/relay"
)

// 直通端点的计费口径与金额计算（2026-11 P2）。
//
// 图像 / 语音端点不走 token 计量 —— 上游对它们的计价单位是「张 / 秒 / 字符」
// （见 model.ModelPrice 的三个维度字段与 pricing.Price 的 Cost* 系列）。
// 判定与取数全部抽成纯函数：这是账本逻辑，必须能脱离 HTTP 与数据库测。

// directBillingKind 按入站协议返回这次调用的计费口径。
// 空串 = token 口径（不发生在 direct 端点里，兜底防御）。
func directBillingKind(inboundProto string) string {
	switch inboundProto {
	case "openai-images":
		return pricing.BillingKindImage
	case "openai-audio-speech":
		return pricing.BillingKindTTS
	case "openai-audio-transcriptions", "openai-audio-translations":
		return pricing.BillingKindAudio
	default:
		return ""
	}
}

// imageCountFromResponse 从图像生成的响应 JSON 里数张数（data 数组长度）。
//
// 计量认**响应**而不是请求的 n 参数：上游可能少给（内容政策拦掉一部分时
// OpenAI 返回的 data 就更短），按响应计费才与上游账单对得上。
// 响应不是合法 JSON（异常截断等）返回 0 —— 费用记 0 比按猜测计费诚实。
func imageCountFromResponse(body []byte) int {
	var payload struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0
	}
	return len(payload.Data)
}

// ttsInputChars 从 TTS 请求体里取 input 文本的字符数（按 Unicode 码点）。
//
// OpenAI 的 TTS 按**字符**计价，对中文即字数。用码点而不是字节：UTF-8 下
// 一个汉字三字节，按字节算会把中文账单放大三倍。响应不是 JSON 返回 0
// （上游没消费这次请求的可能性极高，不记账）。
func ttsInputChars(body []byte) int {
	var payload struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0
	}
	if payload.Input == "" {
		return 0
	}
	return utf8.RuneCountInString(payload.Input)
}

// audioDurationFromResponse 从转写响应里取音频时长（秒）。
//
// 只有 verbose_json 响应格式带 duration 字段。没有时长就没有按秒计费的
// 依据 —— 返回 0，调用方记 0 元并在用量上如实标注，不要拿响应长度凑数
// （那和漏配价一样：账面看不出来，但对不上上游账单）。
func audioDurationFromResponse(body []byte) decimal.Decimal {
	var payload struct {
		Duration float64 `json:"duration"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Duration <= 0 {
		return decimal.Zero
	}
	return decimal.NewFromFloat(payload.Duration)
}

// directCost 计算直通端点的费用与口径，返回 (金额, 口径)。
//
// 各口径的计量来源（都来自**已实际发生**的那一侧）：
//
//	image  响应 data 数组的张数 —— 上游可能比请求的 n 少给（内容政策），
//	       按响应计费才与账单一致；
//	tts    请求 input 文本的字符数 —— TTS 的「输入」就是它；
//	audio  响应的 usage（真实回报而非估算时按 token）或 duration（按秒）；
//	       两者都没有记 0 元 —— 没有计量依据的账，宁可不记也不要猜。
func directCost(kind string, reqBody, respBody []byte, usage relay.Usage, p pricing.Price) (decimal.Decimal, string) {
	switch kind {
	case pricing.BillingKindImage:
		return p.CostImages(imageCountFromResponse(respBody)), pricing.BillingKindImage
	case pricing.BillingKindTTS:
		return p.CostCharacters(ttsInputChars(reqBody)), pricing.BillingKindTTS
	case pricing.BillingKindAudio:
		// 只认上游真实回报的 usage：EstimateUsage 的按报文长度折算对音频
		// 毫无意义（一段 25MB 的 mp3 会被折成几百万「token」）
		if usage.BillableTokens() > 0 && !usage.Estimated {
			return p.Cost(usage), pricing.BillingKindToken
		}
		if d := audioDurationFromResponse(respBody); d.Sign() > 0 {
			return p.CostAudioSeconds(d), pricing.BillingKindAudio
		}
		return decimal.Zero, pricing.BillingKindAudio
	}
	return decimal.Zero, ""
}

// directRespBody 取这次转发上游的响应体（失败路径可能为 nil）。
func directRespBody(res *relay.RelayResult) []byte {
	if res != nil && res.Attempt != nil {
		return res.Attempt.Body
	}
	return nil
}
