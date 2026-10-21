package api

// /v1/models 的 supported_efforts：写入路径的规整 + 读出路径的合并形状。
//
// 读出侧（listModels）必须连库验证：它是一条手写 Select 的 JOIN 查询，
// 列名写错时 GORM 不报编译错，只有真正执行才暴露（2026-10-20 的实时日志
// 事故就是这一种）。用 LLMRELAY_TEST_DSN 门控，与其余 PG 集成测试同一套。

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

	"llm-relay/internal/model"
	"llm-relay/internal/store"
)

// 写入路径：没传 = 不动（nil），空数组 = 清掉回落推导，非法值 = 丢掉。
func TestNormalizeWhitelistSupportedEfforts(t *testing.T) {
	mk := func(efforts *model.StringList) []whitelistItem {
		return []whitelistItem{{PublicName: "m", Enabled: bptr(true), SupportedEfforts: efforts}}
	}
	empty := model.StringList{}
	declared := model.StringList{"Max", "low", "max", "ultra"}

	t.Run("没传时保持 nil（表示回落推导）", func(t *testing.T) {
		rows, err := normalizeWhitelist(mk(nil))
		if err != nil {
			t.Fatal(err)
		}
		if rows[0].SupportedEfforts != nil {
			t.Errorf("没传 supported_efforts 时应是 nil，实际 %v", rows[0].SupportedEfforts)
		}
	})
	t.Run("显式空数组归一成 nil", func(t *testing.T) {
		rows, err := normalizeWhitelist(mk(&empty))
		if err != nil {
			t.Fatal(err)
		}
		if rows[0].SupportedEfforts != nil {
			t.Errorf("空数组应归一成 nil，实际 %v", rows[0].SupportedEfforts)
		}
	})
	t.Run("大小写、重复、强度序与未知档位", func(t *testing.T) {
		rows, err := normalizeWhitelist(mk(&declared))
		if err != nil {
			t.Fatal(err)
		}
		got := []string(rows[0].SupportedEfforts)
		want := []string{"low", "max", "ultra"}
		if len(got) != len(want) {
			t.Fatalf("得到 %v，期望 %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("得到 %v，期望 %v", got, want)
			}
		}
	})
}

// probeEffortData 是本用例写进测试库的那组渠道/白名单，返回释放函数。
//
// 全部用 eff-probe- 前缀：测试库可能同时跑别的用例，清理时按前缀删干净。
func probeEffortData(t *testing.T, db *gorm.DB) {
	t.Helper()
	cleanup := func() {
		db.Exec(`DELETE FROM channel_models WHERE public_name LIKE 'eff-probe-%'`)
		db.Exec(`DELETE FROM channels WHERE name LIKE 'eff-probe-%'`)
		db.Exec(`DELETE FROM channel_groups WHERE name LIKE 'eff-probe-%'`)
	}
	cleanup()
	t.Cleanup(cleanup)

	grp := model.ChannelGroup{Name: "eff-probe-group", Enabled: true}
	if err := db.Create(&grp).Error; err != nil {
		t.Fatalf("建分组失败: %v", err)
	}
	off := model.ChannelGroup{Name: "eff-probe-group-off", Enabled: false}
	if err := db.Create(&off).Error; err != nil {
		t.Fatalf("建停用分组失败: %v", err)
	}

	var modelRows []model.ChannelModel
	// weight 是组内优先级序号，库上有 (group_id, weight) 唯一索引 ——
	// 同一个分组里逐条递增，别全写 1
	nextWeight := map[uint]int{}
	mkChannel := func(name, protocol string, groupID uint, enabled bool, bindings []model.ChannelModel) {
		nextWeight[groupID]++
		ch := model.Channel{
			Name: name, GroupID: groupID, Protocol: protocol,
			BaseURL: "http://127.0.0.1:9/v1", Enabled: enabled, Weight: nextWeight[groupID],
		}
		if err := db.Create(&ch).Error; err != nil {
			t.Fatalf("建渠道 %s 失败: %v", name, err)
		}
		for _, b := range bindings {
			b.ChannelID = ch.ID
			modelRows = append(modelRows, b)
		}
	}

	b := func(public string, enabled bool, efforts ...string) model.ChannelModel {
		row := model.ChannelModel{PublicName: public, UpstreamName: public, Enabled: enabled}
		if len(efforts) > 0 {
			row.SupportedEfforts = model.StringList(efforts)
		}
		return row
	}

	// 同名模型挂在两条不同协议的渠道上：档位要取并集
	mkChannel("eff-probe-anthropic", model.ProtocolAnthropic, grp.ID, true,
		[]model.ChannelModel{b("eff-probe-shared", true)})
	mkChannel("eff-probe-openai", model.ProtocolOpenAIChat, grp.ID, true,
		[]model.ChannelModel{
			b("eff-probe-shared", true),
			// 显式声明压在协议推导之上（openai-chat 推导只有五档）
			b("eff-probe-declared", true, "off", "max"),
			// 停用的行不出现在列表里
			b("eff-probe-disabled", false),
		})
	// 停用渠道、停用分组里的模型同理
	mkChannel("eff-probe-off-channel", model.ProtocolAnthropic, grp.ID, false,
		[]model.ChannelModel{b("eff-probe-off-channel-model", true)})
	mkChannel("eff-probe-off-group", model.ProtocolAnthropic, off.ID, true,
		[]model.ChannelModel{b("eff-probe-off-group-model", true)})
	// embeddings 渠道没有思考概念：声明为空数组（不是 null）
	mkChannel("eff-probe-embed", model.ProtocolEmbeddings, grp.ID, true,
		[]model.ChannelModel{b("eff-probe-embeddings", true)})

	if err := db.Create(&modelRows).Error; err != nil {
		t.Fatalf("写白名单失败: %v", err)
	}
}

