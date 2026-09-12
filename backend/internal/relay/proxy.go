package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"llm-relay/internal/model"
)

// Prepared 是发往上游的请求描述。
type Prepared struct {
	URL     string
	Method  string
	Headers http.Header
	Body    []byte
}

// BuildUpstreamURL 拼接上游地址。
// 兼容两种 BaseURL 写法：带 /v1 后缀（https://host/v1）与不带（https://host）。
func BuildUpstreamURL(baseURL, path string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return path
	}
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1/") {
		return base + strings.TrimPrefix(path, "/v1")
	}
	if strings.HasSuffix(base, "/v1beta") && strings.HasPrefix(path, "/v1beta/") {
		return base + strings.TrimPrefix(path, "/v1beta")
	}
	return base + path
}

// ApplyAuth 按协议写入鉴权头。
func ApplyAuth(req *http.Request, protocol, apiKey string, custom model.JSONMap) {
	if apiKey == "" {
		return
	}
	switch protocol {
	case model.ProtocolAnthropic:
		req.Header.Set("x-api-key", apiKey)
		if req.Header.Get("anthropic-version") == "" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
	case model.ProtocolGemini:
		req.Header.Set("x-goog-api-key", apiKey)
	case model.ProtocolCustom:
		header := "Authorization"
		prefix := "Bearer "
		if custom != nil {
			if h, ok := custom["auth_header"].(string); ok && h != "" {
				header = h
			}
			if p, ok := custom["auth_prefix"].(string); ok {
				prefix = p
			}
		}
		req.Header.Set(header, prefix+apiKey)
	default: // openai-chat / openai-responses / openai-embeddings
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

// RewriteModel 把请求体里的模型名替换为上游真实模型名，并按需注入 usage 回传开关。
// 保留其余字段原样，避免破坏厂商扩展参数。
func RewriteModel(raw []byte, upstreamModel string, injectStreamUsage bool) ([]byte, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("请求体不是合法 JSON: %w", err)
	}
	if upstreamModel != "" {
		payload["model"] = upstreamModel
	}
	if injectStreamUsage {
		if isStream, _ := payload["stream"].(bool); isStream {
			opts, _ := payload["stream_options"].(map[string]any)
			if opts == nil {
				opts = map[string]any{}
			}
			if _, exists := opts["include_usage"]; !exists {
				opts["include_usage"] = true
			}
			payload["stream_options"] = opts
		}
	}
	return json.Marshal(payload)
}

// ExtractModel 读取请求体里的模型名。
func ExtractModel(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return payload.Model
}

// ExtractStream 判断是否为流式请求。
func ExtractStream(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var payload struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	return payload.Stream
}

// UsageTee 在透传字节流的同时旁路解析 SSE，抓取上游返回的 usage。
// 它只观察、不改写，因此对流式转发的延迟没有影响。
type UsageTee struct {
	buf     []byte
	usage   Usage
	gotAny  bool
	total   int
	onUsage func(Usage)
}

// Bytes 返回已透传的字节数，供缺少 usage 时兜底估算输出长度。
func (t *UsageTee) Bytes() int { return t.total }

// NewUsageTee 构造旁路解析器。
func NewUsageTee(onUsage func(Usage)) *UsageTee {
	return &UsageTee{onUsage: onUsage}
}

// Usage 返回当前已解析到的用量。
func (t *UsageTee) Usage() (Usage, bool) { return t.usage, t.gotAny }

// Write 记录透传的字节并解析其中的完整行。
func (t *UsageTee) Write(p []byte) (int, error) {
	t.total += len(p)
	t.buf = append(t.buf, p...)
	for {
		idx := bytes.IndexByte(t.buf, '\n')
		if idx < 0 {
			break
		}
		t.handleLine(t.buf[:idx])
		t.buf = t.buf[idx+1:]
	}
	// 已消费前缀长期占用底层数组时做一次紧凑拷贝，避免内存持续增长
	if len(t.buf) == 0 {
		t.buf = nil
	} else if cap(t.buf) > 4096 && cap(t.buf) > 4*len(t.buf) {
		t.buf = append([]byte(nil), t.buf...)
	}
	return len(p), nil
}

// Flush 处理最后一行没有换行符的残留数据。
func (t *UsageTee) Flush() {
	if len(t.buf) > 0 {
		t.handleLine(t.buf)
		t.buf = nil
	}
}

func (t *UsageTee) handleLine(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	// 同时兼容 SSE（data: 前缀）与裸 JSON 行（部分自定义上游）
	payload := line
	if bytes.HasPrefix(line, []byte("data:")) {
		payload = bytes.TrimSpace(line[len("data:"):])
	}
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	if payload[0] != '{' {
		return
	}
	var event map[string]any
	if err := json.Unmarshal(payload, &event); err != nil {
		return
	}

	merged := false
	// OpenAI 风格：usage 在事件顶层
	if raw, ok := event["usage"].(map[string]any); ok {
		t.mergeUsage(NormalizeUsage(raw))
		merged = true
	}
	// Gemini 风格
	if raw, ok := event["usageMetadata"].(map[string]any); ok {
		t.mergeUsage(NormalizeUsage(raw))
		merged = true
	}
	// Anthropic 风格：message_start 里嵌在 message.usage
	if msg, ok := event["message"].(map[string]any); ok {
		if raw, ok := msg["usage"].(map[string]any); ok {
			t.mergeUsage(NormalizeUsage(raw))
			merged = true
		}
	}
	if merged && t.onUsage != nil {
		t.onUsage(t.usage)
	}
}

// mergeUsage 合并增量用量。Anthropic 的输入在 message_start、输出在 message_delta，
// 因此取各字段的非零值而非整体覆盖。
func (t *UsageTee) mergeUsage(u Usage) {
	if u.PromptTokens > 0 {
		t.usage.PromptTokens = u.PromptTokens
	}
	if u.CachedTokens > 0 {
		t.usage.CachedTokens = u.CachedTokens
	}
	if u.CacheCreationTokens > 0 {
		t.usage.CacheCreationTokens = u.CacheCreationTokens
	}
	if u.CompletionTokens > 0 {
		t.usage.CompletionTokens = u.CompletionTokens
	}
	if u.ReasoningTokens > 0 {
		t.usage.ReasoningTokens = u.ReasoningTokens
	}
	if u.TotalTokens > 0 {
		t.usage.TotalTokens = u.TotalTokens
	}
	if t.usage.TotalTokens == 0 {
		t.usage.TotalTokens = t.usage.PromptTokens + t.usage.CompletionTokens + t.usage.CachedTokens
	}
	t.gotAny = true
}

// BuildClient 构造上游 HTTP 客户端。流式请求不能设整体超时，只能约束握手阶段。
func BuildClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: 0,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   20,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: timeout,
			ExpectContinueTimeout: 2 * time.Second,
			ForceAttemptHTTP2:     true,
		},
	}
}

// CopyHeaders 复制上游响应头到下游，过滤掉逐跳头。
func CopyHeaders(dst http.Header, src http.Header) {
	hopByHop := map[string]bool{
		"Connection":          true,
		"Keep-Alive":          true,
		"Proxy-Authenticate":  true,
		"Proxy-Authorization": true,
		"Te":                  true,
		"Trailer":             true,
		"Transfer-Encoding":   true,
		"Upgrade":             true,
		"Content-Length":      true,
	}
	for k, vals := range src {
		if hopByHop[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}
