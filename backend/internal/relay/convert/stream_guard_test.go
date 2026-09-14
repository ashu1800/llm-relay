package convert

import (
	"io"
	"strings"
	"testing"
)

// 上游在没给过 finishReason 的情况下干净断开，必须报成截断而不是正常结束。
//
// 修复前：stopReason 为空时被兜底成 "stop" 并补出 [DONE]，而 relay_handler
// 只把非 EOF 错误当截断 —— 于是「答到一半断了」被完整伪装成「模型答完了」，
// 客户端与计费都按成功处理。
func TestGeminiUpstreamCleanEOFWithoutFinishIsTruncation(t *testing.T) {
	// 只有正文分片，一帧 finishReason 都没有
	body := `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"半句话"}]},"index":0}]}` + "\n\n"
	src := io.NopCloser(strings.NewReader(body))
	s := NewGeminiStreamToOpenAIChat(src, "m")

	// 用循环读而不是 io.ReadAll：后者会把已读到的内容一并丢掉，
	// 这里要同时断言「内容仍交付」与「错误码表示截断」。
	var out []byte
	buf := make([]byte, 512)
	var readErr error
	for {
		n, err := s.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			readErr = err
			break
		}
	}
	if readErr != io.ErrUnexpectedEOF {
		t.Fatalf("干净断开且没有 finishReason 应报 io.ErrUnexpectedEOF，实际 %v", readErr)
	}
	if !strings.Contains(string(out), "半句话") {
		t.Fatalf("已收到的正文不应丢失:\n%s", out)
	}
}

// 上游明确给了 finishReason 后断开，是正常收尾。
func TestGeminiUpstreamWithFinishReasonIsNormal(t *testing.T) {
	body := `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"完整"}]},"finishReason":"STOP","index":0}]}` + "\n\n"
	src := io.NopCloser(strings.NewReader(body))
	s := NewGeminiStreamToOpenAIChat(src, "m")

	out, err := io.ReadAll(s)
	if err != nil {
		t.Fatalf("有 finishReason 的正常收尾不应报错: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"finish_reason":"stop"`) {
		t.Fatalf("正常收尾应带 finish_reason:\n%s", got)
	}
	if !strings.Contains(got, "[DONE]") {
		t.Fatalf("正常收尾应带 [DONE]:\n%s", got)
	}
}

// 上游报了工具调用（finishReason: STOP 之外的正常信号）也算已知结束。
func TestGeminiUpstreamFunctionCallCountsAsFinish(t *testing.T) {
	body := `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"f","args":{}}}]},"finishReason":"STOP","index":0}]}` + "\n\n"
	s := NewGeminiStreamToOpenAIChat(io.NopCloser(strings.NewReader(body)), "m")
	out, err := io.ReadAll(s)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if !strings.Contains(string(out), "tool_calls") {
		t.Fatalf("工具调用应被转出:\n%s", out)
	}
}

// 行缓冲必须有上限：持续喂入不带换行的数据不能让内存无界增长。
//
// 小数据量仍然要缓冲（跨分片的行必须拼起来才能解析），所以断言的是
// 「始终不超过上限」，而不是「立刻清空」。
func TestLineSplitterBoundedWithoutNewline(t *testing.T) {
	var s LineSplitter
	chunk := make([]byte, 64*1024)
	for i := range chunk {
		chunk[i] = 'x'
	}
	var lines int
	maxSeen := 0
	// 喂 64 MiB。没有上限时缓冲区会一路涨到 64 MiB。
	for i := 0; i < 1024; i++ {
		s.Feed(chunk, func([]byte) { lines++ })
		if len(s.buf) > maxSeen {
			maxSeen = len(s.buf)
		}
		if len(s.buf) > MaxSSELine {
			t.Fatalf("第 %d 块后缓冲区超过上限：%d > %d", i, len(s.buf), MaxSSELine)
		}
	}
	if lines != 0 {
		t.Fatalf("没有换行不该产生任何完整行，实际 %d", lines)
	}
	// 关键断言：喂了 64 MiB 之后，峰值占用必须远小于喂入量
	if maxSeen > MaxSSELine {
		t.Fatalf("峰值缓冲 %d 超过上限 %d", maxSeen, MaxSSELine)
	}
}

// 正常的多行输入仍然逐行切分。
func TestLineSplitterStillSplits(t *testing.T) {
	var s LineSplitter
	var got []string
	s.Feed([]byte("a\nb\r\nc"), func(l []byte) { got = append(got, string(l)) })
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("切分结果不对: %v", got)
	}
	s.Flush(func(l []byte) { got = append(got, string(l)) })
	if len(got) != 3 || got[2] != "c" {
		t.Fatalf("flush 后应拿到残留行: %v", got)
	}
}

// 跨越多个分片的单行要被正确拼起来。
func TestLineSplitterJoinsAcrossWrites(t *testing.T) {
	var s LineSplitter
	var got []string
	s.Feed([]byte("he"), func(l []byte) { got = append(got, string(l)) })
	s.Feed([]byte("llo\n"), func(l []byte) { got = append(got, string(l)) })
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("跨分片的行应被拼起来: %v", got)
	}
}
