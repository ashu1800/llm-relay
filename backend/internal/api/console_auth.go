package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/config"
	"llm-relay/internal/consoleauth"
)

// 管理台登录鉴权（无账号模型）。
//
// 原来管理后台的安全性完全建立在「只绑 127.0.0.1」上：接口层只有一道
// sameOriginOnly 的来源校验，谁摸得到端口谁就是管理员。这套假设在
// WSL2 本地部署下成立，一旦部署到公网服务器（哪怕只开 8888 一个端口）
// 就等于把渠道密钥、调用日志和全部管理操作交给整个互联网。
//
// 现在的认证是「二选一」的两把钥匙之一，外加一把引导钥匙：
//
//   - 引导密钥：来自环境变量 RELAY_ADMIN_KEY，只在认证方式为 unset
//     （首次部署、存量部署未完成向导）时是有效登录凭据。它存在的唯一
//     目的是保护设置向导 —— 公网部署上，「安装完成到用户完成初始化」
//     之间的窗口如果不设防，任何人都能抢先绑定自己的 2FA。完成向导
//     后它立即退役：登录凭据完全由用户自己掌控。
//   - 密钥模式（method=key）：登录凭用户在界面里手动设置的密钥。
//     哈希落在 settings 表（consoleauth.StateKey 那一行），改密、切换
//     都在界面完成，不再需要登服务器改环境变量。
//   - 2FA 模式（method=totp）：登录只凭 6 位动态码（RFC 6238）。
//
// 登录成功后签发**无状态**会话令牌（HMAC-SHA256 签名），写进
// HttpOnly Cookie。不建 session 表：个人自用工具不需要「踢掉某个
// 会话」的粒度，吊销靠「凭据版本」（下述）补齐。
//
// 会话签名密钥从 RELAY_SECRET 派生（而不是直接用任何一把登录钥匙）：
// 渠道密钥的 AES 派生是裸 SHA-256(secret)，这里用带域分隔前缀的
// SHA-256("llm-relay/console-session|"+secret)，两把钥匙互不相同，
// 其中一把被逆推出来也牵连不到另一把。
const (
	consoleCookieName = "console_session"
	// 令牌格式版本号。将来改载荷布局时升成 v2，校验按前缀分派。
	consoleTokenVersion = "v1"

	// 登录限流：15 分钟窗口内累计错 5 次就锁到窗口结束。
	// 公网部署后这里是唯一的暴力破解闸门，参数取「人手抖五次绰绰有余、
	// 脚本穷举基本无产出」的折中。按 IP 计数，IP 由 Gin 依
	// TrustedProxies 配置从 X-Forwarded-For 解析（见 config.ServerConfig）。
	loginMaxFailures = 5
	loginWindow      = 15 * time.Minute

	// 失败计数的 IP 容量上限。公网上的攻击者可以伪造海量源地址（每个请求
	// 一个新 IP）把这张 map 撑到很大的规模 —— 到顶之后不再为**新** IP 记账，
	// 换取内存有界；此时既有 IP 的限流照常生效，而「用超大规模 IP 池攻击」
	// 本来就不是这套个人工具要对抗的威胁级。正常的单人使用永远到不了这个量。
	loginMaxTrackedIPs = 8192
)

// loginFailureDelay 是失败路径的固定延迟：不挡人（人感觉不到半秒），
// 只让自动化穷举的单次成本从微秒级抬到百毫秒级。成功路径不睡。
// 是 var 而非 const：测试里要把它临时归零，不然限流用例要白等数秒。
var loginFailureDelay = 300 * time.Millisecond

