package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"llm-relay/internal/config"
	"llm-relay/internal/model"
	"llm-relay/internal/secure"
	"llm-relay/internal/store"
)

// TestUpdateKeyModelChannelsRoundTrip 复现并钉住「渠道顺序保存不了」：
// 编辑密钥时整份提交 model_channels（顺序换过的候选列表），服务端要把
// 新顺序写进 jsonb，GET 列表按**新序**回显。
//
// 为什么值得一条端到端用例：这条链路上有两处会静默吞顺序 ——
//  1. normalizeKeyRoutes 的去重保序逻辑把顺序弄乱（写侧）；
//  2. GORM Updates(map) 对 driver.Valuer 的处理把 jsonb 写坏（DB 侧）。
//
// 两处都不会报错，只回显时才能看见。
func TestUpdateKeyModelChannelsRoundTrip(t *testing.T) {
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
		if sqlDB, _ := db.DB(); sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&model.Channel{}, &model.ChannelModel{}, &model.APIKey{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}

	chA := model.Channel{Name: "rt-ch-a", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:1", Enabled: true}
	chB := model.Channel{Name: "rt-ch-b", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:1", Enabled: true}
	if err := db.Create(&chA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&chB).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Delete(&chA)
		db.Delete(&chB)
	})
	for _, ch := range []model.Channel{chA, chB} {
		row := model.ChannelModel{ChannelID: ch.ID, PublicName: "rt-model", UpstreamName: "rt-model", Enabled: true}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	st, err := store.OpenByDSN(dsn)
	if err != nil {
		t.Fatalf("建 store 失败: %v", err)
	}
	// createKey 要加密明文、写后要失效密钥缓存：补齐这两个依赖
	cipher, err := secure.NewCipher(strings.Repeat("a", 32))
	if err != nil {
		t.Fatalf("建 cipher 失败: %v", err)
	}
	s := &Server{deps: &Deps{Store: st, Cipher: cipher}, keys: newKeyCache(), lastTouch: map[uint]time.Time{}}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/admin")
	registerKeyRoutes(g, s)

	do := func(method, path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 1) 创建：顺序 B 先 A 后
	w := do(http.MethodPost, "/api/admin/keys", map[string]any{
		"name":           "rt-key",
		"allowed_models": []string{"rt-model"},
		"model_channels": map[string]any{"rt-model": []uint{chB.ID, chA.ID}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("create: code=%d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	idPath := strconv.FormatUint(uint64(created.ID), 10)
	t.Cleanup(func() {
		do(http.MethodDelete, "/api/admin/keys/"+idPath, nil)
	})

	// 2) 编辑：换成 A 先 B 后（用户报障的操作）
	w2 := do(http.MethodPut, "/api/admin/keys/"+idPath, map[string]any{
		"name":           "rt-key",
		"allowed_models": []string{"rt-model"},
		"model_channels": map[string]any{"rt-model": []uint{chA.ID, chB.ID}},
		"enabled":        true,
	})
	if w2.Code != http.StatusOK {
		t.Fatalf("update: code=%d body=%s", w2.Code, w2.Body.String())
	}

	// 3) 列表回读：断言顺序是 A 先 B 后
	w3 := do(http.MethodGet, "/api/admin/keys", nil)
	if w3.Code != http.StatusOK {
		t.Fatalf("list: code=%d", w3.Code)
	}
	var list struct {
		Items []struct {
			ID            uint              `json:"id"`
			ModelChannels map[string][]uint `json:"model_channels"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w3.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, k := range list.Items {
		if k.ID != created.ID {
			continue
		}
		got := k.ModelChannels["rt-model"]
		if len(got) != 2 {
			t.Fatalf("routes: %#v", k.ModelChannels)
		}
		if got[0] != chA.ID || got[1] != chB.ID {
			t.Fatalf("顺序没保存: want [%d %d], got %v", chA.ID, chB.ID, got)
		}
		return
	}
	t.Fatalf("列表里没找到密钥 #%d", created.ID)
}

// TestRelayRequestBlocksEmptyRoute 钉住「空路由列表 ≠ 不限制」：
// 密钥白名单里有模型、但 model_channels 里该模型的列表为空/缺失时，
// 转发入口要在网关侧明确拒绝（锚定 502 + 指路文案），而不是把空列表
// 交给转发层变成「不按密钥限制」—— 那会把候选放宽到全库能跑该模型的
// 渠道（2026-10-26 评审 C2：备份导入置空条目、手工改库是现实触发面）。
func TestRelayRequestBlocksEmptyRoute(t *testing.T) {
	dsn := os.Getenv("LLMRELAY_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 LLMRELAY_TEST_DSN，跳过 PG 集成测试")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连测试库失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, _ := db.DB(); sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&model.APIKey{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}

	// 白名单有模型、路由列表空（备份导入置空的形状）。路由键与白名单对齐、
	// 列表为空 —— 需要绕过 normalizeKeyRoutes 直接落库，这正是该用例的覆盖面
	k := model.APIKey{
		Name: "empty-route-probe", KeyHash: "empty-route-hash", KeyEnc: "x",
		AllowedModels: model.StringList{"dead-model"},
		ModelRoutes:   model.JSONMap{"dead-model": []uint{}},
	}
	if err := db.Create(&k).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Delete(&k) })

	st, err := store.OpenByDSN(dsn)
	if err != nil {
		t.Fatalf("建 store 失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, _ := st.DB().DB(); sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	// Config 只为 relayRequest 开头的报文上限读字段；守卫在并发闸门之前触发，
	// Gate/Service 不会被触及
	cfg := &config.Config{}
	cfg.Relay.MaxRequestBodyMB = 8
	s := &Server{deps: &Deps{Store: st, Config: cfg}}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/chat/completions", func(c *gin.Context) {
		// 守卫只读 AllowedModels / ModelRoutes 两个字段，直接用库里那行即可
		c.Set(ctxAPIKey, &k)
		s.relayRequest(c, profileOpenAIChat, "", false)
	})

	body := `{"model":"dead-model","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("空路由应 502，实际 %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "没有可用的候选渠道") {
		t.Errorf("报错应指路密钥路由，实际 %s", w.Body.String())
	}
}
