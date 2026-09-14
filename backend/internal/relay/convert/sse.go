package convert

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
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
	// dropped 记录因为超过 MaxSSELine 而被丢弃的字节数（供日志/测试观察）
	dropped int
}

// MaxSSELine 是单条 SSE 行的缓冲上限。
//
// 没有这个上限时，只要上游持续不发换行符，缓冲区就会无界增长：
// 紧凑拷贝那段只在「已经出现并消费过 \n」之后才有机会触发，而这里的前提
// 恰恰是一个换行都没有。实测连续喂入 4 MiB 无换行数据，缓冲区就是 4 MiB。
// 单进程自用服务会一路涨到 OOM，并同时打挂在途的所有请求。
//
// 1 MiB 远大于正常帧（一张 base64 图片或一个长 tool 参数也就几十 KB），
// 真超了说明上游行为异常，丢弃到下一个换行比继续吞内存安全。
const MaxSSELine = 1 << 20

// LineSplitter 是给包外（relay.UsageTee）复用的行切分器。
//
// 抽出来的原因：同样的「按 \n 切行 + 紧凑拷贝」逻辑此前在 convert 与
// relay 两处各写了一份，修内存上限时很容易只改一处。
type LineSplitter struct {
	buf []byte
}

// Feed 喂入一段字节，对每个完整行回调（行尾的 \r 已去掉）。
func (s *LineSplitter) Feed(p []byte, onLine func([]byte)) {
	// 先看这一段里有没有换行。没有、且加上去会超限时直接丢弃 ——
	// 不这么做的话 append 会先把整段（可能很大）复制进缓冲区，
	// 上限就形同虚设了。
	if !bytes.ContainsRune(p, '\n') && len(s.buf)+len(p) > MaxSSELine {
		s.buf = nil
		return
	}
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
	if len(s.buf) > MaxSSELine {
		s.buf = nil
		return
	}
	if len(s.buf) == 0 {
		s.buf = nil
	} else if cap(s.buf) > 4096 && cap(s.buf) > 4*len(s.buf) {
		s.buf = append([]byte(nil), s.buf...)
	}
}

// Flush 处理结尾没有换行符的残留。
func (s *LineSplitter) Flush(onLine func([]byte)) {
	if len(s.buf) > 0 {
		onLine(bytes.TrimRight(s.buf, "\r"))
		s.buf = nil
	}
}

// feed 喂入一段字节，对每个完整行回调。
func (s *sseSplitter) feed(p []byte, onLine func([]byte)) {
	// 与 LineSplitter.Feed 同理：整段无换行且加上去会超限时直接丢弃，
	// 否则 append 会先把大段数据复制进来，上限就白设了。
	if !bytes.ContainsRune(p, '\n') && len(s.buf)+len(p) > MaxSSELine {
		s.dropped += len(s.buf) + len(p)
		s.buf = nil
		return
	}
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
	// 超限丢弃：整段还没出现换行，说明这是一条异常长的行。
	// 丢掉已缓存的部分并继续往后续读到换行，避免无界增长。
	if len(s.buf) > MaxSSELine {
		s.dropped += len(s.buf)
		s.buf = nil
		return
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
//
// 也处理 json.Number 与数字字符串：encode/json 默认把数字解成 float64，
// 但一旦哪处解码用了 UseNumber()（relay.getInt 就专门处理了这种情形），
// 数字会变成 json.Number —— 不认它的话 max_tokens 这类字段会静默变 0，
// 表现为「限长参数被忽略」而不是报错，很难查。
// 字符串形态同理：有些客户端会把 max_tokens 写成 "100"。
func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
		// 不是整数（例如 "1.5"）时退回浮点，避免整段丢失
		if f, err := n.Float64(); err == nil {
			return int(f)
		}
		return 0
	case string:
		if s := strings.TrimSpace(n); s != "" {
			if i, err := strconv.Atoi(s); err == nil {
				return i
			}
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				return int(f)
			}
		}
		return 0
	default:
		return 0
	}
}
