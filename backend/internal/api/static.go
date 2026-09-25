package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// 静态资源的发送策略（2026-09-25）。
//
// 起因是「静态资源压缩，降低后端与前端之间的带宽压力」。改造前实测的数据：
//
//	dist 全部产物     1,611,657 B → gzip 522,982 B（32.4%）
//	最大的 antd chunk   885,607 B → gzip 264,456 B（29.9%）
//	fonts/*.ttf         312,340 B → gzip 102,589 B（32.8%）
//
// 而当时 `curl -I` 显示这些响应**一个缓存相关头都没有**（只有 Content-Type 与
// Date）：既没有 Content-Encoding（不压缩，1.6 MB 原样传），也没有 ETag /
// Cache-Control（浏览器每次都得重新下载，无法复用）。所以这里做两件事：
//
//  1. **预压缩**：压缩在构建期完成（cmd/precompress 生成同目录的 .gz），
//     运行时只是挑文件发出去。不在请求路径上现压，省掉每次请求的 CPU，
//     而且压缩级别可以开到最高 —— 构建期慢一点无所谓。
//  2. **正确的缓存策略**：Vite 产物名里带内容哈希，内容变了名字就变，
//     所以 /assets/ 下的文件可以 `immutable` 长缓存；而 index.html 必须
//     **每次校验**，否则用户会拿着旧的 HTML 去请求已经删掉的 chunk 名，
//     页面直接白屏。这就是下面 hashedAssetRe 与两种 Cache-Control 的区别。
//
// 不碰接口与 WebSocket：这次只处理静态资源（API 响应压缩是另一件事，
// 涉及的排除逻辑不同 —— SSE 流式响应、已经压缩过的响应体都要跳过）。
const (
	// 带内容哈希的产物（如 assets/antd-DJOU_eaF.js）可以永久缓存：
	// 内容一变，文件名里的哈希就变，URL 自然也就变了。
	// 名字里必须有哈希才敢这么缓存，所以这里用正则卡住，而不是「只要在
	// assets/ 下就永久缓存」—— 将来万一有没带哈希的文件放进 assets/，
	// 它就落到下面的 no-cache 分支，不会被钉死一年。
	immutableCacheControl = "public, max-age=31536000, immutable"
	// 其余文件（index.html、favicon、光标图、字体）都要每次校验。
	// no-cache 不是「不缓存」，而是「用之前先问一下」：命中就是 304，
	// 没有响应体，成本极低；文件变了则立刻拿到新的。
	revalidateCacheControl = "no-cache"
)

var hashedAssetRe = regexp.MustCompile(`^assets/.+-[A-Za-z0-9_-]{8,}\.[A-Za-z0-9]+$`)

// 显式列出 Content-Type，不走 mime.TypeByExtension。
//
// 后者在 Linux 容器里没问题，但在 Windows 上会读注册表，同一个扩展名
// 可能返回 application/x-javascript 之类的结果 —— 测试跑在 Windows、
// 服务跑在 Alpine，两边不一致就会让「本机通过、线上不同」的坑很难查。
// 而且 .gz 文件必须报**原始类型**（.js.gz 要报 text/javascript），
// 所以类型一定要按原文件名算、单独给出。
var staticContentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json; charset=utf-8",
	".map":         "application/json; charset=utf-8",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".gif":         "image/gif",
	".webp":        "image/webp",
	".ico":         "image/x-icon",
	".ttf":         "font/ttf",
	".otf":         "font/otf",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".txt":         "text/plain; charset=utf-8",
	".webmanifest": "application/manifest+json",
}

func contentTypeFor(name string) string {
	if ct, ok := staticContentTypes[strings.ToLower(path.Ext(name))]; ok {
		return ct
	}
	return "application/octet-stream"
}

// staticAssets 负责把嵌入的前端产物发出去。
//
// 分成两条路：
//   - 带哈希的产物走流式发送（不整份读进内存）—— 它们体积最大，而
//     embed.FS 的文件支持 Seek，http.ServeContent 需要一个 ReadSeeker
//     就能处理 Range / If-None-Match / HEAD，不必自己实现。
//   - 其它文件（index.html、字体、图标）体积都很小，第一次被请求时读进内存
//     并算好 ETag 缓存起来：它们要参与 304 校验，而 ETag 必须对内容做哈希，
//     每次请求都重算 312 KB 字体的哈希是纯浪费。
type staticAssets struct {
	dist     fs.FS
	gz       map[string]struct{} // 存在 .gz 兄弟文件的路径集合
	indexErr error
	logger   *slog.Logger
	small    sync.Map // reprName -> *cachedAsset
}

