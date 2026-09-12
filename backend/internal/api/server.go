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
	Syncer  *pricing.Syncer
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
		v1.POST("/embeddings", s.embeddings)
		// Anthropic Messages 兼容端点，供 Claude Code 等客户端直连
		v1.POST("/messages", s.anthropicMessages)
	}

	// ---- 管理后台 API：本地自用，不做登录 ----
	admin := r.Group("/api/admin")
	{
		admin.GET("/system/info", s.systemInfo)
		registerChannelRoutes(admin, s)
		registerGroupRoutes(admin, s)
		registerModelRoutes(admin, s)
		registerKeyRoutes(admin, s)
		registerLogRoutes(admin, s)
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

func (s *Server) systemInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"name":          "llm-relay",
		"version":       Version,
		"port":          s.deps.Config.Server.Port,
		"started_at":    s.startedAt.UTC().Format(time.RFC3339),
		"pricing_sync":  s.deps.Config.Pricing.UpdateIntervalHours,
		"payload_store": s.deps.Config.Relay.PayloadStorageMode,
	})
}

// Version 由构建时注入。
var Version = "dev"
