package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
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
	"llm-relay/internal/pricing"
	"llm-relay/internal/relay"
)

// 直通端点（图像 / 音频）的端到端验收（2026-11 P2，LLMRELAY_TEST_DSN 门控）。
//
// 走真实链路：gin 路由 → relayRequest（Direct profile）→ Router（RequireProtocols）
// → Forwarder.Do（跳协议转换 + multipart 模型改写）→ finalizeLog（维度计费落账）。
//
// 为什么必须连库：计费断言打在 request_logs 行上（usage_kind / estimated_cost /
// pricing_snapshot），要经过 GORM 建表、异步 LogWriter 与 numeric(18,8) 列
// 才能成立；「渠道协议过滤」也只有真实 SQL 才能证明。
//
// 三条用例各用独立的测试上游 + 独立的 itest- 前缀模型名，可重复运行。

// directTestEnv 是一条用例的全部夹具。
type directTestEnv struct {
	db      *gorm.DB
	logs    *relay.LogWriter
	server  *Server
	cleanup func(t *testing.T)
}

func newDirectTestEnv(t *testing.T) *directTestEnv {
	t.Helper()
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
	if err := db.AutoMigrate(&model.Channel{}, &model.ChannelModel{}, &model.ModelPrice{},
		&model.RequestLog{}, &model.RequestPayload{}, &model.APIKey{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	env := &directTestEnv{db: db}
	env.logs = relay.NewLogWriterWithClientMap(db, 64, nil, relay.ClientMapOptions{Limit: 4, TTL: time.Hour})
	env.server = &Server{deps: &Deps{
		Config:  config.Default(),
		Logs:    env.logs,
		Pricing: pricing.NewEngine(db, time.Millisecond),
		Service: relay.NewService(db, relay.NewRouter(db, nil), relay.Options{}, nil),
	}}
	env.cleanup = func(t *testing.T) {
		db.Exec(`DELETE FROM request_payloads WHERE log_id IN (SELECT id FROM request_logs WHERE model_requested LIKE 'itest-d%')`)
		db.Exec(`DELETE FROM request_logs WHERE model_requested LIKE 'itest-d%'`)
		db.Exec(`DELETE FROM channel_models WHERE public_name LIKE 'itest-d%'`)
		db.Exec(`DELETE FROM model_prices WHERE model_name LIKE 'itest-d%'`)
		db.Exec(`DELETE FROM channels WHERE name LIKE 'itest-d%'`)
	}
	t.Cleanup(func() {
		env.cleanup(t)
		if sqlDB, _ := db.DB(); sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return env
}

// fixtureChannel 建一条指向假上游的 OpenAI 兼容渠道，白名单里挂一个
// itest- 前缀的模型（对外名 → 上游名刻意不同，验证 multipart/JSON 改写都生效），
// 并配一条「只含维度单价」的模型价。
func (e *directTestEnv) fixtureChannel(t *testing.T, upstream *httptest.Server, publicName, upstreamName string, price model.ModelPrice) model.Channel {
	t.Helper()
	ch := model.Channel{Name: "itest-direct-" + publicName, BaseURL: upstream.URL,
		Currency: model.CurrencyUSD, Enabled: true, Protocol: model.ProtocolOpenAIChat}
	if err := e.db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.db.Create(&model.ChannelModel{
		ChannelID: ch.ID, PublicName: publicName, UpstreamName: upstreamName, Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	price.ModelName = publicName
	if err := e.db.Create(&price).Error; err != nil {
		t.Fatal(err)
	}
	return ch
}

// capturedRequest 收集假上游收到的请求，供断言改写是否生效。
type capturedRequest struct {
	ContentType string
	Body        []byte
}

func captureServer(t *testing.T, respond func(r *http.Request) ([]byte, string)) (*httptest.Server, *[]capturedRequest) {
	t.Helper()
	var captured []capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		captured = append(captured, capturedRequest{ContentType: r.Header.Get("Content-Type"), Body: buf.Bytes()})
		respBody, ct := respond(r)
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		_, _ = w.Write(respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

// TestImagesGenerationsEndpoint 文生图：JSON 直通 + 按张计费。
func TestImagesGenerationsEndpoint(t *testing.T) {
	env := newDirectTestEnv(t)

	upstream, captured := captureServer(t, func(r *http.Request) ([]byte, string) {
		return []byte(`{"created":1,"data":[{"url":"u1"},{"url":"u2"}]}`), "application/json"
	})
	env.fixtureChannel(t, upstream, "itest-d-image", "itest-d-image-up",
		model.ModelPrice{USDPerImage: mustDec("0.04")})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/images/generations", env.server.imagesGenerations)

	body := `{"model":"itest-d-image","prompt":"a cat","n":2}`
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d，body: %s", w.Code, w.Body.String())
	}
	// 响应原样透传（不经过 OpenAI Chat 改写）
	if !strings.Contains(w.Body.String(), `"data":[{"url":"u1"},{"url":"u2"}]`) {
		t.Errorf("响应被改写或损坏: %s", w.Body.String())
	}
	if len(*captured) != 1 {
		t.Fatalf("上游被调用 %d 次", len(*captured))
	}
	// 请求体里的模型名被改写成上游名，其余字段保留
	var sent map[string]any
	if err := json.Unmarshal((*captured)[0].Body, &sent); err != nil {
		t.Fatalf("上游收到的不是合法 JSON: %v", err)
	}
	if sent["model"] != "itest-d-image-up" {
		t.Errorf("上游收到的 model = %v，期望改写成 itest-d-image-up", sent["model"])
	}
	if sent["prompt"] != "a cat" {
		t.Errorf("prompt 丢失: %v", sent["prompt"])
	}

	env.logs.CloseAndFlush(10*time.Second, nil)
	var logRow model.RequestLog
	if err := env.db.Where("model_requested = ?", "itest-d-image").Order("id desc").First(&logRow).Error; err != nil {
		t.Fatalf("日志未落库: %v", err)
	}
	if logRow.UsageKind != "image" {
		t.Errorf("usage_kind = %q，期望 image", logRow.UsageKind)
	}
	// 2 张 × $0.04/张 = 0.08
	if logRow.EstimatedCost.String() != "0.08" {
		t.Errorf("estimated_cost = %s，期望 0.08（2 张 × 0.04）", logRow.EstimatedCost)
	}
	if logRow.TotalTokens != 0 {
		t.Errorf("图像请求不应有 token 计量: %d", logRow.TotalTokens)
	}
}

// TestAudioSpeechEndpoint 语音合成：二进制响应透传 + 按字符计费。
func TestAudioSpeechEndpoint(t *testing.T) {
	env := newDirectTestEnv(t)

	audio := []byte("FAKE-MP3-DATA-0123456789")
	upstream, captured := captureServer(t, func(r *http.Request) ([]byte, string) {
		return audio, "audio/mpeg"
	})
	env.fixtureChannel(t, upstream, "itest-d-tts", "itest-d-tts-up",
		model.ModelPrice{USDPerCharacter: mustDec("0.000015")})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/audio/speech", env.server.audioSpeech)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech",
		strings.NewReader(`{"model":"itest-d-tts","input":"你好世界","voice":"alloy"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d，body: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != string(audio) {
		t.Errorf("音频响应被改写: %q", w.Body.String())
	}
	// 响应 Content-Type 必须是上游的 audio/mpeg（标成 JSON 客户端就废了）
	if got := w.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Errorf("响应 Content-Type = %q，期望 audio/mpeg", got)
	}
	if len(*captured) != 1 {
		t.Fatalf("上游被调用 %d 次", len(*captured))
	}
	var sent map[string]any
	if err := json.Unmarshal((*captured)[0].Body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["model"] != "itest-d-tts-up" || sent["input"] != "你好世界" {
		t.Errorf("上游收到的请求不对: %v", sent)
	}

	env.logs.CloseAndFlush(10*time.Second, nil)
	var logRow model.RequestLog
	if err := env.db.Where("model_requested = ?", "itest-d-tts").Order("id desc").First(&logRow).Error; err != nil {
		t.Fatalf("日志未落库: %v", err)
	}
	if logRow.UsageKind != "tts" {
		t.Errorf("usage_kind = %q，期望 tts", logRow.UsageKind)
	}
	// 4 个字符 × $0.000015 = 0.00006（按 Unicode 码点：一个汉字一个字符）
	if logRow.EstimatedCost.String() != "0.00006" {
		t.Errorf("estimated_cost = %s，期望 0.00006（4 字符 × 0.000015）", logRow.EstimatedCost)
	}
}

// TestAudioTranscriptionsEndpoint 语音转写：multipart 模型改写 + 按秒计费。
func TestAudioTranscriptionsEndpoint(t *testing.T) {
	env := newDirectTestEnv(t)

	upstream, captured := captureServer(t, func(r *http.Request) ([]byte, string) {
		return []byte(`{"text":"hello","duration":30.5}`), "application/json"
	})
	env.fixtureChannel(t, upstream, "itest-d-asr", "itest-d-asr-up",
		model.ModelPrice{USDPerSecond: mustDec("0.0001")})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/audio/transcriptions", env.server.audioTranscriptions)

	// 构造 multipart：model 字段（对外名）+ 文件部件
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model", "itest-d-asr")
	fw, _ := mw.CreateFormFile("file", "audio.mp3")
	_, _ = fw.Write([]byte("FAKE-AUDIO-BYTES"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d，body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"text":"hello"`) {
		t.Errorf("响应被改写: %s", w.Body.String())
	}
	if len(*captured) != 1 {
		t.Fatalf("上游被调用 %d 次", len(*captured))
	}
	// 上游收到的 multipart：模型名是上游名、文件内容原样
	upReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader((*captured)[0].Body))
	upReq.Header.Set("Content-Type", (*captured)[0].ContentType)
	if err := upReq.ParseMultipartForm(32 << 20); err != nil {
		t.Fatalf("上游收到的报文不是合法 multipart: %v", err)
	}
	if got := upReq.MultipartForm.Value["model"]; len(got) != 1 || got[0] != "itest-d-asr-up" {
		t.Errorf("上游收到的 model = %v，期望 itest-d-asr-up", got)
	}
	file, _, err := upReq.FormFile("file")
	if err != nil {
		t.Fatalf("文件部件丢失: %v", err)
	}
	var fileContent bytes.Buffer
	_, _ = fileContent.ReadFrom(file)
	if fileContent.String() != "FAKE-AUDIO-BYTES" {
		t.Errorf("音频文件内容损坏: %q", fileContent.String())
	}

	env.logs.CloseAndFlush(10*time.Second, nil)
	var logRow model.RequestLog
	if err := env.db.Where("model_requested = ?", "itest-d-asr").Order("id desc").First(&logRow).Error; err != nil {
		t.Fatalf("日志未落库: %v", err)
	}
	if logRow.UsageKind != "audio" {
		t.Errorf("usage_kind = %q，期望 audio（上游未报 usage，按 duration 计）", logRow.UsageKind)
	}
	// 30.5 秒 × $0.0001/秒 = 0.00305
	if logRow.EstimatedCost.String() != "0.00305" {
		t.Errorf("estimated_cost = %s，期望 0.00305（30.5 秒 × 0.0001）", logRow.EstimatedCost)
	}
}

// TestDirectEndpointsRejectAnthropicChannel 直通请求不会落到 Anthropic 协议渠道：
// RequireProtocols 在路由层排除，而不是等转发失败。
func TestDirectEndpointsRejectAnthropicChannel(t *testing.T) {
	env := newDirectTestEnv(t)

	// 一条 Anthropic 协议渠道挂着同名模型：直通请求必须当它不存在
	ch := model.Channel{Name: "itest-d-anthropic", BaseURL: "http://127.0.0.1:1",
		Currency: model.CurrencyUSD, Enabled: true, Protocol: model.ProtocolAnthropic}
	if err := env.db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.db.Create(&model.ChannelModel{
		ChannelID: ch.ID, PublicName: "itest-d-reject", UpstreamName: "itest-d-reject", Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/images/generations", env.server.imagesGenerations)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"model":"itest-d-reject","prompt":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// 唯一的候选被协议过滤排除 → 无可用渠道 → 502（与对话端点同语义）
	if w.Code != http.StatusBadGateway {
		t.Fatalf("状态码 %d，期望 502（无可用渠道）。body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "没有可用的渠道") {
		t.Errorf("错误信息应说明无可用渠道: %s", w.Body.String())
	}
}
