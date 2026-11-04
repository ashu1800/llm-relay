package pricing

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"testing"
)

// fixture 构造一小份「上游原文件」形状的 JSON，覆盖裁剪规则的每个分支。
func fixture(t *testing.T) []byte {
	t.Helper()
	raw := map[string]any{
		// token 四档：上游是「每 token」小数，解析后必须折成「每百万」
		"gpt-4o": map[string]any{
			"mode":                            "chat",
			"litellm_provider":                "openai",
			"input_cost_per_token":            2.5e-06,
			"output_cost_per_token":           1e-05,
			"cache_read_input_token_cost":     1.25e-06,
			"cache_creation_input_token_cost": 1.25e-06,
			"max_input_tokens":                128000,
			"max_output_tokens":               16384,
			// variant 档（批量/优先级/弹性）：裁剪规则明确不收
			"input_cost_per_token_batches":  1e-06,
			"input_cost_per_token_priority": 9e-06,
		},
		// 图像：output_cost_per_image 优先
		"dall-e-3": map[string]any{
			"mode":                  "image_generation",
			"litellm_provider":      "openai",
			"output_cost_per_image": 0.04,
			"input_cost_per_image":  99.0, // 有 output 时 input 不该被取到
		},
		// 图像：没有 output_cost_per_image 时退 input_cost_per_image
		"weird-image-model": map[string]any{
			"mode":                 "image_generation",
			"litellm_provider":     "openai",
			"input_cost_per_image": 0.01,
		},
		"whisper-1": map[string]any{
			"mode":                  "audio_transcription",
			"litellm_provider":      "openai",
			"input_cost_per_second": 0.0001,
		},
		"tts-1": map[string]any{
			"mode":                     "audio_speech",
			"litellm_provider":         "openai",
			"input_cost_per_character": 1.5e-05,
		},
		// 供应商前缀 + 日期后缀：归一化桶的测试样本
		"anthropic/claude-3-5-sonnet-20241022": map[string]any{
			"mode":                 "chat",
			"litellm_provider":     "anthropic",
			"input_cost_per_token": 3e-06,
		},
		"claude-3-5-sonnet": map[string]any{
			"mode":                 "chat",
			"litellm_provider":     "anthropic",
			"input_cost_per_token": 3e-06,
		},
		// 只有元数据没有价格：搜索必须把它过滤掉
		"metadata-only-model": map[string]any{
			"mode":             "chat",
			"litellm_provider": "openai",
			"max_input_tokens": 8192,
		},
		// 上游的格式说明样例：解析必须跳过
		"sample_spec": map[string]any{
			"mode":                 "chat",
			"litellm_provider":     "sample_spec",
			"input_cost_per_token": 0,
		},
		// 未知供应商前缀：归一化时不能剥
		"unknown-vendor/gpt-4o": map[string]any{
			"mode":                 "chat",
			"litellm_provider":     "openai",
			"input_cost_per_token": 5e-06,
		},
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("构造 fixture 失败: %v", err)
	}
	return b
}

func mustParseFixture(t *testing.T) *Catalog {
	t.Helper()
	cat, err := ParseLiteLLMRaw(fixture(t))
	if err != nil {
		t.Fatalf("解析 fixture 失败: %v", err)
	}
	return cat
}

// 裁剪与折算是「建议价」正确性的根基：折算错一个数量级，一键填充
// 写进 model_prices 的就是错十倍的价格，账面照样能平（0 元才显眼），
// 只能靠测试在这里拦。
func TestParseLiteLLMRawTrim(t *testing.T) {
	cat := mustParseFixture(t)

	gpt4o, ok := cat.Entries["gpt-4o"]
	if !ok {
		t.Fatal("gpt-4o 条目丢失")
	}
	if gpt4o.InputPer1M != 2.5 || gpt4o.OutputPer1M != 10 || gpt4o.CacheReadPer1M != 1.25 {
		t.Errorf("token 四档折算错误: in=%v out=%v cr=%v，期望 2.5/10/1.25（每百万）",
			gpt4o.InputPer1M, gpt4o.OutputPer1M, gpt4o.CacheReadPer1M)
	}
	if gpt4o.MaxInput != 128000 || gpt4o.MaxOutput != 16384 {
		t.Errorf("上下文窗口丢失: %v/%v", gpt4o.MaxInput, gpt4o.MaxOutput)
	}

	if e := cat.Entries["dall-e-3"]; e.PerImage != 0.04 {
		t.Errorf("图像单价应取 output_cost_per_image=0.04，得到 %v", e.PerImage)
	}
	if e := cat.Entries["weird-image-model"]; e.PerImage != 0.01 {
		t.Errorf("无 output_cost_per_image 时应退 input_cost_per_image=0.01，得到 %v", e.PerImage)
	}
	if e := cat.Entries["whisper-1"]; e.PerSecond != 0.0001 {
		t.Errorf("音频每秒单价错误: %v", e.PerSecond)
	}
	if e := cat.Entries["tts-1"]; e.PerCharacter != 1.5e-05 {
		t.Errorf("TTS 每字符单价错误: %v", e.PerCharacter)
	}

	if _, exists := cat.Entries["sample_spec"]; exists {
		t.Error("sample_spec 不该进目录")
	}
}

