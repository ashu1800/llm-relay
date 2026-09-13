package model

import "strings"

// 记账币种。
//
// 币种是**渠道属性**：一家上游按什么币种收钱，是那家上游决定的，同一个模型名
// 在不同渠道本来就可能是两种货币。库里只存币种代码，不做任何汇率换算 ——
// 换算率从哪来、什么时候更新、历史按当时还是现状，是三个没有正确答案的问题，
// 而「哪条渠道在烧钱」根本不需要回答它们。
//
// 直接后果：**不同币种的钱不能相加**。所以「合计」只在同一币种内成立，
// 看板按币种分别统计（见 api.costsByCurrency），日志按各自渠道的币种显示。
const (
	CurrencyCNY = "CNY"
	CurrencyUSD = "USD"
)

// SupportedCurrencies 是可选币种，顺序固定：下拉框、图例、看板的多币种排列
// 都按它来，免得同一个页面上两处顺序不一致。
var SupportedCurrencies = []string{CurrencyCNY, CurrencyUSD}

// NormalizeCurrency 归一化用户填的币种代码（大小写与空白都容忍）。
//
// 认不出来时返回 false，而不是退回一个默认值：填错一个代码就静默按另一个
// 币种展示的话，金额本身看不出任何异常（0.15 还是 0.15），只能靠肉眼发现。
func NormalizeCurrency(raw string) (string, bool) {
	c := strings.ToUpper(strings.TrimSpace(raw))
	for _, s := range SupportedCurrencies {
		if c == s {
			return c, true
		}
	}
	return "", false
}
