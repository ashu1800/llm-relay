package api

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/config"
	"llm-relay/internal/model"
	"llm-relay/internal/pricing"
	"llm-relay/internal/relay"
	"llm-relay/internal/secure"
	"llm-relay/internal/store"
	"llm-relay/internal/update"
	"llm-relay/internal/version"
	"llm-relay/internal/web"
)

// Deps 汇总 HTTP 层依赖，便于测试时替换。
type Deps struct {
	Config *config.Config
	Store  *store.Store
	Cipher *secure.Cipher
	// Live 是实时推送的订阅中心（WebSocket）。为 nil 时实时功能整体关闭，
	// 接口返回 501 而不是悄悄什么都不推
	Live    *liveHub
	Logger  *slog.Logger
	Router  *relay.Router
	Service *relay.Service
	Logs    *relay.LogWriter
	Pricing *pricing.Engine
	// State 是渠道运行期状态（冷却 / 连击 / 在途）：与 Router、Service 持有
	// 同一份实例。管理接口用它把「这条渠道现在能不能被路由到」并进响应 ——
	// 熔断早已生效，这里负责让它看得见。nil 时接口里缺省该字段。
	State *relay.ChannelState

	// 限流与并发控制
	RateLimiter *relay.RateLimiter
	Gate        *relay.ConcurrencyGate
	// GroupLimit 是分组级的每分钟额度，与密钥级限流是两套独立的口子
	GroupLimit *relay.GroupLimiter

	// Update 是版本更新服务（检测 / 下载 / 替换 / 回滚）。
	// 为 nil 时更新相关的接口回 501，而 /system/version 仍然可用 ——
	// 「我现在跑的是哪一版」不该依赖更新功能是否启用。
	// 能否一键更新没有独立探测：/update/check 的 can_apply 就是答案。
	Update *update.Service
}

// Server 持有 HTTP 层状态。
type Server struct {
	deps      *Deps
	startedAt time.Time
	// keys 是密钥的进程内缓存（转发热路径不再每请求查库）；
	// lastTouch 记录各密钥最近一次 last_used_at 落库时间，用于写库节流
	keys      *keyCache
	touchMu   sync.Mutex
	lastTouch map[uint]time.Time

	// ---- 管理台登录鉴权（无账号模型，详见 console_auth.go）----
	// adminKeyHash 为 nil 表示鉴权关闭（未配置 RELAY_ADMIN_KEY）
	adminKeyHash    []byte
	adminKeyVersion []byte
	sessionKey      []byte
	sessionTTL      time.Duration
	loginGuards     *loginLimiter
}

// New 构造 HTTP 层。
func New(deps *Deps) *Server {
	// 把渠道健康事件接到 live 推送上（channel_health 帧）。
	// 事件的产生在 relay.Service（冷却 / 自动熔断 / 恢复三处触发点），
	// 广播能力在 liveHub，两边的接线只能在这一层做 ——
	// relay 包不该知道 WebSocket 的存在。
	// 渠道名在这里补齐：relay 侧只有 id，查一次库换来前端能直接显示名字。
	if deps.Service != nil && deps.Live != nil {
		deps.Service.OnChannelEvent = func(e relay.ChannelEvent) {
			name := ""
			if deps.Store != nil {
				var ch model.Channel
				if err := deps.Store.DB().Select("name").First(&ch, e.ChannelID).Error; err == nil {
					name = ch.Name
				}
			}
			deps.Live.broadcast(mustJSON(liveMessage{Type: "channel_health", Data: gin.H{
				"channel_id":   e.ChannelID,
				"channel_name": name,
				"kind":         e.Kind,
				"reason":       e.Reason,
				"until":        e.Until,
			}}))
		}
	}
	srv := &Server{
		deps:      deps,
		startedAt: time.Now(),
		keys:      newKeyCache(),
		lastTouch: map[uint]time.Time{},
	}
	srv.initConsoleAuth(deps.Config, deps.Logger)
	return srv
}

// Register 挂载全部路由。
func (s *Server) Register(r *gin.Engine) {
	r.GET("/healthz", s.healthz)
	r.GET("/readyz", s.readyz)

	// ---- 中转端点：OpenAI 兼容，需要 API Key ----
	v1 := r.Group("/v1", s.requireAPIKey())
	{
		v1.GET("/models", s.listModels)
		v1.POST("/chat/completions", s.chatCompletions)
		v1.POST("/responses", s.responses)
		v1.POST("/embeddings", s.embeddings)
		// Anthropic Messages 兼容端点，供 Claude Code 等客户端直连
		v1.POST("/messages", s.anthropicMessages)
	}

	// ---- Gemini 兼容端点：模型名写在路径里而不是请求体，故单独一组 ----
	v1beta := r.Group("/v1beta", s.requireAPIKey())
	{
		v1beta.POST("/models/*action", s.geminiGenerateContent)
	}

	s.registerAdminRoutes(r)

	s.registerStatic(r)
}