// TestEmbeddedCatalogShape 对编译期内嵌快照做结构断言。
//
// 刻意不钉死具体价格数字（上游随时调价，钉死会让每次 make update-prices
// 都红一次测试）：断言「条目存在 + 与同一份原文件里的原始值折算一致」。
// 折算系数（×1e6）写错十倍是这里最危险的静默错误。
func TestEmbeddedCatalogShape(t *testing.T) {
	cat, err := ParseLiteLLMRaw(embeddedCatalogRaw)
	if err != nil {
		t.Fatalf("内嵌参考价库解析失败: %v", err)
	}
	if len(cat.Entries) < 1000 {
		t.Fatalf("内嵌目录条目数异常: %d（上游有 4000+ 条，裁剪逻辑可能坏了）", len(cat.Entries))
	}

	// 用原文件自身校验折算：对 gpt-4o 与 whisper-1 各核对一遍。
	var rawFile map[string]map[string]any
	if err := json.Unmarshal(embeddedCatalogRaw, &rawFile); err != nil {
		t.Fatalf("内嵌原文件解析失败: %v", err)
	}
	for _, key := range []string{"gpt-4o", "whisper-1", "tts-1"} {
		entry, ok := cat.Entries[key]
		if !ok {
			t.Fatalf("内嵌目录缺少 %s", key)
		}
		rawEntry := rawFile[key]
		if v := numField(rawEntry, "input_cost_per_token"); v > 0 {
			if math.Abs(entry.InputPer1M-v*perMillion) > 1e-6 {
				t.Errorf("%s 输入单价折算错误: %v，期望 %v", key, entry.InputPer1M, v*perMillion)
			}
		}
		if v := numField(rawEntry, "input_cost_per_second"); v > 0 && entry.PerSecond != v {
			t.Errorf("%s 每秒单价错误: %v，期望 %v", key, entry.PerSecond, v)
		}
		if v := numField(rawEntry, "input_cost_per_character"); v > 0 && entry.PerCharacter != v {
			t.Errorf("%s 每字符单价错误: %v，期望 %v", key, entry.PerCharacter, v)
		}
	}
}

