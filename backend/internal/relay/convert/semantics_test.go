package convert

// 转换语义补齐批次（审查报告 R-中2~中5）的测试：
//   - R-中2 入站 Gemini/Responses 流式翻译器透传 finish_reason（截断呈现为截断）
//   - R-中3 入站 Anthropic 的 thinking/top_k 等特有参数经私有字段往返
//   - R-中4 入站 Responses 的 previous_response_id 显式拒绝
//   - R-中5 出站 Gemini/Responses 对不认识的内容块显式拒绝（Gemini 收音频）

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// ---------- R-中2：入站流式翻译器透传 finish_reason ----------

// 上游报 finish_reason:length 时，Gemini 客户端要看到 MAX_TOKENS ——
// 修复前一律 STOP，撞 max_tokens 的截断被呈现成完整结束。
func TestGeminiStreamTranslatorPropagatesLength(t *testing.T) {
	var buf bytes.Buffer
	tr := NewGeminiStreamTranslator(&buf, "m")
	sse := `data: {"choices":[{"delta":{"content":"半句"}}]}

data: {"choices":[{"delta":{},"finish_reason":"length"}]}

`
	if _, err := tr.Write([]byte(sse)); err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, `"finishReason":"MAX_TOKENS"`) {
		t.Fatalf("finish_reason=length 应映射为 MAX_TOKENS:\n%s", got)
	}
}

// content_filter 同理映射为 SAFETY。
func TestGeminiStreamTranslatorPropagatesContentFilter(t *testing.T) {
	var buf bytes.Buffer
	tr := NewGeminiStreamTranslator(&buf, "m")
	sse := `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"content_filter"}]}

`
	if _, err := tr.Write([]byte(sse)); err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, `"finishReason":"SAFETY"`) {
		t.Fatalf("finish_reason=content_filter 应映射为 SAFETY:\n%s", got)
	}
}

// 正常收尾仍是 STOP（默认路径不受影响）。
func TestGeminiStreamTranslatorNormalStop(t *testing.T) {
	var buf bytes.Buffer
	tr := NewGeminiStreamTranslator(&buf, "m")
	if _, err := tr.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")); err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, `"finishReason":"STOP"`) {
		t.Fatalf("正常收尾应保持 STOP:\n%s", got)
	}
}

// Responses 客户端侧：length -> status incomplete + incomplete_details。
func TestResponsesStreamTranslatorPropagatesLength(t *testing.T) {
	var buf bytes.Buffer
	tr := NewResponsesStreamTranslator(&buf, "m")
	sse := `data: {"choices":[{"delta":{"content":"半句"}}]}

data: {"choices":[{"delta":{},"finish_reason":"length"}]}

`
	if _, err := tr.Write([]byte(sse)); err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, `"status":"incomplete"`) || !strings.Contains(got, `"max_output_tokens"`) {
		t.Fatalf("finish_reason=length 应报 incomplete + max_output_tokens:\n%s", got)
	}
}

func TestResponsesStreamTranslatorNormalCompleted(t *testing.T) {
	var buf bytes.Buffer
	tr := NewResponsesStreamTranslator(&buf, "m")
	if _, err := tr.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")); err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, `"status":"completed"`) {
		t.Fatalf("正常收尾应保持 completed:\n%s", got)
	}
}

// ---------- R-中3：Anthropic 特有顶层参数经私有字段往返 ----------

func TestAnthropicThinkingTopKRoundTrip(t *testing.T) {
	src := []byte(`{
		"model": "claude-3",
		"max_tokens": 1024,
		"thinking": {"type": "enabled", "budget_tokens": 2048},
		"top_k": 40,
		"messages": [{"role": "user", "content": "hi"}]
	}`)
	mid, err := AnthropicRequestToOpenAIChat(src)
	if err != nil {
		t.Fatal(err)
	}
	// 中间通用语里应该有暂存（出站 Anthropic 才能恢复）
	var midMap map[string]any
	_ = json.Unmarshal(mid, &midMap)
	if takeAnthropicParams(midMap) == nil {
		t.Fatal("通用语里应有 anthropic_params 暂存")
	}
	out, err := OpenAIChatToAnthropicRequest(mid)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	th := asMap(got["thinking"])
	if th == nil || asString(th["type"]) != "enabled" || asInt(th["budget_tokens"]) != 2048 {
		t.Fatalf("thinking 应原样恢复（含 budget_tokens），实际 %v", got["thinking"])
	}
	if asInt(got["top_k"]) != 40 {
		t.Fatalf("top_k 应原样恢复，实际 %v", got["top_k"])
	}
}

