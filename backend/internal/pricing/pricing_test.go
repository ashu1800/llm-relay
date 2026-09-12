package pricing

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"llm-relay/internal/relay"
)

// 样本按 2026-09 官方页面的真实结构精简而成：
// 表头给模型名，随后每个指标一行 OFF-PEAK 跟一行 PEAK。
const deepseekTableFixture = `<html><body>
<table>
<tr><td>MODEL</td><td>deepseek-flash(1)</td><td>deepseek-v4-pro(2)</td></tr>
<tr><td>MODEL VERSION</td><td>DeepSeek-V4.1-Flash</td><td>DeepSeek-V4-Pro-0813</td></tr>
<tr><td>CONTEXT LENGTH</td><td>1M</td><td>1M</td></tr>
<tr><td>MAX OUTPUT</td><td>MAXIMUM: 384K</td><td>MAXIMUM: 384K</td></tr>
<tr><td>FEATURES</td><td>Json Output</td><td>✓</td></tr>
<tr><td>PRICING(3)</td><td>1M INPUT TOKENS(CACHE HIT)</td><td>OFF-PEAK</td><td>$0.003</td><td>$0.022</td></tr>
<tr><td></td><td></td><td>PEAK</td><td>$0.006</td><td>$0.044</td></tr>
<tr><td></td><td>1M INPUT TOKENS(CACHE MISS)</td><td>OFF-PEAK</td><td>$0.15</td><td>$0.66</td></tr>
<tr><td></td><td></td><td>PEAK</td><td>$0.3</td><td>$1.32</td></tr>
<tr><td></td><td>1M OUTPUT TOKENS</td><td>OFF-PEAK</td><td>$0.6</td><td>$1.98</td></tr>
<tr><td></td><td></td><td>PEAK</td><td>$1.2</td><td>$3.96</td></tr>
<tr><td>Concurrency Limit(4)</td><td>2500</td><td>500</td></tr>
</table>
</body></html>`

func TestParseDeepSeekOfficialTable(t *testing.T) {
	entries, err := parseDeepSeekTable([]byte(deepseekTableFixture))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("应解析出 2 个模型，实际 %d", len(entries))
	}

	byKey := map[string]Entry{}
	for _, e := range entries {
		byKey[e.ModelKey] = e
	}

	flash, ok := byKey["deepseek-flash"]
	if !ok {
		t.Fatalf("缺少 deepseek-flash，实际键: %v", keysOf(byKey))
	}
	// 基准价必须是谷时价
	assertDec(t, "flash 输入", flash.InputPer1M, "0.15")
	assertDec(t, "flash 输出", flash.OutputPer1M, "0.6")
	assertDec(t, "flash 缓存读取", flash.CacheReadPer1M, "0.003")

	pro, ok := byKey["deepseek-v4-pro"]
	if !ok {
		t.Fatalf("缺少 deepseek-v4-pro")
	}
	assertDec(t, "pro 输入", pro.InputPer1M, "0.66")
	assertDec(t, "pro 输出", pro.OutputPer1M, "1.98")
	assertDec(t, "pro 缓存读取", pro.CacheReadPer1M, "0.022")

	// 倍率应取自官方实际比值 1.32/0.66 = 2
	if len(pro.PeakRules) == 0 {
		t.Fatal("应带有峰时规则")
	}
	mult, _ := pro.PeakRules[0]["multiplier"].(float64)
	if mult != 2 {
		t.Fatalf("峰时倍率应为 2，实际 %v", mult)
	}
}

func TestParseDeepSeekTableRejectsEmpty(t *testing.T) {
	if _, err := parseDeepSeekTable([]byte("<html>没有表格</html>")); err == nil {
		t.Fatal("没有表格时应报错")
	}
}

// 2026-09-14 是周一，2026-09-12 是周六。
func TestDeepSeekPeakMatching(t *testing.T) {
	rules := DeepSeekPeakRules()

	cases := []struct {
		at     string
		expect float64
		desc   string
	}{
		{"2026-09-14T02:00:00Z", 2, "周一 02:00 落在 01:00-04:00 峰时"},
		{"2026-09-14T07:30:00Z", 2, "周一 07:30 落在 06:00-10:00 峰时"},
		{"2026-09-14T12:00:00Z", 1, "周一 12:00 为谷时"},
		{"2026-09-14T00:30:00Z", 1, "周一 00:30 尚未进入峰时"},
		{"2026-09-12T02:00:00Z", 1, "周六即使时刻在区间内也是谷时"},
		{"2026-09-13T07:30:00Z", 1, "周日为谷时"},
	}
	for _, c := range cases {
		at, err := time.Parse(time.RFC3339, c.at)
		if err != nil {
			t.Fatalf("时间解析失败: %v", err)
		}
		got, _ := MatchPeak(rules, at)
		if got != c.expect {
			t.Errorf("%s: 倍率应为 %v，实际 %v", c.desc, c.expect, got)
		}
	}
}

