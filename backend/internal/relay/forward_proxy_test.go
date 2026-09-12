package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"llm-relay/internal/model"
	"llm-relay/internal/proxy"
)

// 代理不可用时必须失败，绝不能悄悄直连 ——
// 用户配代理往往就是为了不让请求从本机 IP 出去，
// 静默回退会把真实 IP 暴露给上游，而界面上一切正常。
func TestForwarderRefusesToFallBackToDirect(t *testing.T) {
	// 一个真实可达的上游：直连能通，所以「失败」只可能来自代理这一侧
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer up.Close()

	f := NewForwarder(5 * time.Second)
	f.SetProxyResolver(func(id uint) (proxy.Config, bool) {
		// 解析不出来 = 代理不存在或已停用
		return proxy.Config{}, false
	})

	cand := Candidate{
		Channel: model.Channel{BaseURL: up.URL, Protocol: model.ProtocolOpenAIChat, ProxyID: 7},
		Binding: model.ChannelModel{PublicName: "m", UpstreamName: "m"},
	}
	_, err := f.Do(context.Background(), cand, "/v1/chat/completions",
		[]byte(`{"model":"m","messages":[]}`), http.Header{}, false)
	if err == nil {
		t.Fatal("代理不可用时不该成功")
	}
	if !strings.Contains(err.Error(), "不存在或已停用") {
		t.Errorf("错误应当指出代理不可用，实际 %v", err)
	}
}

// 模型条目上的代理优先于渠道级：同一条渠道里可以只让某个模型走代理。
func TestCandidateEgressProxyPriority(t *testing.T) {
	base := Candidate{Channel: model.Channel{ProxyID: 3}}
	if got := base.EgressProxyID(); got != 3 {
		t.Errorf("没有模型级设置时应跟随渠道，实际 %d", got)
	}
	override := Candidate{Channel: model.Channel{ProxyID: 3}, Binding: model.ChannelModel{ProxyID: 9}}
	if got := override.EgressProxyID(); got != 9 {
		t.Errorf("模型级应当覆盖渠道级，实际 %d", got)
	}
	direct := Candidate{Channel: model.Channel{}}
	if got := direct.EgressProxyID(); got != 0 {
		t.Errorf("都没配就是直连，实际 %d", got)
	}
}
