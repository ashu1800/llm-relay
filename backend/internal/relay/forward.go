package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"llm-relay/internal/model"
	"llm-relay/internal/relay/convert"
)

// Attempt 表示一次上游调用的结果。
// 流式场景下 Stream 由调用方负责关闭并转发。
type Attempt struct {
	StatusCode int
	Headers    http.Header
	Stream     io.ReadCloser
	Body       []byte
	Usage      Usage
	HasUsage   bool
	HeaderMs   int
	StartedAt  time.Time
}

// Forwarder 执行单次上游调用。
type Forwarder struct {
	client *http.Client
}

// NewForwarder 构造转发器。timeout 只约束「等待响应头」，不限制流式响应体时长。
func NewForwarder(headerTimeout time.Duration) *Forwarder {
	return &Forwarder{client: BuildClient(headerTimeout)}
}

// Do 向上游发起一次请求。
// inboundBody 是客户端原始请求体；injectUsage 为真且为流式时补上 usage 回传开关。
func (f *Forwarder) Do(
	ctx context.Context,
	cand Candidate,
	upstreamPath string,
	inboundBody []byte,
	inboundHeaders http.Header,
	injectUsage bool,
) (*Attempt, error) {
	att := &Attempt{StartedAt: time.Now()}

	body, err := RewriteModel(inboundBody, cand.Binding.UpstreamName, injectUsage)
	if err != nil {
		return nil, fmt.Errorf("改写请求体失败: %w", err)
	}

	// 出站协议转换：站内统一是 OpenAI Chat，渠道声明的是上游协议。
	// 路径也要一起换（Anthropic 是 /v1/messages，Gemini 的模型名在路径里）。
	path, body, err := convert.UpstreamRequest(cand.Channel.Protocol, upstreamPath, body, cand.Binding.UpstreamName)
	if err != nil {
		return nil, fmt.Errorf("转换上游请求失败: %w", err)
	}

	url := BuildUpstreamURL(cand.Channel.BaseURL, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("构造上游请求失败: %w", err)
	}

	// 透传客户端的内容协商相关头，其余一律用我们自己的，避免泄漏或串味。
	//
	// Accept-Encoding 特意不在其中，这不是遗漏：
	// Go 的 http.Transport 只在请求里**没有**这个头时才会自己加上
	// Accept-Encoding: gzip 并透明解压；一旦调用方自己设了（哪怕是 gzip），
	// 传输层就把压缩字节原样交出来。
	//
	// 实测客户端带 Accept-Encoding: gzip 时中继拿到的是 gzip 数据，
	// 解析不出 usage，于是退化成按报文长度估算：
	// 16 个输出 token 被估成 159，费用从 0.00001455 涨到 0.00010125，高了 7 倍。
	// 因为 Content-Encoding 会透传给客户端、浏览器照常解压，
	// 所以客户端看起来一切正常，只有统计和账单是错的。
	// 协议转换路径更糟：正文解析失败会直接产出空回复。
	//
	// 不透传的代价只是「中继到客户端」这一段不压缩，而那一端通常就在本机。
	for _, h := range []string{"Content-Type", "Accept", "User-Agent"} {
		if v := inboundHeaders.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	// Anthropic 的能力开关有一部分是**按请求**用头声明的（例如提示缓存的
	// anthropic-beta、以及客户端在用的 API 版本）。不透传的话，客户端明明在用
	// 提示缓存，转出去的请求里却没有这个头 —— 缓存会静默失效，账单悄悄变贵，
	// 而客户端看到的响应一切正常。所以这两个头按协议透传。
	if cand.Channel.Protocol == model.ProtocolAnthropic {
		for _, h := range []string{"anthropic-beta", "anthropic-version"} {
			if v := inboundHeaders.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
	}
	req.Header.Set("Accept", "text/event-stream, application/json")

	ApplyAuth(req, cand.Channel.Protocol, cand.APIKeyPlain, cand.Channel.CustomMap)
	applyExtraHeaders(req, cand.Channel)

	att.Headers = make(http.Header)
	resp, err := f.client.Do(req)
	att.HeaderMs = int(time.Since(att.StartedAt).Milliseconds())
	if err != nil {
		return att, fmt.Errorf("请求上游失败: %w", err)
	}

	att.StatusCode = resp.StatusCode
	CopyHeaders(att.Headers, resp.Header)

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		// 错误体通常很小，直接读全便于落库与排障
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		att.Body = raw
		return att, nil
	}

	// 依据响应内容类型判定是否流式，而不是只看请求参数
	if isEventStream(resp.Header.Get("Content-Type")) {
		// 上游协议与站内通用语不一致时在这里就地转换：
		// 下游的用量抓取、日志留存与入站改写器看到的都是 OpenAI 的 SSE 分片
		att.Stream = convert.UpstreamStream(cand.Channel.Protocol, resp.Body, cand.Binding.UpstreamName)
		return att, nil
	}

	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return att, fmt.Errorf("读取上游响应失败: %w", err)
	}
	att.Body = convert.UpstreamResponseBody(cand.Channel.Protocol, raw, cand.Binding.UpstreamName)
	att.Usage, att.HasUsage = extractUsageFromJSON(att.Body)
	return att, nil
}

// applyExtraHeaders 写入渠道自定义的附加请求头（如阿里云、火山方舟等特有头）。
func applyExtraHeaders(req *http.Request, ch model.Channel) {
	if ch.ExtraConfig == nil {
		return
	}
	raw, ok := ch.ExtraConfig["headers"]
	if !ok {
		return
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return
	}
	for k, v := range m {
		if s, ok := v.(string); ok && s != "" {
			req.Header.Set(k, s)
		}
	}
}

func isEventStream(contentType string) bool {
	return bytes.Contains(bytes.ToLower([]byte(contentType)), []byte("text/event-stream"))
}

// extractUsageFromJSON 从非流式响应体中提取用量。
func extractUsageFromJSON(raw []byte) (Usage, bool) {
	if len(raw) == 0 {
		return Usage{}, false
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Usage{}, false
	}
	if u, ok := payload["usage"].(map[string]any); ok {
		return NormalizeUsage(u), true
	}
	if u, ok := payload["usageMetadata"].(map[string]any); ok {
		return NormalizeUsage(u), true
	}
	// Anthropic 的 message 结构
	if msg, ok := payload["message"].(map[string]any); ok {
		if u, ok := msg["usage"].(map[string]any); ok {
			return NormalizeUsage(u), true
		}
	}
	// 部分上游用 data 包裹
	if d, ok := payload["data"].(map[string]any); ok {
		if u, ok := d["usage"].(map[string]any); ok {
			return NormalizeUsage(u), true
		}
	}
	return Usage{}, false
}

// Retryable 判断该次失败是否值得换渠道重试。
// 4xx 中只有 408/409/429 属于可重试，其余是请求本身的问题，换渠道也没用。
func (a *Attempt) Retryable() bool {
	if a == nil {
		return true
	}
	switch {
	case a.StatusCode == 0: // 网络层失败
		return true
	case a.StatusCode == 408, a.StatusCode == 409, a.StatusCode == 429:
		return true
	case a.StatusCode >= 500:
		return true
	default:
		return false
	}
}
