// Package update 实现「检测新版本 → 下载 → 校验 → 应用 → 可回滚」的完整链路。
//
// 设计上有三个刻意的取舍，先写在最前面，因为它们解释了后面几乎每一处形状。
//
// # 1. 三种部署形态，三套更新动作
//
// 同一份代码可能以三种方式跑着，而「更新」对它们意味着完全不同的事：
//
//	source  本地随手 go build 出来的。没有发布产物与之对应 ——
//	        界面上只提示有新版本并给一个跳转链接，**绝不**提供一键更新，
//	        否则会用官方二进制覆盖掉开发者本地的构建物。
//	binary  官方预编译二进制 + systemd。进程可以直接原子替换自己的可执行文件，
//	        然后退出让 systemd（Restart=always）用新文件把自己拉起来。
//	docker  官方镜像。进程在容器里**不能**替换自己 —— 替换了也没用，
//	        容器一重建就没了。必须请宿主侧的 updater 拉取新镜像并重建容器。
//
// 把这三者混成一条路径是这类功能最常见的错误：它能编译、能跑通测试、
// 却会在真实部署上做错事（最典型的是在容器里下载二进制覆盖 /app/llm-relay，
// 看上去更新成功，重启后一切照旧）。
//
// # 2. 更新是**异步任务**，不是一次 HTTP 请求
//
// docker 形态要拉一个上百 MB 的镜像，binary 形态要跨洋下载归档 ——
// 都是分钟级的操作。同步 HTTP 请求在这条时间线上必然出事：浏览器 30 秒
// 断开、nginx 60 秒掐连接、用户切个标签页就前功尽弃。
//
// 所以 Apply 只负责「启动任务」并立刻返回，真正的进度由任务状态轮询接口
// 暴露给界面（阶段 + 百分比 + 日志）。这样即使前端断线、刷新页面，
// 任务仍在跑，回来还能看到它跑到哪一步了。
//
// # 3. 更新完不自动重启，回滚要留证据
//
// 替换完文件后进程**不自杀**，而是把「需要重启」这个事实返回给界面，
// 由人点一下重启。理由是排障：替换后如果新版本启动就崩，自动重启会把
// 服务变成崩溃循环，而此刻还留在旧进程上的管理台正是唯一的救援入口。
//
// 回滚能力依赖两处证据：binary 形态是替换时留下的 .backup 文件，
// docker 形态是宿主侧 updater 在动手前打上的**回滚标签**
// （<镜像名>:rollback）。两者都在应用新版本之前写就，所以哪怕更新中途
// 断电，回滚依据依然完整。
//
// docker 形态为什么是标签而不是「上一个镜像的 ID」：实测发现
// `docker compose build` 把 tag 指向新镜像后，旧镜像会被回收
// （Docker 29 + containerd snapshotter），此后用那个 ID 回滚必然
// 报 "No such image"。详见 cmd/updater 里 tagRollback 的注释。
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"llm-relay/internal/netguard"
)

// 发布源相关常量。
const (
	// defaultRepo 是发布产物所在的 GitHub 仓库。
	defaultRepo = "ashu1800/llm-relay"

	// 允许下载的主机白名单。
	//
	// GitHub 的 release 资源下载会**三级跳**：
	//   github.com/.../releases/download/...  →  302
	//   objects.githubusercontent.com/...     →  200（签名 URL，几分钟后失效）
	// 所以只放行 github.com 会让下载永远停在第一跳。这里把整条链上的
	// 主机都列出来，并且**每一跳都重新校验一次**（见 download 的实现）——
	// 不是只在开头校验一次就闭眼跟随。
	allowedAPPHost   = "api.github.com"
	allowedHTMLHost  = "github.com"
	allowedAssetHost = "objects.githubusercontent.com"
	// 备用资源域。GitHub 会按区域/负载切换，实测两种都出现过
	allowedAltAssetHost = "release-assets.githubusercontent.com"

	// apiUserAgent 是 GitHub API 要求必须带的（不带会被拒）。
	apiUserAgent = "llm-relay-updater"
)

// 安全上限。这些数字不是拍脑袋来的，每一个都对应一种具体的攻击或事故：
const (
	// 归档下载上限。本项目二进制约 30MB，500MB 给足余量又不至于
	// 让一次「下载」把磁盘写满（Content-Length 缺失时靠 LimitReader 兜底）
	maxArchiveSize = 500 * 1024 * 1024

	// 解压出的单文件上限：防解压炸弹（几 KB 的 gzip 能解出几十 GB）
	maxBinarySize = 300 * 1024 * 1024
)

// ReleaseInfo 是给界面看的发布信息。
type ReleaseInfo struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	PublishedAt string `json:"published_at"`
	HTMLURL     string `json:"html_url"`
	Prerelease  bool   `json:"prerelease"`
}

