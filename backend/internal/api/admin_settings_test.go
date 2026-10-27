package api

// 系统设置接口的 PG 集成测试（LLMRELAY_TEST_DSN 门控，与其余集成测试同一约定）。
//
// 这条用例专门钉一个已经上线过的 500：getSettings 里的原生 SQL 曾经统计
// channel_models 上四档单价非 0 的行数，而 2026-10-27 把单价迁到 model_prices
// 时那四列被 DROP 掉了。全新库（AutoMigrate 建出来的表根本没有这些列）与
// 升级后的库都会直接报 `column "input_per1_m" does not exist`，
// 整个「系统设置」页打不开 —— 而当时一条测试都没有覆盖过这个接口。
//
// 为什么必须是真库：这条 SQL 有六条子查询，唯一会坏的地方是「列/表名不存在」，
// 内存 fake 与编译期都不会发现。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"llm-relay/internal/config"
	"llm-relay/internal/model"
	"llm-relay/internal/store"
)

func TestGetSettingsSucceedsOnCurrentSchema(t *testing.T) {
	dsn := os.Getenv("LLMRELAY_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 LLMRELAY_TEST_DSN，跳过 PG 集成测试")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("连测试库失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	// 按实体定义建表：channel_models 因此**没有**那四个单价列，
	// 与线上升级后的库形状一致（这正是本用例要复现的前提）。
	if err := db.AutoMigrate(
		&model.Channel{}, &model.ChannelModel{}, &model.ModelPrice{},
		&model.APIKey{}, &model.RequestLog{}, &model.RequestPayload{},
	); err != nil {
		t.Fatalf("建表失败: %v", err)
	}

	// 造一条已定价的模型：只断言「能跑通」还不够，priced 的口径也要对上
	price := model.ModelPrice{ModelName: "itest-settings-priced"}
	if err := db.Create(&price).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM model_prices WHERE model_name LIKE 'itest-settings-%'`)
	})

	st, err := store.OpenByDSN(dsn)
	if err != nil {
		t.Fatalf("建 store 失败: %v", err)
	}

	s := &Server{deps: &Deps{Store: st, Config: &config.Config{}}}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerSettingsRoutes(r.Group("/api/admin"), s)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/settings: code=%d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Counts struct {
			Priced int64 `json:"priced"`
			Models int64 `json:"models"`
		} `json:"counts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// 口径是「model_prices 的行数」，所以至少包含刚造的那一条
	if resp.Counts.Priced < 1 {
		t.Fatalf("priced 至少应为 1（model_prices 里有一条 itest 行），实际 %d", resp.Counts.Priced)
	}
}
