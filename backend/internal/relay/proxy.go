package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"llm-relay/internal/model"
	"llm-relay/internal/relay/convert"
)

// Prepared 是发往上游的请求描述。
type Prepared struct {
	URL     string
	Method  string
	Headers http.Header
	Body    []byte
}

// BuildUpstreamURL 拼接上游地址。
//
// 老的实现只认「base 以 /v1 结尾」和「base 以 /v1beta 结尾」两种写法，
// 其余一律 base + path。这在 base 已经自带版本段、但版本号不是 /v1 时会拼错：
// 智谱 GLM Coding Plan 的 OpenAI 协议 base 是
// https://open.bigmodel.cn/api/coding/paas/v4，拼出来是 /v4/v1/chat/completions，
// 上游直接 404（实测响应体里 path 字段就是这么回显的）。
//
// 判据改成「base 的最后一段本身就是版本段」：v1 / v1beta / v4 / paas/v4 都算。
// 这样只要 base 带了版本，就不再重复插入路径里的版本段；
// base 不带版本（https://api.openai.com、https://api.anthropic.com）时，
// 仍然由 path 提供版本段 —— 两条路都保持原来的正确结果。
func BuildUpstreamURL(baseURL, path string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return path
	}
	if seg := lastPathSegment(base); isVersionSegment(seg) {
		return base + stripVersionPrefix(path)
	}
	return base + path
}

// lastPathSegment 取 URL 路径的最后一段。
// 用字符串切分而不是 net/url：这里只需要「最后一段长什么样」，
// 而 url.Parse 对没写 scheme 的配置（用户填 example.com/v1 这种）会解析失败。
func lastPathSegment(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	if i := strings.LastIndex(u, "/"); i >= 0 {
		return u[i+1:]
	}
	return ""
}

// isVersionSegment 判断某一段是不是版本段（v1 / v1beta / v2 / v4 …）。
// 只认「v + 数字 + 可选字母后缀」，避免把 /api、/openai 这类普通段误判成版本。
func isVersionSegment(seg string) bool {
	if len(seg) < 2 || (seg[0] != 'v' && seg[0] != 'V') {
		return false
	}
	i := 1
	for i < len(seg) && seg[i] >= '0' && seg[i] <= '9' {
		i++
	}
	if i == 1 {
		return false // 必须至少有一位数字：v、version 都不算
	}
	for ; i < len(seg); i++ {
		c := seg[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

// stripVersionPrefix 去掉 path 开头的版本段（/v1/… 或 /v1beta/…）。
//
// 只在这一段**确实是版本段**时才去掉，不能简单地剪掉前两段：
// Gemini 的路径是 /v1beta/models/gemini-2.0:generateContent，
// 剪错会把 models/... 一起削掉。
func stripVersionPrefix(path string) string {
	if !strings.HasPrefix(path, "/") {
		return path
	}
	rest := path[1:]
	seg := rest
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		seg = rest[:i]
	}
	if !isVersionSegment(seg) {
		return path
	}
	trimmed := rest[len(seg):]
	if trimmed == "" {
		return "/"
	}
	if !strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "?") {
		trimmed = "/" + trimmed
	}
	return trimmed
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
	// 只做顶层浅解析（map[string]json.RawMessage）：要动的只有 model 与
	// stream_options 两个顶层键，其余字段的原文以 RawMessage 透传。
	// 与全量 map 往返相比有两个收益：一是不再递归解析整个报文（长对话
	// 可达数 MB，故障转移时每个候选都要再来一遍）；二是数字不再经
	// float64 还原 —— 超过 2^53 的整数（如超长 seed）在 map 往返里会失真。
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("请求体不是合法 JSON: %w", err)
	}
	if upstreamModel != "" {
		m, _ := json.Marshal(upstreamModel)
		top["model"] = m
	}
	if injectStreamUsage {
		var stream bool
		if v, ok := top["stream"]; ok {
			_ = json.Unmarshal(v, &stream)
		}
		if stream {
			// 客户端已显式给过 include_usage 就不动它
			opts := map[string]json.RawMessage{}
			if existing, ok := top["stream_options"]; ok {
				_ = json.Unmarshal(existing, &opts)
			}
			if _, exists := opts["include_usage"]; !exists {
				opts["include_usage"] = json.RawMessage("true")
			}
			merged, err := json.Marshal(opts)
			if err == nil {
				top["stream_options"] = merged
			}
		}
	}
	return json.Marshal(top)
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

// IsMultipartContentType 判断 Content-Type 是否为 multipart/form-data。
// 语音转写端点的请求体是 multipart，模型名在表单字段里而不是 JSON 里 ——
// RewriteModel（JSON 浅解析）对它无能为力，也绝不能去「解析」一个 25MB 的
// 音频字节流当 JSON。
func IsMultipartContentType(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)),
		"multipart/form-data")
}

