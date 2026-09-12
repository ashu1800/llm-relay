package pricing

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// 官方页面脚注原文（2026-09 抓取）。
// 表头给 deepseek-flash 挂了 (1)，正文写明旧名字仍被接受并按 Flash 价格计费。
const deepseekAliasFootnote = ` (1) Use deepseek-flash as the model name. The legacy names deepseek-v4-flash and deepseek-v4-flash-vision-exp are still accepted, but the corresponding models have been retired, their requests are served by the DeepSeek-V4.1-Flash model and billed at the Flash price. (2) In response to user demand, we have decided to continue providing API services for DeepSeek V4 Pro after September 14, 2026, with the billing method remaining unchanged. (3) Off-peak rates are half of the peak rates. Peak hours are 01:00 - 04:00 and 06:00 - 10:00 UTC, Monday through Friday (all other hours are off-peak). `

func aliasFixture() string {
	return strings.Replace(deepseekTableFixture, "</body></html>", deepseekAliasFootnote+"</body></html>", 1)
}

// 官方脚注承认的遗留别名必须生成定价行，且价格与峰时规则和正式名一致。
//
// 线上就是靠这条兜住的：调用方用的模型名是 deepseek-v4-flash（官方遗留别名），
// 而正式名是 deepseek-flash —— 没有别名行时它匹配不到官方价，
// 会落到 LiteLLM 里某家无关托管商的报价上（实测是 tencent 的 0.14/0.28，
// 而官方谷时是 0.15/0.6，输出价少算 2 倍多，峰时还要再差一倍）。
func TestParseDeepSeekTableEmitsLegacyAliasRows(t *testing.T) {
	entries, err := parseDeepSeekTable([]byte(aliasFixture()))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	byKey := map[string]Entry{}
	for _, e := range entries {
		byKey[e.ModelKey] = e
	}

	flash, ok := byKey["deepseek-flash"]
	if !ok {
		t.Fatalf("缺少正式名 deepseek-flash，实际键: %v", keysOf(byKey))
	}
	if !flash.InputPer1M.Equal(decimal.RequireFromString("0.15")) {
		t.Fatalf("deepseek-flash 输入价应为 0.15，实际 %s", flash.InputPer1M)
	}

	for _, alias := range []string{"deepseek-v4-flash", "deepseek-v4-flash-vision-exp"} {
		a, ok := byKey[alias]
		if !ok {
			t.Fatalf("缺少别名定价行 %s，实际键: %v", alias, keysOf(byKey))
		}
		if !a.InputPer1M.Equal(flash.InputPer1M) {
			t.Errorf("%s 输入价 %s 与正式名 %s 不一致", alias, a.InputPer1M, flash.InputPer1M)
		}
		if !a.OutputPer1M.Equal(flash.OutputPer1M) {
			t.Errorf("%s 输出价 %s 与正式名 %s 不一致", alias, a.OutputPer1M, flash.OutputPer1M)
		}
		if !a.CacheReadPer1M.Equal(flash.CacheReadPer1M) {
			t.Errorf("%s 缓存读价 %s 与正式名 %s 不一致", alias, a.CacheReadPer1M, flash.CacheReadPer1M)
		}
		if len(a.PeakRules) != len(flash.PeakRules) {
			t.Errorf("%s 峰时规则条数 %d 与正式名 %d 不一致 —— 缺规则会让峰时少算一倍",
				alias, len(a.PeakRules), len(flash.PeakRules))
		}
	}

	// 脚注 (2) 只讲了继续提供服务，没提 legacy names，不该凭空产生别名
	if _, bad := byKey["deepseek-v4-pro-0813"]; bad {
		t.Error("脚注 (2) 未声明遗留别名，不应生成别名行")
	}
}

// 没有脚注正文时不应产生任何别名，也不应报错。
func TestFootnoteAliasesAbsentWithoutFootnoteText(t *testing.T) {
	entries, err := parseDeepSeekTable([]byte(deepseekTableFixture))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("无脚注时应只有 2 条，实际 %d: %v", len(entries), keysOf(map[string]Entry{}))
	}
}

// 表头里的模型名也可能匹配到脚注编号的正则，必须取正文而不是表头那次。
func TestFootnoteAliasesPrefersFootnoteBodyOverHeader(t *testing.T) {
	got := footnoteAliases([]byte(aliasFixture()), []string{"MODEL", "deepseek-flash(1)", "deepseek-v4-pro(2)"})
	aliases := got[0]
	if len(aliases) != 2 {
		t.Fatalf("应抽到 2 个别名，实际 %v", aliases)
	}
	seen := map[string]bool{}
	for _, a := range aliases {
		seen[a] = true
	}
	for _, want := range []string{"deepseek-v4-flash", "deepseek-v4-flash-vision-exp"} {
		if !seen[want] {
			t.Errorf("缺少别名 %s，实际 %v", want, aliases)
		}
	}
}
