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

// clearProxyEnv 清掉环境变量里的代理。
//
// 这些用例验的是「直连」与「显式配置的代理」两条路径，而开发机上完全可能设着
// HTTPS_PROXY=127.0.0.1:7890（本机跑 Clash 的常见形态）—— 那会让请求改走代理，
// 断言随之漂移。t.Setenv 顺带保证用例不并行。
func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "all_proxy", "no_proxy",
	} {
		t.Setenv(k, "")
	}
}

// 本机地址上的 HTTP 代理必须可用 —— 这是本功能最典型的部署形态
// （本机跑 Clash/v2ray），也是修复前会被 netguard 拦下的那条路径。
func TestGuardedTransportAllowsLocalProxy(t *testing.T) {
	clearProxyEnv(t)
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

// 直连的公网校验必须保留：豁免只给配置过的代理，不给直连目标。
func TestGuardedTransportDirectStillGuards(t *testing.T) {
	clearProxyEnv(t)
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

// 环境变量里的代理与界面上填的代理同等对待：它是运维自己配的可信出口
// （系统级 Clash/v2ray 就写在 HTTPS_PROXY 里），不该被当成 SSRF 目标拦下。
//
// 修复前的实际表现：面板里手填同一个代理能用，而 HTTPS_PROXY=127.0.0.1:7890
// 时报「目标地址指向内网或本机，已拒绝: 127.0.0.1（回环地址）」。
func TestGuardedTransportAllowsEnvProxy(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7890")

	tr, err := guardedTransport("")
	if err != nil {
		t.Fatal(err)
	}
	// 代理地址被豁免：不会报「被拦」，最多是连不上（本机没有代理在听）
	_, err = tr.DialContext(t.Context(), "tcp", "127.0.0.1:7890")
	if errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("环境变量里的代理不该被当成内网目标拦下，实际 %v", err)
	}
	// 同一台机器上别的端口不在名单里，照旧拦
	if _, err := tr.DialContext(t.Context(), "tcp", "127.0.0.1:7891"); !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("名单外的回环地址应被拦，实际 %v", err)
	}
	// 真实目标（公网域名）不在豁免之列，仍要过校验
	if _, err := tr.DialContext(t.Context(), "tcp", "169.254.169.254:80"); !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("云元数据地址应被拦，实际 %v", err)
	}
}

// envProxyAddrs 的归一化：没写端口时按 scheme 补默认端口，
// 否则与 Go 实际拨号用的 "host:80"/"host:443" 对不上，豁免会静默失效；
// 没写 scheme 的简写要与 Go 一致地按 http 处理。
func TestEnvProxyAddrsNormalizesDefaultPorts(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	t.Setenv("HTTP_PROXY", "http://proxy.example")
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1080")

	got := envProxyAddrs()
	want := map[string]bool{
		"proxy.example:3128": true,
		"proxy.example:80":   true,
		"127.0.0.1:1080":     true,
	}
	if len(got) != len(want) {
		t.Fatalf("应得到 %d 个地址，实际 %v", len(want), got)
	}
	for _, a := range got {
		if !want[a] {
			t.Errorf("出现了预期外的地址 %q（全部：%v）", a, got)
		}
	}
}

func TestEnvProxyAddrsAcceptsSchemalessShorthand(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "127.0.0.1:7890") // 常见简写：Clash 用户常这么写

	got := envProxyAddrs()
	if len(got) != 1 || got[0] != "127.0.0.1:7890" {
		t.Fatalf("没写 scheme 的代理地址应当照样豁免，实际 %v", got)
	}
}
