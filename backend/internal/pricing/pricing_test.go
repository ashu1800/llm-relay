package pricing

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"llm-relay/internal/model"
	"llm-relay/internal/relay"
)

// 时段规则的时间语义是**服务器本地时间**（用户填的就是他手表上的钟点）。
// 这里固定用本地时区构造时刻，避免测试跟着容器时区漂。
func localTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.ParseInLocation("2006-01-02T15:04:05", s, time.Local)
	if err != nil {
		t.Fatalf("时间解析失败: %v", err)
	}
	return at
}

// 2026-09-14 是周一，2026-09-12 是周六。
func TestPeakWindowMatching(t *testing.T) {
	rules := mustRules(t, `[
		{"days": [1,2,3,4,5], "start": "09:00", "end": "12:00", "multiplier": 2, "label": "上午高峰"},
		{"days": [1,2,3,4,5], "start": "14:00", "end": "18:00", "multiplier": 2, "label": "下午高峰"}
	]`)

	cases := []struct {
		at     string
		expect float64
		desc   string
	}{
		{"2026-09-14T10:00:00", 2, "周一 10:00 落在上午高峰"},
		{"2026-09-14T15:30:00", 2, "周一 15:30 落在下午高峰"},
		{"2026-09-14T13:00:00", 1, "周一 13:00 是午休，不命中"},
		{"2026-09-14T08:30:00", 1, "周一 08:30 尚未进入高峰"},
		{"2026-09-12T10:00:00", 1, "周六即使时刻在区间内也不命中"},
	}
	for _, c := range cases {
		got, _, hit := MatchPeak(rules, localTime(t, c.at))
		if got != c.expect {
			t.Errorf("%s: 倍率应为 %v，实际 %v", c.desc, c.expect, got)
		}
		if hit != (c.expect != 1) {
			t.Errorf("%s: 命中标记应为 %v", c.desc, c.expect != 1)
		}
	}
}

// 多条规则同时命中时，**列表里靠后的那条生效**。
// 这条语义是刻意的：倍率可以是折扣，取最大值会让打折规则永远不生效。
func TestMatchPeakLastRuleWins(t *testing.T) {
	rules := mustRules(t, `[
		{"days": [], "start": "00:00", "end": "23:59", "multiplier": 1.5, "label": "全天"},
		{"days": [], "start": "08:00", "end": "12:00", "multiplier": 3, "label": "高峰"}
	]`)
	got, label, hit := MatchPeak(rules, localTime(t, "2026-09-14T09:00:00"))
	if !hit || got != 3 || label != "高峰" {
		t.Fatalf("重叠时应以最后一条为准（3/高峰），实际 %v/%q/%v", got, label, hit)
	}

	// 反过来：打折规则排在后面时要能压过前面的加价规则
	discount := mustRules(t, `[
		{"days": [], "start": "00:00", "end": "23:59", "multiplier": 2, "label": "白天"},
		{"days": [], "start": "22:00", "end": "23:59", "multiplier": 0.5, "label": "夜间五折"}
	]`)
	if got, label, _ := MatchPeak(discount, localTime(t, "2026-09-14T23:00:00")); got != 0.5 || label != "夜间五折" {
		t.Fatalf("折扣规则应生效（0.5），实际 %v/%q", got, label)
	}
}

// 跨午夜的窗口（22:30-00:30）也要能命中，否则夜里的请求会按白天价计。
func TestMatchPeakCrossesMidnight(t *testing.T) {
	rules := mustRules(t, `[{"days": [], "start": "22:30", "end": "00:30", "multiplier": 2}]`)
	for _, c := range []struct {
		at     string
		expect float64
	}{
		{"2026-09-14T23:00:00", 2},
		{"2026-09-15T00:15:00", 2},
		{"2026-09-14T22:00:00", 1},
		{"2026-09-15T01:00:00", 1},
	} {
		if got, _, _ := MatchPeak(rules, localTime(t, c.at)); got != c.expect {
			t.Errorf("%s 倍率应为 %v，实际 %v", c.at, c.expect, got)
		}
	}
}

// 跨午夜窗口配了星期时，尾段归属**起始日**的星期：
// 「周五 22:00–02:00」（days=[5]）在周六凌晨必须仍然命中 ——
// 修复前 inWindow 先按发生时刻的星期过滤，尾段永远不生效。
func TestMatchPeakCrossMidnightWeekdayBelongsToStartDay(t *testing.T) {
	// 2026-09-18 是周五，2026-09-19 是周六
	rules := mustRules(t, `[{"days": [5], "start": "22:00", "end": "02:00", "multiplier": 2}]`)
	for _, c := range []struct {
		at     string
		expect float64
	}{
		{"2026-09-18T23:00:00", 2}, // 周五午夜前：起始日本身
		{"2026-09-19T00:30:00", 2}, // 周六凌晨：尾段仍属周五 —— 修复点
		{"2026-09-19T01:30:00", 2}, // 尾段最后一刻仍命中
		{"2026-09-19T02:30:00", 1}, // 窗口结束之后
		{"2026-09-19T23:00:00", 1}, // 周六午夜前：星期不匹配
		{"2026-09-20T00:30:00", 1}, // 周日凌晨：不匹配（属周六的尾段，周六没配）
	} {
		if got, _, _ := MatchPeak(rules, localTime(t, c.at)); got != c.expect {
			t.Errorf("%s 倍率应为 %v，实际 %v", c.at, c.expect, got)
		}
	}

	// 周一起始的窗口，尾段落在周二凌晨 —— 起始日判定与回绕无关（归属周一）
	mondayRules := mustRules(t, `[{"days": [1], "start": "22:00", "end": "01:00", "multiplier": 2}]`)
	// 2026-09-21 是周一，2026-09-22 是周二
	if got, _, _ := MatchPeak(mondayRules, localTime(t, "2026-09-22T00:30:00")); got != 2 {
		t.Errorf("周一起始窗口的周二凌晨尾段应命中，实际 %v", got)
	}
}