// initConsoleAuth 从配置派生登录鉴权所需的全部派生量，并打开认证状态。
//
// 两个独立的部分，刻意不互相短路：
//  1. 会话签名密钥与会话时长：只依赖 RELAY_SECRET，任何时候都要有 ——
//     没有它连「鉴权关闭」的部署也无法在 UI 里启用认证。
//  2. authStore：认证状态（方式 + 凭据 + 会话版本）。打开失败（库不可达、
//     状态行损坏）不让进程起不来：authStore 置 nil，登录接口 fail closed
//     并明确报错，其余功能照常。
//
// 引导密钥（AdminKey）为空时只是「unset 状态下的登录关闭」（旧本地部署
// 行为不变）：此时用户仍可从登录页走设置向导启用认证 —— 那条路是
// 本地部署升级为受保护部署的唯一入口，不能因为没设环境变量就堵死。
func (s *Server) initConsoleAuth(cfg *config.Config, logger *slog.Logger) {
	s.sessionTTL = cfg.Security.SessionTTL
	s.loginGuards = newLoginLimiter()
	// 域分隔派生：与渠道密钥的 AES 派生（裸 SHA-256(secret)）互不干扰
	sessionSum := sha256.Sum256([]byte("llm-relay/console-session|" + cfg.Security.Secret))
	s.sessionKey = append([]byte(nil), sessionSum[:]...)

	if strings.TrimSpace(cfg.Security.AdminKey) == "" {
		if logger != nil {
			logger.Warn("管理后台登录鉴权未启用（未设置 RELAY_ADMIN_KEY）。" +
				"服务只绑回环时尚可接受；可在登录页走设置向导启用认证 —— " +
				"公网部署则必须先设置引导密钥")
		}
	} else {
		sum := sha256.Sum256([]byte(cfg.Security.AdminKey))
		s.adminKeyHash = append([]byte(nil), sum[:]...)
		s.adminKeyVersion = append([]byte(nil), sum[:8]...)
	}

	// 认证状态（方式 + 凭据）从 settings 表加载。库还没就绪时 store 为 nil，
	// 登录接口 fail closed；openStoreWithRetry 的启动重试保证这种情况罕见。
	if s.deps != nil && s.deps.Store != nil {
		sealer, err := consoleauth.NewSealer(cfg.Security.Secret)
		if err != nil {
			if logger != nil {
				logger.Warn("认证状态加密器初始化失败，登录不可用", "err", err)
			}
			return
		}
		store, err := consoleauth.NewStore(s.deps.Store.DB(), sealer)
		if err != nil {
			if logger != nil {
				logger.Warn("认证状态加载失败，登录不可用（修复后重启恢复）", "err", err)
			}
			return
		}
		s.authStore = store
	}
}

// authMethod 返回当前认证方式；状态不可用时返回 unset（最保守：
// unset 模式下登录需要引导密钥，攻击者拿不到比拿到更多）。
func (s *Server) authMethod() consoleauth.Method {
	if s.authStore == nil {
		return consoleauth.MethodUnset
	}
	return s.authStore.Snapshot().Method
}

// consoleAuthEnabled 报告登录鉴权是否启用：
// unset 状态需要引导密钥（没配 = 关闭，旧行为）；
// key/totp 状态天然启用 —— 用户自己的凭据不依赖环境变量。
func (s *Server) consoleAuthEnabled() bool {
	method := s.authMethod()
	switch method {
	case consoleauth.MethodKey, consoleauth.MethodTOTP:
		return true
	default:
		return s.adminKeyHash != nil
	}
}

// consoleLoginBlocked 报告登录是否因依赖缺失而不可用（fail closed）：
// authStore 为 nil 且已配置引导密钥 —— 库不可达或状态行损坏，
// 凭据验证依赖状态，宁可拒服务也不能退回「只比引导密钥」的旧口径
// （那会让 key/totp 用户的会话校验、2FA 用户的登录全部失去意义）。
func (s *Server) consoleLoginBlocked() bool {
	if s.authStore != nil {
		return false
	}
	// 没装配数据库的极端环境（部分纯 API 测试）：unset + 引导密钥
	// 模式仍按旧口径工作 —— 与「鉴权关闭」的行为一致
	return s.deps != nil && s.deps.Store != nil && s.adminKeyHash != nil
}

// adminKeyMatches 常数时间比对登录密钥与引导密钥。
// 双方都比成定长 SHA-256 再比对，长度差异不会从耗时里漏出去。
func (s *Server) adminKeyMatches(presented string) bool {
	if s.adminKeyHash == nil {
		return false
	}
	sum := sha256.Sum256([]byte(presented))
	return hmac.Equal(sum[:], s.adminKeyHash)
}

// ---- 会话令牌 ----
//
// 令牌形如 v1.<base64url(payload)>.<base64url(sig)>：
//   payload = 8 字节绝对过期时间（unix 秒，big-endian）|| 8 字节凭据版本
//   sig     = HMAC-SHA256(sessionKey, payload)
//
// 过期判定放在签发时刻一次性定死（绝对过期），进程重启、换了浏览器
// 时区都不影响；想要更长会话就调大 RELAY_SESSION_TTL，不做滑动续期 ——
// 个人工具里「记得每 7 天登录一次」的代价远小于一套续期逻辑的复杂度。
//
// 凭据版本来自 consoleauth.State.CredVersion：改密、换绑、切换、
// 重新初始化都会轮换它，旧令牌即刻失效 —— 无状态令牌的吊销能力。