type cachedAsset struct {
	body []byte
	etag string
}

// newStaticAssets 扫描产物目录，索引出哪些文件有预压缩版本。
//
// 预先扫一遍而不是每次请求去试 dist.Open(name+".gz")：后者对每个没有 .gz
// 的文件都会走一次失败查找（还不好缓存失败的结论），而产物目录只有几百个
// 文件，启动时走一遍更省事，也能顺便在日志里报出「压了多少、省了多少」，
// 让部署的人一眼看到压缩有没有真的生效。
func newStaticAssets(dist fs.FS, logger *slog.Logger) *staticAssets {
	a := &staticAssets{dist: dist, gz: map[string]struct{}{}, logger: logger}

	var totalRaw, totalGz int64
	_ = fs.WalkDir(dist, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".gz") {
			return nil
		}
		base := strings.TrimSuffix(p, ".gz")
		a.gz[base] = struct{}{}
		if gi, gerr := fs.Stat(dist, p); gerr == nil {
			totalGz += gi.Size()
			if oi, oerr := fs.Stat(dist, base); oerr == nil {
				totalRaw += oi.Size()
			}
		}
		return nil
	})

	if len(a.gz) > 0 && logger != nil {
		logger.Info("静态资源已启用预压缩",
			"files", len(a.gz),
			"raw_bytes", totalRaw,
			"gz_bytes", totalGz,
			"ratio", fmt.Sprintf("%.1f%%", float64(totalGz)/float64(max(totalRaw, 1))*100))
	}

	if _, err := fsReadFile(dist, "index.html"); err != nil {
		a.indexErr = err
	}
	return a
}

// handler 返回 SPA 回退处理器：真实文件按上面的策略发送，
// 其余非接口路径一律回退到 index.html（前端路由接管）。
func (a *staticAssets) handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		if isAPIPath(p) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"message": "接口不存在", "type": "not_found_error"}})
			return
		}

		rel := cleanRelPath(p)
		switch {
		case rel == "":
			a.serveRevalidate(c, "index.html")
			return
		case rel == "index.html":
			// 与改造前 http.FileServer 的行为保持一致：/index.html → /
			c.Redirect(http.StatusMovedPermanently, "/")
			return
		}

		if st, err := fs.Stat(a.dist, rel); err == nil && !st.IsDir() {
			if hashedAssetRe.MatchString(rel) {
				a.serveImmutable(c, rel)
			} else {
				a.serveRevalidate(c, rel)
			}
			return
		}
		// 不是真实文件 → 交给前端路由
		a.serveRevalidate(c, "index.html")
	}
}

// cleanRelPath 把请求路径收敛成 fs.FS 能接受的相对路径。
//
// 走 path.Clean 而不是直接去掉前导斜杠：`/../etc/passwd` 这类路径必须在这里
// 被消解掉（Clean 会把越出根的部分去掉），而且 embed.FS 对非法路径会直接
// 报错。统一收口成「一定是合法相对路径」，后面的 Stat 就不必再防一次。
func cleanRelPath(p string) string {
	rel := path.Clean("/" + strings.TrimPrefix(p, "/"))
	rel = strings.TrimPrefix(rel, "/")
	if rel == "." {
		return ""
	}
	return rel
}

// serveImmutable 发送带内容哈希的产物：长缓存、流式、不做 ETag 校验。
//
// 不发 ETag 是有意的：`immutable` 的语义就是「一年内不要来问」，
// 浏览器不会发校验请求，算了也没人用；省下的是每次请求给整份文件做哈希。
func (a *staticAssets) serveImmutable(c *gin.Context, rel string) {
	repr, encoded := a.pickEncoding(c, rel)
	c.Header("Content-Type", contentTypeFor(rel))
	c.Header("Cache-Control", immutableCacheControl)
	// Vary 必须声明：同一个 URL 会按 Accept-Encoding 给出不同的字节，
	// 少了它，中间缓存（或浏览器自己的缓存键）可能把 gzip 的那份
	// 发给不支持 gzip 的客户端。
	c.Header("Vary", "Accept-Encoding")

	if err := a.serveFile(c, repr, rel, encoded); err != nil {
		c.Status(http.StatusNotFound)
	}
}

