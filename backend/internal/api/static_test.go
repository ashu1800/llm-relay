package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

// 静态资源发送策略的测试。
//
// 这里盯的是几件「不报错、只是悄悄变坏」的事：
//   - 该压缩的没压缩（带宽白花，没人会发现）；
//   - 把 gzip 发给明确拒绝 gzip 的客户端（页面直接白屏）；
//   - index.html 被当成不可变资源缓存一年（发版后用户拿着旧 HTML
//     去请求已经删掉的 chunk，白屏且很难查）；
//   - 带哈希的产物没拿到长缓存（每次刷新重新下载 1.6 MB）。
//
// 用 fstest.MapFS 造一份假产物，不依赖真实的 dist 目录（它不入库）。

const (
	testAssetBody = "console.log('hello static assets')\n" // 必须 > 0，内容不重要
	testIndexBody = "<!doctype html><html><body><div id=app></div></body></html>"
)

// 带内容哈希的产物名，符合 assets/<name>-<hash>.<ext> 的形状。
const (
	hashedJS  = "assets/index-BGySQ1GG.js"
	plainFont = "fonts/Harding-Regular.ttf"
)

func gzipBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf strings.Builder
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatalf("gzip 写入失败: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip 关闭失败: %v", err)
	}
	return []byte(buf.String())
}

// newStaticTestFS 造一份产物：正文比压缩版长，.gz 只在部分文件上存在，
// 这样「有压缩版就用、没有就发原文」两条路都能被测到。
func newStaticTestFS(t *testing.T) fstest.MapFS {
	t.Helper()
	jsBody := strings.Repeat(testAssetBody, 40)
	fontBody := strings.Repeat("FONT", 100)
	return fstest.MapFS{
		"index.html": {Data: []byte(testIndexBody)},
		hashedJS:     {Data: []byte(jsBody)},
		hashedJS + ".gz": {
			Data: gzipBytes(t, jsBody),
		},
		// 字体故意**没有** .gz：模拟「压不动 / 没压」的文件
		plainFont:     {Data: []byte(fontBody)},
		"favicon.svg": {Data: []byte("<svg></svg>")},
	}
}

func newStaticTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	assets := newStaticAssets(newStaticTestFS(t), nil)
	r.NoRoute(assets.handler())
	return r
}

func doGet(r *gin.Engine, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestStaticHashedAssetUsesPrecompressed(t *testing.T) {
	r := newStaticTestRouter(t)
	w := doGet(r, "/"+hashedJS, map[string]string{"Accept-Encoding": "gzip"})

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200", w.Code)
	}
	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q，想要 gzip（客户端支持却没发压缩版，带宽白花）", got)
	}
	// 类型必须按**原文件名**算，不能因为发的是 .gz 就变成 application/gzip
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
		t.Errorf("Content-Type = %q，想要 text/javascript（按原文件名判断）", got)
	}
	if got := w.Header().Get("Cache-Control"); got != immutableCacheControl {
		t.Errorf("Cache-Control = %q，想要 %q（带哈希的产物应长缓存）", got, immutableCacheControl)
	}
	if got := w.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary = %q，必须含 Accept-Encoding（否则中间缓存可能把 gzip 发给不支持的客户端）", got)
	}

	// 响应体必须真的是 gzip，且能解出原文
	if got := w.Body.String(); got == testAssetBody {
		t.Error("响应体是明文，没有真正压缩")
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("响应体不是合法的 gzip: %v", err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip 解压失败: %v", err)
	}
	if string(plain) != strings.Repeat(testAssetBody, 40) {
		t.Error("解压后的内容与原文不一致（发错了文件）")
	}
}

func TestStaticHashedAssetWithoutGzipSupport(t *testing.T) {
	r := newStaticTestRouter(t)
	// 客户端明确不支持压缩时必须发原文，否则页面直接白屏
	w := doGet(r, "/"+hashedJS, map[string]string{"Accept-Encoding": "identity"})

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200", w.Code)
	}
	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q，想要空（客户端不支持 gzip）", got)
	}
	if w.Body.String() != strings.Repeat(testAssetBody, 40) {
		t.Error("响应体不是原文")
	}
}

