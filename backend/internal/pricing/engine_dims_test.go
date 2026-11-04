package pricing

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"llm-relay/internal/model"
	"llm-relay/internal/relay"
)

// dimsPrice 构造一个七档单价齐全、倍率 2 的 Price，供维度计费断言。
func dimsPrice() Price {
	two := decimal.NewFromInt(2)
	return Price{
		ChannelID:    7,
		ModelName:    "dims-model",
		Currency:     model.CurrencyUSD,
		InputPer1M:   decimal.NewFromFloat(10).Mul(two),
		OutputPer1M:  decimal.NewFromFloat(20).Mul(two),
		PerImage:     decimal.NewFromFloat(0.04).Mul(two),
		PerSecond:    decimal.NewFromFloat(0.0001).Mul(two),
		PerCharacter: decimal.NewFromFloat(0.000015).Mul(two),
		Multiplier:   two,
		Source:       MultiplierSourceFixed,
		Base:         model.ChannelModel{Multiplier: 2},
		Priced:       true,
	}
}

// 倍率必须同样作用在非 token 维度上：渠道倍率表达「这条渠道比标价贵/便宜
// 多少」，图像模型也逃不出这个逻辑 —— 漏乘的后果是图像账面永远按原价。
func TestCostDimensionAppliesMultiplier(t *testing.T) {
	p := dimsPrice()

	if got := p.CostImages(10); !got.Equal(decimal.NewFromFloat(0.8)) {
		t.Errorf("CostImages(10) = %s，期望 0.8（0.04×2×10）", got)
	}
	if got := p.CostCharacters(1000000); !got.Equal(decimal.NewFromFloat(30)) {
		t.Errorf("CostCharacters(1e6) = %s，期望 30（0.000015×2×1e6）", got)
	}
	if got := p.CostAudioSeconds(decimal.NewFromInt(60)); !got.Equal(decimal.NewFromFloat(0.012)) {
		t.Errorf("CostAudioSeconds(60) = %s，期望 0.012（0.0001×2×60）", got)
	}
}

// 边界：零张 / 零秒 / 未配价维度一律 0，不产出负数或 NaN。
func TestCostDimensionEdgeCases(t *testing.T) {
	p := dimsPrice()
	if !p.CostImages(0).IsZero() || !p.CostImages(-3).IsZero() {
		t.Error("张数 <= 0 应记 0")
	}
	if !p.CostAudioSeconds(decimal.Zero).IsZero() || !p.CostAudioSeconds(decimal.NewFromInt(-1)).IsZero() {
		t.Error("秒数 <= 0 应记 0")
	}

	empty := Price{}
	if !empty.CostImages(5).IsZero() || !empty.CostCharacters(5).IsZero() {
		t.Error("未配价的维度应记 0")
	}
}

// token 口径的 Cost 不受非 token 维度影响（对话端点不碰它们）。
func TestCostTokensUnaffectedByDims(t *testing.T) {
	p := dimsPrice()
	u := relay.Usage{PromptTokens: 1000, CompletionTokens: 500}
	// 10×2×1000/1e6 + 20×2×500/1e6 = 0.02 + 0.02 = 0.04
	if got := p.Cost(u); !got.Equal(decimal.NewFromFloat(0.04)) {
		t.Errorf("Cost = %s，期望 0.04", got)
	}
}

// PriceSet.IsZero 现在包含非 token 维度：只配了图像价的模型必须算「有价」，
// 否则引擎 Resolve 会把它当漏配（Priced=false），图像账全部记 0。
func TestPriceSetIsZeroIncludesDims(t *testing.T) {
	tokensOnly := model.PriceSet{
		InputPer1M:  decimal.NewFromFloat(2.5),
		OutputPer1M: decimal.NewFromFloat(10),
	}
	if tokensOnly.IsZero() {
		t.Error("token 档有价不应判零")
	}

	imageOnly := model.PriceSet{PerImage: decimal.NewFromFloat(0.04)}
	if imageOnly.IsZero() {
		t.Error("只配图像价不应判零 —— 图像端点靠它计费")
	}

	if zero := (model.PriceSet{}); !zero.IsZero() {
		t.Error("全零应判零")
	}
}

// ModelPrice.PriceFor 必须把非 token 维度按币种各归各档：
// 错档的表现与 token 价一样隐蔽 —— 人民币渠道按美元档记账，金额对、币种错。
func TestPriceForIncludesDims(t *testing.T) {
	mp := model.ModelPrice{
		USDPerImage: decimal.NewFromFloat(0.04),
		RMBPerImage: decimal.NewFromFloat(0.30),
	}
	usd := mp.PriceFor(model.CurrencyUSD)
	if !usd.PerImage.Equal(decimal.NewFromFloat(0.04)) || !usd.PerSecond.IsZero() {
		t.Errorf("美元档 PerImage/PerSecond 错误: %v / %v", usd.PerImage, usd.PerSecond)
	}
	cny := mp.PriceFor(model.CurrencyCNY)
	if !cny.PerImage.Equal(decimal.NewFromFloat(0.30)) {
		t.Errorf("人民币档 PerImage 错误: %v", cny.PerImage)
	}
}

// 快照必须携带 billing_kind 与维度单价：事后核对「这笔按什么口径算的」
// 不能依赖猜测，价格改了之后历史账目也不能跟着漂。
func TestSnapshotCarriesKindAndDims(t *testing.T) {
	p := dimsPrice()
	p.Kind = BillingKindImage
	snap := p.Snapshot(time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))
	if snap["billing_kind"] != BillingKindImage {
		t.Errorf("billing_kind = %v，期望 image", snap["billing_kind"])
	}
	if snap["per_image"] != "0.08" {
		t.Errorf("per_image = %v，期望 0.08（0.04×2）", snap["per_image"])
	}

	// Kind 未设置时按 token 记 —— 空串不是合法口径词表成员
	plain := dimsPrice()
	plain.Kind = ""
	if snap := plain.Snapshot(time.Now()); snap["billing_kind"] != BillingKindToken {
		t.Errorf("缺省 billing_kind = %v，期望 token", snap["billing_kind"])
	}
}