// registerAdminRoutes 挂载管理后台 API。
//
// 三层防线，从外到内：
//   1. sameOriginOnly —— 拒绝来自其它网页的跨站请求（表单 CSRF 的主防线，
//      见其注释）。没有登录时它几乎是唯一的防线，有了登录后依然必要。
//   2. requireConsoleAuth —— 无账号模型的密钥登录（console_auth.go）。
//      未配置 RELAY_ADMIN_KEY 时放行一切，保持旧部署行为。
//   3. limitAdminBody —— 管理接口请求体上限，输入全部来自网络。
//
// 登录/状态三个接口挂在独立的公开子组上：它们必须在鉴权之前可达，
// 否则永远拿不到会话。公开子组同样受 1、3 两层约束 —— 登录接口的
// 输入同样来自网络。抽成独立方法而非留在 Register 里，是让测试能
// 挂载与管理环境完全相同的路由接线（含中间件顺序）。
func (s *Server) registerAdminRoutes(r *gin.Engine) {
	adminPublic := r.Group("/api/admin", sameOriginOnly(), limitAdminBody)
	{
		adminPublic.POST("/auth/login", s.consoleLogin)
		adminPublic.POST("/auth/logout", s.consoleLogout)
		adminPublic.GET("/auth/status", s.consoleAuthStatus)
	}
	admin := r.Group("/api/admin", sameOriginOnly(), s.requireConsoleAuth(), limitAdminBody)
	{
		admin.GET("/system/info", s.systemInfo)
		registerSystemRoutes(admin, s)
		registerChannelRoutes(admin, s)
		registerGroupRoutes(admin, s)
		registerKeyRoutes(admin, s)
		registerLogRoutes(admin, s)
		registerBackupRoutes(admin, s)
		registerSettingsRoutes(admin, s)

		registerProxyRoutes(admin, s)
		registerStatsRoutes(admin, s)
		// 实时推送（WebSocket）：看板数值与请求日志由它主动推给前端。
		// 升级握手前会先过 requireConsoleAuth —— 浏览器发起 WebSocket
		// 时自动携带同源 Cookie，登录态随握手一起送达，前端无需额外传参
		admin.GET("/live", s.liveSocket)
	}
}

// registerStatic 挂载嵌入的前端资源，并为前端路由提供 SPA 回退。
//
// 发送策略（预压缩 / 长缓存 / ETag 校验）都在 static.go 里，这里只负责接线。
// 原先直接用 http.FileServer 是「能发出去就行」：实测产物 1.6 MB 全部原样传、
// 且没有任何缓存头，浏览器每次刷新都要重新下载一遍。
func (s *Server) registerStatic(r *gin.Engine) {
	dist, err := web.Dist()
	if err != nil {
		return
	}
	assets := newStaticAssets(dist, s.deps.Logger)
	r.NoRoute(assets.handler())
}

// healthz 只表示进程存活，不依赖任何外部组件。
func (s *Server) healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"uptime": time.Since(s.startedAt).Round(time.Second).String(),
	})
}

// readyz 表示数据库就绪，用于容器健康检查。
func (s *Server) readyz(c *gin.Context) {
	if s.deps != nil && s.deps.Store != nil {
		if err := s.deps.Store.Ready(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

// defaultSecret 是仓库里公开的占位密钥。
//
// 用它在启动日志里警告一次是不够的：容器日志一滚就看不见了，
// 而渠道密钥的加密主密钥就是它 —— 也就是说数据库或备份文件落到别人手里时，
// 里面的上游密钥等于明文。加密看起来生效了，实际没有提供任何保护。
// 所以把它作为一项状态暴露出去，让界面能持续提醒。
const defaultSecret = "llm-relay-dev-secret-change-me"

// UsingDefaultSecret 供 main 与接口共用同一份判断。
func UsingDefaultSecret(secret string) bool { return secret == defaultSecret }

func (s *Server) systemInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"name": "llm-relay",
		// version 保持「完整版本号」，install.sh --verify 读的就是它。
		// 徽标用的短版本号与构建形态走 /system/version（version 包是
		// 唯一事实来源），不在这里重复一份。
		"version":       version.Version,
		"port":          s.deps.Config.Server.Port,
		"started_at":    s.startedAt.UTC().Format(time.RFC3339),
		"payload_store": s.deps.Config.Relay.PayloadStorageMode,
		// 前端据此显示持久告警，而不是只在启动日志里提一句
		"using_default_secret": UsingDefaultSecret(s.deps.Config.Security.Secret),
		// 管理台登录鉴权状态：未启用时前端同样显示持久告警。
		// 只暴露布尔位，密钥本身（哪怕哈希）永远不随接口出去
		"console_auth_enabled": s.consoleAuthEnabled(),
	})
}
