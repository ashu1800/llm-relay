package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"llm-relay/internal/model"
	"llm-relay/internal/proxy"
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

// ProxyResolver 按 id 取出站代理配置。
//
// 失败时返回 error 而不是 bool：不存在、被停用、密码解不开这三种情况
// 的排障方向完全不同，压成一个 bool 之后用户只会看到
// 「代理 #N 不存在或已停用」，然后跑去代理列表里找一个其实存在、
// 只是密文解不开的代理。
type ProxyResolver func(id uint) (proxy.Config, error)

// Forwarder 执行单次上游调用。
//
// 客户端按「出站代理」分开缓存：代理是 per-channel/per-model 的配置，
// 共用一个 http.Client 就没法让不同的渠道走不同的出口。
// 每个代理一个 client 也顺带保住了各自的连接池。
type Forwarder struct {
	headerTimeout time.Duration
	// bodyTimeout 是非流式调用的总时长上限；流式请求不受它约束
	bodyTimeout time.Duration
	base        *http.Client

	mu      sync.Mutex
	proxied map[uint]*http.Client
	// proxiedCfg 与 proxied 同步维护：翻译错误信息时要说明连的是哪个代理
	proxiedCfg map[uint]proxy.Config
	resolve    ProxyResolver
}

// NewForwarder 构造转发器。headerTimeout 只约束「等待响应头」；
// bodyTimeout 给非流式调用兜一个总时长上限，0 表示不设。流式正文两者都不限制。
func NewForwarder(headerTimeout, bodyTimeout time.Duration) *Forwarder {
	return &Forwarder{
		headerTimeout: headerTimeout,
		bodyTimeout:   bodyTimeout,
		base:          BuildClient(headerTimeout),
		proxied:       make(map[uint]*http.Client),
		proxiedCfg:    make(map[uint]proxy.Config),
	}
}

// SetProxyResolver 注入「按 id 查代理配置」的能力。
//
// 用回调而不是直接持有 store：relay 包不该知道数据库长什么样，
// 而且测试里塞一个假实现就能验证「代理不可用时绝不直连」。
func (f *Forwarder) SetProxyResolver(fn ProxyResolver) {
	f.mu.Lock()
	old := f.proxied
	f.resolve = fn
	// 换了数据源，旧的缓存一律作废
	f.proxied = make(map[uint]*http.Client)
	f.proxiedCfg = make(map[uint]proxy.Config)
	f.mu.Unlock()
	closeIdle(old)
}

// closeIdle 关掉被丢弃的客户端的空闲连接。
//
// 只从 map 里删掉是不够的：旧 Transport 的空闲连接要等 IdleConnTimeout（90 秒）
// 才回收，反复改代理配置会短时间堆出一批多余的连接与 goroutine。
func closeIdle(clients map[uint]*http.Client) {
	for _, c := range clients {
		if tr, ok := c.Transport.(*http.Transport); ok {
			tr.CloseIdleConnections()
		}
	}
}

// InvalidateProxy 丢弃某个代理缓存的客户端。
// 代理的地址、端口、密码一改，必须调用它 —— 否则旧连接会继续按老配置拨下去，
// 表现为「改了配置却不生效」。
func (f *Forwarder) InvalidateProxy(id uint) {
	f.mu.Lock()
	c := f.proxied[id]
	delete(f.proxied, id)
	delete(f.proxiedCfg, id)
	f.mu.Unlock()
	if c != nil {
		closeIdle(map[uint]*http.Client{id: c})
	}
}