func TestStaticFileWithoutPrecompressed(t *testing.T) {
	r := newStaticTestRouter(t)
	// 字体没有 .gz：即使客户端支持 gzip 也只能发原文，且不能报 404
	w := doGet(r, "/"+plainFont, map[string]string{"Accept-Encoding": "gzip"})

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200（没有预压缩版本应回退发原文，而不是 404）", w.Code)
	}
	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q，想要空（并没有压缩版本）", got)
	}
	// 字体在 assets/ 之外，且文件名不含哈希 —— 必须每次校验，
	// 不能被长缓存钉死（否则换了字体用户永远看不到）
	if got := w.Header().Get("Cache-Control"); got != revalidateCacheControl {
		t.Errorf("Cache-Control = %q，想要 %q", got, revalidateCacheControl)
	}
	if got := w.Header().Get("ETag"); got == "" {
		t.Error("缺少 ETag，浏览器无法做条件请求（每次都要重下 312 KB 字体）")
	}
}

func TestStaticIndexIsRevalidatedNotImmutable(t *testing.T) {
	r := newStaticTestRouter(t)
	w := doGet(r, "/", map[string]string{"Accept-Encoding": "gzip"})

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200", w.Code)
	}
	// 这是最关键的一条：index.html 引用的 chunk 名会随发版变化，
	// 一旦被长缓存，用户会拿着旧 HTML 去请求已经不存在的 chunk → 白屏。
	cc := w.Header().Get("Cache-Control")
	if strings.Contains(cc, "immutable") || strings.Contains(cc, "max-age") {
		t.Errorf("index.html 的 Cache-Control = %q，绝不能长缓存（发版后会白屏）", cc)
	}
	if cc != revalidateCacheControl {
		t.Errorf("index.html 的 Cache-Control = %q，想要 %q", cc, revalidateCacheControl)
	}
	if w.Header().Get("ETag") == "" {
		t.Error("index.html 缺少 ETag，无法做 304 校验")
	}
	if !strings.Contains(w.Body.String(), "id=app") {
		t.Error("响应体不是 index.html")
	}
}

func TestStaticConditionalRequestReturns304(t *testing.T) {
	r := newStaticTestRouter(t)
	first := doGet(r, "/", map[string]string{"Accept-Encoding": "gzip"})
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("首次响应没有 ETag")
	}

	second := doGet(r, "/", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": etag})
	if second.Code != http.StatusNotModified {
		t.Fatalf("带 If-None-Match 的状态码 = %d，想要 304（否则每次刷新都要重传）", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 响应不应有响应体，却有 %d 字节", second.Body.Len())
	}
}

func TestStaticSPAFallback(t *testing.T) {
	r := newStaticTestRouter(t)
	// 前端路由（/console/dashboard）不是真实文件，必须回退到 index.html，
	// 否则刷新页面就是 404
	w := doGet(r, "/console/dashboard", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200（SPA 回退）", w.Code)
	}
	if !strings.Contains(w.Body.String(), "id=app") {
		t.Error("SPA 回退没有返回 index.html")
	}

	// 接口路径必须留在 404，不能被 SPA 回退吞掉 —— 否则前端会把
	// 一个 HTML 页面当成 JSON 解析，报出与真实原因无关的错误
	api := doGet(r, "/api/admin/whatever", nil)
	if api.Code != http.StatusNotFound {
		t.Errorf("/api/... 状态码 = %d，想要 404（不能被 SPA 回退吞掉）", api.Code)
	}
	if strings.Contains(api.Body.String(), "id=app") {
		t.Error("/api/... 返回了 index.html，接口命名空间被 SPA 回退吞掉了")
	}
}

func TestStaticIndexHTMLRedirectKeepsOldBehavior(t *testing.T) {
	r := newStaticTestRouter(t)
	// 改造前是 http.FileServer，它会把 /index.html 301 到 /。
	// 这里保持一致，避免多出一个「两个 URL 同一内容」的缓存副本。
	w := doGet(r, "/index.html", nil)
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("状态码 = %d，想要 301", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q，想要 /", got)
	}
}

func TestStaticHeadRequest(t *testing.T) {
	r := newStaticTestRouter(t)
	req := httptest.NewRequest(http.MethodHead, "/"+hashedJS, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HEAD 状态码 = %d，想要 200", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("HEAD 不应有响应体，却有 %d 字节", w.Body.Len())
	}
	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("HEAD 的 Content-Encoding = %q，想要 gzip（头部应与 GET 一致）", got)
	}
}

