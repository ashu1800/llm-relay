package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"llm-relay/internal/model"
)

// 任何非 2xx 的状态码都必须触发故障转移 —— 这是产品层面的明确决策：
// 同一状态码在不同上游的含义完全不同（有的渠道用 404 表达「本渠道没有
// 这个模型」），与其在中继侧替上游猜语义，不如一律换下一个候选再试。
// 2xx 一律视为成功（201/204 也是成功语义）；0 仍表示网络层失败，照旧转移。
func TestRetryableAnyNon2xx(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{0, true},    // 网络层失败（连接不上等）
		{200, false}, // 2xx 均为成功
		{201, false},
		{204, false},
		{299, false},
		{301, true}, // Go client 会跟随重定向，拿到 3xx 属于异常，转移
		{400, true}, // 历史上被判「换渠道也没用」，现在一律转移
		{401, true},
		{402, true},
		{403, true},
		{404, true},
		{405, true},
		{408, true},
		{409, true},
		{413, true},
		{422, true},
		{429, true},
		{500, true},
		{502, true},
		{503, true},
		{504, true},
	}
	for _, c := range cases {
		att := &Attempt{StatusCode: c.status}
		if got := att.Retryable(); got != c.want {
			t.Errorf("状态码 %d：Retryable 应为 %v，实际 %v", c.status, c.want, got)
		}
	}
	// nil 尝试按可重试处理（调用方拿着空结果没有别的出路）
	var nilAtt *Attempt
	if !nilAtt.Retryable() {
		t.Error("nil 尝试应视为可重试")
	}
}

// 请求形状类状态码的分类与物理性短路的判定。
// physicalRequestError 是 requestShapeStatus 的子集（413/414/431），
// 两者搭配决定：物理性错误第一个渠道就短路返回，
// 其余形状类错误试完候选后按共识豁免记账。
func TestRequestShapeStatusClassification(t *testing.T) {
	shapeWant := map[int]bool{
		400: true, 404: true, 413: true, 414: true, 422: true, 431: true,
		401: false, 403: false, 408: false, 409: false, 429: false, // 渠道级
		500: false, 502: false, 503: false, // 渠道级
		301: false, 200: false,
	}
	for status, want := range shapeWant {
		if got := requestShapeStatus[status]; got != want {
			t.Errorf("状态码 %d：requestShapeStatus 应为 %v，实际 %v", status, want, got)
		}
	}
	physWant := map[int]bool{
		413: true, 414: true, 431: true,
		400: false, 404: false, 422: false, // 形状类但不物理：走共识，不短路
		401: false, 429: false, 500: false,
	}
	for status, want := range physWant {
		if got := physicalRequestError(status); got != want {
			t.Errorf("状态码 %d：physicalRequestError 应为 %v，实际 %v", status, want, got)
		}
	}
}

// 共识豁免的判定规则：
//   - 同一请求形状类状态码出现 >=2 次 → 相关记录全部豁免（请求的问题，渠道无辜）
//   - 不同状态码、渠道级错误、网络失败不豁免
func TestExemptedByConsensus(t *testing.T) {
	cases := []struct {
		name     string
		failures []failRecord
		want     []bool
	}{
		{"两个渠道都 400：豁免", []failRecord{
			{channelID: 1, status: 400, shape: true},
			{channelID: 2, status: 400, shape: true},
		}, []bool{true, true}},
		{"一个 400 一个 404：不同状态码不算共识", []failRecord{
			{channelID: 1, status: 400, shape: true},
			{channelID: 2, status: 404, shape: true},
		}, []bool{false, false}},
		{"单个 400 加渠道级失败：只豁免是不可能的，全记", []failRecord{
			{channelID: 1, status: 400, shape: true},
			{channelID: 2, status: 500},
		}, []bool{false, false}},
		{"三个 429：渠道级不豁免", []failRecord{
			{channelID: 1, status: 429},
			{channelID: 2, status: 429},
			{channelID: 3, status: 429},
		}, []bool{false, false, false}},
		{"两 400 一 500：只豁免 400 的", []failRecord{
			{channelID: 1, status: 400, shape: true},
			{channelID: 2, status: 400, shape: true},
			{channelID: 3, status: 500},
		}, []bool{true, true, false}},
		{"网络失败不豁免", []failRecord{
			{channelID: 1, status: 0},
			{channelID: 2, status: 0},
		}, []bool{false, false}},
		{"空列表", nil, []bool{}},
	}
	for _, c := range cases {
		got := exemptedByConsensus(c.failures)
		if len(got) != len(c.want) {
			t.Fatalf("%s：结果长度 %d 与期望 %d 不符", c.name, len(got), len(c.want))
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s：第 %d 条应为 %v，实际 %v", c.name, i, c.want[i], got[i])
			}
		}
	}
}

// openAICand 造一条指向指定地址的 OpenAI 协议候选，供转发器测试使用。
func openAICand(baseURL string) Candidate {
	return Candidate{
		Channel: model.Channel{BaseURL: baseURL, Protocol: model.ProtocolOpenAIChat},
		Binding: model.ChannelModel{UpstreamName: "upstream-model"},
	}
}

// 客户端主动断开（手动结束推理）后，fwd.Do 以错误收场，且外层 ctx 已取消 ——
// 这正是 Relay 里豁免故障转移的判定条件（ctx.Err() != nil）。
// 钉住它：如果哪天取消不再传播到错误返回，豁免就会悄悄失效，
// 客户端断开又会变成逐渠道重发 + 冤枉健康渠道。
func TestClientGoneCancelsContextSeenByRelay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 挂住不放，直到客户端断开波及到这次上游请求。
		// 加 3 秒兜底：Close() 会等未完成的请求收尾，某些环境下
		// 服务端感知不到客户端取消，没有兜底会让测试永远挂在这里。
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel() // 模拟客户端在等待响应头期间手动结束推理
	}()

	fwd := NewForwarder(5*time.Second, 30*time.Second)
	_, err := fwd.Do(ctx, openAICand(srv.URL), "/v1/chat/completions",
		[]byte(`{"model":"public","messages":[{"role":"user","content":"hi"}]}`),
		http.Header{}, false)
	if err == nil {
		t.Fatal("客户端断开后 fwd.Do 应返回错误")
	}
	if ctx.Err() == nil {
		t.Fatal("客户端断开后外层 ctx 应已取消 —— Relay 的豁免判定依赖这一点")
	}
}

// 对照场景：bodyTimeout 到期（上游太慢）同样让 fwd.Do 报错，
// 但外层 ctx **没有**被取消 —— 豁免判定不成立，这种失败必须照常故障转移。
// 防止有人把判定写成「任何 context 错误都豁免」，把上游超时也吞进不重试。
func TestUpstreamSlowDoesNotCancelOuterContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 比非流式总时长上限睡得更久，逼 bodyTimeout 先到期；
		// 同样给 Close() 留出有界的收尾时间
		time.Sleep(1 * time.Second)
	}))
	defer srv.Close()

	ctx := context.Background()
	fwd := NewForwarder(5*time.Second, 200*time.Millisecond)
	_, err := fwd.Do(ctx, openAICand(srv.URL), "/v1/chat/completions",
		[]byte(`{"model":"public","messages":[{"role":"user","content":"hi"}]}`),
		http.Header{}, false)
	if err == nil {
		t.Fatal("上游超过总时长上限时 fwd.Do 应返回错误")
	}
	if ctx.Err() != nil {
		t.Fatalf("bodyTimeout 是 fwd.Do 内部的超时，不应取消外层 ctx（否则上游慢会被误判成客户端断开）: %v", ctx.Err())
	}
}
