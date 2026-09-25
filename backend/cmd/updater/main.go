// updater 是跑在**宿主机上**的更新器，让容器里的管理台能够一键更新自己。
//
// # 为什么必须有这个东西
//
// 容器里的进程没法更新自己。这是容器模型的直接后果，不是实现上的偷懒：
// 容器文件系统是镜像的一个可写层，把新二进制写进去，下一次
// `docker compose up -d` 就随着旧容器一起消失 —— 而「下一次」正是
// 这次更新要触发的动作。镜像才是事实来源，而改镜像只能从宿主侧下手。
//
// 于是有了这个进程：它以 root 跑在宿主上，监听一个 unix socket，
// 收到请求后执行「拉取新镜像 → 重建容器 → 等健康检查 → 失败则回滚」。
// 管理台通过挂载进容器的同一个 socket 与它通话。
//
// # 为什么用 unix socket 而不是 TCP
//
// socket 文件在宿主机上，只有能访问该文件的进程才能连上 ——
// 容器里的服务是通过显式挂载拿到它的。也就是说**权限边界由文件系统
// 决定**，而不是由一个端口号加一个口令决定。TCP 监听哪怕只绑回环，
// 也意味着宿主上任何用户的任何进程都能试着连它。
//
// # 安全设计
//
// 这个进程能重建容器（等价于对 llm-relay 这个服务有完全控制权），
// 所以它做的每一件事都是**固定动作**，不接受任何来自 socket 的参数：
//
//   - 镜像名写死（-image 参数在启动时确定，之后不可变）
//   - compose 文件路径写死（-compose-file）
//   - 执行的命令是固定的几条 docker 命令，没有任何字符串拼接进 shell
//   - 不接受「指定版本」之类的输入 —— 回滚用的是它自己记下的上一个镜像 ID
//
// 换句话说：socket 上唯一的输入是「升级」或「回滚」这两个动作名。
// 一个能连上它的人最多只能让服务更新到最新版，或者退回上一版 ——
// 这两件事本来就是站主自己会做的事。这与「把 docker.sock 挂进容器」
// 的方案有本质区别：后者等于把整个 Docker 控制面交出去。
//
// # 用法
//
//	llm-relay-updater -socket /run/llm-relay-updater.sock \
//	                  -compose-file /opt/llm-relay/deploy/docker-compose.yml \
//	                  -image ghcr.io/ashu1800/llm-relay:latest
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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// 升级流程的各个阶段。这些字符串会原样出现在管理台的进度提示里，
// 所以用中文 —— 前端直接显示，不做二次翻译。
const (
	phaseIdle        = "idle"
	phasePull        = "pull"
	phaseRecreate    = "recreate"
	phaseWait        = "wait"
	phaseDone        = "done"
	phaseFailed      = "failed"
	phaseRollingBack = "rolling_back"
)

// upgrade 是一次升级/回滚任务的状态。
//
// 每个导出字段都要有 json tag —— 漏掉就会被序列化成大写原名，
// 而管理台读的是小写（`done` 而不是 `Done`），结果是界面永远
// 认为任务在运行。这个坑在 update 包的任务结构上踩过一次，
// 见 backend/internal/update/update_test.go 的 TestTaskJSONFieldNames。
type upgrade struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"` // upgrade / rollback
	Phase   string    `json:"phase"`
	Percent int       `json:"percent"`
	Message string    `json:"message"`
	Done    bool      `json:"done"`
	Failed  bool      `json:"failed"`
	Started time.Time `json:"started_at"`
	// Logs 不加 omitempty：空数组与「字段不存在」在前端是两件事
	Logs []string `json:"logs"`

	// previousImage 是本次操作前的镜像 ID，用于失败时自动回滚。
	// 只在内存里 —— 更新器重启后这个依据会丢，这是有意的取舍：
	// 落盘的状态文件会在「更新到一半断电」时留下一个指向未知状态的
	// 记录，而那种记录比没有记录更危险（会让人以为能回滚到某处）
	previousImage string
}

