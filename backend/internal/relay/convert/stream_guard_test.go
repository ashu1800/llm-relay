package convert

import (
	"encoding/json"
	"errors"
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

// ---------- Anthropic 上游流的同款守卫 ----------

const anthropicTruncatedBody = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"m","usage":{"input_tokens":3}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"半句话"}}

`

const anthropicCompleteBody = anthropicTruncatedBody + `event: message_stop
data: {"type":"message_stop"}

`

// 已见 message_start 却没等到 message_stop 的干净 EOF 是截断，
// 且已收到的正文必须照常交付（修复前补成 stop+[DONE] 伪装成答完）。
func TestAnthropicUpstreamCleanEOFWithoutStopIsTruncation(t *testing.T) {
	s := NewAnthropicStreamToOpenAIChat(io.NopCloser(strings.NewReader(anthropicTruncatedBody)), "m")
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
		t.Fatalf("干净断开且没有 message_stop 应报 io.ErrUnexpectedEOF，实际 %v", readErr)
	}
	if !strings.Contains(string(out), "半句话") {
		t.Fatalf("已收到的正文不应丢失:\n%s", out)
	}
}

// 有 message_stop 的正常收尾不报错、分片齐全。
func TestAnthropicUpstreamWithMessageStopIsNormal(t *testing.T) {
	s := NewAnthropicStreamToOpenAIChat(io.NopCloser(strings.NewReader(anthropicCompleteBody)), "m")
	out, err := io.ReadAll(s)
	if err != nil {
		t.Fatalf("正常收尾不应报错: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "[DONE]") || !strings.Contains(got, "半句话") {
		t.Fatalf("正常收尾应带正文与 [DONE]:\n%s", got)
	}
}

// ---------- Responses 上游流的同款守卫 ----------

const responsesTruncatedBody = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","model":"m"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"半句话"}

`

const responsesCompleteBody = responsesTruncatedBody + `event: response.completed
data: {"type":"response.completed","response":{"status":"completed"}}

`

func TestResponsesUpstreamCleanEOFWithoutCompleteIsTruncation(t *testing.T) {
	s := NewResponsesStreamToOpenAIChat(io.NopCloser(strings.NewReader(responsesTruncatedBody)), "m")
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
		t.Fatalf("干净断开且没有终止事件应报 io.ErrUnexpectedEOF，实际 %v", readErr)
	}
	if !strings.Contains(string(out), "半句话") {
		t.Fatalf("已收到的正文不应丢失:\n%s", out)
	}
}

func TestResponsesUpstreamWithCompleteIsNormal(t *testing.T) {
	s := NewResponsesStreamToOpenAIChat(io.NopCloser(strings.NewReader(responsesCompleteBody)), "m")
	out, err := io.ReadAll(s)
	if err != nil {
		t.Fatalf("正常收尾不应报错: %v", err)
	}
	if got := string(out); !strings.Contains(got, "[DONE]") || !strings.Contains(got, "半句话") {
		t.Fatalf("正常收尾应带正文与 [DONE]:\n%s", got)
	}
}

// response.failed 必须发错误分片给客户端：修复前它与 completed 同路处理，
// error 字段被丢弃、补出 stop+[DONE]，服务端生成中途失败被伪装成正常完成。
func TestResponsesUpstreamFailedEmitsErrorChunk(t *testing.T) {
	body := `event: response.failed
data: {"type":"response.failed","response":{"status":"failed","error":{"code":"quota","message":"额度不足"}}}

`
	s := NewResponsesStreamToOpenAIChat(io.NopCloser(strings.NewReader(body)), "m")
	out, err := io.ReadAll(s)
	// failed 后干净关闭：错误分片已交付，EOF 属正常收尾（sawStop 已置位）
	if err != nil {
		t.Fatalf("failed 之后的 EOF 不应再报错: %v", err)
	}
	if got := string(out); !strings.Contains(got, "额度不足") || !strings.Contains(got, "upstream_error") {
		t.Fatalf("failed 应发出带原因的错误分片:\n%s", got)
	}
}

// ---------- Gemini 入站多模态（R-高2） ----------

// inlineData 图片必须转成 image_url 的 data URI：修复前被无声跳过，
// 模型收不到图按纯文本作答，无任何报错。
func TestGeminiRequestInlineDataImage(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[` +
		`{"text":"看图说话"},` +
		`{"inlineData":{"mimeType":"image/png","data":"aGk="}}]}]}`)
	out, err := GeminiRequestToOpenAIChat(body, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("应有一条消息，实际 %d", len(msgs))
	}
	content, ok := asMap(msgs[0])["content"].([]any)
	if !ok {
		t.Fatalf("带图消息的 content 应是块数组，实际 %T", asMap(msgs[0])["content"])
	}
	if len(content) != 2 {
		t.Fatalf("文本 + 图片两块，实际 %d 块", len(content))
	}
	img := asMap(content[1])
	if asString(img["type"]) != "image_url" {
		t.Fatalf("第二块应是 image_url，实际 %v", img["type"])
	}
	url := asString(asMap(img["image_url"])["url"])
	if url != "data:image/png;base64,aGk=" {
		t.Fatalf("data URI 拼接错误: %q", url)
	}
	// 纯文本块的形状不受影响：数组里混排的 text 块保留原样
	if asString(asMap(content[0])["text"]) != "看图说话" {
		t.Fatalf("文本块丢失:\n%s", out)
	}
}

// 纯文本请求的 content 仍然拍平成字符串（兼容面最广的形状不能变）。
func TestGeminiRequestPlainTextStillFlattens(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"你好"},{"text":"呀"}]}]}`)
	out, err := GeminiRequestToOpenAIChat(body, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	msgs, _ := got["messages"].([]any)
	if c, ok := asMap(msgs[0])["content"].(string); !ok || c != "你好呀" {
		t.Fatalf("纯文本应拍平成字符串，实际 %v", asMap(msgs[0])["content"])
	}
}

// fileData 是 Files API 的文件引用，中继没有那份文件 —— 明确拒绝
// 而不是静默丢掉让模型当纯文本作答。
func TestGeminiRequestFileDataRejected(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"fileData":{"mimeType":"application/pdf","fileUri":"files/abc"}}]}]}`)
	_, err := GeminiRequestToOpenAIChat(body, "m", false)
	if !errorIsUnsupported(err) {
		t.Fatalf("fileData 应显式报 ErrUnsupportedContent，实际 %v", err)
	}
}

// 不认识的 inlineData MIME（如 PDF 内联）同样显式拒绝。
func TestGeminiRequestInlinePDFRejected(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"application/pdf","data":"aGk="}}]}]}`)
	_, err := GeminiRequestToOpenAIChat(body, "m", false)
	if !errorIsUnsupported(err) {
		t.Fatalf("PDF inlineData 应显式报 ErrUnsupportedContent，实际 %v", err)
	}
}

func errorIsUnsupported(err error) bool { return errors.Is(err, ErrUnsupportedContent) }