// 规则写错必须当场报错，并指出是第几条 ——
// 写错的窗口不会报错、只会永不命中，用户会以为已经配好了双倍计费。
func TestNormalizeRulesRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"缺结束时间", `[{"start":"09:00","end":"","multiplier":2}]`, "开始与结束时间都要填"},
		{"时间格式错", `[{"start":"9点","end":"12:00","multiplier":2}]`, "不是 HH:MM 格式"},
		{"倍率为零", `[{"start":"09:00","end":"12:00","multiplier":0}]`, "倍率必须是大于 0"},
		{"星期越界", `[{"days":[0],"start":"09:00","end":"12:00","multiplier":2}]`, "1-7"},
		{"倍率过大", `[{"start":"09:00","end":"12:00","multiplier":1000}]`, "倍率不能超过"},
	}
	for _, c := range cases {
		_, err := NormalizeRules(mustRules(t, c.raw))
		if err == nil {
			t.Errorf("%s：应当报错", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：错误信息应包含 %q，实际 %q", c.name, c.want, err.Error())
		}
		if !strings.Contains(err.Error(), "第 1 条") {
			t.Errorf("%s：错误信息应指明第几条，实际 %q", c.name, err.Error())
		}
	}

	// 合法的规则应被规整成规范结构（days 去重、时间去空格）
	out, err := NormalizeRules(mustRules(t, `[{"days":[1,1,2],"start":" 09:00 ","end":"12:00","multiplier":2}]`))
	if err != nil {
		t.Fatalf("合法规则不该报错: %v", err)
	}
	days, _ := out[0]["days"].([]int)
	if len(days) != 2 {
		t.Errorf("星期应去重成 2 个，实际 %v", days)
	}
	if out[0]["start"] != "09:00" {
		t.Errorf("时间应去掉空格，实际 %v", out[0]["start"])
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
		ChannelID: 7, ModelName: "deepseek-v4-pro", Currency: "USD",
		InputPer1M:      decimal.RequireFromString("1.32"),
		OutputPer1M:     decimal.RequireFromString("3.96"),
		CacheReadPer1M:  decimal.RequireFromString("0.044"),
		CacheWritePer1M: decimal.RequireFromString("1.32"),
		Multiplier:      decimal.NewFromInt(2), Source: MultiplierSourcePeak,
		PeakApplied: true, PeakLabel: "峰时",
		Base: model.ChannelModel{Multiplier: 1.5},
	}
	snap := p.Snapshot(time.Date(2026, 9, 14, 10, 0, 0, 0, time.Local))

	if snap["input_per_1m"] != "1.32" {
		t.Fatalf("快照应固化当时的输入价，实际 %v", snap["input_per_1m"])
	}
	if snap["peak_applied"] != true {
		t.Fatalf("快照应标记倍率命中，实际 %v", snap["peak_applied"])
	}
	if snap["multiplier"] != "2" {
		t.Fatalf("快照应记录生效倍率，实际 %v", snap["multiplier"])
	}
	if snap["multiplier_source"] != MultiplierSourcePeak {
		t.Fatalf("快照应记录倍率来源，实际 %v", snap["multiplier_source"])
	}
	if snap["fixed_multiplier"] != "1.5" {
		t.Fatalf("快照应记录固定倍率，实际 %v", snap["fixed_multiplier"])
	}
	// 时刻要带时区偏移：规则按本地时间判断，只记 UTC 事后没法核对
	if s, _ := snap["resolved_at"].(string); !strings.Contains(s, "+") && !strings.HasSuffix(s, "Z") {
		t.Fatalf("resolved_at 应带时区，实际 %v", snap["resolved_at"])
	}
	// 价格是「渠道 × 模型」维度的：快照必须记下渠道，
	// 否则事后看到一笔金额无从判断它按哪条渠道的价算的
	if snap["channel_id"] != uint(7) {
		t.Fatalf("快照应记录渠道 ID，实际 %v", snap["channel_id"])
	}
	if snap["model_key"] != "deepseek-v4-pro" {
		t.Fatalf("快照的 model_key 应保持原键名（历史日志按它解析），实际 %v", snap["model_key"])
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
