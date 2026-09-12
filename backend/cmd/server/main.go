package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/api"
	"llm-relay/internal/config"
	"llm-relay/internal/pricing"
	"llm-relay/internal/relay"
	"llm-relay/internal/secure"
	"llm-relay/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", envOr("CONFIG_PATH", "config.yaml"), "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	logger := newLogger(cfg.Log)
	slog.SetDefault(logger)
	api.Version = buildVersion()

	// ---- 加密主密钥 ----
	cipher, err := secure.NewCipher(cfg.Security.Secret)
	if err != nil {
		return fmt.Errorf("初始化加密模块失败: %w", err)
	}
	if cfg.Security.Secret == "llm-relay-dev-secret-change-me" {
		logger.Warn("正在使用默认加密密钥，请通过 RELAY_SECRET 环境变量覆盖")
	}

	// ---- 数据库 ----
	st, err := store.Open(cfg)
	if err != nil {
		return err
	}
	if err := st.Migrate(); err != nil {
		return err
	}
	if err := st.Seed(); err != nil {
		return err
	}
	// 必须排在 Seed 之后：回填模板分组需要默认分组已经存在，
	// 否则全新安装时这里找不到分组就直接跳过了，外键一直不会被建出来
	if err := st.EnsureForeignKeys(); err != nil {
		return err
	}

	// ---- 转发内核 ----
	// 渠道运行期状态（冷却与在途计数）由 Router 与 Service 共享，
	// 这样「选定渠道」和「转发出去」看到的是同一份事实。
	state := relay.NewChannelState()
	router := relay.NewRouter(st.DB(), cipher)
	router.SetChannelState(state)
	svc := relay.NewService(st.DB(), router, relay.Options{
		MaxRetries:        cfg.Relay.MaxRetries,
		UpstreamTimeout:   cfg.Relay.FirstByteTimeout,
		InjectStreamUsage: true,
	}, logger)
	svc.SetChannelState(state)
	gate := relay.NewConcurrencyGate(cfg.Relay.MaxConcurrency)
	rateLimiter := relay.NewRateLimiter()
	logs := relay.NewLogWriter(st.DB(), 2048, logger)
	defer logs.Close()

	// ---- 定价 ----
	priceEngine := pricing.NewEngine(st.DB(), 5*time.Minute)
	syncer := pricing.NewSyncer(st.DB(), priceEngine, logger)

	gin.SetMode(ginMode(cfg.Server.Mode))
	engine := gin.New()
	engine.Use(gin.Recovery(), requestLogger(logger))

	srv := api.New(&api.Deps{
		Config:  cfg,
		Store:   st,
		Cipher:  cipher,
		Router:  router,
		Service: svc,
		Logs:    logs,
		Pricing: priceEngine,
		Syncer:  syncer,

		RateLimiter: rateLimiter,
		Gate:        gate,
		State:       state,
	})
	srv.Register(engine)

	httpSrv := &http.Server{
		Addr:              cfg.Server.Addr(),
		Handler:           engine,
		ReadHeaderTimeout: 20 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startPricingScheduler(ctx, syncer, cfg, logger)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("LLM Relay 已启动",
			"addr", cfg.Server.Addr(),
			"version", api.Version,
			"mode", cfg.Server.Mode,
		)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("收到退出信号，开始优雅关闭")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("优雅关闭失败: %w", err)
	}
	logger.Info("已退出")
	return nil
}

// startPricingScheduler 周期性同步模型单价。
// 首次同步延后执行，避免和启动流程抢网络与数据库连接。
func startPricingScheduler(ctx context.Context, syncer *pricing.Syncer, cfg *config.Config, logger *slog.Logger) {
	if !cfg.Pricing.OfficialSyncEnabled || cfg.Pricing.UpdateIntervalHours <= 0 {
		logger.Info("定价定时同步未启用")
		return
	}
	interval := time.Duration(cfg.Pricing.UpdateIntervalHours) * time.Hour
	go func() {
		select {
		case <-time.After(30 * time.Second):
		case <-ctx.Done():
			return
		}
		syncPricingOnce(ctx, syncer, logger)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				syncPricingOnce(ctx, syncer, logger)
			case <-ctx.Done():
				return
			}
		}
	}()
	logger.Info("定价定时同步已启用", "interval_hours", cfg.Pricing.UpdateIntervalHours)
}

func syncPricingOnce(ctx context.Context, syncer *pricing.Syncer, logger *slog.Logger) {
	sctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	started := time.Now()
	for _, r := range syncer.SyncAll(sctx) {
		if r.Status != "ok" {
			logger.Warn("定价同步未成功", "source", r.Source, "error", r.Error)
		}
	}
	logger.Info("定价同步流程结束", "cost", time.Since(started).Round(time.Second).String())
}

func newLogger(cfg config.LogConfig) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.Format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.Debug("http",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"cost", time.Since(start).Round(time.Millisecond).String(),
		)
	}
}

func ginMode(mode string) string {
	if mode == "debug" {
		return gin.DebugMode
	}
	return gin.ReleaseMode
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// version 由构建时注入：-ldflags "-X main.version=..."
var version = "dev"

func buildVersion() string { return version }
