package pricing

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"llm-relay/internal/model"
)

// seed 往 Engine 里塞一条价格，并让缓存处于「新鲜」状态。
//
// 为什么直接操作内部字段而不建 *gorm.DB：refresh 的判据是
// `time.Since(loadedAt) < ttl && loadedAt != 零值`（engine.go:225），
// 把 loadedAt 设成当前时刻后它立刻返回，整条 Resolve 路径不碰数据库 ——
// 为一条精度断言起一套 PG 不划算。
func seed(e *Engine, channelID uint, name string, cm model.ChannelModel, currency string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cache[cacheKey(channelID, name)] = cm
	e.currencies[channelID] = currency
	e.loadedAt = time.Now()
}

// 四位小数倍率（2026-09-24）：channel_models.multiplier 是 numeric(10,4)，
// 上游按折扣率报价时常见 0.1875 / 0.1234 这类四位值。
//
// 用 0.15 × 0.1875 = 0.028125 这个算式钉住，它同时覆盖两件事：
//   - 倍率从 float64 转 decimal 不能丢精度。0.1875 = 3/16，在二进制浮点里
//     恰好可表示，所以这个断言能证明链路是真的精确转换，而不是碰巧
//     （换成 0.1 这种不可精确表示的值就不具备这个性质）。
//   - 乘积不被截断成更少的小数位。
//
// 为什么值得单独一条测试：倍率精度出问题时**账面上看不出异常** ——
// 0.1875 变成 0.19 只会让金额多 1.3%，而日志里显示的倍率就是那个错的
// 0.19，回头核对「怎么看都对」。这条断言把它钉在具体数字上。
func TestFourDecimalMultiplierExact(t *testing.T) {
	e := NewEngine(nil, time.Minute)
	seed(e, 1, "m", model.ChannelModel{
		ChannelID:   1,
		PublicName:  "m",
		InputPer1M:  decimal.RequireFromString("0.15"),
		OutputPer1M: decimal.RequireFromString("0.6"),
		Multiplier:  0.1875,
	}, model.CurrencyUSD)

	p, ok := e.Resolve(context.Background(), 1, "m", time.Now())
	if !ok {
		t.Fatal("应能解析出价格")
	}
	if got := p.Multiplier.String(); got != "0.1875" {
		t.Errorf("倍率应为 0.1875，实际 %s", got)
	}
	if got := p.InputPer1M.String(); got != "0.028125" {
		t.Errorf("0.15 × 0.1875 应为 0.028125，实际 %s", got)
	}
	if got := p.OutputPer1M.String(); got != "0.1125" {
		t.Errorf("0.6 × 0.1875 应为 0.1125，实际 %s", got)
	}
	if p.Source != MultiplierSourceFixed {
		t.Errorf("倍率来源应为 fixed，实际 %s", p.Source)
	}
	// 快照里也要是四位：日志按它展示「这笔按 ×0.1875 算」，
	// 截断的话用户按日志复算会对不上
	if got := p.Snapshot(time.Now())["multiplier"]; got != "0.1875" {
		t.Errorf("定价快照的倍率应为 0.1875，实际 %v", got)
	}
}

// 四位倍率的边界：最小可表示值 0.0001（numeric(10,4) 的下限）
// 与四位都非零的 0.1234。
func TestFourDecimalMultiplierBounds(t *testing.T) {
	cases := []struct {
		mult      float64
		wantDec   string
		wantInput string // 0.15 × mult
	}{
		{0.0001, "0.0001", "0.000015"},
		{0.1234, "0.1234", "0.01851"},
		{1.2345, "1.2345", "0.185175"},
	}
	for _, c := range cases {
		e := NewEngine(nil, time.Minute)
		seed(e, 1, "m", model.ChannelModel{
			ChannelID:  1,
			PublicName: "m",
			InputPer1M: decimal.RequireFromString("0.15"),
			Multiplier: c.mult,
		}, model.CurrencyUSD)

		p, ok := e.Resolve(context.Background(), 1, "m", time.Now())
		if !ok {
			t.Fatalf("倍率 %v：应能解析出价格", c.mult)
		}
		if got := p.Multiplier.String(); got != c.wantDec {
			t.Errorf("倍率 %v 应为 %s，实际 %s", c.mult, c.wantDec, got)
		}
		if got := p.InputPer1M.String(); got != c.wantInput {
			t.Errorf("0.15 × %v 应为 %s，实际 %s", c.mult, c.wantInput, got)
		}
	}
}

// 四位倍率用在时段规则里同样要精确：MatchPeak 走的是 float64 → JSON，
// 这条确认那段转换不会把 0.1875 弄成 0.18749999…
func TestFourDecimalMultiplierInPeakRule(t *testing.T) {
	rules, err := NormalizeRules(mustRules(t, `[{"days":[],"start":"00:00","end":"23:59","multiplier":0.1875}]`))
	if err != nil {
		t.Fatalf("规则应合法: %v", err)
	}
	m, label, hit := MatchPeak(rules, time.Now())
	if !hit {
		t.Fatal("全天窗口应当命中")
	}
	if m != 0.1875 {
		t.Errorf("时段倍率应为 0.1875，实际 %v", m)
	}
	_ = label

	e := NewEngine(nil, time.Minute)
	seed(e, 1, "m", model.ChannelModel{
		ChannelID:  1,
		PublicName: "m",
		InputPer1M: decimal.RequireFromString("0.15"),
		PeakRules:  rules,
	}, model.CurrencyUSD)

	p, ok := e.Resolve(context.Background(), 1, "m", time.Now())
	if !ok {
		t.Fatal("应能解析出价格")
	}
	if got := p.Multiplier.String(); got != "0.1875" {
		t.Errorf("时段命中的倍率应为 0.1875，实际 %s", got)
	}
	if p.Source != MultiplierSourcePeak {
		t.Errorf("倍率来源应为 peak，实际 %s", p.Source)
	}
}
