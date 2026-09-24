package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/config"
)

// 管理台登录鉴权（无账号模型）。
//
// 原来管理后台的安全性完全建立在「只绑 127.0.0.1」上：接口层只有一道
// sameOriginOnly 的来源校验，谁摸得到端口谁就是管理员。这套假设在
// WSL2 本地部署下成立，一旦部署到公网服务器（哪怕只开 8888 一个端口）
// 就等于把渠道密钥、调用日志和全部管理操作交给整个互联网。
//
// 这里的方案刻意保持最小：
//   - 没有用户表、没有注册、没有找回 —— 全站只有一把「管理密钥」，
//     来自环境变量 RELAY_ADMIN_KEY，不落库、不随数据库备份泄露。
//   - 登录成功后签发**无状态**会话令牌（HMAC-SHA256 签名），写进
//     HttpOnly Cookie。不建 session 表：个人自用工具不需要「踢掉某个
//     会话」的粒度，需要全体下线时轮换密钥即可（见下）。
//   - 令牌载荷绑定「密钥版本」（管理密钥哈希的前 8 字节）：换掉
//     RELAY_ADMIN_KEY 后，旧令牌的版本对不上新密钥，即刻全部失效 ——
//     这是无状态令牌缺失的吊销能力，用轮换补齐。
//
// 会话签名密钥从 RELAY_SECRET 派生（而不是直接用主密钥）：
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

// initConsoleAuth 从配置派生登录鉴权所需的全部派生量。
// AdminKey 为空时 adminKeyHash 保持 nil，鉴权整体关闭（旧部署行为不变），
// 但启动日志与 /api/admin/system/info 都会把这件事说出来。
func (s *Server) initConsoleAuth(cfg *config.Config, logger *slog.Logger) {
	s.sessionTTL = cfg.Security.SessionTTL
	s.loginGuards = newLoginLimiter()
	if strings.TrimSpace(cfg.Security.AdminKey) == "" {
		if logger != nil {
			logger.Warn("管理后台登录鉴权未启用（未设置 RELAY_ADMIN_KEY）。" +
				"服务只绑回环时尚可接受；公网部署必须设置，否则管理接口对外完全裸奔")
		}
		return
	}
	sum := sha256.Sum256([]byte(cfg.Security.AdminKey))
	s.adminKeyHash = append([]byte(nil), sum[:]...)
	s.adminKeyVersion = append([]byte(nil), sum[:8]...)
	// 域分隔派生：与渠道密钥的 AES 派生（裸 SHA-256(secret)）互不干扰
	sessionSum := sha256.Sum256([]byte("llm-relay/console-session|" + cfg.Security.Secret))
	s.sessionKey = append([]byte(nil), sessionSum[:]...)
}

// consoleAuthEnabled 报告登录鉴权是否启用。
func (s *Server) consoleAuthEnabled() bool { return s.adminKeyHash != nil }

// adminKeyMatches 常数时间比对登录密钥。
// 双方都比成定长 SHA-256 再比对，长度差异不会从耗时里漏出去。
func (s *Server) adminKeyMatches(presented string) bool {
	if !s.consoleAuthEnabled() {
		return false
	}
	sum := sha256.Sum256([]byte(presented))
	return hmac.Equal(sum[:], s.adminKeyHash)
}

// ---- 会话令牌 ----
//
// 令牌形如 v1.<base64url(payload)>.<base64url(sig)>：
//   payload = 8 字节绝对过期时间（unix 秒，big-endian）|| 8 字节密钥版本
//   sig     = HMAC-SHA256(sessionKey, payload)
//
// 过期判定放在签发时刻一次性定死（绝对过期），进程重启、换了浏览器
// 时区都不影响；想要更长会话就调大 RELAY_SESSION_TTL，不做滑动续期 ——
// 个人工具里「记得每 7 天登录一次」的代价远小于一套续期逻辑的复杂度。

func (s *Server) signSessionToken(exp time.Time) string {
	payload := make([]byte, 16)
	binary.BigEndian.PutUint64(payload[:8], uint64(exp.Unix()))
	copy(payload[8:], s.adminKeyVersion)
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
	// 密钥版本不符 = 令牌签发时用的还是旧密钥（或从别的实例抄来的），
	// 轮换 RELAY_ADMIN_KEY 后全部旧会话在这里被拦下
	if subtle.ConstantTimeCompare(payload[8:], s.adminKeyVersion) != 1 {
		return false
	}
	exp := time.Unix(int64(binary.BigEndian.Uint64(payload[:8])), 0)
	return time.Now().Before(exp)
}

// requestIsHTTPS 判断本次请求是否走了 TLS。
// 服务本身以明文 HTTP 跑，HTTPS 由前面的反代终结，只能靠
// X-Forwarded-Proto 还原；Secure 标志因此只在确认是 HTTPS 时才加，
// 否则本机 HTTP 调试时浏览器会直接拒收 Cookie。
func requestIsHTTPS(c *gin.Context) bool {
	return c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
}

// ---- 登录 / 登出 / 状态 ----

func (s *Server) consoleLogin(c *gin.Context) {
	if !s.consoleAuthEnabled() {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"code":    http.StatusBadRequest,
			"message": "管理后台未启用登录鉴权（未设置 RELAY_ADMIN_KEY）",
			"type":    "invalid_request_error",
		}})
		return
	}

	var body struct {
		Key string `json:"key"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"code":    http.StatusBadRequest,
			"message": "请求体格式不正确",
			"type":    "invalid_request_error",
		}})
		return
	}

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
		return
	}

	if !s.adminKeyMatches(body.Key) {
		s.loginGuards.recordFailure(ip)
		time.Sleep(loginFailureDelay)
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{
			"code":    http.StatusUnauthorized,
			"message": "管理密钥不正确",
			"type":    "unauthorized_error",
		}})
		return
	}
	s.loginGuards.recordSuccess(ip)

	exp := time.Now().Add(s.sessionTTL)
	// SetSameSite 必须在 SetCookie 之前调用：它只对紧随其后的那次
	// SetCookie 生效。SameSite=Lax 让 Cookie 在跨站请求里根本不会被
	// 携带，与 sameOriginOnly 一起构成 CSRF 的两道防线。
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(consoleCookieName, s.signSessionToken(exp),
		int(s.sessionTTL.Seconds()), "/", "", requestIsHTTPS(c), true)
	c.JSON(http.StatusOK, gin.H{
		"expires_at":        exp.UTC().Format(time.RFC3339),
		"session_ttl_hours": int(s.sessionTTL.Hours()),
	})
}

func (s *Server) consoleLogout(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	// Max-Age<0 会被编码成 Max-Age=0，浏览器随即删除 Cookie。
	// 无状态令牌本身无法服务端吊销：复制下来的令牌在自然过期前仍有效，
	// 需要强制全体下线时轮换 RELAY_ADMIN_KEY 并重启（版本不符即失效）。
	c.SetCookie(consoleCookieName, "", -1, "/", "", requestIsHTTPS(c), true)
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
	resp := gin.H{"enabled": enabled, "authenticated": authenticated}
	if enabled {
		resp["session_ttl_hours"] = int(s.sessionTTL.Hours())
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