func (s *Server) signSessionToken(exp time.Time) string {
	ver := s.credVersionBytes()
	if ver == nil {
		// 认证状态不可用时拒绝签发（登录路径已 fail closed，
		// 这里是最后一道防线：宁可不发令牌，不发「版本未知」的令牌）
		return ""
	}
	payload := make([]byte, 16)
	binary.BigEndian.PutUint64(payload[:8], uint64(exp.Unix()))
	copy(payload[8:], ver)
	mac := hmac.New(sha256.New, s.sessionKey)
	mac.Write(payload)
	sig := mac.Sum(nil)

	var b strings.Builder
	b.WriteString(consoleTokenVersion)
	b.WriteByte('.')
	b.WriteString(base64.RawURLEncoding.EncodeToString(payload))
	b.WriteByte('.')
	b.WriteString(base64.RawURLEncoding.EncodeToString(sig))
	return b.String()
}

// credVersionBytes 把当前凭据版本转成令牌载荷里的 8 字节。
// 状态不可用/版本为空时返回 nil：sign 端拒绝签发、verify 端拒绝一切
// —— fail closed，绝不出现「全零版本」这种两边都认的退化值。
func (s *Server) credVersionBytes() []byte {
	v := s.credVersionHex()
	if len(v) != 16 {
		return nil
	}
	raw, err := hexDecode8(v)
	if err != nil {
		return nil
	}
	return raw
}

func (s *Server) credVersionHex() string {
	if s.authStore == nil {
		return ""
	}
	return s.authStore.Snapshot().CredVersion
}

func hexDecode8(s string) ([]byte, error) {
	if len(s) != 16 {
		return nil, errors.New("版本长度不是 16")
	}
	raw := make([]byte, 8)
	for i := 0; i < 8; i++ {
		hi, ok1 := hexVal(s[i*2])
		lo, ok2 := hexVal(s[i*2+1])
		if !ok1 || !ok2 {
			return nil, errors.New("版本含非 hex 字符")
		}
		raw[i] = hi<<4 | lo
	}
	return raw, nil
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func (s *Server) verifySessionToken(token string) bool {
	if !s.consoleAuthEnabled() {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != consoleTokenVersion {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) != 16 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, s.sessionKey)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return false
	}
	// 凭据版本不符 = 令牌签发时用的还是旧凭据（或从别的实例抄来的）：
	// 改密/换绑/切换/重初始化后，全部旧会话在这里被拦下。
	// 版本读取失败（状态不可用）按不符处理 —— fail closed。
	current := s.credVersionBytes()
	if current == nil || subtle.ConstantTimeCompare(payload[8:], current) != 1 {
		return false
	}
	exp := time.Unix(int64(binary.BigEndian.Uint64(payload[:8])), 0)
	return time.Now().Before(exp)
}

// requestIsHTTPS 判断本次请求是否走了 TLS。
// 服务本身以明文 HTTP 跑，HTTPS 由前面的反代终结，只能靠
// X-Forwarded-Proto 还原；Secure 标志因此只在确认是 HTTPS 时才加，
// 否则本机 HTTP 调试时浏览器会直接拒收 Cookie。
func (s *Server) requestIsHTTPS(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	if c.Request.TLS != nil {
		return true
	}
	if !s.isTrustedProxy(c) {
		return false
	}
	return strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
}

func (s *Server) isTrustedProxy(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		host = c.Request.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || s.deps == nil || s.deps.Config == nil {
		return false
	}
	for _, raw := range s.deps.Config.Server.TrustedProxies {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if proxyIP := net.ParseIP(raw); proxyIP != nil && proxyIP.Equal(ip) {
			return true
		}
		if _, network, err := net.ParseCIDR(raw); err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

// issueSession 签发会话并写 Cookie（登录与「凭据变更后重签」共用）。
// 变更凭据的接口必须重签：版本已轮换，旧 Cookie 立即失效，
// 用户不被登出，但复制过旧令牌的人全部出局。
func (s *Server) issueSession(c *gin.Context) (time.Time, error) {
	if s.credVersionBytes() == nil {
		return time.Time{}, errors.New("认证状态不可用，无法签发会话")
	}
	exp := time.Now().Add(s.sessionTTL)
	// SetSameSite 必须在 SetCookie 之前调用：它只对紧随其后的那次
	// SetCookie 生效。SameSite=Lax 让 Cookie 在跨站请求里根本不会被
	// 携带，与 sameOriginOnly 一起构成 CSRF 的两道防线。
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(consoleCookieName, s.signSessionToken(exp),
		int(s.sessionTTL.Seconds()), "/", "", s.requestIsHTTPS(c), true)
	return exp, nil
}

// loginFailure(c, msg) 统一失败路径：记账 + 固定延迟 + 中文提示。
func (s *Server) loginFailure(c *gin.Context, message string) {
	ip := c.ClientIP()
	s.loginGuards.recordFailure(ip)
	time.Sleep(loginFailureDelay)
	c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{
		"code":    http.StatusUnauthorized,
		"message": message,
		"type":    "unauthorized_error",
	}})
}

