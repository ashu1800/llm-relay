package convert

import (
	"bytes"
	"encoding/json"
	"io"
)

// Translator 把上游返回的字节流改写成目标协议的字节流。
// 同协议场景用 Passthrough 直接转发，零改写开销。
type Translator interface {
	Write(p []byte) (int, error)
	Close() error
}

// Passthrough 原样转发。
type Passthrough struct{ w io.Writer }

// NewPassthrough 构造透传改写器。
func NewPassthrough(w io.Writer) *Passthrough { return &Passthrough{w: w} }

// Write 直接写入下游。
func (p *Passthrough) Write(b []byte) (int, error) { return p.w.Write(b) }

// Close 无收尾动作。
func (p *Passthrough) Close() error { return nil }

// sseSplitter 从任意分片的字节流里切出完整的 SSE 行。
// 上游分片边界与事件边界无关，必须自行缓冲拼接。
type sseSplitter struct {
	buf []byte
}

// feed 喂入一段字节，对每个完整行回调。
func (s *sseSplitter) feed(p []byte, onLine func([]byte)) {
	s.buf = append(s.buf, p...)
	for {
		idx := bytes.IndexByte(s.buf, '\n')
		if idx < 0 {
			break
		}
		line := s.buf[:idx]
		s.buf = s.buf[idx+1:]
		onLine(bytes.TrimRight(line, "\r"))
	}
	// 已消费前缀长期占用底层数组时做一次紧凑拷贝
	if len(s.buf) == 0 {
		s.buf = nil
	} else if cap(s.buf) > 4096 && cap(s.buf) > 4*len(s.buf) {
		s.buf = append([]byte(nil), s.buf...)
	}
}

// flush 处理结尾没有换行符的残留。
func (s *sseSplitter) flush(onLine func([]byte)) {
	if len(s.buf) > 0 {
		onLine(bytes.TrimRight(s.buf, "\r"))
		s.buf = nil
	}
}

// dataPayload 从 SSE 行里取出 data 字段的 JSON 内容。
// 非 data 行（event:/id:/注释/空行）返回 nil。
func dataPayload(line []byte) []byte {
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	if len(payload) == 0 || payload[0] != '{' {
		return nil
	}
	return payload
}

// Aborter 由「需要区分正常收尾与异常中断」的改写器实现。
//
// 为什么必须区分：上游流中断时如果仍补发协议的正常结束标记，客户端会把截断的
// 回复当成完整回复 —— 内容少了一半却没有任何错误信号，用户看到的是「模型答到
// 一半停了」而系统显示成功。反过来什么都不发，客户端会一直等下去。
// 所以异常收尾发的是**错误型终止事件**：既结束等待，又明确说明这次不完整。
//
// 同协议透传（Passthrough）不需要实现：上游的结束标记本身就没发出来，
// 客户端靠它的缺失即可判断截断。
type Aborter interface {
	Abort(reason string) error
}

// StreamAbortedMessage 是上游中断时下发给客户端的说明。
const StreamAbortedMessage = "上游连接在响应完成前中断，本次回复不完整"

// writeSSE 以 Anthropic 的 event+data 形式写出一个事件。
func writeSSE(w io.Writer, event string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, "event: "+event+"\n"); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "data: "); err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n\n")
	return err
}

// asMap 安全地把任意值转成字符串键映射。
func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// asString 安全取字符串。
func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// asInt 安全取整数，兼容 JSON 的 float64。
func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}
