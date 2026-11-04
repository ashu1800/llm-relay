package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"llm-relay/internal/config"
	"llm-relay/internal/model"
	"llm-relay/internal/relay"
	relaycache "llm-relay/internal/relay/cache"
)

// 响应缓存的端到端验收（2026-11 P3，LLMRELAY_TEST_DSN 门控）。
//
// 断言的核心不变量：
//   相同请求打两次，上游只被调用一次；第二次响应走重放路径，日志标 cache_hit。
//   外加三条边界：密钥开关关闭时不缓存、流式形态单独成键且可重放、
//   跨协议命中（openai-chat 存的缓存被 anthropic 入站重放成 anthropic 形状）。
//
// 为什么必须连库：命中语义的最后一环（BuildLog → CacheHit 列 → 异步落库）
// 是 GORM 行为；密钥路由（model_channels）也在库里的白名单行上解析。

type cacheTestEnv struct {
	db     *gorm.DB
	server *Server
	cache  *relaycache.Cache
}

func newCacheTestEnv(t *testing.T, cacheEnabled bool) *cacheTestEnv {
	t.Helper()
	dsn := os.Getenv("LLMRELAY_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 LLMRELAY_TEST_DSN，跳过 PG 集成测试")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连测试库失败: %v", err)
	}
	// api_keys.cache_enabled 与生产启动同款补列（store.migrateLegacySchema）：
	// 实体无 default，AutoMigrate 给已有数据的表加 NOT NULL 列会被 PG 拒绝
	//（CLAUDE.md 规则 12）—— 必须先手工补列，再让 AutoMigrate 做其余的事。
	if err := db.Exec(`ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS cache_enabled boolean NOT NULL DEFAULT false`).Error; err != nil {
		t.Fatalf("补 cache_enabled 列失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Channel{}, &model.ChannelModel{}, &model.APIKey{},
		&model.RequestLog{}, &model.RequestPayload{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM request_payloads WHERE log_id IN (SELECT id FROM request_logs WHERE model_requested LIKE 'itest-c%')`)
		db.Exec(`DELETE FROM request_logs WHERE model_requested LIKE 'itest-c%'`)
		db.Exec(`DELETE FROM channel_models WHERE public_name LIKE 'itest-c%'`)
		db.Exec(`DELETE FROM api_keys WHERE name LIKE 'itest-c%'`)
		db.Exec(`DELETE FROM channels WHERE name LIKE 'itest-c%'`)
		if sqlDB, _ := db.DB(); sqlDB != nil {
			_ = sqlDB.Close()
		}
	})

	cfg := config.Default()
	cfg.Relay.Cache.Enabled = cacheEnabled
	c := relaycache.New(cfg.Relay.Cache.TTL, cfg.Relay.Cache.MaxEntries, int64(cfg.Relay.Cache.MaxTotalMB)<<20)
	logs := relay.NewLogWriterWithClientMap(db, 64, nil, relay.ClientMapOptions{Limit: 4, TTL: time.Hour})
	return &cacheTestEnv{
		db:    db,
		cache: c,
		server: &Server{deps: &Deps{
			Config:  cfg,
			Cache:   c,
			Logs:    logs,
			Service: relay.NewService(db, relay.NewRouter(db, nil), relay.Options{}, nil),
		}},
	}
}

// withCacheKey 注入密钥上下文：缓存按密钥开启，handler 从 ctx 取密钥。
func withCacheKey(key *model.APIKey, h gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(ctxAPIKey, key)
		h(c)
	}
}

// fixtureCacheChannelAndKey 建渠道（白名单 public → upstream）与一把
// 开启缓存的密钥（allowed + 路由都指向该模型）。
func fixtureCacheChannelAndKey(t *testing.T, db *gorm.DB, upstreamURL string, keyCacheEnabled bool) *model.APIKey {
	t.Helper()
	ch := model.Channel{Name: "itest-cache-ch", BaseURL: upstreamURL,
		Currency: model.CurrencyUSD, Enabled: true, Protocol: model.ProtocolOpenAIChat}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	const publicName = "itest-cache-model"
	if err := db.Create(&model.ChannelModel{
		ChannelID: ch.ID, PublicName: publicName, UpstreamName: "itest-cache-up", Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	key := model.APIKey{
		Name:          "itest-cache-key",
		Enabled:       true,
		AllowedModels: model.StringList{publicName},
		ModelRoutes:   model.JSONMap{publicName: []any{float64(ch.ID)}},
		CacheEnabled:  keyCacheEnabled,
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	return &key
}

const chatBody = `{"model":"itest-cache-model","messages":[{"role":"user","content":"hi"}]}`

// TestResponseCacheHitReplaysWithoutUpstream 两次相同请求，上游只被打一次；
// 第二次命中缓存：X-Relay-Cache: hit，日志 cache_hit=true 且 token/金额为 0。
func TestResponseCacheHitReplaysWithoutUpstream(t *testing.T) {
	env := newCacheTestEnv(t, true)

	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"itest-cache-up","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`))
	}))
	defer upstream.Close()

	key := fixtureCacheChannelAndKey(t, env.db, upstream.URL, true)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/chat/completions", withCacheKey(key, env.server.chatCompletions))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求状态码 %d: %s", i+1, w.Code, w.Body.String())
		}
		if i == 0 && w.Header().Get("X-Relay-Cache") == "hit" {
			t.Error("首次请求不应命中缓存")
		}
		if i == 1 {
			if w.Header().Get("X-Relay-Cache") != "hit" {
				t.Errorf("第二次请求应命中缓存，响应头: %v", w.Header())
			}
			if !strings.Contains(w.Body.String(), `"content":"hello"`) {
				t.Errorf("重放响应内容不对: %s", w.Body.String())
			}
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("上游被调用 %d 次，期望 1（第二次必须命中缓存）", calls.Load())
	}

	env.server.deps.Logs.CloseAndFlush(10*time.Second, nil)
	var rows []model.RequestLog
	if err := env.db.Where("model_requested = ?", "itest-cache-model").Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("日志行数 %d，期望 2", len(rows))
	}
	if rows[0].CacheHit {
		t.Error("第一次请求不应标 cache_hit")
	}
	if !rows[1].CacheHit {
		t.Error("第二次请求应标 cache_hit")
	}
	if rows[1].TotalTokens != 0 || !rows[1].EstimatedCost.IsZero() {
		t.Errorf("命中请求的 token/金额应如实为 0: tokens=%d cost=%s", rows[1].TotalTokens, rows[1].EstimatedCost)
	}
	if rows[1].ChannelID != 0 {
		t.Errorf("命中请求没有渠道: %d", rows[1].ChannelID)
	}
}

