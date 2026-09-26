package update

// guardedTransport 的行为边界：走显式配置的代理时**不**做内网拦截
// （代理地址常常就是 127.0.0.1 或内网网关，代理是管理员显式配置的可信出口），
// 直连时对目标保留公网校验。目标侧的白名单校验（checkDownloadTarget）
// 在 download 路径上，由 checksums_redirect_test.go 覆盖。

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"llm-relay/internal/netguard"
)

// 本机地址上的 HTTP 代理必须可用 —— 这是本功能最典型的部署形态
// （本机跑 Clash/v2ray），也是修复前会被 netguard 拦下的那条路径。
func TestGuardedTransportAllowsLocalProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	// 一个会转发的最小 HTTP 代理：确认「确实经过代理」而不是只连上了代理
	var gotURI string
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.URL.String()
		out, err := http.Get(gotURI)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer out.Body.Close()
		w.WriteHeader(out.StatusCode)
		_, _ = io.Copy(w, out.Body)
	}))
	defer proxySrv.Close()

	tr, err := guardedTransport(proxySrv.URL) // httptest 地址必然是 127.0.0.1
	if err != nil {
		t.Fatalf("构造走代理的 transport 失败: %v", err)
	}
	client := &http.Client{Transport: tr}
	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("本机代理应当可用（netguard 不应拦截代理地址）: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("目标应返回 204，实际 %d", resp.StatusCode)
	}
	if !strings.HasPrefix(gotURI, "http") {
		t.Fatalf("请求应经代理转发，代理实际收到 %q", gotURI)
	}
}

// 直连的公网校验必须保留：豁免只给显式配置的代理，不给直连目标。
func TestGuardedTransportDirectStillGuards(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("直连内网目标不应真的连上")
	}))
	defer srv.Close()

	tr, err := guardedTransport("")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: tr}
	_, err = client.Get(srv.URL)
	if err == nil {
		t.Fatal("直连 127.0.0.1 应被 netguard 拦截")
	}
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("拦截原因应是内网地址（ErrBlocked），实际 %v", err)
	}
}
