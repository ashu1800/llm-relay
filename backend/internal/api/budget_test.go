package api

import "testing"

// 预算归一化的纯逻辑测试（真正的超支判定/提醒去重在 liveStatsLoop 的
// 同一拍里，依赖库与时间，集成场景由看板实测覆盖）。

// 大小写归一、丢弃非法条目（非数字、负数、0、空币种）。
func TestNormalizeBudgetAmounts(t *testing.T) {
	got := normalizeBudgetAmounts(map[string]any{
		"cny":  50.0,  // 小写要归一成 CNY
		"USD":  10.0,
		"usd2": "abc", // 非数字：丢弃
		"JPY":  -1.0,  // 负数：丢弃
		"EUR":  0.0,   // 零 = 没设：丢弃
		"":     5.0,   // 空币种：丢弃
	})
	if len(got) != 2 {
		t.Fatalf("应只保留 CNY/USD 两条，实际 %v", got)
	}
	if got["CNY"] != 50 || got["USD"] != 10 {
		t.Fatalf("归一结果不对：%v", got)
	}
}

// 提交归一：空对象 = 清除预算（返回 nil,nil）；负数是错误而不是静默丢弃 ——
// 「清除」与「填错」必须分得开，静默丢弃会让用户以为清掉了其实还在生效。
func TestNormalizeDailyBudget(t *testing.T) {
	// 空对象 = 清除
	if out, err := normalizeDailyBudget(map[string]float64{}); out != nil || err != nil {
		t.Fatalf("空对象应返回 nil,nil，实际 %v, %v", out, err)
	}
	// 负数 = 明确报错
	if _, err := normalizeDailyBudget(map[string]float64{"CNY": -5}); err == nil {
		t.Fatal("负数预算应报错而不是静默丢弃")
	}
	// 正常：小写归一、零值滤掉、全零回落成清除
	out, err := normalizeDailyBudget(map[string]float64{"cny": 50, "usd": 0})
	if err != nil {
		t.Fatalf("正常提交不应报错：%v", err)
	}
	if out["CNY"] != 50.0 || len(out) != 1 {
		t.Fatalf("归一结果不对：%v", out)
	}
}
