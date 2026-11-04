package api

import (
	"testing"

	"github.com/shopspring/decimal"

	"llm-relay/internal/pricing"
	"llm-relay/internal/relay"
)

// 直通端点计费判据的纯函数测试（账本逻辑，不碰 HTTP 与数据库）。

func TestDirectBillingKind(t *testing.T) {
	cases := map[string]string{
		"openai-images":               "image",
		"openai-audio-speech":         "tts",
		"openai-audio-transcriptions": "audio",
		"openai-audio-translations":   "audio",
		"openai-chat":                 "", // 对话端点不是直通，兜底返回空
		"":                            "",
	}
	for proto, want := range cases {
		if got := directBillingKind(proto); got != want {
			t.Errorf("directBillingKind(%q) = %q，期望 %q", proto, got, want)
		}
	}
}

func TestImageCountFromResponse(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"两张", `{"data":[{"url":"a"},{"b64":"b"}]}`, 2},
		{"空 data", `{"data":[]}`, 0},
		{"缺 data", `{"created":1}`, 0},
		{"非 JSON（上游异常）", `binary garbage`, 0},
		{"空报文", ``, 0},
	}
	for _, c := range cases {
		if got := imageCountFromResponse([]byte(c.body)); got != c.want {
			t.Errorf("%s: imageCountFromResponse = %d，期望 %d", c.name, got, c.want)
		}
	}
}

func TestTTSInputChars(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		// 按 Unicode 码点：一个汉字一个字符 —— 按字节会把中文账单放大三倍
		{"四个汉字", `{"input":"你好世界"}`, 4},
		{"英文", `{"input":"hello"}`, 5},
		{"混合", `{"input":"a好b"}`, 3},
		{"缺 input", `{"model":"tts-1"}`, 0},
		{"非 JSON", `garbage`, 0},
	}
	for _, c := range cases {
		if got := ttsInputChars([]byte(c.body)); got != c.want {
			t.Errorf("%s: ttsInputChars = %d，期望 %d", c.name, got, c.want)
		}
	}
}

func TestAudioDurationFromResponse(t *testing.T) {
	if d := audioDurationFromResponse([]byte(`{"text":"hi","duration":30.5}`)); d.String() != "30.5" {
		t.Errorf("duration = %s，期望 30.5", d)
	}
	if d := audioDurationFromResponse([]byte(`{"text":"hi"}`)); !d.IsZero() {
		t.Errorf("缺 duration 应为 0，得到 %s", d)
	}
	if d := audioDurationFromResponse([]byte(`{"duration":0}`)); !d.IsZero() {
		t.Errorf("duration=0 应为 0（没有计量依据不记账），得到 %s", d)
	}
	if d := audioDurationFromResponse([]byte(`garbage`)); !d.IsZero() {
		t.Errorf("非 JSON 应为 0，得到 %s", d)
	}
}

// dimsPrice 构造七档单价齐全、倍率 1 的 Price（本包计费断言用）。
func dimsPrice() pricing.Price {
	one := decimal.NewFromInt(1)
	return pricing.Price{
		Currency:     "USD",
		InputPer1M:   one,
		OutputPer1M:  one,
		PerImage:     decimal.NewFromFloat(0.04),
		PerSecond:    decimal.NewFromFloat(0.0001),
		PerCharacter: decimal.NewFromFloat(0.000015),
		Multiplier:   one,
	}
}

func TestDirectCostImage(t *testing.T) {
	p := dimsPrice()
	cost, kind := directCost(pricing.BillingKindImage,
		[]byte(`{"n":3}`), // 请求的 n=3 不参与计费
		[]byte(`{"data":[{"url":"a"},{"b64":"b"}]}`),
		relay.Usage{}, p)
	if kind != "image" || cost.String() != "0.08" {
		t.Errorf("image: cost=%s kind=%s，期望 0.08/image（按响应的 2 张）", cost, kind)
	}
}

func TestDirectCostTTS(t *testing.T) {
	p := dimsPrice()
	cost, kind := directCost(pricing.BillingKindTTS,
		[]byte(`{"input":"你好"}`), []byte(`binary`),
		relay.Usage{}, p)
	if kind != "tts" || cost.String() != "0.00003" {
		t.Errorf("tts: cost=%s kind=%s，期望 0.00003/tts（2 字符）", cost, kind)
	}
}

// 转写响应带真实 usage 时按 token 计（gpt-4o-transcribe 就是这种）；
// 带的若是「估算值」（非流式路径的兜底估算对音频是垃圾数），不认。
func TestDirectCostAudio(t *testing.T) {
	p := dimsPrice()

	// 真实 usage：按 token 四档，口径回 token
	cost, kind := directCost(pricing.BillingKindAudio,
		[]byte(`x`), []byte(`{"text":"hi"}`),
		relay.Usage{PromptTokens: 1000, CompletionTokens: 1000}, p)
	if kind != "token" || cost.String() != "0.002" {
		t.Errorf("audio+usage: cost=%s kind=%s，期望 0.002/token", cost, kind)
	}

	// 估算 usage：不认，退 duration
	estimated := relay.EstimateUsage(1000, 1000)
	if !estimated.Estimated {
		t.Fatal("EstimateUsage 应带 Estimated 标记")
	}
	cost, kind = directCost(pricing.BillingKindAudio,
		[]byte(`x`), []byte(`{"duration":10}`), estimated, p)
	if kind != "audio" || cost.String() != "0.001" {
		t.Errorf("audio+估算 usage: cost=%s kind=%s，期望 0.001/audio（10 秒 × 0.0001）", cost, kind)
	}

	// 无 usage 无 duration：记 0（不猜）
	cost, kind = directCost(pricing.BillingKindAudio,
		[]byte(`x`), []byte(`{"text":"hi"}`), relay.Usage{}, p)
	if kind != "audio" || !cost.IsZero() {
		t.Errorf("audio 无计量依据: cost=%s kind=%s，期望 0/audio", cost, kind)
	}
}