// loginLocked 拒绝处于限流锁定期的请求。
func (s *Server) loginLocked(c *gin.Context) bool {
	ip := c.ClientIP()
	if wait, locked := s.loginGuards.blocked(ip); locked {
		secs := int(wait.Seconds())
		if secs < 1 {
			secs = 1
		}
		c.Header("Retry-After", strconv.Itoa(secs))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": gin.H{
			"code":    http.StatusTooManyRequests,
			"message": "失败次数过多，请 " + strconv.Itoa(secs) + " 秒后再试",
			"type":    "rate_limit_error",
		}})
		return true
	}
	return false
}

// ---- 登录 / 登出 / 状态 ----

func (s *Server) consoleLogin(c *gin.Context) {
	enabled := s.consoleAuthEnabled()
	if !enabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"code":    http.StatusBadRequest,
			"message": "管理后台未启用登录鉴权（未设置 RELAY_ADMIN_KEY）",
			"type":    "invalid_request_error",
		}})
		return
	}
	if s.consoleLoginBlocked() {
		// 库不可达/状态损坏：凭据无法验证，宁可拒服务也不能放行
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"code":    http.StatusServiceUnavailable,
			"message": "认证状态不可用，请稍后重试；持续出现请检查数据库",
			"type":    "unavailable_error",
		}})
		return
	}

	var body struct {
		Key  string `json:"key"`
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"code":    http.StatusBadRequest,
			"message": "请求体格式不正确",
			"type":    "invalid_request_error",
		}})
		return
	}

	if s.loginLocked(c) {
		return
	}

	method := s.authMethod()
	switch method {
	case consoleauth.MethodTOTP:
		// 二选一：2FA 模式不收密钥。多给的信息直接忽略会让人误以为
		// 「密钥还有效」，明确拒绝才是对的。
		if strings.TrimSpace(body.Key) != "" {
			s.loginFailure(c, "当前使用动态码登录，无需输入密钥")
			return
		}
		if err := s.authStore.VerifyTOTP(strings.TrimSpace(body.Code), time.Now()); err != nil {
			switch {
			case errors.Is(err, consoleauth.ErrReplayed):
				s.loginFailure(c, err.Error()) // 文案面向人，原样给出
			case errors.Is(err, consoleauth.ErrWrongMethod):
				s.loginFailure(c, "尚未绑定动态码，请重新完成初始化")
			default:
				s.loginFailure(c, "动态码不正确")
			}
			return
		}
		s.loginSuccess(c)

	case consoleauth.MethodKey:
		if strings.TrimSpace(body.Code) != "" {
			s.loginFailure(c, "当前使用密钥登录，无需输入动态码")
			return
		}
		if !s.authStore.MatchKey(body.Key) {
			s.loginFailure(c, "管理密钥不正确")
			return
		}
		s.loginSuccess(c)

	default: // MethodUnset：引导密钥
		if !s.adminKeyMatches(body.Key) {
			s.loginFailure(c, "引导密钥不正确（首次部署时由安装脚本生成，见服务器 .env 的 RELAY_ADMIN_KEY）")
			return
		}
		s.loginSuccess(c)
	}
}

// loginSuccess 记成功账 + 签发会话。
func (s *Server) loginSuccess(c *gin.Context) {
	s.loginGuards.recordSuccess(c.ClientIP())
	exp, err := s.issueSession(c)
	if err != nil {
		writeAuthUnavailable(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"expires_at":        exp.UTC().Format(time.RFC3339),
		"session_ttl_hours": int(s.sessionTTL.Hours()),
		// 向导需要的上下文随登录响应一起出去：免去一次 status 往返
		"method": string(s.authMethod()),
	})
}

