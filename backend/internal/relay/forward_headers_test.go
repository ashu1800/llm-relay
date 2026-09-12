package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"llm-relay/internal/model"
)

// 出站请求的头部处理：Anthropic 渠道要透传能力声明头，其它渠道不能乱透传。
func TestForwarderHeadersByProtocol(t *testing.T) {
	var got http.Header
	var gotPath string
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	fwd := NewForwarder(5e9)
	inbound := http.Header{}
	inbound.Set("Content-Type", "application/json")
	inbound.Set("anthropic-beta", "prompt-caching-2024-07-31")
	inbound.Set("anthropic-version", "2023-06-01")
	inbound.Set("X-Secret", "不该透传")

	cand := Candidate{
		Channel: model.Channel{Protocol: model.ProtocolAnthropic, BaseURL: srv.URL},
		Binding: model.ChannelModel{PublicName: "m", UpstreamName: "m"},
	}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	if _, err := fwd.Do(t.Context(), cand, "/v1/chat/completions", body, inbound, false); err != nil {
		t.Fatal(err)
	}

	if gotPath != "/v1/messages" {
		t.Errorf("Anthropic 渠道应请求 /v1/messages，实际 %s", gotPath)
	}
	if v := got.Get("anthropic-beta"); v != "prompt-caching-2024-07-31" {
		t.Errorf("能力声明头必须透传（否则提示缓存静默失效），实际 %q", v)
	}
	if v := got.Get("anthropic-version"); v != "2023-06-01" {
		t.Errorf("客户端在用的 API 版本应透传，实际 %q", v)
	}
	if v := got.Get("X-Secret"); v != "" {
		t.Errorf("任意头不能透传（凭据泄漏），实际 %q", v)
	}
	if v := got.Get("x-api-key"); v != "" {
		t.Errorf("没配密钥时不应有 x-api-key，实际 %q", v)
	}
	if !strings.Contains(gotBody, `max_tokens`) {
		t.Errorf("Anthropic 请求体必须有 max_tokens，实际 %s", gotBody)
	}

	// 非 Anthropic 渠道：这两个头不该跟过去
	got = nil
	cand.Channel.Protocol = model.ProtocolOpenAIChat
	if _, err := fwd.Do(t.Context(), cand, "/v1/chat/completions", body, inbound, false); err != nil {
		t.Fatal(err)
	}
	if v := got.Get("anthropic-beta"); v != "" {
		t.Errorf("OpenAI 渠道不该收到 anthropic-beta，实际 %q", v)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("OpenAI 渠道路径应保持，实际 %s", gotPath)
	}
}
