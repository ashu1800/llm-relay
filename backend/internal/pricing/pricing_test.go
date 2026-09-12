package pricing

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"llm-relay/internal/model"
	"llm-relay/internal/relay"
)

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

// 跨午夜的窗口（22:30-00:30）也要能命中，否则夜里的请求会按白天价计。
func TestMatchPeakCrossesMidnight(t *testing.T) {
	rules := mustRules(t, `[{"days": [], "start": "22:30", "end": "00:30", "multiplier": 2}]`)
	for _, c := range []struct {
		at     string
		expect float64
	}{
		{"2026-09-14T23:00:00Z", 2},
		{"2026-09-15T00:15:00Z", 2},
		{"2026-09-14T22:00:00Z", 1},
		{"2026-09-15T01:00:00Z", 1},
	} {
		at, _ := time.Parse(time.RFC3339, c.at)
		if got, _ := MatchPeak(rules, at); got != c.expect {
			t.Errorf("%s 倍率应为 %v，实际 %v", c.at, c.expect, got)
		}
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

// 快照是写进日志的历史账目：单价与倍率必须固化下来，
// 日后改价或改倍率规则都不能让旧日志里的金额跟着变。
func TestSnapshotCarriesResolvedPrices(t *testing.T) {
	p := Price{
		ModelKey: "deepseek-v4-pro", Currency: "USD",
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
		t.Fatalf("快照应标记倍率命中，实际 %v", snap["peak_applied"])
	}
	if snap["multiplier"] != "2" {
		t.Fatalf("快照应记录生效倍率，实际 %v", snap["multiplier"])
	}
}

// ---------- 测试辅助 ----------

func mustRules(t *testing.T, s string) model.JSONList {
	t.Helper()
	var l model.JSONList
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
