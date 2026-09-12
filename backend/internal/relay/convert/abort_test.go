package convert

import (
	"bytes"
	"strings"
	"testing"
)

// 上游流中断时，各协议下发的必须是**错误型终止事件**，而不是正常结束标记。
//
// 这是「截断被伪装成完整回复」的核心防线：一份少了一半的回复，
// 如果结尾带着 message_stop / response.completed / finishReason，
// 客户端（Claude Code 等）会认为生成正常完成，用户完全无从察觉。
func TestAbortSendsErrorNotNormalCompletion(t *testing.T) {
	const reason = "上游连接在响应完成前中断，本次回复不完整"
	// 先喂一个正常分片，让改写器进入「已经开始输出」的状态 ——
	// 这正是真实中断发生的位置
	chunk := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")

	cases := []struct {
		name       string
		make       func(w *bytes.Buffer) Aborter
		wantSub    string
		unwantSubs []string
	}{
		{
			name: "Anthropic",
			make: func(w *bytes.Buffer) Aborter {
				return NewAnthropicStreamTranslator(w, "test-model")
			},
			wantSub:    "event: error",
			unwantSubs: []string{"message_stop", "message_delta"},
		},
		{
			name: "OpenAI Responses",
			make: func(w *bytes.Buffer) Aborter {
				return NewResponsesStreamTranslator(w, "test-model")
			},
			wantSub:    "event: error",
			unwantSubs: []string{"response.completed"},
		},
		{
			name: "Gemini",
			make: func(w *bytes.Buffer) Aborter {
				return NewGeminiStreamTranslator(w, "test-model")
			},
			wantSub:    "\"error\"",
			unwantSubs: []string{"finishReason"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			ab := c.make(&buf)
			// Aborter 与 Translator 是同一批对象，这里按接口取用
			tr, ok := ab.(Translator)
			if !ok {
				t.Fatal("改写器必须同时实现 Translator")
			}
			if _, err := tr.Write(chunk); err != nil {
				t.Fatalf("写入分片失败: %v", err)
			}
			if err := ab.Abort(reason); err != nil {
				t.Fatalf("Abort 失败: %v", err)
			}

			got := buf.String()
			if !strings.Contains(got, c.wantSub) {
				t.Errorf("应下发错误终止事件（含 %s），实际输出:\n%s", c.wantSub, got)
			}
			if !strings.Contains(got, reason) {
				t.Errorf("错误事件应带上说明文字，实际输出:\n%s", got)
			}
			for _, bad := range c.unwantSubs {
				if strings.Contains(got, bad) {
					t.Errorf("中断时不得出现正常结束标记 %q，否则客户端会把截断当成完整回复。实际输出:\n%s", bad, got)
				}
			}
		})
	}
}

// Close 的语义是「上游正常结束」，仍然要补全结束标记 —— 客户端靠它判断流结束。
// 不能因为引入了 Abort 就把 Close 的行为也改掉了。
func TestCloseStillCompletesNormally(t *testing.T) {
	var buf bytes.Buffer
	tr := NewAnthropicStreamTranslator(&buf, "test-model")
	if _, err := tr.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "message_stop") {
		t.Errorf("正常结束时必须补 message_stop，否则客户端会一直等。实际输出:\n%s", got)
	}
	if strings.Contains(got, "event: error") {
		t.Errorf("正常结束不应出现 error 事件。实际输出:\n%s", got)
	}
}

// 同协议透传不需要 Aborter：上游的结束标记本身就没发出来，
// 客户端靠它的缺失即可判断截断。
func TestPassthroughIsNotAborter(t *testing.T) {
	var buf bytes.Buffer
	p := NewPassthrough(&buf)
	if _, ok := any(p).(Aborter); ok {
		t.Error("Passthrough 不应实现 Aborter：透传场景下截断靠缺失结束标记即可判断")
	}
}
