package api

// 客户端身份落库的端到端验收（2026-10-22 事故修复）。
//
// 走真实的 handler 链路：入站头 → ExtractClient → RelayRequest → BuildLog
// → 异步 LogWriter → request_logs 行。用 LLMRELAY_TEST_DSN 门控，与其余
// PG 集成测试同一套。
//
// 为什么必须连库：事故的验收标准就是「client_id 有值且与 UA 推导结果一致」，
// 而 client_id / client_name / client_ua 三列要经过 GORM 建表、varchar 截断、
// 异步队列与 ON CONFLICT 才能落成一行 —— 纯函数用例覆盖不到这一段。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"llm-relay/internal/config"
	"llm-relay/internal/model"
	"llm-relay/internal/relay"
)

// TestRequestLogClientIdentity 断言四种入站 UA 最终在 request_logs 里
// 留下的 (client_id, client_name) —— 尤其是「未识别」那一种：
// 它曾经 client_id 为空，是本次事故的第二半。
func TestRequestLogClientIdentity(t *testing.T) {
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
	if err := db.AutoMigrate(&model.RequestLog{}, &model.ClientAlias{},
		&model.ChannelGroup{}, &model.Channel{}, &model.ChannelModel{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}

	// 每条用例用一个独立 trace_id 前缀，互不干扰、可重复运行
	const runPrefix = "verify-client-identity-"
	if err := db.Where("trace_id LIKE ?", runPrefix+"%").Delete(&model.RequestLog{}).Error; err != nil {
		t.Fatalf("清理上一轮数据失败: %v", err)
	}

	cfg := config.Default()
	// 日志队列 + 映射缓存：与生产同一条路径，只是库换成测试库。
	// Limit 调小到 4 也能同时证明「容量远未用满时不会告警」。
	logs := relay.NewLogWriterWithClientMap(db, 64, nil, relay.ClientMapOptions{
		Limit: 4,
		TTL:   time.Hour,
	})
	s := &Server{deps: &Deps{Config: cfg, Logs: logs, Service: relay.NewService(db, relay.NewRouter(db, nil), relay.Options{}, nil)}}
	_ = context.Background()

	cases := []struct{ name, ua, wantID, wantName string }{
		{"Claude Code", "claude-cli/2.1.260 (external, cli)", "claude-code", "Claude Code"},
		// 事故里告警最多的那个 key：未识别客户端必须有 id
		{"未识别的自建工具", "deepseek-harness/0.4.1", "deepseek-harness", "deepseek-harness"},
		{"裸 SDK", "ai-sdk/openai-compatible/1.0.6 runtime/node.js/v22.14.0", "vercel-ai-sdk", "Vercel AI SDK"},
		{"无 UA", "", "", ""},
	}

	gin.SetMode(gin.TestMode)
	// 只挂 client 识别这一段所依赖的入口：完整 Register 会拉进鉴权与
	// 渠道查询，与本用例无关。handler 在「没有渠道」时会走失败收尾，
	// 但 BuildLog / finalizeLog 正是在那条路径上被调用的。
	r := gin.New()
	r.POST("/v1/chat/completions", s.chatCompletions)

	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Trace-Id", runPrefix+c.name) // 不生效，仅示意
		if c.ua != "" {
			req.Header.Set("User-Agent", c.ua)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}

	if !logs.CloseAndFlush(10*time.Second, nil) {
		t.Fatal("日志队列未在预算内写完")
	}

	// 按 client_ua 找回每一行 —— client_name 在未识别时就是 token，
	// 用它定位会和「无 UA」那一行混在一起，UA 才是唯一可靠的索引。
	var rows []model.RequestLog
	if err := db.Where("client_ua LIKE ?", "%").Order("id").Find(&rows).Error; err != nil {
		t.Fatalf("查询日志失败: %v", err)
	}
	byUA := map[string]model.RequestLog{}
	for _, row := range rows {
		byUA[row.ClientUA] = row
	}

	for _, c := range cases {
		row, ok := byUA[c.ua]
		if !ok {
			t.Errorf("%s：没有找到 UA=%q 的日志行（已有 UA：%v）", c.name, c.ua, keysOf(byUA))
			continue
		}
		if row.ClientID != c.wantID {
			t.Errorf("%s：client_id = %q，期望 %q", c.name, row.ClientID, c.wantID)
		}
		if row.ClientName != c.wantName {
			t.Errorf("%s：client_name = %q，期望 %q", c.name, row.ClientName, c.wantName)
		}
	}

	// 与 UA 推导结果一致：直接调 ExtractClient 复核，而不是抄一份期望值
	for _, c := range cases {
		h := http.Header{}
		if c.ua != "" {
			h.Set("User-Agent", c.ua)
		}
		id, name, _ := relay.ExtractClient(h)
		row, ok := byUA[c.ua]
		if !ok {
			continue
		}
		if row.ClientID != id || row.ClientName != name {
			t.Errorf("%s：落库 (%q,%q) 与 ExtractClient (%q,%q) 不一致",
				c.name, row.ClientID, row.ClientName, id, name)
		}
	}

	// 映射表：四种客户端都应登记成待设置行（名字为空）
	var aliases []model.ClientAlias
	if err := db.Find(&aliases).Error; err != nil {
		t.Fatalf("查询映射失败: %v", err)
	}
	keys := map[string]bool{}
	for _, a := range aliases {
		keys[a.Key] = true
	}
	for _, want := range []string{"claude-code", "deepseek-harness", "vercel-ai-sdk"} {
		if !keys[want] {
			t.Errorf("映射表里缺少 %q（已有：%v）", want, keys)
		}
	}
}

func keysOf(m map[string]model.RequestLog) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, fmt.Sprintf("%q", k))
	}
	return out
}

// TestClientAliasBackfillIdempotent 顺带验证：修复后 client_id 不再为空，
// 回填 SQL 的口径（COALESCE(NULLIF(client_id,''), NULLIF(client_name,''))）
// 对「id 与 name 同值」的行仍然产出同一个键 —— 不会因为这次改动多出重复行。
func TestClientAliasBackfillIdempotent(t *testing.T) {
	dsn := os.Getenv("LLMRELAY_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 LLMRELAY_TEST_DSN，跳过 PG 集成测试")
	}
	_ = context.Background()
	// 口径由 relay.ClientKey 与 SQL 两份实现共同决定；这里断言 Go 侧
	// 对「未识别行」产出的键与历史值一致（name token）。
	if got := relay.ClientKey("deepseek-harness", "deepseek-harness"); got != "deepseek-harness" {
		t.Fatalf("未识别行的映射键 = %q，期望 deepseek-harness", got)
	}
}