// runner 持有升级所需的固定配置与当前任务状态。
type runner struct {
	composeFile string
	service     string
	image       string
	// strategy 决定「升级」在这个部署上是什么意思：
	//
	//	image  拉取远端新镜像（镜像来自 GHCR，更新 = 换镜像）
	//	build  在本地用源码重新构建镜像（源码就在这台机器上，
	//	       更新 = git pull + 重新构建 —— 这是本项目的默认部署方式）
	//
	// 这两者的差别不是实现细节而是**部署事实**：前者的事实来源是
	// 镜像仓库，后者的来源是这台机器上的源码目录。判错了就会出现
	// 「点了更新、提示成功、版本号纹丝不动」——因为镜像本来就没变。
	strategy string
	repoDir  string
	// repo 是发布源仓库（owner/name），strategy=build 时从它下载源码归档
	repo string
	// rollbackTag 是回滚目标的镜像标签名。
	//
	// 它必须是一个**能被 docker 解析的名字**，而不是 content ID：
	// 实测发现 compose build 之后旧镜像的 ID 会被回收，用 ID 回滚必然
	// 报 "No such image"。详见 docker.go 里 tagRollback 的注释。
	rollbackTag string
	url         string
	logger      *slog.Logger
	// http 是共享的 HTTP 客户端，用于访问 GitHub API 与 codeload
	http *http.Client

	mu      sync.Mutex
	current *upgrade
	// history 是最近几次任务的快照，供「刷新页面后还能看到刚才的结果」。
	// 只留最近若干条，避免长跑进程内存缓慢增长
	history []*upgrade
}