func TestMatchPeakTakesMaxMultiplier(t *testing.T) {
	// 两个窗口重叠时应取最大倍率，而不是相乘
	rules := mustRules(t, `[
		{"days": [], "start": "00:00", "end": "23:59", "multiplier": 1.5},
		{"days": [], "start": "08:00", "end": "12:00", "multiplier": 3}
	]`)
	at, _ := time.Parse(time.RFC3339, "2026-09-14T09:00:00Z")
	got, _ := MatchPeak(rules, at)
	if got != 3 {
		t.Fatalf("重叠窗口应取最大倍率 3，实际 %v", got)
	}
}

// 用真实价格验证一次费用计算，含缓存命中与未命中两条不同单价的输入。
func TestCostComputation(t *testing.T) {
	offPeak := Price{
		Currency:        "USD",
		InputPer1M:      decimal.RequireFromString("0.66"),
		OutputPer1M:     decimal.RequireFromString("1.98"),
		CacheReadPer1M:  decimal.RequireFromString("0.022"),
		CacheWritePer1M: decimal.RequireFromString("0.66"),
	}
	usage := relay.Usage{PromptTokens: 66, CachedTokens: 294, CompletionTokens: 10}
	// 0.66*66 + 1.98*10 + 0.022*294 = 43.56 + 19.8 + 6.468 = 69.828 （单位 1e-6 美元）
	want := decimal.RequireFromString("0.000069828")
	got := offPeak.Cost(usage)
	if !got.Equal(want) {
		t.Fatalf("谷时费用应为 %s，实际 %s", want, got)
	}

	peak := offPeak
	peak.InputPer1M = offPeak.InputPer1M.Mul(decimal.NewFromInt(2))
	peak.OutputPer1M = offPeak.OutputPer1M.Mul(decimal.NewFromInt(2))
	peak.CacheReadPer1M = offPeak.CacheReadPer1M.Mul(decimal.NewFromInt(2))
	if got := peak.Cost(usage); !got.Equal(want.Mul(decimal.NewFromInt(2))) {
		t.Fatalf("峰时费用应为谷时的两倍，实际 %s", got)
	}
}

func TestCostIgnoresEmptyUsage(t *testing.T) {
	p := Price{
		InputPer1M:  decimal.RequireFromString("1"),
		OutputPer1M: decimal.RequireFromString("2"),
	}
	if got := p.Cost(relay.Usage{}); !got.IsZero() {
		t.Fatalf("零用量费用应为 0，实际 %s", got)
	}
}

// LiteLLM 以每 token 计价，且 DeepSeek 记的是峰时价，两者都要在入库前换算。
func TestPerTokenToPer1M(t *testing.T) {
	// LiteLLM 中 deepseek-v4-pro 的 input_cost_per_token = 1.32e-06（峰时）
	got := perTokenToPer1M(1.32e-06)
	assertDec(t, "峰时输入价", got, "1.32")

	// 除以 2 后应还原为官方谷时价 0.66
	halved := got.Div(decimal.NewFromInt(2))
	assertDec(t, "还原后的谷时输入价", halved, "0.66")

	if !perTokenToPer1M(nil).IsZero() {
		t.Fatal("非数字输入应返回 0")
	}
	if !perTokenToPer1M(0.0).IsZero() {
		t.Fatal("零价应返回 0")
	}
}

func TestSnapshotCarriesResolvedPrices(t *testing.T) {
	p := Price{
		ModelKey: "deepseek-v4-pro", Currency: "USD", Source: SourceOfficial,
		InputPer1M:      decimal.RequireFromString("1.32"),
		OutputPer1M:     decimal.RequireFromString("3.96"),
		CacheReadPer1M:  decimal.RequireFromString("0.044"),
		CacheWritePer1M: decimal.RequireFromString("1.32"),
		Multiplier:      decimal.NewFromInt(2), PeakApplied: true, PeakLabel: "峰时",
	}
	snap := p.Snapshot(time.Date(2026, 9, 14, 2, 0, 0, 0, time.UTC))

	if snap["input_per_1m"] != "1.32" {
		t.Fatalf("快照应固化当时的输入价，实际 %v", snap["input_per_1m"])
	}
	if snap["peak_applied"] != true {
		t.Fatalf("快照应标记峰时，实际 %v", snap["peak_applied"])
	}
	if snap["source"] != SourceOfficial {
		t.Fatalf("快照应记录来源，实际 %v", snap["source"])
	}
}

// ---------- 测试辅助 ----------

func mustRules(t *testing.T, s string) []map[string]any {
	t.Helper()
	var l []map[string]any
	if err := jsonUnmarshal(s, &l); err != nil {
		t.Fatalf("解析规则失败: %v", err)
	}
	return l
}

func assertDec(t *testing.T, name string, got decimal.Decimal, want string) {
	t.Helper()
	w := decimal.RequireFromString(want)
	if !got.Equal(w) {
		t.Fatalf("%s 应为 %s，实际 %s", name, want, got)
	}
}

func keysOf(m map[string]Entry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