// Release 是 GitHub releases 接口的一条记录。
type Release struct {
	TagName     string  `json:"tag_name"`
	Name        string  `json:"name"`
	Body        string  `json:"body"`
	PublishedAt string  `json:"published_at"`
	HTMLURL     string  `json:"html_url"`
	Draft       bool    `json:"draft"`
	Prerelease  bool    `json:"prerelease"`
	Assets      []Asset `json:"assets"`
}

// Asset 是发布产物里的一个文件。
type Asset struct {
	Name string `json:"name"`
	// BrowserDownloadURL 是浏览器可下载的地址（github.com/.../releases/download/...），
	// 它会 302 到带签名的 CDN 地址 —— 这也是下载要跟随跳转的原因
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// ArchiveName 返回当前平台对应的归档文件名。
//
// 命名契约（由 .goreleaser.yaml 与 .github/workflows/release.yml 保证）：
//
//	llm-relay_<version>_<goos>_<goarch>.tar.gz     windows 用 .zip
//
// 版本号**不带 v 前缀**。这个函数与发布流水线是一对：改一边必须改另一边，
// 否则表现是「检测到新版本但找不到对应产物」，而那要到真发版时才会暴露。
func ArchiveName(version, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("llm-relay_%s_%s_%s.%s", strings.TrimPrefix(version, "v"), goos, goarch, ext)
}

// BinaryName 是归档内可执行文件的名字（windows 带 .exe）。
// 解压时按精确名字查找，所以它同时是发布流水线的约束。
func BinaryName(goos string) string {
	if goos == "windows" {
		return "llm-relay.exe"
	}
	return "llm-relay"
}

// ChecksumAssetName 是校验和文件名。
const ChecksumAssetName = "checksums.txt"

// Client 访问 GitHub Releases。
type Client struct {
	repo    string
	token   string
	api     *http.Client
	fetcher *http.Client
}

// ClientOptions 是 Client 的构造参数。
type ClientOptions struct {
	// Repo 形如 "owner/name"，留空用 defaultRepo
	Repo string
	// Token 是可选的 GitHub token。仅对 api.github.com 发送 ——
	// 它能把 API 限额从 60 次/小时提到 5000，国内共享出口 IP 很容易撞限额
	Token string
	// ProxyURL 是可选的上游代理（http/https/socks5）。
	// 国内服务器直连 GitHub 常年不通，这是给那种部署留的口子
	ProxyURL string
}

// NewClient 构造 GitHub 客户端。
//
// 两个 client 分开是有意的：API 查询是几十毫秒的小请求，30 秒超时足够；
// 下载归档是分钟级的大流量，需要完全不同的超时。用一个 client 只能二选一，
// 结果不是「查询经常超时」就是「下载被提前掐断」。
func NewClient(opts ClientOptions) (*Client, error) {
	repo := strings.TrimSpace(opts.Repo)
	if repo == "" {
		repo = defaultRepo
	}
	if !strings.Contains(repo, "/") {
		return nil, fmt.Errorf("update.repo 必须形如 owner/name，当前: %q", repo)
	}

	// 代理只影响「连出去」的方式，不影响目标校验。
	// netguard 的 Control 仍然挂在 DialContext 上：走代理时它校验的是
	// 代理地址（运维自己配的可信出口），这正是想要的行为。
	base, err := guardedTransport(opts.ProxyURL)
	if err != nil {
		return nil, err
	}

	apiTransport := base.Clone()
	fetchTransport := base.Clone()

	return &Client{
		repo:  repo,
		token: strings.TrimSpace(opts.Token),
		api: &http.Client{
			Timeout:   30 * time.Second,
			Transport: apiTransport,
			// API 请求不允许跳转：跳转会带着 Authorization 头去别的域
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		fetcher: &http.Client{
			Timeout:   10 * time.Minute,
			Transport: fetchTransport,
			// 下载**需要**跳转，但这里不让 http 包自己跳 ——
			// 每次跳转都要重新过一遍主机与 IP 校验，所以手动跟（见 download）。
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// guardedTransport 构造带 SSRF 防护的 transport，可选走代理。
func guardedTransport(proxyURL string) (*http.Transport, error) {
	var t *http.Transport
	if strings.TrimSpace(proxyURL) != "" {
		u, err := url.Parse(strings.TrimSpace(proxyURL))
		if err != nil {
			return nil, fmt.Errorf("update.proxy 不是合法 URL: %w", err)
		}
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return nil, fmt.Errorf("update.proxy 只支持 http/https/socks5/socks5h，当前: %q", u.Scheme)
		}
		t = &http.Transport{
			Proxy: http.ProxyURL(u),
			// 复用 Go 默认 transport 的连接参数（超时、keep-alive、HTTP/2）
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
	}
	return netguard.GuardedTransport(t), nil
}

// FetchLatestRelease 取最新的正式版本（不含 draft 与 prerelease）。
//
// 用 /releases/latest 而不是 /releases 的第一条：后者会把预发布版本
// 排在前面，于是「最新」可能是一个用户根本不该装的 rc。
func (c *Client) FetchLatestRelease(ctx context.Context) (*Release, error) {
	var rel Release
	if err := c.getJSON(ctx, fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", c.repo), &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// FetchReleases 取最近若干个版本（供回滚列表使用）。
func (c *Client) FetchReleases(ctx context.Context, perPage int) ([]Release, error) {
	if perPage <= 0 {
		perPage = 10
	}
	if perPage > 100 {
		// GitHub API 的硬上限就是 100，传更大不会报错但会被静默截断
		perPage = 100
	}
	var list []Release
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=%d", c.repo, perPage)
	if err := c.getJSON(ctx, endpoint, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// getJSON 发一次 API 请求并解析 JSON 响应。
func (c *Client) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", apiUserAgent)
	// token 只发给 api.github.com，且只在 host 完全相等时 ——
	// 用 HasSuffix 之类会让 evil-api.github.com 也拿到它
	if c.token != "" {
		if u, err := url.Parse(endpoint); err == nil && strings.EqualFold(u.Host, allowedAPPHost) {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
	}

	resp, err := c.api.Do(req)
	if err != nil {
		return fmt.Errorf("请求 GitHub 失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("仓库 %s 不存在或没有发布记录", c.repo)
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		// 国内共享出口 IP 很容易撞上：未带 token 时限额只有 60 次/小时
		return fmt.Errorf("GitHub API 限额已用尽（%d）。可在系统设置里配置 update.token 提升额度", resp.StatusCode)
	default:
		return fmt.Errorf("GitHub API 返回 %d", resp.StatusCode)
	}

	// 限制读取量：正常响应几十 KB，给 8MB 足够，
	// 避免一个异常大的响应把内存吃掉
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return fmt.Errorf("解析 GitHub 响应失败: %w", err)
	}
	return nil
}

// FindAsset 在发布产物里找平台归档与校验和文件。
//
// 用**精确相等**匹配而不是 strings.Contains：Contains 会让
// `llm-relay_1.2.3_linux_amd64.tar.gz` 也匹配上
// `llm-relay_1.2.3_linux_amd64_musl.tar.gz`，从而下错文件 ——
// 而校验和会拦下来，表现成一句莫名其妙的「校验和不匹配」。
func FindAsset(assets []Asset, name string) (Asset, bool) {
	for _, a := range assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// ChecksumURL 从发布产物里取 checksums.txt 的下载地址。
func ChecksumURL(assets []Asset) string {
	if a, ok := FindAsset(assets, ChecksumAssetName); ok {
		return a.BrowserDownloadURL
	}
	return ""
}

// ---------------------------------------------------------------------------
// 下载
// ---------------------------------------------------------------------------

// maxRedirects 是手动跟随跳转的次数上限。
// GitHub 实际用 2 跳（github.com → objects.githubusercontent.com），
// 给 5 跳足够，同时防止一个恶意/错误的 Location 链把请求无限续下去。
const maxRedirects = 5

// validateDownloadURL 校验一个下载地址是否可信。
//
// 两道关，缺一不可：
//
//  1. **必须是 HTTPS** —— HTTP 会让归档在途中被替换，而校验和文件同样走
//     HTTP 的话，攻击者把两者一起换掉就完全绕过了校验。
//  2. **主机必须在白名单内** —— 这是防 SSRF 的第一层。GitHub 发布页上
//     的 browser_download_url 由 GitHub 生成、可信，但这里不假设它可信：
//     走代理、DNS 被污染、或者将来换成自建发布源时，这个假设都会失效。
func validateDownloadURL(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("下载地址不是合法 URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("下载地址必须是 https，当前: %q", u.Scheme)
	}
	if u.User != nil {
		// https://user:pass@github.com/... 这种形状没有任何正当用途，
		// 只会被用来把凭据混进 URL 或欺骗肉眼检查
		return fmt.Errorf("下载地址不允许携带用户名密码")
	}
	// 用 Hostname() 而不是 Host：后者含端口，
	// 于是 https://github.com:8443/... 会被判为不在白名单 —— 这是 fail-closed，
	// 但错误信息会让人以为是网络问题，所以显式取出主机名来比
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("下载地址缺少主机名")
	}
	switch {
	case host == allowedHTMLHost, strings.HasSuffix(host, "."+allowedHTMLHost):
	case host == allowedAssetHost, strings.HasSuffix(host, "."+allowedAssetHost):
	case host == allowedAltAssetHost, strings.HasSuffix(host, "."+allowedAltAssetHost):
	default:
		return fmt.Errorf("下载地址指向不受信任的主机: %s", host)
	}
	return nil
}

// Download 把 rawURL 指向的文件下载到 dest，并限制大小。
//
// 跳转是**手动**跟随的，每一跳都重跑一遍主机白名单与 IP 校验。
// 让 http.Client 自动跟随是不行的：那样只有第一个地址被校验过，
// 之后的每一跳都是盲信 —— 一个被劫持的 302 就能把下载引到任意地址，
// 而我们还在往磁盘上写一个「官方二进制」。
func (c *Client) Download(ctx context.Context, rawURL, dest string, maxSize int64) error {
	if maxSize <= 0 {
		maxSize = maxArchiveSize
	}
	current := strings.TrimSpace(rawURL)

	for hop := 0; hop <= maxRedirects; hop++ {
		if err := validateDownloadURL(current); err != nil {
			return err
		}
		u, err := url.Parse(current)
		if err != nil {
			return err
		}
		// 第二层：解析出的每个 IP 都必须是公网地址。
		// 挡的是「白名单域名被解析到内网」这种形态（DNS 污染 / hosts 投毒），
		// 以及把 github.com 指到 127.0.0.1 来读本机文件的尝试
		if _, err := netguard.CheckHost(u.Hostname()); err != nil {
			return fmt.Errorf("下载目标被拒绝: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", apiUserAgent)
		req.Header.Set("Accept", "application/octet-stream")

		resp, err := c.fetcher.Do(req)
		if err != nil {
			return fmt.Errorf("下载失败: %w", err)
		}

		// 跳转：读出下一跳地址，关掉响应体后继续循环
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := resp.Header.Get("Location")
			_ = resp.Body.Close()
			if loc == "" {
				return fmt.Errorf("服务端返回 %d 但没有给 Location", resp.StatusCode)
			}
			next, err := resp.Request.URL.Parse(loc)
			if err != nil {
				return fmt.Errorf("跳转地址无法解析: %w", err)
			}
			current = next.String()
			continue
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return fmt.Errorf("下载返回 %d", resp.StatusCode)
		}

		err = writeLimited(resp, dest, maxSize)
		_ = resp.Body.Close()
		return err
	}
	return fmt.Errorf("跳转次数超过 %d 次，已中止", maxRedirects)
}

// writeLimited 把响应体写到文件，超过上限就报错并删掉半成品。
//
// 三重防护：
//  1. Content-Length 预检（有的话）—— 能在下载前就拒掉一个巨大的文件；
//  2. LimitReader(maxSize+1) 兜底 —— Content-Length 可能缺失或撒谎
//     （分块传输、或者恶意服务端），所以实流也要限；
//  3. 多读 1 字节再判 `written > maxSize`：只靠 LimitReader 无法区分
//     「正好等于上限」与「被截断了」，多读一个字节才能确定是超限。
func writeLimited(resp *http.Response, dest string, maxSize int64) error {
	if resp.ContentLength > maxSize {
		return fmt.Errorf("文件过大: %d 字节（上限 %d）", resp.ContentLength, maxSize)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}

	written, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxSize+1))
	closeErr := f.Close()
	if copyErr != nil {
		// 半成品必须删掉：留着它，下一轮校验和会报「不匹配」，
		// 把「网络中断」伪装成「文件被篡改」，排查方向完全跑偏
		_ = os.Remove(dest)
		return fmt.Errorf("写入失败: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("关闭文件失败: %w", closeErr)
	}
	if written > maxSize {
		_ = os.Remove(dest)
		return fmt.Errorf("下载超过上限 %d 字节，已中止", maxSize)
	}
	if written == 0 {
		_ = os.Remove(dest)
		return fmt.Errorf("下载到的文件是空的")
	}
	return nil
}

// FetchChecksums 取校验和文件的原始内容。
func (c *Client) FetchChecksums(ctx context.Context, rawURL string) ([]byte, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, fmt.Errorf("没有校验和文件地址")
	}
	if err := validateDownloadURL(rawURL); err != nil {
		return nil, err
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if _, err := netguard.CheckHost(u.Hostname()); err != nil {
		return nil, fmt.Errorf("校验和地址被拒绝: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", apiUserAgent)

	// 校验和文件很小，用 api client（30 秒超时）足够，
	// 而且它不跟随跳转 —— 校验和地址理论上不该跳转，跳了就当作异常
	resp, err := c.api.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载校验和失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载校验和返回 %d", resp.StatusCode)
	}
	// 1MB 上限：正常的 checksums.txt 只有几百字节到几 KB
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