// multipartFormValue 遍历 multipart 报文，返回指定表单字段的值。
//
// 刻意不使用 multipart.Reader.ReadForm：它会把超过 maxMemory 的部件写到
// 临时文件再让我们删 —— 音频文件动辄十几 MB，纯粹多一轮磁盘 IO。这里
// 手动逐部件读：只关心 form 字段（模型名，几十字节），文件部件原样跳过
// （内容已在调用方的字节切片里，为「跳过」再读一遍只是 CPU 成本）。
//
// 返回 (值, 是否找到)。字段值截断在 4KB：模型名不会那么长，截断只发生在
// 报文被人为构造坏掉的场景 —— 那时值本身已不可信。
func multipartFormValue(raw []byte, contentType, field string) (string, bool, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", false, fmt.Errorf("解析 Content-Type 失败: %w", err)
	}
	boundary := params["boundary"]
	if boundary == "" {
		return "", false, fmt.Errorf("multipart 报文缺少 boundary")
	}
	reader := multipart.NewReader(bytes.NewReader(raw), boundary)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("解析 multipart 报文失败: %w", err)
		}
		if part.FileName() != "" {
			// 文件部件：清空内容后才能继续遍历（Reader 要求读尽当前部件）
			_, _ = io.Copy(io.Discard, part)
			_ = part.Close()
			continue
		}
		if name := part.FormName(); name == field {
			value, _ := io.ReadAll(io.LimitReader(part, 4096))
			_ = part.Close()
			return strings.TrimSpace(string(value)), true, nil
		}
		_, _ = io.Copy(io.Discard, part)
		_ = part.Close()
	}
}

// MultipartModelField 从 multipart 请求体里取模型名（语音转写端点用）。
// 没有 model 字段时返回空串 —— 与 ExtractModel 对 JSON 缺字段的行为一致。
func MultipartModelField(raw []byte, contentType string) string {
	v, _, _ := multipartFormValue(raw, contentType, "model")
	return v
}

// RewriteMultipartModel 把 multipart 报文里 model 字段的值替换成上游模型名。
//
// 与 RewriteModel（JSON）的对称物，但有两个绕不开的差异：
//  1. 改字段值必须**重新编码整个报文**（multipart 的部件带长度边界），
//     新报文用新 boundary，返回值里带新的 Content-Type —— 调用方必须
//     用它替换出站请求头，否则上游按旧 boundary 切分直接解析失败；
//  2. 大文件会被整体复制一遍（读写两个缓冲都在内存）。所以只有
//     「对外名 ≠ 上游名」时才走重编码 —— 同名直通（绝大多数部署的
//     配置）零成本原样转发。
//
// 没有 model 字段时原样返回（交给上游去报缺参数，语义比我们猜的准）。
func RewriteMultipartModel(raw []byte, contentType, upstreamModel string) ([]byte, string, error) {
	if upstreamModel == "" {
		return raw, contentType, nil
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, "", fmt.Errorf("解析 Content-Type 失败: %w", err)
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, "", fmt.Errorf("multipart 报文缺少 boundary")
	}

	// 先探测 model 字段的当前值：值已一致时直接原样返回。
	// multipart.Reader 消费完就回不去了，而「值是否相同」只有读完才知道
	// —— 重编码除了换 boundary 什么都没改，却要把整份文件复制一遍，
	// 所以宁可多遍历一次报文（内存里的字节拷贝，成本可忽略）。
	current := MultipartModelField(raw, contentType)
	if current == upstreamModel {
		return raw, contentType, nil
	}

	reader := multipart.NewReader(bytes.NewReader(raw), boundary)
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	rewritten := false
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("解析 multipart 报文失败: %w", err)
		}
		name := part.FormName()
		isModelField := part.FileName() == "" && name == "model"

		header := textproto.MIMEHeader{}
		for k, vs := range part.Header {
			if isModelField && strings.EqualFold(k, "Content-Disposition") {
				// 旧 disposition 描述的是原字段名，下面按 name=model 重写。
				// 注意 CreatePart **不会**自动补 disposition：不带这个头的
				// 部件没有字段名，解析侧会直接丢掉 model —— 必须显式设置
				continue
			}
			for _, v := range vs {
				header.Add(k, v)
			}
		}
		if isModelField {
			header.Set("Content-Disposition",
				fmt.Sprintf(`form-data; name="%s"`, escapeQuotes(name)))
		}
		dst, err := writer.CreatePart(header)
		if err != nil {
			return nil, "", err
		}
		if isModelField {
			if _, err := dst.Write([]byte(upstreamModel)); err != nil {
				return nil, "", err
			}
			rewritten = true
		} else if _, err := io.Copy(dst, part); err != nil {
			return nil, "", fmt.Errorf("复制 multipart 部件失败: %w", err)
		}
		_ = part.Close()
	}
	if !rewritten {
		// 没有 model 字段：交给上游去报缺参数，语义比我们猜的准
		return raw, contentType, nil
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "multipart/form-data; boundary=" + writer.Boundary(), nil
}

