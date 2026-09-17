package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/api"
	"llm-relay/internal/config"
	"llm-relay/internal/model"
	"llm-relay/internal/pricing"
	"llm-relay/internal/proxy"
	"llm-relay/internal/relay"
	"llm-relay/internal/secure"
	"llm-relay/internal/store"
)

// 退出预算。三个数加起来必须小于 deploy/docker-compose.yml 里的
// stop_grace_period（5 秒），否则容器会被 SIGKILL —— 那时连日志都来不及落库，
// 比多停机几秒更糟。改这几个值时要和那个配置一起看。
const (
	// 等在途请求收尾的上限
	shutdownHTTPTimeout = 3 * time.Second
	// 等日志队列写完的上限（见 relay.LogWriter.Close）
	shutdownFlushTimeout = 1500 * time.Millisecond
	// 给「请求头还没收完」的连接留的时间，见下面的 connState 说明
	shutdownHeaderGrace = 500 * time.Millisecond
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
	if api.UsingDefaultSecret(cfg.Security.Secret) {
		logger.Warn("正在使用默认加密密钥，渠道密钥的加密形同虚设；请通过 RELAY_SECRET 环境变量覆盖。" +
			"管理后台首页也会持续提示这一点")
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
	router.SetLogger(logger)
	router.SetChannelState(state)
	svc := relay.NewService(st.DB(), router, relay.Options{
		MaxRetries:        cfg.Relay.MaxRetries,
		FirstByteTimeout:  cfg.Relay.FirstByteTimeout,
		UpstreamTimeout:   cfg.Relay.UpstreamTimeout,
		InjectStreamUsage: true,
	}, logger)
	svc.SetChannelState(state)
	// 分组级每分钟额度：分组上配的 RPM / TPM 由它生效。
	// 与密钥级限流（RateLimiter）是两套独立的口子：前者保护上游，
	// 后者约束单把密钥，两者都不配就是不限制。
	groupLimit := relay.NewGroupLimiter()
	svc.SetGroupLimiter(groupLimit)
	// 出站代理：渠道（或某个模型）指定走哪个代理时，由它按 id 取配置。
	// 停用的代理在这里被判为不可用 —— 转发器收到「不可用」会直接让请求失败，
	// 而不是回退直连：用户配代理往往就是为了不让请求从本机 IP 出去。
	svc.SetProxyResolver(func(id uint) (proxy.Config, error) {
		var p model.Proxy
		if err := st.DB().First(&p, id).Error; err != nil {
			// 与「停用」「解不开」分开报：三种情况的下一步动作完全不同
			return proxy.Config{}, fmt.Errorf("不存在")
		}
		if !p.Enabled {
			return proxy.Config{}, fmt.Errorf("已被停用")
		}
		return proxy.FromEntity(p, cipher)
	})
	// 实时推送：看板数值与请求日志由 WebSocket 主动推给前端，
	// 前端只更新变化的那几个数字，不重绘整页
	live := api.NewLiveHub(logger)
	gate := relay.NewConcurrencyGate(cfg.Relay.MaxConcurrency)
	rateLimiter := relay.NewRateLimiter()
	logs := relay.NewLogWriter(st.DB(), 2048, logger)
	// 注意这里**没有** defer logs.Close()：关闭要等队列写完，而 defer 是在
	// run() 返回时才跑，那时已经过了优雅关闭的预算。收尾统一放在下面
	// logs.CloseAndFlush(...)，给它一个明确的上限。

	// ---- 定价 ----
	// 单价一律手工录入：外部价格表（LiteLLM / 官网）里的模型命名与本站
	// 对外模型名并不一致，自动同步会写进大量用不上的行，还会在用户改价后
	// 被下一轮同步覆盖。宁可让人自己填，也不要有会漂移的假数据。
	priceEngine := pricing.NewEngine(st.DB(), 5*time.Minute)

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

		RateLimiter: rateLimiter,
		Gate:        gate,
		GroupLimit:  groupLimit,
		Live:        live,
		Logger:      logger,
		// 渠道运行期状态（冷却 / 在途）由 Router 与 Service 自己持有，
		// 不经过 HTTP 层：原来这里的 State 字段没有任何读取方，
		// 是路由分析页留下的最后一点残留
	})
	srv.Register(engine)

	// connState 记录「已连接但请求头还没收完」的连接，退出时用来避免白等 5 秒。
	//
	// 为什么需要它：Go 的 http.Server.Shutdown 会等所有非空闲连接结束，而它对
	// StateNew（连上了、请求头还没读完）的连接有一条特判（golang/go#22682）：
	// 只有连接存在超过 **5 秒**才把它当作空闲关掉。浏览器的预连接（preconnect）
	// 恰好就是这种连接 —— TCP 握手完成、一个字节都没发。于是每次重新部署，
	// Shutdown 都被它拖满 5 秒，而 compose 里 stop_grace_period 正好也是 5 秒：
	// 进程刚要退就被 SIGKILL，「已退出」那行日志都打不出来。
	//
	// 实测（.shots/exp-shutdown.sh，五种连接各测一次）：
	//   空闲          → 容器退出 1565ms
	//   新建连接(1s)  → 5414ms   ← 就是这条
	//   同样的连接但存在 8s → 522ms（过了那 5 秒特判，Go 直接关掉它）
	//   WebSocket     → 741ms（握手后连接被 hijack，Shutdown 本来就不等它）
	// 注意最后一条：锅不在实时推送的长连接上，虽然它是最像嫌疑犯的那个。
	conns := &newConnTracker{conns: make(map[net.Conn]struct{})}

	httpSrv := &http.Server{
		Addr:              cfg.Server.Addr(),
		Handler:           engine,
		ReadHeaderTimeout: 20 * time.Second,
		ConnState: func(c net.Conn, st http.ConnState) {
			switch st {
			case http.StateNew:
				conns.track(c)
			default:
				// Active（请求已进处理器）/ Idle（等下一个请求）/
				// Hijacked（WebSocket）/ Closed 都不再是「占着不放的新连接」
				conns.untrack(c)
			}
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 推送循环跟着进程生命周期走：退出时 ctx 结束，两个 goroutine 自行收尾
	srv.StartLive(ctx)

	// 密钥限流窗口的定期回收 + 日志保留期的自动清理，都跟着进程生命周期走；
	// 渠道状态（冷却条目）与分组限流窗口同样挂上回收 —— 它们的 map
	// 都只在被动访问时清理，删除的渠道/分组会留永久残余
	rateLimiter.StartSweeper(ctx, 10*time.Minute)
	state.StartSweeper(ctx, 10*time.Minute)
	groupLimit.StartSweeper(ctx, 10*time.Minute)
	srv.StartRetentionLoop(ctx)

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

	// 先叫醒那些「连上了但一个字节都没发」的连接，再 Shutdown。
	//
	// Shutdown 内部会关掉所有空闲连接，但它对 StateNew 的连接有 5 秒特判
	// （见上面 connState 的说明），所以这里不等它：给这些连接一个很短的窗口，
	// 让「马上要发请求」的客户端把请求头写完（读完请求头的连接会变成 Active，
	// Shutdown 就会正常等它），然后把仍然一声不吭的全部主动关掉 ——
	// 浏览器预连接属于后者，本来就没有请求要发，关掉不会有任何损失。
	//
	// 这个 grace 之所以短到 500ms：它每一毫秒都直接算进部署的停机时间里，
	// 而真实的请求不会「连上之后先愣半秒」——那半秒本来就是网络往返的一部分，
	// 由客户端发起请求时才开始计时。宁可少等，也不要每次部署都白停半秒。
	//
	// 没有任何这种连接时**一秒都不等**：这是常态（本机 curl、脚本调用都不会
	// 留下预连接），不该为一种少见情况让每次部署都白停半秒。
	//
	// 清理要一直做到 Shutdown 返回，不能只做一次：检查的这一刻没有预连接，
	// 不代表 Shutdown 期间不会有 —— 浏览器完全可能正好在部署那一瞬新开一条，
	// 而那种情况恰恰就是原来的 bug（白等 5 秒）。所以先按需等一个 grace，
	// 之后每隔一小段再扫一遍，直到 Shutdown 结束。
	shutdownDone := make(chan struct{})
	go func() {
		if conns.count() > 0 {
			time.Sleep(shutdownHeaderGrace)
			if n := conns.closeAll(); n > 0 {
				logger.Info("关闭尚未发出请求的连接", "count", n)
			}
		}
		for {
			select {
			case <-shutdownDone:
				return
			case <-time.After(200 * time.Millisecond):
				conns.closeAll()
			}
		}
	}()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownHTTPTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		// 超时也要往下走：日志队列里还有没落库的记录，直接 return 会连它一起丢。
		// （原样返回 err 会让 run() 走 os.Exit(1)，defer 里的收尾不会执行。）
		logger.Warn("优雅关闭未在预算内完成，继续收尾", "err", err)
	}
	close(shutdownDone)

	// 把日志队列里还没落库的记录写完再退。这个 defer 原本注册在 logs 创建处，
	// 而它只 close 通道、不等写完 —— 进程一退出，队列里剩下的请求日志就没了。
	// 队列上限 2048，必须在宽限期内做完，所以给它一个明确的上限。
	logs.CloseAndFlush(shutdownFlushTimeout, logger)

	logger.Info("已退出")
	return nil
}

// newConnTracker 记录「已连接但请求头还没收完」的连接。
//
// 这批连接是 Shutdown 里唯一会白等 5 秒的东西（见 main 里 connState 的说明），
// 所以单独盯住它们、退出时主动关掉。
type newConnTracker struct {
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func (t *newConnTracker) track(c net.Conn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.conns[c] = struct{}{}
}

func (t *newConnTracker) untrack(c net.Conn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.conns, c)
}

// count 返回当前仍在等待请求头的连接数。
func (t *newConnTracker) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.conns)
}

// closeAll 关掉所有仍在等待请求头的连接，返回关掉的数量。
func (t *newConnTracker) closeAll() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for c := range t.conns {
		// 关闭失败无需处理：连接可能刚好在关它之前自己断了，
		// 那种情况下它已经从 ConnState 里被 untrack 掉，目的已经达到
		_ = c.Close()
		delete(t.conns, c)
		n++
	}
	return n
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
