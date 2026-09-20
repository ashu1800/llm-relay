package convert

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// 上游在 2xx 流内发出 error 分片（通用语形态 data: {"error":{...}}）时，
// 三个入站改写器都必须：把流按失败收尾（发该协议的错误事件）、返回
// ErrUpstreamReported 让转发层记 502，且绝不补「正常结束」标记 ——
// 修复前三个改写器都只认 choices/candidates，error 分片被吞，
// Close 补出正常结束，半截回复伪装成完整成功。

const errorShardOKThenErr = `data: {"choices":[{"delta":{"content":"半句"}}]}

data: {"error":{"message":"overloaded_error","code":529}}

`

// TestAnthropicStreamErrorShardFailsStream 验证 Anthropic 入站：
// 收到 error 事件、没有 message_stop。
func TestAnthropicStreamErrorShardFailsStream(t *testing.T) {
	var out bytes.Buffer
	tr := NewAnthropicStreamTranslator(&out, "m")
	_, err := tr.Write([]byte(errorShardOKThenErr))
	if !errors.Is(err, ErrUpstreamReported) {
		t.Fatalf("应返回 ErrUpstreamReported，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "overloaded_error") {
		t.Fatalf("哨兵错误应带上游原始 message:\n%v", err)
	}
	got := out.String()
	if !strings.Contains(got, `"type":"error"`) {
		t.Fatalf("应向客户端下发 error 事件:\n%s", got)
	}
	if strings.Contains(got, "message_stop") {
		t.Fatalf("绝不能补 message_stop 把截断伪装成正常结束:\n%s", got)
	}
	if !strings.Contains(got, "半句") {
		t.Fatalf("错误前的正文应已交付:\n%s", got)
	}
}

// TestGeminiStreamErrorShardFailsStream 验证 Gemini 入站：
// 收到 error 结构、没有补 finishReason。
func TestGeminiStreamErrorShardFailsStream(t *testing.T) {
	var out bytes.Buffer
	tr := NewGeminiStreamTranslator(&out, "m")
	_, err := tr.Write([]byte(errorShardOKThenErr))
	if !errors.Is(err, ErrUpstreamReported) {
		t.Fatalf("应返回 ErrUpstreamReported，实际 %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `"error"`) {
		t.Fatalf("应向客户端下发 error 结构:\n%s", got)
	}
	if strings.Contains(got, "finishReason") {
		t.Fatalf("绝不能补 finishReason 把截断伪装成正常结束:\n%s", got)
	}
}

// TestResponsesStreamErrorShardFailsStream 验证 Responses 入站：
// 收到 error 事件、没有 response.completed。
func TestResponsesStreamErrorShardFailsStream(t *testing.T) {
	var out bytes.Buffer
	tr := NewResponsesStreamTranslator(&out, "m")
	_, err := tr.Write([]byte(errorShardOKThenErr))
	if !errors.Is(err, ErrUpstreamReported) {
		t.Fatalf("应返回 ErrUpstreamReported，实际 %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `"type":"error"`) {
		t.Fatalf("应向客户端下发 error 事件:\n%s", got)
	}
	if strings.Contains(got, "response.completed") {
		t.Fatalf("绝不能补 response.completed 把截断伪装成正常结束:\n%s", got)
	}
}

// TestStreamAbortIsIdempotent 错误事件只发一次：流内 error 与转发层收尾
// 都可能调 Abort，第二次必须静默返回。
func TestStreamAbortIsIdempotent(t *testing.T) {
	var out bytes.Buffer
	tr := NewAnthropicStreamTranslator(&out, "m")
	if err := tr.Abort("第一次"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Abort("第二次"); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out.String(), `"type":"error"`); n != 1 {
		t.Fatalf("error 事件应只发一次，实际 %d 次:\n%s", n, out.String())
	}
}