// escapeQuotes 转义 Content-Disposition 里的引号（multipart.Writer 的
// EscapeQuotes 是私有函数，这里复刻一份 —— 语义单一，不值得为此引依赖）。
func escapeQuotes(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

// UsageTee 在透传字节流的同时旁路解析 SSE，抓取上游返回的 usage。
// 它只观察、不改写，因此对流式转发的延迟没有影响。
type UsageTee struct {
	// splitter 复用 convert 包的行切分器：同样的逻辑此前在这里和 convert
	// 各写了一份，内存上限的修复很容易只落到其中一处。
	splitter convert.LineSplitter
	usage    Usage
	gotAny   bool
	total    int
	onUsage  func(Usage)
}

// usageNeedle 是 handleLine 粗筛用的子串（见其注释）。
var usageNeedle = []byte("usage")

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
	t.splitter.Feed(p, t.handleLine)
	return len(p), nil
}

// Flush 处理最后一行没有换行符的残留数据。
func (t *UsageTee) Flush() {
	t.splitter.Flush(t.handleLine)
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
	// 粗筛：三种来源的键（usage / usageMetadata / message.usage）都含 "usage"
	// 子串。99% 的 delta 分片根本没有用量，为它们做全量 JSON 解析纯属浪费
	// —— 每分片一次 map 分配，长流就是几百次。子串不命中直接跳过，
	// 误报（正文里恰好写了 "usage"）只是多付一次原要付的解析，不会漏。
	if !bytes.Contains(payload, usageNeedle) {
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
		t.usage.TotalTokens = UsageTotal(t.usage.PromptTokens, t.usage.CompletionTokens, t.usage.CachedTokens, t.usage.CacheCreationTokens)
	}
	t.gotAny = true
}

// BuildClient 构造上游 HTTP 客户端。流式请求不能设整体超时，只能约束握手阶段。
//
// CheckRedirect 返回 ErrUseLastResponse：上游回 30x 时把响应**原样**交回
// 故障转移链（3xx 属于异常，见 Attempt.Retryable 的对照表），而不是让
// Go 默认行为接管 —— 默认跟随会把 POST 降级成 GET、丢掉请求体，
// 上游若真把 POST 重定向到 GET 端点，客户端收到的是一次语义全错的
// 「成功」。Proxy 显式为 nil：直连渠道必须真直连，HTTP_PROXY 之类的
// 环境变量会静默接管出站路径 —— 与「代理不可用绝不静默回退直连」
// 正好是反向的漏洞（配了直连却被环境变量带去走代理）。
// 需要走代理的渠道由 clientFor 用渠道配置的代理单独构造，不经这里。
func BuildClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: 0,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			Proxy:                 nil,
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