func TestAcceptsGzip(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{"gzip", true},
		{"gzip, deflate, br", true},
		{"deflate, br", false},
		{"gzip;q=0", false},    // 明确拒绝 gzip
		{"gzip;q=0.0", false},  // 同上
		{"gzip;q=0, *", false}, // 点名 gzip 拒绝，优先级高于 *
		{"br, *", true},        // * 表示接受其它编码
		{"*", true},            // 通配
		{"*;q=0", false},       // 全部拒绝
		{"identity", false},
		{"x-gzip", true},              // 历史别名
		{"GZIP", true},                // 大小写不敏感
		{"deflate, gzip;q=0.5", true}, // 带权重的接受
	}
	for _, c := range cases {
		if got := acceptsGzip(c.header); got != c.want {
			t.Errorf("acceptsGzip(%q) = %v，想要 %v", c.header, got, c.want)
		}
	}
}

func TestHashedAssetPattern(t *testing.T) {
	// 只有**名字里带内容哈希**的产物才允许长缓存。这个判断一旦放宽，
	// 没带哈希的文件就会被钉死一年、内容改了用户也拿不到。
	hashed := []string{
		"assets/index-BGySQ1GG.js",
		"assets/antd-txdUIxG_.js",
		"assets/index-R-LChBsp.css",
		"assets/MainLayout-DPy_Mro_.js",
	}
	plain := []string{
		"index.html",
		"favicon.svg",
		"fonts/Harding-Regular.ttf",
		"assets/logo.svg",       // 在 assets/ 下但没有哈希
		"assets/readme.css",     // 同上
		"assets/app.js",         // 短名字，不视为哈希
		"cursor-arrow-dark.svg", // 根目录
	}
	for _, p := range hashed {
		if !hashedAssetRe.MatchString(p) {
			t.Errorf("hashedAssetRe(%q) = false，应当视为带哈希可长缓存", p)
		}
	}
	for _, p := range plain {
		if hashedAssetRe.MatchString(p) {
			t.Errorf("hashedAssetRe(%q) = true，不带哈希不该长缓存", p)
		}
	}
}

func TestCleanRelPathRejectsTraversal(t *testing.T) {
	// 路径收敛：`/../etc/passwd` 这类必须被消解，不能逃出 embed 根目录。
	cases := map[string]string{
		"/":                    "",
		"/index.html":          "index.html",
		"/assets/app.js":       "assets/app.js",
		"/../etc/passwd":       "etc/passwd",
		"/assets/../../secret": "secret",
		"//assets//app.js":     "assets/app.js",
		"/./assets/./app.js":   "assets/app.js",
	}
	for in, want := range cases {
		if got := cleanRelPath(in); got != want {
			t.Errorf("cleanRelPath(%q) = %q，想要 %q", in, got, want)
		}
	}
}

func TestContentTypeForReportsOriginalType(t *testing.T) {
	// .gz 必须报原始类型：报成 application/gzip 的话浏览器不会当脚本执行
	cases := map[string]string{
		"app.js":           "text/javascript; charset=utf-8",
		"index.html":       "text/html; charset=utf-8",
		"style.css":        "text/css; charset=utf-8",
		"font.ttf":         "font/ttf",
		"icon.svg":         "image/svg+xml",
		"data.webmanifest": "application/manifest+json",
		"unknown.xyz":      "application/octet-stream",
	}
	for name, want := range cases {
		if got := contentTypeFor(name); got != want {
			t.Errorf("contentTypeFor(%q) = %q，想要 %q", name, got, want)
		}
	}
}
