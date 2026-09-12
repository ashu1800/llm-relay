package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/config"
	"llm-relay/internal/pricing"
	"llm-relay/internal/relay"
	"llm-relay/internal/secure"
	"llm-relay/internal/store"
	"llm-relay/internal/web"
)

// Deps 汇总 HTTP 层依赖，便于测试时替换。
type Deps struct {
	Config  *config.Config
	Store   *store.Store
	Cipher  *secure.Cipher
	Router  *relay.Router
	Service *relay.Service
	Logs    *relay.LogWriter
	Pricing *pricing.Engine

	// 限流与并发控制
	RateLimiter *relay.RateLimiter
	Gate        *relay.ConcurrencyGate
	// GroupLimit 是分组级的每分钟额度，与密钥级限流是两套独立的口子
	GroupLimit *relay.GroupLimiter
}

// Server 持有 HTTP 层状态。
type Server struct {
	deps      *Deps
	startedAt time.Time
}

// New 构造 HTTP 层。
func New(deps *Deps) *Server {
	return &Server{deps: deps, startedAt: time.Now()}
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

	// ---- 管理后台 API：本地自用，不做登录 ----
	//
	// 不做登录不等于可以不做来源校验：没有这道中间件，浏览器里任意一个网页
	// 都能用表单提交触发管理操作（详见 sameOriginOnly 的说明）
	admin := r.Group("/api/admin", sameOriginOnly())
	{
		admin.GET("/system/info", s.systemInfo)
		registerChannelRoutes(admin, s)
		registerGroupRoutes(admin, s)
		registerKeyRoutes(admin, s)
		registerLogRoutes(admin, s)
		registerBackupRoutes(admin, s)
		registerSettingsRoutes(admin, s)
		registerPricingRoutes(admin, s)
		registerStatsRoutes(admin, s)
	}

	s.registerStatic(r)
}

// registerStatic 挂载嵌入的前端资源，并为前端路由提供 SPA 回退。
func (s *Server) registerStatic(r *gin.Engine) {
	dist, err := web.Dist()
	if err != nil {
		return
	}
	indexHTML, indexErr := fsReadFile(dist, "index.html")
	fileServer := http.FileServer(http.FS(dist))

	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if isAPIPath(p) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"message": "接口不存在", "type": "not_found_error"}})
			return
		}
		if rel := trimLeadingSlash(p); rel != "" {
			if f, err := dist.Open(rel); err == nil {
				_ = f.Close()
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}
		}
		if indexErr != nil {
			c.String(http.StatusOK, "前端资源尚未构建")
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
	})
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
		"name":          "llm-relay",
		"version":       Version,
		"port":          s.deps.Config.Server.Port,
		"started_at":    s.startedAt.UTC().Format(time.RFC3339),
		"payload_store": s.deps.Config.Relay.PayloadStorageMode,
		// 前端据此显示持久告警，而不是只在启动日志里提一句
		"using_default_secret": UsingDefaultSecret(s.deps.Config.Security.Secret),
	})
}

// Version 由构建时注入。
var Version = "dev"