// clientFor 选出这次请求该用的客户端。
//
// 代理不可用时**返回错误，绝不回退直连**：用户配代理往往就是为了不让请求
// 从本机 IP 出去，静默回退会把真实 IP 暴露给上游，而界面上一切正常 ——
// 这种「看起来在走代理、其实直连」的失败方式是排查不出来的。
func (f *Forwarder) clientFor(proxyID uint) (*http.Client, proxy.Config, error) {
	if proxyID == 0 {
		return f.base, proxy.Config{}, nil
	}
	f.mu.Lock()
	if c, ok := f.proxied[proxyID]; ok {
		cfg := f.proxiedCfg[proxyID]
		f.mu.Unlock()
		return c, cfg, nil
	}
	resolve := f.resolve
	f.mu.Unlock()

	// 查库放在锁外：resolve 会走一次数据库查询，持锁做它会把
	// 所有走代理的请求以及 InvalidateProxy 一起卡住
	if resolve == nil {
		return nil, proxy.Config{}, fmt.Errorf("渠道指定的代理 #%d 无法解析（服务未接入代理配置）", proxyID)
	}
	cfg, err := resolve(proxyID)
	if err != nil {
		return nil, proxy.Config{}, fmt.Errorf("渠道指定的代理 #%d 不可用: %w", proxyID, err)
	}
	tr, err := cfg.Transport(15 * time.Second)
	if err != nil {
		return nil, proxy.Config{}, fmt.Errorf("代理 #%d 配置不可用: %w", proxyID, err)
	}
	// 与直连客户端保持同一套超时口径，只换 Transport
	tr.ResponseHeaderTimeout = f.headerTimeout
	c := &http.Client{Timeout: 0, Transport: tr}

	f.mu.Lock()
	// 双检：并发请求可能已经填过同一个代理，后到的那个直接复用并丢掉自己这份
	if existing, ok := f.proxied[proxyID]; ok {
		existingCfg := f.proxiedCfg[proxyID]
		f.mu.Unlock()
		tr.CloseIdleConnections()
		return existing, existingCfg, nil
	}
	f.proxied[proxyID] = c
	f.proxiedCfg[proxyID] = cfg
	f.mu.Unlock()
	return c, cfg, nil
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

	// 非流式请求给整次调用一个总时长上限：headerTimeout 只覆盖到响应头，
	// 之后正文挂起的话请求会被无限拖住（客户端不断开就一直挂着）。
	// 流式请求不能加 —— 长流的正文时长没有合理上限，ctx 一断流就死了。
	// 依据入站 body 里的 stream 标记判断（站内通用语始终带这个字段）。
	if f.bodyTimeout > 0 && !ExtractStream(inboundBody) {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.bodyTimeout)
		defer cancel()
	}

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
	// Content-Type 与 User-Agent 从入站抄过来（客户端的自述对上游有意义）；
	// Accept 刻意不透传 —— 下面会统一声明为「两种都能收」，抄过来的值
	// 永远活不到发出去那一刻，留着只会误导人以为做了内容协商。
	for _, h := range []string{"Content-Type", "User-Agent"} {
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

	client, proxyCfg, err := f.clientFor(cand.EgressProxyID())
	if err != nil {
		return att, err
	}

	att.Headers = make(http.Header)
	resp, err := client.Do(req)
	att.HeaderMs = int(time.Since(att.StartedAt).Milliseconds())
	if err != nil {
		if cand.EgressProxyID() != 0 {
			// 走代理时把底层错误翻译一下：这条错误会落进请求日志，
			// 而 "proxyconnect tcp: ... connection refused" 没人看得懂
			return att, fmt.Errorf("请求上游失败（经代理 %s）: %s",
				proxyCfg.Redacted(), proxy.FriendlyError(err, proxyCfg))
		}
		return att, fmt.Errorf("请求上游失败: %w", err)
	}

	att.StatusCode = resp.StatusCode
	CopyHeaders(att.Headers, resp.Header)

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		// 错误体通常很小，直接读全便于落库与排障
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		att.Body = raw
		// 错误响应里也可能带 usage（如 context-length-exceeded 的 400）——
		// 上游对这部分 token 是真收费的。不提取的话这些请求一律记 0 token，
		// 成本统计与预算提醒系统性偏低，而且看不出来偏低了
		att.Usage, att.HasUsage = extractUsageFromJSON(raw)
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
	// 成功响应体也要有上限：请求体有 MaxRequestBodyMB 保护，响应体原先没有 ——
	// 上游异常放大响应（或大批量 embeddings）时单请求可吃掉数百 MB 内存，
	// 而 MaxConcurrency 只限并发数、不限单请求体量。128MB 远超正常的
	// 非流式回复（KB~几 MB 量级），超限按上游故障处理，给故障转移一个机会
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamResponseBody))
	if err != nil {
		return att, fmt.Errorf("读取上游响应失败: %w", err)
	}
	if len(raw) >= maxUpstreamResponseBody {
		return att, fmt.Errorf("上游响应体超过上限 %d MB，已中止读取", maxUpstreamResponseBody>>20)
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
//
// 任何非 2xx 的状态码都立即触发故障转移 —— 包括历史上被判为
// 「请求本身的问题，换渠道也没用」的 400/401/403/404 等。
// 原因：多渠道场景下，同一状态码在不同上游含义完全不同
// （有的渠道用 404 表达「本渠道没有这个模型」，用 400 表达配额异常），
// 与其在中继侧替上游猜语义，不如一律换下一个候选再试。
// 2xx 一律视为成功（201/204 也是成功语义，不该触发转移）。
//
// 三个例外不在本函数处理：
//   - 客户端主动断开（手动结束推理）导致的报错：由 Relay 检查外层
//     ctx 是否已取消来豁免 —— 客户端已经不要这次请求了，换渠道毫无意义；
//   - 请求内容无法转换（ErrUnsupportedContent）：由 Relay 提前返回；
//   - 物理性的请求级错误（413/414/431，见 physicalRequestError）：
//     请求的大小不因换渠道而变，由 Relay 短路返回。
func (a *Attempt) Retryable() bool {
	if a == nil {
		return true
	}
	// 0 表示网络层失败（连接不上、握手失败等），同样换渠道再试
	return a.StatusCode < 200 || a.StatusCode >= 300
}

// maxUpstreamResponseBody 限制单次非流式成功响应体读入的上限（128MB）。
// 请求体有 MaxRequestBodyMB（默认 64MB）保护，响应体原先没有 ——
// 上游异常放大响应时单请求可吃掉数百 MB 内存，而并发上限只限请求数
// 不限单请求体量。正常非流式回复都在 KB~几 MB 量级，128MB 已极宽裕。
const maxUpstreamResponseBody = 128 << 20

// requestShapeStatus 报告状态码是否属于「请求形状类」错误：
// 参数不合法、模型不存在、报文或头太大 —— 问题大概率出在请求本身。
//
// 这个集合服务于「多渠道共识」判定（见 Relay.recordFailures）：
// 一次转发里 >=2 个渠道报了同一个此类状态码，说明是请求的问题，
// 这些失败不再记到渠道头上，否则用户发一个超长对话就能把所有渠道
// 打成 degraded。渠道级的 401/403/408/409/429 与全部 5xx 刻意不在
// 集合里：两个渠道密钥都失效不代表第三个渠道也有问题。
var requestShapeStatus = map[int]bool{
	http.StatusBadRequest:                  true, // 400
	http.StatusNotFound:                    true, // 404
	http.StatusRequestEntityTooLarge:       true, // 413
	http.StatusRequestURITooLong:           true, // 414
	http.StatusUnprocessableEntity:         true, // 422
	http.StatusRequestHeaderFieldsTooLarge: true, // 431
}

// physicalRequestError 报告状态码是否「物理性请求级」错误：
// 请求体/URI/头太大。这类错误的根源（请求的物理尺寸）不随渠道变化，
// 换渠道必然得到同样的结果，所以连共识判定都省了 —— 第一个渠道
// 报出来就短路返回，不再消耗后面的候选。
//
// 与 requestShapeStatus 的分工：413/414/431 在两个集合里都在，
// 但物理性这一档更强，直接短路；400/404/422 只参与共识豁免记账，
// 仍然把候选试完 —— 万一后面的渠道真支持这个请求呢（例如更长的上下文）。
func physicalRequestError(status int) bool {
	return status == http.StatusRequestEntityTooLarge ||
		status == http.StatusRequestURITooLong ||
		status == http.StatusRequestHeaderFieldsTooLarge
}
