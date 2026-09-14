package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/model"
	"llm-relay/internal/secure"
)

// sameOriginOnly 拦掉来自其它网页的跨站请求。
//
// 管理接口是本地自用、没有登录态的，于是浏览器里的**任何**网页都能对 localhost
// 发起请求。一个
//
//	<form method="post" action="http://127.0.0.1:8888/api/admin/settings/cleanup">
//
// 就足以触发清库这类操作：表单提交属于「简单请求」，不触发预检，
// 浏览器不会拦，服务端收到的报文与正常请求也没有区别。
// 这不是推测 —— 实测用 application/x-www-form-urlencoded 提交确实返回 200 并执行了。
//
// 判断依据用 Origin 头，理由：
//   - 浏览器对所有跨站请求都会带上 Origin，页面脚本改不了它
//   - 同源请求也带，值与本服务地址一致，放行
//   - 命令行客户端（curl、各家 SDK）通常不带 Origin，放行，
//     免得把「本地自用」最常见的调试方式一并挡掉
//
// 覆盖不到的情况：不带 Origin 的请求无法区分（例如 DNS rebinding）。
// 但把「随便打开一个网页就能打管理接口」这条最现实的路径堵上了。
func sameOriginOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}
		u, err := url.Parse(origin)
		// Origin: null（沙箱 iframe、file:// 页面）解析出来 Host 为空，会走拒绝分支
		if err != nil || !strings.EqualFold(u.Host, c.Request.Host) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{
				"code":    http.StatusForbidden,
				"message": "拒绝跨站请求：管理接口只接受来自本服务页面的调用",
				"type":    "forbidden_error",
			}})
			return
		}
		c.Next()
	}
}

const ctxAPIKey = "llm_relay_api_key"

// maxAdminBodyBytes 是管理接口请求体的上限。
//
// 原来整个项目只有转发链路设了上限（relay_handler 里的
// MaxRequestBodyMB），16 处管理接口的 ShouldBindJSON 都是无界的 ——
// 一个超大 JSON 就能把进程内存打满，连带影响所有在途的转发请求。
//
// 8 MB 远大于任何真实管理请求：渠道白名单整表替换、配置导入是最大的两种，
// 实测都在 1 MB 以内。导入接口单独放宽（见 admin_backup.go）。
const maxAdminBodyBytes = 8 << 20

// limitAdminBody 给管理接口加上请求体大小上限。
func limitAdminBody(c *gin.Context) {
	if c.Request.Body != nil {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAdminBodyBytes)
	}
	c.Next()
}

// profileForPath 按请求路径推断入站协议。
// 鉴权失败发生在协议分发之前，若统一用 OpenAI 错误结构，
// Anthropic 客户端会因为解析不到 {"type":"error"} 而报出难懂的错。
func profileForPath(path string) *inboundProfile {
	switch {
	case strings.HasPrefix(path, "/v1/messages"):
		return profileAnthropic
	case strings.HasPrefix(path, "/v1/responses"):
		return profileOpenAIResponses
	case strings.HasPrefix(path, "/v1beta/models"):
		return profileGemini
	default:
		return profileOpenAIChat
	}
}