// TestResponseCacheKeyToggleOff 密钥未勾缓存：两次相同请求都打上游。
func TestResponseCacheKeyToggleOff(t *testing.T) {
	env := newCacheTestEnv(t, true)

	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	key := fixtureCacheChannelAndKey(t, env.db, upstream.URL, false)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/chat/completions", withCacheKey(key, env.server.chatCompletions))
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("状态码 %d: %s", w.Code, w.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Errorf("未开启缓存的密钥应两次都打上游，实际 %d 次", calls.Load())
	}
}

// TestResponseCacheNoCacheHeader X-Relay-Cache: no-cache 强制回源并同样打上游。
func TestResponseCacheNoCacheHeader(t *testing.T) {
	env := newCacheTestEnv(t, true)

	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	key := fixtureCacheChannelAndKey(t, env.db, upstream.URL, true)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/chat/completions", withCacheKey(key, env.server.chatCompletions))
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Relay-Cache", "no-cache")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("状态码 %d", w.Code)
		}
	}
	if calls.Load() != 2 {
		t.Errorf("no-cache 应两次回源，实际 %d 次", calls.Load())
	}
}

// TestResponseCacheStreamReplay 流式形态单独成键并可重放：
// 两次 stream:true 的相同请求，上游只被打一次，第二次重放 SSE 字节。
func TestResponseCacheStreamReplay(t *testing.T) {
	env := newCacheTestEnv(t, true)

	sse := "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"he\"}}]}\n\n" +
		"data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"y\"}}]}\n\n" +
		"data: [DONE]\n\n"
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer upstream.Close()

	key := fixtureCacheChannelAndKey(t, env.db, upstream.URL, true)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/chat/completions", withCacheKey(key, env.server.chatCompletions))

	streamBody := `{"model":"itest-cache-model","messages":[{"role":"user","content":"hi"}],"stream":true}`
	var firstBody string
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(streamBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("第 %d 次状态码 %d: %s", i+1, w.Code, w.Body.String())
		}
		if i == 0 {
			firstBody = w.Body.String()
			continue
		}
		if w.Header().Get("X-Relay-Cache") != "hit" {
			t.Errorf("第二次流式请求应命中缓存: %v", w.Header())
		}
		if w.Body.String() != firstBody {
			t.Errorf("流式重放与首次响应不一致:\n首次: %q\n重放: %q", firstBody, w.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("上游被调用 %d 次，期望 1", calls.Load())
	}
}

// TestResponseCacheCrossProtocol 跨协议命中：openai-chat 存的缓存被
// anthropic 入站请求重放成 anthropic 形状 —— 归一化层键的核心收益。
func TestResponseCacheCrossProtocol(t *testing.T) {
	env := newCacheTestEnv(t, true)

	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","model":"itest-cache-up","choices":[{"index":0,"message":{"role":"assistant","content":"hello there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`))
	}))
	defer upstream.Close()

	key := fixtureCacheChannelAndKey(t, env.db, upstream.URL, true)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/chat/completions", withCacheKey(key, env.server.chatCompletions))
	r.POST("/v1/messages", withCacheKey(key, env.server.anthropicMessages))

	// OpenAI 入站：存缓存。max_tokens 刻意与下面的 anthropic 请求一致 ——
	// 它影响上游输出、必须进缓存键，两边归一化后要逐字节等价才能命中
	//（这是键建在归一化层的保守性：宁可不命中，也不把不同语义的请求
	// 混成同一个键）。
	openaiBody := `{"model":"itest-cache-model","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(openaiBody))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("openai 入站失败 %d: %s", w1.Code, w1.Body.String())
	}

	// Anthropic 入站：AnthropicRequestToOpenAIChat 把它翻译成
	// {"max_tokens":64,"messages":[{"content":"hi","role":"user"}],"model":…}，
	// 与上面 openai 请求的归一化结果等价 → 同一个键。
	// 命中后 AnthropicTranslator 把缓存的 OpenAI 响应改回 anthropic 形状。
	anthropicBody := `{"model":"itest-cache-model","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	req2 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(anthropicBody))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("anthropic 入站失败 %d: %s", w2.Code, w2.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("上游被调用 %d 次，期望 1（跨协议必须命中同一份缓存）", calls.Load())
	}
	if w2.Header().Get("X-Relay-Cache") != "hit" {
		t.Errorf("跨协议请求应命中缓存: %v", w2.Header())
	}
	// 响应是 anthropic 形状（content blocks），不是 openai 的 choices
	var payload map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &payload); err != nil {
		t.Fatalf("anthropic 重放响应不是合法 JSON: %v", err)
	}
	if _, ok := payload["choices"]; ok {
		t.Errorf("anthropic 入站不应收到 openai 形状: %s", w2.Body.String())
	}
	if !bytes.Contains(w2.Body.Bytes(), []byte(`"content"`)) {
		t.Errorf("anthropic 重放响应缺 content: %s", w2.Body.String())
	}
}