func TestNormalizeModelName(t *testing.T) {
	providers := map[string]bool{"openai": true, "anthropic": true, "gemini": true, "deepseek": true}
	cases := []struct {
		in   string
		want string
	}{
		{"gpt-4o", "gpt-4o"},
		{"GPT-4O", "gpt-4o"},
		{"openai/gpt-4o", "gpt-4o"},                         // 已知供应商前缀：剥
		{"deepseek/deepseek-chat", "deepseek-chat"},         // 同上
		{"unknown-vendor/gpt-4o", "unknown-vendor/gpt-4o"},  // 未知前缀：保留（剥了会把不同目录项混桶）
		{"gpt-4o-2024-08-06", "gpt-4o"},                     // 完整日期后缀：剥
		{"claude-3-5-sonnet-20241022", "claude-3-5-sonnet"}, // 紧凑日期后缀：剥
		{"gemini/gemini-2.0-flash-exp", "gemini-2.0-flash-exp"},
		{"llama-3.1-8b-instant", "llama-3.1-8b-instant"}, // 版本号不是日期：保留
		{"gpt-4o-002", "gpt-4o-002"},
	}
	for _, c := range cases {
		if got := NormalizeModelName(c.in, providers); got != c.want {
			t.Errorf("NormalizeModelName(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestMatchBest(t *testing.T) {
	cat := mustParseFixture(t)

	// 精确键
	if h, ok := cat.MatchBest("whisper-1"); !ok || h.Key != "whisper-1" {
		t.Errorf("精确匹配失败: %v %v", h, ok)
	}
	// 归一化相等：供应商前缀 + 日期后缀都剥掉之后相等
	if h, ok := cat.MatchBest("anthropic/claude-3-5-sonnet-20241022"); !ok {
		t.Errorf("带前缀带日期的查询没匹配上")
	} else if normBucket := cat.normalize(h.Key); normBucket != "claude-3-5-sonnet" {
		t.Errorf("匹配到异常键 %s", h.Key)
	}
	// 同族取最新：gpt-4o 在 fixture 里只有无日期的一份（精确命中），
	// 换一个只有带日期条目的名字验证「剥后缀」方向
	if h, ok := cat.MatchBest("claude-3-5-sonnet-20240620"); !ok {
		t.Errorf("日期变体没匹配到同族")
	} else if h.Key != "claude-3-5-sonnet" && h.Key != "anthropic/claude-3-5-sonnet-20241022" {
		t.Errorf("同族匹配到了异常键 %s", h.Key)
	}
	// 无命中必须如实说没有：硬凑一个会让一键填充填上不相干的价格
	if _, ok := cat.MatchBest("totally-made-up-model-xyz"); ok {
		t.Error("不存在的模型不该有匹配")
	}
	if _, ok := cat.MatchBest(""); ok {
		t.Error("空模型名不该有匹配")
	}
}

func TestCatalogSearch(t *testing.T) {
	cat := mustParseFixture(t)

	// 无关键词 + mode 过滤
	hits := cat.Search("", "audio_transcription", 50)
	if len(hits) != 1 || hits[0].Key != "whisper-1" {
		t.Errorf("mode 过滤结果异常: %v", hits)
	}

	// 包含匹配
	hits = cat.Search("whisper", "", 10)
	if len(hits) == 0 || hits[0].Key != "whisper-1" {
		t.Errorf("包含搜索失败: %v", hits)
	}

	// 只有元数据的条目必须被过滤：填不出价格的候选没有意义
	hits = cat.Search("metadata-only", "", 10)
	if len(hits) != 0 {
		t.Errorf("无价条目不该出现在搜索结果里: %v", hits)
	}

	// 未知供应商前缀的条目按原键保留、可被搜到
	hits = cat.Search("unknown-vendor", "", 10)
	if len(hits) != 1 || hits[0].Key != "unknown-vendor/gpt-4o" {
		t.Errorf("未知前缀条目搜索失败: %v", hits)
	}

	// limit 生效
	if got := cat.Search("", "", 2); len(got) > 2 {
		t.Errorf("limit 未生效: %d", len(got))
	}
}

// TestCatalogStoreFallbackAndReplace 用 nil DB 验证内嵌回落路径与
// ReplaceCatalog 的内存替换（带 DB 的持久化验证在 api 的 PG 门控测试里）。
func TestCatalogStoreFallbackAndReplace(t *testing.T) {
	s := NewCatalogStore(nil)
	cat, err := s.Get(context.Background())
	if err != nil {
		t.Fatalf("内嵌回落失败: %v", err)
	}
	if len(cat.Entries) < 1000 {
		t.Fatalf("内嵌目录条目异常: %d", len(cat.Entries))
	}
	if !s.isBuiltin() {
		t.Error("nil DB 时应标记为内嵌来源")
	}

	// 替换成一份小目录：nil DB 时只换内存，不落库
	small, err := ParseLiteLLMRaw(fixture(t))
	if err != nil {
		t.Fatalf("解析 fixture 失败: %v", err)
	}
	replaced, err := s.ReplaceCatalog(context.Background(), fixture(t), "2026-11-04T00:00:00Z", "test")
	if err != nil {
		t.Fatalf("ReplaceCatalog 失败: %v", err)
	}
	if len(replaced.Entries) != len(small.Entries) {
		t.Errorf("替换后的条目数不一致: %d vs %d", len(replaced.Entries), len(small.Entries))
	}
	if s.isBuiltin() {
		t.Error("ReplaceCatalog 后应标记为非内嵌来源")
	}
	st, err := s.Status(context.Background())
	if err != nil {
		t.Fatalf("Status 失败: %v", err)
	}
	if st.FetchedAt != "2026-11-04T00:00:00Z" || st.Builtin {
		t.Errorf("Status 元数据错误: %+v", st)
	}
}

// TestEmbeddedFilePresent 防一个构建期事故：数据文件被误删/未提交时，
// 本测试比「别人 clone 之后 go build 失败」的报错更早、更直白。
func TestEmbeddedFilePresent(t *testing.T) {
	if _, err := os.Stat("catalog_data/litellm_prices.json"); err != nil {
		t.Fatalf("内嵌数据文件丢失（make update-prices 生成后必须提交）: %v", err)
	}
	if len(embeddedCatalogRaw) == 0 {
		t.Fatal("embed 指令没有读到数据")
	}
}