// 暂存字段绝不能漏给出站非 Anthropic 的上游（严格校验的会直接 400）。
//
// 判的是 **Anthropic 私有字段**（anthropic_params 与 Anthropic 那几项顶层参数名），
// 不是「出现 thinking 字样」：入站 Anthropic 的思考强度现在会按档位映射成
// Gemini 自己的 thinkingConfig（见 thinking_map.go），那是修复要的行为。
func TestAnthropicParamsStrippedForGeminiUpstream(t *testing.T) {
	src := []byte(`{
		"model": "claude-3",
		"max_tokens": 100,
		"thinking": {"type": "enabled", "budget_tokens": 512},
		"messages": [{"role": "user", "content": "hi"}]
	}`)
	mid, err := AnthropicRequestToOpenAIChat(src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := OpenAIChatToGeminiRequest(mid)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "anthropic_params") || strings.Contains(string(out), `"thinking":`) {
		t.Fatalf("发给 Gemini 的请求不应带 Anthropic 私有字段:\n%s", out)
	}
	// 强度按档位过去：budget 512 → low → 4096
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	tc := asMap(asMap(got["generationConfig"])["thinkingConfig"])
	if tc == nil || asInt(tc["thinkingBudget"]) != ThinkingBudgetLow {
		t.Fatalf("思考强度应映射成 Gemini 的 thinkingBudget=%d，实际 %v", ThinkingBudgetLow, got["generationConfig"])
	}
}

// ---------- R-中4：previous_response_id 显式拒绝 ----------

func TestResponsesPreviousResponseIDRejected(t *testing.T) {
	body := []byte(`{"model":"m","previous_response_id":"resp_123","input":"续上说过的"}`)
	_, err := ResponsesRequestToOpenAIChat(body)
	if !errors.Is(err, ErrUnsupportedContent) {
		t.Fatalf("previous_response_id 应显式报 ErrUnsupportedContent，实际 %v", err)
	}
}

// ---------- R-中5：出站对不认识的内容块显式拒绝 ----------

// 通用语的 input_audio 发 Gemini 应转成 inlineData（Gemini 原生支持音频）。
func TestOpenAIAudioToGeminiUpstream(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[
		{"type":"text","text":"听写这段"},
		{"type":"input_audio","input_audio":{"data":"aGk=","format":"mp3"}}
	]}]}`)
	out, err := OpenAIChatToGeminiRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "audio/mpeg") || !strings.Contains(string(out), "aGk=") {
		t.Fatalf("input_audio 应转成 Gemini inlineData（audio/mpeg）:\n%s", out)
	}
}

// 出站 Responses 不认识 input_audio：显式拒绝而不是静默丢弃。
func TestOpenAIAudioToResponsesRejected(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[
		{"type":"input_audio","input_audio":{"data":"aGk=","format":"wav"}}
	]}]}`)
	_, err := OpenAIChatToResponsesRequest(body)
	if !errors.Is(err, ErrUnsupportedContent) {
		t.Fatalf("Responses 出站应拒绝 input_audio，实际 %v", err)
	}
}

// 出站 Gemini 对未知块类型（file）同样显式拒绝。
func TestOpenAIFileToGeminiRejected(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[
		{"type":"file","file":{"file_id":"file-123"}}
	]}]}`)
	_, err := OpenAIChatToGeminiRequest(body)
	if !errors.Is(err, ErrUnsupportedContent) {
		t.Fatalf("Gemini 出站应拒绝 file 块，实际 %v", err)
	}
}