func main() {
	var (
		socketPath      = flag.String("socket", "/run/llm-relay-updater.sock", "监听的 unix socket 路径")
		socketMode      = flag.String("socket-mode", "0666", "socket 文件权限（八进制）。默认 0666 是为了让容器内的非 root 用户也能连上")
		composeFile     = flag.String("compose-file", "", "docker-compose.yml 的绝对路径（必填）")
		service         = flag.String("service", "app", "compose 里应用服务的名字")
		image           = flag.String("image", "llm-relay:local", "要使用的镜像名（含 tag）")
		strategy        = flag.String("strategy", "image", "更新策略: image=拉取镜像 | build=在本地用源码重新构建")
		repoDir         = flag.String("repo-dir", "", "strategy=build 时的源码目录（应含 deploy/ 与 backend/）")
		repo            = flag.String("repo", "ashu1800/llm-relay", "发布源仓库（owner/name），strategy=build 时从它下载源码")
		rollbackTagFlag = flag.String("rollback-tag", "", "回滚目标的镜像标签名（默认按 -image 推导为 <镜像名>:rollback）")
		healthURL       = flag.String("health-url", "http://127.0.0.1:8888/readyz", "重建后用于判断成功的健康检查地址")
		logLevel        = flag.String("log-level", "info", "日志级别: debug/info/warn/error")
	)
	flag.Parse()

	logger := newLogger(*logLevel)
	slog.SetDefault(logger)

	if *composeFile == "" {
		fmt.Fprintln(os.Stderr, "必须指定 -compose-file。用 -h 查看说明。")
		os.Exit(2)
	}
	// compose 文件必须存在：更新器在更新**失败**时才最需要它，
	// 而那时才发现路径写错就太晚了。启动时一次性校验。
	if _, err := os.Stat(*composeFile); err != nil {
		fmt.Fprintf(os.Stderr, "找不到 compose 文件 %s: %v\n", *composeFile, err)
		os.Exit(2)
	}
	// strategy=build 需要源码目录。同样是启动时校验 ——
	// 等到用户点了更新才发现目录不存在，是最没必要的一次失败。
	if *strategy == "build" {
		if *repoDir == "" {
			fmt.Fprintln(os.Stderr, "-strategy=build 时必须指定 -repo-dir")
			os.Exit(2)
		}
		if _, err := os.Stat(filepath.Join(*repoDir, "backend", "go.mod")); err != nil {
			fmt.Fprintf(os.Stderr, "源码目录 %s 看起来不对（找不到 backend/go.mod）: %v\n", *repoDir, err)
			os.Exit(2)
		}
	}
	switch *strategy {
	case "image", "build":
	default:
		fmt.Fprintf(os.Stderr, "-strategy 只能是 image 或 build，当前: %q\n", *strategy)
		os.Exit(2)
	}

	r := &runner{
		composeFile: *composeFile,
		service:     *service,
		image:       *image,
		strategy:    *strategy,
		repoDir:     *repoDir,
		repo:        *repo,
		rollbackTag: deriveRollbackTag(*rollbackTagFlag, *image),
		url:         *healthURL,
		logger:      logger,
		// 下载源码归档可能几十 MB，给 10 分钟；
		// API 查询是几十毫秒的小请求，共用这一个客户端没问题
		// （超时由每次请求的 context 控制）
		http: &http.Client{Timeout: 10 * time.Minute},
	}

	// socket 放在 systemd 的 RuntimeDirectory 下时，目录由 systemd 创建；
	// 手工运行时目录可能不存在，这里补一下
	if dir := filepath.Dir(*socketPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "创建 socket 目录失败: %v\n", err)
			os.Exit(1)
		}
	}
	// 清掉陈旧 socket 文件：进程上次被 SIGKILL 时它会留在原地，
	// 而 bind 到已存在的路径会直接失败 —— 那会表现成「更新器启动不了，
	// 但错误信息只说 address already in use」
	if err := os.Remove(*socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "清理旧 socket 失败: %v\n", err)
		os.Exit(1)
	}

	ln, err := net.Listen("unix", *socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "监听 %s 失败: %v\n", *socketPath, err)
		os.Exit(1)
	}

	// socket 权限。
	//
	// 默认 0666 而不是更保守的 0660，理由是**两边 uid 对不上**：
	// 容器里的进程以镜像内的 relay 用户运行（uid 100），而宿主上的
	// 更新器以 root 跑。用 0660 + 固定组名在两侧无法可靠对齐 ——
	// 宿主的组 ID 取决于系统里用户的创建顺序，写死任何一个值都会
	// 在某台机器上恰好撞车。
	//
	// 放宽到 0666 的代价可控：这个 socket 只暴露两个**固定动作**
	// （升级到最新版 / 退回上一版），不接受任何参数（镜像名与 compose
	// 路径都在启动时写死）。也就是说，能连上它的人最多只能让服务
	// 更新或回滚 —— 这两件事本来就是站主自己会做的事，拿不到额外的
	// 能力（不像 docker.sock，那等于整个 Docker 控制面）。
	//
	// 在意这一点的部署可以传 -socket-mode 0660，并把容器改为以
	// 宿主上的某个用户运行（user: 1000:1000），两边就能对齐。
	mode, err := strconv.ParseUint(strings.TrimPrefix(*socketMode, "0o"), 8, 32)
	if err != nil {
		fmt.Fprintf(os.Stderr, "-socket-mode 不是合法的八进制权限: %q\n", *socketMode)
		os.Exit(2)
	}
	if err := os.Chmod(*socketPath, os.FileMode(mode)); err != nil {
		logger.Warn("设置 socket 权限失败", "err", err, "mode", *socketMode)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", r.handleHealthz)
	mux.HandleFunc("/progress", r.handleProgress)
	mux.HandleFunc("/upgrade", r.handleUpgrade)
	mux.HandleFunc("/rollback", r.handleRollback)

	srv := &http.Server{
		Handler: mux,
		// 不设 ReadTimeout：升级请求本身很快返回（后台跑），
		// 但健康检查探测可能持续几十秒 —— 那是写在 handler 里的，
		// 不该被 server 层的超时打断
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("更新器已启动",
			"socket", *socketPath, "image", *image, "compose", *composeFile)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("服务退出", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("收到退出信号，正在关闭")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	// socket 文件要主动删掉：留着它，下次启动前的那个「陈旧 socket」
	// 检查就会触发（虽然能自愈），而且外人看到文件会以为进程还在
	_ = os.Remove(*socketPath)
}

func newLogger(level string) *slog.Logger {
	lv := slog.LevelInfo
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}