func (s *Server) consoleLogout(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	// Max-Age<0 会被编码成 Max-Age=0，浏览器随即删除 Cookie。
	// 无状态令牌本身无法服务端吊销：复制下来的令牌在自然过期前仍有效，
	// 需要强制全体下线时改密/换绑（版本轮换）即可。
	c.SetCookie(consoleCookieName, "", -1, "/", "", s.requestIsHTTPS(c), true)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) consoleAuthStatus(c *gin.Context) {
	enabled := s.consoleAuthEnabled()
	// 鉴权关闭时对前端表现为「已登录」：守卫放行，页面照常使用。
	authenticated := !enabled
	if enabled {
		token, err := c.Cookie(consoleCookieName)
		authenticated = err == nil && s.verifySessionToken(token)
	}
	method := s.authMethod()
	needsSetup := enabled && method == consoleauth.MethodUnset
	// needsSetup 的语义：登录页先给「引导密钥」入口，成功后展示向导。
	// method=key/totp 时登录页只给对应的单一输入框。
	resp := gin.H{
		"enabled":           enabled,
		"authenticated":     authenticated,
		"method":            string(method),
		"needs_setup":       needsSetup,
		"session_ttl_hours": int(s.sessionTTL.Hours()),
	}
	c.JSON(http.StatusOK, resp)
}

// requireConsoleAuth 挡住未携带有效会话的管理请求。
// 鉴权关闭时直接放行 —— 未配置 RELAY_ADMIN_KEY 的旧部署行为不变，
// 「这样不安全」由启动日志、system/info 与前端横幅负责大声说。
func (s *Server) requireConsoleAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.consoleAuthEnabled() {
			c.Next()
			return
		}
		token, err := c.Cookie(consoleCookieName)
		if err != nil || !s.verifySessionToken(token) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{
				"code":    http.StatusUnauthorized,
				"message": "未登录或登录已过期",
				"type":    "unauthorized_error",
			}})
			return
		}
		c.Next()
	}
}

// ---- 登录失败限流 ----

// loginLimiter 按 IP 记录窗口内的失败次数。
//
// 结构与 keyCache 同一风格：进程内 map + mutex，清理搭在写入路径上
// 顺手做（惰性），不另起后台协程 —— 登录是低频操作，不值得一个常驻
// goroutine。
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]*loginAttempt
}

type loginAttempt struct {
	fails       int
	windowStart time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{entries: map[string]*loginAttempt{}}
}

// sweep 清掉窗口已过的条目。窗口过期后历史失败不再计入（「错 5 次」
// 指的是同一个 15 分钟里的 5 次，不是终身制）。
func (l *loginLimiter) sweep(now time.Time) {
	for ip, a := range l.entries {
		if now.Sub(a.windowStart) >= loginWindow {
			delete(l.entries, ip)
		}
	}
}

// blocked 报告该 IP 是否处于锁定期，处于时返回还剩多久。
func (l *loginLimiter) blocked(ip string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.sweep(now)
	a, ok := l.entries[ip]
	if !ok {
		return 0, false
	}
	if a.fails < loginMaxFailures {
		return 0, false
	}
	return loginWindow - now.Sub(a.windowStart), true
}

func (l *loginLimiter) recordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.sweep(now)
	a, ok := l.entries[ip]
	if !ok {
		if len(l.entries) >= loginMaxTrackedIPs {
			// 到顶了：不再为陌生 IP 记账（见 loginMaxTrackedIPs 的说明）。
			// 没有计数 = 该 IP 不受「错 5 次锁定」约束，这是用有限的
			// 防护覆盖换内存安全的取舍。
			return
		}
		l.entries[ip] = &loginAttempt{fails: 1, windowStart: now}
		return
	}
	if now.Sub(a.windowStart) >= loginWindow {
		a.fails = 1
		a.windowStart = now
		return
	}
	a.fails++
}

func (l *loginLimiter) recordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// 成功即清零：限流针对的是「猜不中的持续尝试」，
	// 登录成功说明是人，历史失败不该再拖累他
	delete(l.entries, ip)
}

// 供 setup 接口复用：把认证状态不可用翻译成 503。
func writeAuthUnavailable(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
		"code":    http.StatusServiceUnavailable,
		"message": "认证状态不可用，请稍后重试；持续出现请检查数据库",
		"type":    "unavailable_error",
	}})
}