// serveRevalidate 发送需要每次校验的资源（含 index.html 与 SPA 回退）。
func (a *staticAssets) serveRevalidate(c *gin.Context, rel string) {
	if rel == "index.html" && a.indexErr != nil {
		c.String(http.StatusOK, "前端资源尚未构建")
		return
	}
	repr, encoded := a.pickEncoding(c, rel)
	asset, err := a.cached(repr)
	if err != nil {
		// index.html 读不到通常意味着产物没构建；真实文件读不到则当作回退
		c.String(http.StatusOK, "前端资源尚未构建")
		return
	}

	c.Header("Content-Type", contentTypeFor(rel))
	c.Header("Cache-Control", revalidateCacheControl)
	c.Header("Vary", "Accept-Encoding")
	c.Header("ETag", asset.etag)
	if encoded {
		c.Header("Content-Encoding", "gzip")
	}
	// ServeContent 会拿上面设好的 ETag 去比对 If-None-Match，
	// 命中就回 304（无响应体），顺带处理 Range / HEAD。
	// modtime 传零值：embed.FS 里的时间戳没有意义，用 ETag 做校验更可靠。
	http.ServeContent(c.Writer, c.Request, rel, time.Time{}, bytes.NewReader(asset.body))
}

// pickEncoding 决定这次发原文件还是预压缩版本。
func (a *staticAssets) pickEncoding(c *gin.Context, rel string) (repr string, encoded bool) {
	if _, ok := a.gz[rel]; !ok {
		return rel, false
	}
	if !acceptsGzip(c.Request.Header.Get("Accept-Encoding")) {
		return rel, false
	}
	return rel + ".gz", true
}

// serveFile 发送一份文件（流式）。readSeeker 优先，退化为整份读入。
func (a *staticAssets) serveFile(c *gin.Context, repr, rel string, encoded bool) error {
	f, err := a.dist.Open(repr)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if encoded {
		c.Header("Content-Encoding", "gzip")
	}
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(c.Writer, c.Request, rel, time.Time{}, rs)
		return nil
	}
	// 兜底：某些 fs.FS 实现的文件不支持 Seek
	body, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	http.ServeContent(c.Writer, c.Request, rel, time.Time{}, bytes.NewReader(body))
	return nil
}

// cached 读取小文件并缓存内容与 ETag（按「表示」缓存：.gz 与原文各一份）。
func (a *staticAssets) cached(repr string) (*cachedAsset, error) {
	if v, ok := a.small.Load(repr); ok {
		return v.(*cachedAsset), nil
	}
	body, err := fsReadFile(a.dist, repr)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	asset := &cachedAsset{body: body, etag: `"` + hex.EncodeToString(sum[:16]) + `"`}
	actual, _ := a.small.LoadOrStore(repr, asset)
	return actual.(*cachedAsset), nil
}

// acceptsGzip 解析 Accept-Encoding，判断 gzip 是否可用。
//
// 不能只做 strings.Contains：`gzip;q=0` 是明确的**拒绝**，
// 而 `br, *` 里的 `*` 又表示 gzip 可以接受。两种都必须判对 ——
// 前者判错会给出客户端明确说不要的编码，后者判错则白白放弃压缩。
func acceptsGzip(header string) bool {
	if header == "" {
		return false
	}
	gzipQ := -1.0
	starQ := -1.0
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name := part
		q := 1.0
		if i := strings.Index(part, ";"); i >= 0 {
			name = strings.TrimSpace(part[:i])
			for _, param := range strings.Split(part[i+1:], ";") {
				param = strings.TrimSpace(param)
				if v, ok := strings.CutPrefix(strings.ToLower(param), "q="); ok {
					if parsed, perr := strconv.ParseFloat(strings.TrimSpace(v), 64); perr == nil {
						q = parsed
					}
				}
			}
		}
		switch strings.ToLower(name) {
		case "gzip", "x-gzip":
			if q > gzipQ {
				gzipQ = q
			}
		case "*":
			if q > starQ {
				starQ = q
			}
		}
	}
	if gzipQ >= 0 {
		return gzipQ > 0
	}
	return starQ > 0
}