// requireAPIKey 校验 Authorization: Bearer sk-xxx。
// 中转服务面向任意 OpenAI 兼容客户端，因此沿用标准 Bearer 约定。
func (s *Server) requireAPIKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		profile := profileForPath(c.Request.URL.Path)

		// 各家的密钥传递方式不同，逐个尝试：
		//   OpenAI    Authorization: Bearer sk-...
		//   Anthropic x-api-key: sk-...
		//   Gemini    x-goog-api-key: ... 或 ?key=...
		raw := extractBearer(c.GetHeader("Authorization"))
		if raw == "" {
			raw = c.GetHeader("x-api-key")
		}
		if raw == "" {
			raw = c.GetHeader("x-goog-api-key")
		}
		if raw == "" {
			raw = c.Query("key")
		}
		if raw == "" {
			profile.writeError(c, http.StatusUnauthorized, "缺少 API Key", "invalid_request_error")
			c.Abort()
			return
		}

		// 热路径上每个请求都查一次库太浪费：密钥极少变动，进程内短缓存即可。
		// 代价是「停用的密钥最多还能用 keyCacheTTL 这么久」，个人自用可接受；
		// 新建/更新/删除密钥时会整表失效（见 invalidateKeyCache）。
		hash := secure.HashKey(raw)
		key, cached := s.keys.get(hash)
		if !cached {
			var k model.APIKey
			err := s.deps.Store.DB().
				Where("key_hash = ? AND enabled = ?", hash, true).
				First(&k).Error
			if err != nil {
				profile.writeError(c, http.StatusUnauthorized, "API Key 无效或已禁用", "invalid_request_error")
				c.Abort()
				return
			}
			s.keys.put(hash, k)
			key = k
		}

		// 密钥级限流：先看该密钥自己的额度，没配就用全局默认值。
		// 负值表示这把密钥不限流，便于本地脚本或压测单独放行。
		limit := key.RateLimitRPM
		if limit == 0 {
			limit = s.deps.Config.Relay.DefaultRPM
		}
		if ok, wait := s.deps.RateLimiter.Allow(key.ID, limit, time.Now()); !ok {
			secs := int(wait.Seconds())
			if secs < 1 {
				secs = 1
			}
			c.Header("Retry-After", strconv.Itoa(secs))
			profile.writeError(c, http.StatusTooManyRequests,
				"请求过于频繁，密钥 "+key.Name+" 的限制为每分钟 "+strconv.Itoa(limit)+" 次", "rate_limit_error")
			c.Abort()
			return
		}

		c.Set(ctxAPIKey, &key)
		s.touchAPIKey(key.ID)
		c.Next()
	}
}

func extractBearer(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return header
}

// touchAPIKey 异步刷新最后使用时间，失败不影响请求。
// 同一把密钥一分钟内只写一次：last_used_at 只是给人看的参考时间，
// 高频调用下每个请求都写库纯属浪费。
func (s *Server) touchAPIKey(id uint) {
	now := time.Now()
	s.touchMu.Lock()
	if last, ok := s.lastTouch[id]; ok && now.Sub(last) < time.Minute {
		s.touchMu.Unlock()
		return
	}
	s.lastTouch[id] = now
	s.touchMu.Unlock()

	go func() {
		_ = s.deps.Store.DB().Model(&model.APIKey{}).
			Where("id = ?", id).
			Update("last_used_at", nowUTC()).Error
	}()
}

func apiKeyFromContext(c *gin.Context) *model.APIKey {
	if v, ok := c.Get(ctxAPIKey); ok {
		if k, ok := v.(*model.APIKey); ok {
			return k
		}
	}
	return nil
}

// ============================ 密钥进程内缓存 ============================

// keyCacheTTL 是密钥缓存的存活期。取「足够短，停用密钥后很快失效」与
// 「足够长，扛住转发热路径」之间的折中。
const keyCacheTTL = 15 * time.Second

// keyCache 按密钥哈希缓存整行，避免每个转发请求都查一次数据库。
type keyCache struct {
	mu      sync.Mutex
	entries map[string]keyCacheEntry
}

type keyCacheEntry struct {
	key model.APIKey
	at  time.Time
}

func newKeyCache() *keyCache { return &keyCache{entries: map[string]keyCacheEntry{}} }

func (c *keyCache) get(hash string) (model.APIKey, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[hash]
	if !ok || time.Since(e.at) > keyCacheTTL {
		return model.APIKey{}, false
	}
	return e.key, true
}

func (c *keyCache) put(hash string, k model.APIKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	// 顺带清掉过期项，map 不随时间无限增长
	for h, e := range c.entries {
		if now.Sub(e.at) > keyCacheTTL {
			delete(c.entries, h)
		}
	}
	c.entries[hash] = keyCacheEntry{key: k, at: now}
}

// invalidate 整表失效。密钥的任何写操作后调用，宁可多查一次库也不要留旧配置。
func (c *keyCache) invalidate() {
	c.mu.Lock()
	c.entries = map[string]keyCacheEntry{}
	c.mu.Unlock()
}

// invalidateKeyCache 供密钥管理接口在写操作后调用。
func (s *Server) invalidateKeyCache() { s.keys.invalidate() }
