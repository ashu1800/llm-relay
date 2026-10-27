package api

import (
	"context"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/pricing"
)

// 模型定价的 PG 集成测试（LLMRELAY_TEST_DSN 门控，与其余集成测试同一约定）。
//
// 为什么必须有真库测试：这条链路上有两个只有真实 SQL 才会暴露的坑 ——
//
//  1. **列别名映射**：`Select("id, currency")` 装进 `struct{ ChannelID uint }`
//     时 GORM 按「channel_id」找列，拿到的是零值。表现是币种键变成空串、
//     界面提示全错，而且不报错（实测踩过）。
//  2. **列名与实体标签不一致**：upsert 手写 SQL 里的列名一旦与
//     gorm tag 对不上，就是「保存请求 200、库里没变」—— 只能靠真库发现。
//
// 内存 fake 对这两类问题都是盲的。
func openPricingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("LLMRELAY_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 LLMRELAY_TEST_DSN，跳过 PG 集成测试")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("连测试库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Channel{}, &model.ChannelModel{}, &model.ModelPrice{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		// 只清本测试造的数据：库可能是共享的开发库
		db.Exec(`DELETE FROM channel_models WHERE public_name LIKE 'itest-%'`)
		db.Exec(`DELETE FROM model_prices WHERE model_name LIKE 'itest-%'`)
		db.Exec(`DELETE FROM channels WHERE name LIKE 'itest-%'`)
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// 币种映射必须按渠道 ID 对上号（GORM 列别名的坑）。
func TestChannelCurrencyMappingByAlias(t *testing.T) {
	db := openPricingTestDB(t)

	usd := model.Channel{Name: "itest-usd", BaseURL: "http://x", Currency: model.CurrencyUSD}
	cny := model.Channel{Name: "itest-cny", BaseURL: "http://y", Currency: model.CurrencyCNY}
	if err := db.Create(&usd).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cny).Error; err != nil {
		t.Fatal(err)
	}

	// 与 admin_model_prices.go 的 listModelPrices 完全同一条查询
	type chanCurrency struct {
		ChannelID uint
		Currency  string
	}
	var cc []chanCurrency
	if err := db.Model(&model.Channel{}).Select("id AS channel_id, currency").Find(&cc).Error; err != nil {
		t.Fatal(err)
	}
	got := map[uint]string{}
	for _, r := range cc {
		got[r.ChannelID] = r.Currency
	}
	if got[usd.ID] != model.CurrencyUSD {
		t.Errorf("渠道 %d 的币种应是 USD，实际 %q（ChannelID 可能没映射上，恒为 0）", usd.ID, got[usd.ID])
	}
	if got[cny.ID] != model.CurrencyCNY {
		t.Errorf("渠道 %d 的币种应是 CNY，实际 %q", cny.ID, got[cny.ID])
	}
}

// 写入 → 读出 → 计价：三个币种渠道取到各自那一档单价。
//
// 这条是「同一模型同时有美元价和人民币价」需求的端到端断言：
// 人民币渠道**必须**拿到人民币那一档，取错档的表现是
// 「人民币渠道按美元数字记账」，账面上完全看不出异常。
func TestPricingResolvesPerChannelCurrency(t *testing.T) {
	db := openPricingTestDB(t)

	usd := model.Channel{Name: "itest-usd", BaseURL: "http://x", Currency: model.CurrencyUSD, Enabled: true}
	cny := model.Channel{Name: "itest-cny", BaseURL: "http://y", Currency: model.CurrencyCNY, Enabled: true}
	if err := db.Create(&usd).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cny).Error; err != nil {
		t.Fatal(err)
	}

	const modelName = "itest-model"
	for _, chID := range []uint{usd.ID, cny.ID} {
		row := model.ChannelModel{
			ChannelID: chID, PublicName: modelName, UpstreamName: modelName, Enabled: true,
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 走生产用的那条 upsert（model.UpsertModelPrice）：列名写错会在这里暴露。
	// 以前这里手抄了一份「形状相同」的 SQL —— 手抄的那份若写错，测试自己先挂，
	// 反而验证不到生产路径。
	mp := model.ModelPrice{
		ModelName:     modelName,
		USDInputPer1M: mustDec("1"), USDOutputPer1M: mustDec("10"),
		RMBInputPer1M: mustDec("7"), RMBOutputPer1M: mustDec("70"),
	}
	if err := model.UpsertModelPrice(db, mp); err != nil {
		t.Fatalf("upsert 失败（列名可能与实体标签不一致）: %v", err)
	}

	// 计价引擎
	eng := pricing.NewEngine(db, time.Millisecond)
	now := time.Now()

	pUSD, ok := eng.Resolve(context.Background(), usd.ID, modelName, now)
	if !ok {
		t.Fatal("美元渠道应能解析出价格")
	}
	if !pUSD.Priced || pUSD.Currency != model.CurrencyUSD || pUSD.InputPer1M.String() != "1" {
		t.Errorf("美元渠道取价错误: priced=%v currency=%s input=%s", pUSD.Priced, pUSD.Currency, pUSD.InputPer1M)
	}

	pCNY, ok := eng.Resolve(context.Background(), cny.ID, modelName, now)
	if !ok {
		t.Fatal("人民币渠道应能解析出价格")
	}
	if !pCNY.Priced || pCNY.Currency != model.CurrencyCNY || pCNY.InputPer1M.String() != "7" {
		t.Errorf("人民币渠道取价错误（应取人民币那一档）: priced=%v currency=%s input=%s",
			pCNY.Priced, pCNY.Currency, pCNY.InputPer1M)
	}
}

// 倍率仍来自渠道：同一模型在两条渠道上单价相同、倍率不同，金额要能分开。
func TestChannelMultiplierStillApplies(t *testing.T) {
	db := openPricingTestDB(t)

	ch := model.Channel{Name: "itest-usd", BaseURL: "http://x", Currency: model.CurrencyUSD, Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	const modelName = "itest-mult"
	if err := db.Create(&model.ChannelModel{
		ChannelID: ch.ID, PublicName: modelName, UpstreamName: modelName, Enabled: true, Multiplier: 2,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ModelPrice{
		ModelName: modelName, USDInputPer1M: mustDec("1"), USDOutputPer1M: mustDec("2"),
	}).Error; err != nil {
		t.Fatal(err)
	}

	eng := pricing.NewEngine(db, time.Millisecond)
	p, ok := eng.Resolve(context.Background(), ch.ID, modelName, time.Now())
	if !ok {
		t.Fatal("应能解析出价格")
	}
	// 1 × 2 = 2：倍率生效
	if p.InputPer1M.String() != "2" {
		t.Errorf("渠道倍率 ×2 应从单价 1 得到 2，实际 %s", p.InputPer1M)
	}
	if p.Source != pricing.MultiplierSourceFixed {
		t.Errorf("倍率来源应为 fixed，实际 %s", p.Source)
	}
}