// TestListModelsSupportedEfforts 验证 /v1/models 的返回形状：
// 每个模型带 supported_efforts、同名多渠道取并集、空能力回 [] 而不是 null。
func TestListModelsSupportedEfforts(t *testing.T) {
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
	if err := db.AutoMigrate(&model.ChannelGroup{}, &model.Channel{}, &model.ChannelModel{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	probeEffortData(t, db)

	// 起在同一个 Schema 上，但只挂这一个 handler —— 完整 Register 会拉进
	// 登录鉴权、密钥缓存等一串与本用例无关的依赖
	st, err := store.OpenByDSN(dsn)
	if err != nil {
		t.Fatalf("建 store 失败: %v", err)
	}
	s := &Server{deps: &Deps{Store: st}}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/models", s.listModels)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d，响应: %s", w.Code, w.Body.String())
	}

	// 直接看原始 JSON：null 与 [] 的差别只有在这一层才看得见
	var raw struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("响应不是合法 JSON: %v\n%s", err, w.Body.String())
	}
	byID := map[string]map[string]any{}
	for _, m := range raw.Data {
		byID[m["id"].(string)] = m
	}
	effortsOf := func(id string) []string {
		t.Helper()
		m, ok := byID[id]
		if !ok {
			t.Fatalf("列表里没有 %s：%s", id, w.Body.String())
		}
		vs, ok := m["supported_efforts"].([]any)
		if !ok {
			t.Fatalf("%s 的 supported_efforts 不是数组（null？）：%v", id, m["supported_efforts"])
		}
		out := make([]string, 0, len(vs))
		for _, v := range vs {
			out = append(out, v.(string))
		}
		return out
	}
	eq := func(id string, want []string) {
		t.Helper()
		got := effortsOf(id)
		if len(got) != len(want) {
			t.Fatalf("%s: 得到 %v，期望 %v", id, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: 得到 %v，期望 %v", id, got, want)
			}
		}
	}

	// anthropic（八档）∪ openai-chat（五档）= 八档
	eq("eff-probe-shared", []string{"off", "auto", "minimal", "low", "medium", "high", "xhigh", "max"})
	// 显式声明优先于协议推导
	eq("eff-probe-declared", []string{"off", "max"})
	// 没有思考概念的协议：空数组
	eq("eff-probe-embeddings", []string{})

	for _, hidden := range []string{"eff-probe-disabled", "eff-probe-off-channel-model", "eff-probe-off-group-model"} {
		if _, ok := byID[hidden]; ok {
			t.Errorf("%s 不该出现在列表里（停用行 / 停用渠道 / 停用分组）", hidden)
		}
	}
	// created / owned_by 这些 OpenAI 兼容字段必须还在：客户端靠它们解析
	if m := byID["eff-probe-shared"]; m["object"] != "model" || m["owned_by"] != "llm-relay" {
		t.Errorf("OpenAI 兼容字段缺失: %v", m)
	}
	if raw.Object != "list" {
		t.Errorf("顶层 object 应为 list，实际 %q", raw.Object)
	}
}
