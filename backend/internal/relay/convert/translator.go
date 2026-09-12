package convert

import (
	"encoding/json"
	"io"
)

// DefaultErrorBody 生成 OpenAI 结构的错误响应体，供各适配器复用。
func DefaultErrorBody(status int, message string) []byte {
	raw, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": message, "type": "upstream_error", "code": status},
	})
	return raw
}

// NewAnthropicTranslator 按是否流式返回对应的改写器。
// 非流式先缓冲整个响应体，再一次性改写成 Anthropic 结构。
func NewAnthropicTranslator(w io.Writer, model string, stream bool) Translator {
	if stream {
		return NewAnthropicStreamTranslator(w, model)
	}
	return &bodyTranslator{w: w, model: model, conv: OpenAIChatToAnthropicResponse}
}

// bodyTranslator 缓冲完整响应体后一次性转换，适用于非流式场景。
type bodyTranslator struct {
	w     io.Writer
	model string
	conv  func([]byte, string) ([]byte, error)
	buf   []byte
}

// Write 累积响应体。
func (t *bodyTranslator) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	return len(p), nil
}

// Close 转换并写出。
func (t *bodyTranslator) Close() error {
	out, err := t.conv(t.buf, t.model)
	if err != nil {
		return err
	}
	_, err = t.w.Write(out)
	return err
}
