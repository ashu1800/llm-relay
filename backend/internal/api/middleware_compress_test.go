package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// gzip 响应压缩中间件的测试。
//
// 覆盖面按「坏了会以什么形态炸」来选：
//   - 压缩后的字节解不开 / 解开不等于原文 —— 腐坏，比不压缩糟糕得多；
//   - 协商判断错（gzip;q=0 当成要压、* 通配漏判）—— 给出客户端明确
//     拒绝的编码，或白白放弃压缩；
//   - SSE / 已编码响应被二次压缩 —— 流式语义被破坏；
//   - WebSocket 升级被包装器挡住 —— /live 断连（由 live_heartbeat_test
//     走真实握手覆盖，它挂的就是含本中间件的 registerAdminRoutes）。
func newCompressTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/admin", gzipResponses())
	{
		// 模拟日志列表这类「重复结构大 JSON」：重复键名的数据
		// 是压缩收益的主力，也最容易在校验「解压后逐字节相等」时
		// 暴露截断或错位
		g.GET("/json", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"items":  strings.Split(strings.Repeat("alpha,beta,gamma,", 64), ","),
				"total":  192,
				"status": "ok",
			})
		})
		// 模拟 CSV 导出：handler 先显式设 Content-Type 再 c.String
		// （与 exportLogs 的写法一致），BOM 必须在解压后原样保留
		g.GET("/csv", func(c *gin.Context) {
			c.Header("Content-Type", "text/csv; charset=utf-8")
			c.String(http.StatusOK, "\ufeff请求时间,模型,状态\n"+strings.Repeat("2026-09-26T00:00:00Z,gpt-4,200\n", 300))
		})
		// 已带 Content-Encoding 的响应必须旁路（防御性用例）
		g.GET("/pre-encoded", func(c *gin.Context) {
			c.Header("Content-Encoding", "br")
			c.String(http.StatusOK, "already encoded payload")
		})
		// 无响应体（204）：决策点永远不会触发，close 必须是安全空操作
		g.GET("/no-content", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	}
	return r
}

func getBody(t *testing.T, url string, acceptEncoding string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	w := httptest.NewRecorder()
	newCompressTestRouter().ServeHTTP(w, req)
	res := w.Result()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("读响应体失败：%v", err)
	}
	return res, string(body)
}

func gunzip(t *testing.T, body string) string {
	t.Helper()
	zr, err := gzip.NewReader(strings.NewReader(body))
	if err != nil {
		t.Fatalf("响应体不是合法 gzip 流：%v", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip 流解压不完整（疑似漏了收尾 Close）：%v", err)
	}
	return string(out)
}

// 带与不带 Accept-Encoding 的两次请求，压缩版解压后必须与明文版
// 逐字节相等，且响应头正确声明了编码与 Vary。
func TestGzipResponsesRoundTrip(t *testing.T) {
	plainRes, plainBody := getBody(t, "/api/admin/json", "")
	if plainRes.Header.Get("Content-Encoding") != "" {
		t.Fatalf("客户端未协商 gzip 却收到了 Content-Encoding: %q", plainRes.Header.Get("Content-Encoding"))
	}

	gzRes, gzBody := getBody(t, "/api/admin/json", "gzip")
	if got := gzRes.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding 期望 gzip，得到 %q", got)
	}
	if got := gzRes.Header.Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("Vary 缺少 Accept-Encoding，得到 %q", got)
	}
	if got := gzRes.Header.Get("Content-Length"); got != "" {
		t.Fatalf("压缩响应带了过期 Content-Length: %q", got)
	}
	if len(gzBody) >= len(plainBody) {
		t.Fatalf("重复结构 JSON 竟然压不小：压缩 %d 字节 vs 明文 %d 字节", len(gzBody), len(plainBody))
	}
	if inflated := gunzip(t, gzBody); inflated != plainBody {
		t.Fatalf("解压后与明文响应不一致（长度 %d vs %d）", len(inflated), len(plainBody))
	}
}

// 协商矩阵：acceptsGzip 的判定直接决定这套中间件的行为，
// 把关键边界钉在这里（q=0 是明确拒绝；* 通配代表 gzip 可接受）。
func TestGzipResponsesNegotiation(t *testing.T) {
	cases := []struct {
		header   string
		compress bool
	}{
		{"", false},
		{"identity", false},
		{"deflate, br", false},
		{"gzip;q=0", false},
		{"gzip;q=0.0", false},
		{"gzip", true},
		{"br, gzip", true},
		{"gzip;q=0, *", false},     // RFC 7231：显式 q=0 拒绝 gzip，优先级高于 * 通配
		{"deflate, *;q=0.5", true}, // 无 gzip 条目，* 表示任何编码都行
	}
	for _, tc := range cases {
		res, body := getBody(t, "/api/admin/json", tc.header)
		got := res.Header.Get("Content-Encoding") == "gzip"
		if got != tc.compress {
			t.Errorf("Accept-Encoding: %q 期望压缩=%v 实际=%v", tc.header, tc.compress, got)
		}
		if tc.compress {
			gunzip(t, body) // 顺带验证流完整
		}
	}
}

// CSV 导出这类「handler 先设 Content-Type 再整段 c.String」的响应：
// 压缩必须可用，BOM 与表头在解压后原样保留（Excel 按 BOM 识别 UTF-8）。
func TestGzipResponsesCSV(t *testing.T) {
	res, body := getBody(t, "/api/admin/csv", "gzip")
	if res.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("CSV 导出未被压缩")
	}
	inflated := gunzip(t, body)
	if !strings.HasPrefix(inflated, "\ufeff请求时间,模型,状态\n") {
		t.Fatalf("解压后 BOM/表头损坏，开头为 %q", inflated[:min(len(inflated), 40)])
	}
}

// 已带 Content-Encoding 的响应必须旁路：双重编码没有客户端解得开。
func TestGzipResponsesBypassesPreEncoded(t *testing.T) {
	res, body := getBody(t, "/api/admin/pre-encoded", "gzip")
	if res.Header.Get("Content-Encoding") != "br" {
		t.Fatalf("预设编码被覆盖：%q", res.Header.Get("Content-Encoding"))
	}
	if body != "already encoded payload" {
		t.Fatalf("旁路路径的响应体被改写：%q", body)
	}
}

// 无响应体（204）不触发任何压缩路径，close 为空操作。
func TestGzipResponsesNoBody(t *testing.T) {
	res, body := getBody(t, "/api/admin/no-content", "gzip")
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("状态码期望 204，得到 %d", res.StatusCode)
	}
	if body != "" || res.Header.Get("Content-Encoding") != "" {
		t.Fatalf("空响应不应产生响应体或编码头：body=%q enc=%q", body, res.Header.Get("Content-Encoding"))
	}
}
