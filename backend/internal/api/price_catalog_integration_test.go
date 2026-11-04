package api

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/pricing"
)

// 参考价库与非 token 计价维度的 PG 集成测试（LLMRELAY_TEST_DSN 门控）。
//
// 真库才能暴露的坑与 model_prices_integration_test.go 同源：
// upsert 手写 SQL 的列名/占位符数与实体标签不一致（2026-11 P1 新增了
// 6 个维度列，占位符改为按列数生成 —— 这层改动本身就该有真库断言兜底），
// 以及 settings 行的 Create/Save 分支在真实主键约束下的行为。

func openCatalogTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("LLMRELAY_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 LLMRELAY_TEST_DSN，跳过 PG 集成测试")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("连测试库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Channel{}, &model.ChannelModel{}, &model.ModelPrice{}, &model.Setting{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM channel_models WHERE public_name LIKE 'itest-%'`)
		db.Exec(`DELETE FROM model_prices WHERE model_name LIKE 'itest-%'`)
		db.Exec(`DELETE FROM channels WHERE name LIKE 'itest-%'`)
		db.Exec(`DELETE FROM settings WHERE key = ?`, pricing.CatalogSettingKey)
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// catalogFixture 构造一份最小「上游原文件」，两条目各带不同维度价格。
func catalogFixture(t *testing.T) []byte {
	t.Helper()
	raw := map[string]any{
		"itest-image-model": map[string]any{
			"mode":                  "image_generation",
			"litellm_provider":      "openai",
			"output_cost_per_image": 0.04,
		},
		"itest-chat-model": map[string]any{
			"mode":                 "chat",
			"litellm_provider":     "openai",
			"input_cost_per_token": 2e-06,
			"max_input_tokens":     4096,
		},
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("构造 fixture 失败: %v", err)
	}
	return b
}

// 维度列的 upsert → 读回 → 计价端到端：
// 只配图像价的模型必须被 Resolve 判成「有价」—— 这是 P1 最关键的一条
// 行为变化（此前 IsZero 只看 token 四档，图像价会被当成漏配记 0）。
func TestDimensionPriceRoundtripAndResolve(t *testing.T) {
	db := openCatalogTestDB(t)

	ch := model.Channel{Name: "itest-usd", BaseURL: "http://x", Currency: model.CurrencyUSD, Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	const name = "itest-image-model"
	if err := db.Create(&model.ChannelModel{
		ChannelID: ch.ID, PublicName: name, UpstreamName: name, Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	mp := model.ModelPrice{
		ModelName:       name,
		USDPerImage:     mustDec("0.04"),
		USDPerSecond:    mustDec("0.0001"),
		USDPerCharacter: mustDec("0.000015"),
		RMBPerImage:     mustDec("0.30"),
	}
	if err := model.UpsertModelPrice(db, mp); err != nil {
		t.Fatalf("含维度列的 upsert 失败（占位符数可能与列清单不一致）: %v", err)
	}

	// 覆盖一遍：维度列走 ON CONFLICT DO UPDATE 也要落对
	mp2 := mp
	mp2.USDPerImage = mustDec("0.08")
	if err := model.UpsertModelPrice(db, mp2); err != nil {
		t.Fatalf("覆盖 upsert 失败: %v", err)
	}

	var back model.ModelPrice
	if err := db.Where("model_name = ?", name).First(&back).Error; err != nil {
		t.Fatal(err)
	}
	if back.USDPerImage.String() != "0.08" || back.USDPerSecond.String() != "0.0001" {
		t.Errorf("维度列读回错误: per_image=%s per_second=%s", back.USDPerImage, back.USDPerSecond)
	}
	// 用 Equal 而不是字符串比较：decimal 会规范化掉尾零（0.30 → "0.3"）
	if !back.RMBPerImage.Equal(mustDec("0.30")) {
		t.Errorf("人民币维度列读回错误: %s", back.RMBPerImage)
	}

	eng := pricing.NewEngine(db, time.Millisecond)
	p, ok := eng.Resolve(context.Background(), ch.ID, name, time.Now())
	if !ok {
		t.Fatal("只配图像价的模型应能解析出价格")
	}
	if !p.Priced {
		t.Error("只配图像价不应被判为漏配（Priced=false 会让图像账记 0）")
	}
	if !p.PerImage.Equal(mustDec("0.08")) {
		t.Errorf("Resolve 的 PerImage 错误: %s", p.PerImage)
	}
	if !p.CostImages(2).Equal(mustDec("0.16")) {
		t.Errorf("CostImages(2) = %s，期望 0.16", p.CostImages(2))
	}
}

// 参考价库的持久化闭环：ReplaceCatalog 落库 → 新的 CatalogStore 从
// settings 读回同一份 → 再 Replace 一次验证覆盖（不走 Create 分支）。
func TestCatalogStorePersistsInSettings(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()

	s1 := pricing.NewCatalogStore(db)
	fixture := catalogFixture(t)
	first, err := s1.ReplaceCatalog(ctx, fixture, "2026-11-04T10:00:00Z", "https://example/first.json")
	if err != nil {
		t.Fatalf("首次 ReplaceCatalog 失败: %v", err)
	}
	if len(first.Entries) != 2 {
		t.Fatalf("fixture 条目数异常: %d", len(first.Entries))
	}

	// 全新的 store 实例（模拟进程重启后）必须从 DB 读到同一份
	s2 := pricing.NewCatalogStore(db)
	cat, err := s2.Get(ctx)
	if err != nil {
		t.Fatalf("从 DB 读回目录失败: %v", err)
	}
	if len(cat.Entries) != 2 {
		t.Errorf("DB 副本条目数错误: %d", len(cat.Entries))
	}
	if e, ok := cat.Entries["itest-chat-model"]; !ok || e.InputPer1M != 2 {
		t.Errorf("DB 副本条目内容错误: %+v", e)
	}
	st, err := s2.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Builtin || st.FetchedAt != "2026-11-04T10:00:00Z" {
		t.Errorf("Status 应指向 DB 副本: %+v", st)
	}
	if hit, ok := cat.MatchBest("itest-image-model"); !ok || hit.Key != "itest-image-model" {
		t.Errorf("MatchBest 应命中 DB 副本里的条目: %v %v", hit, ok)
	}

	// 覆盖写入：同一 settings 行被更新而不是主键冲突
	if _, err := s2.ReplaceCatalog(ctx, fixture, "2026-11-04T11:00:00Z", "https://example/second.json"); err != nil {
		t.Fatalf("第二次 ReplaceCatalog 失败（可能主键冲突走了错误分支）: %v", err)
	}
	st2, _ := s2.Status(ctx)
	if st2.FetchedAt != "2026-11-04T11:00:00Z" {
		t.Errorf("覆盖后的抓取时间应为新值: %+v", st2)
	}
}
