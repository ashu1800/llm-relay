package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/version"
)

// 更新源与缓存相关的默认值。
const (
	// settingKey 是更新配置在 settings 表里的键。
	// 放进数据库而不是环境变量，是因为「临时改一下代理看能不能通 GitHub」
	// 是排障时的常规动作，为它重启一次服务代价太大。
	settingKey = "update"

	// checkCacheTTL 是检测结果的缓存时长。
	// 20 分钟的理由：GitHub 未带 token 时的限额是 60 次/小时，
	// 而管理台每次刷新页面都会问一次 —— 不缓存的话，一个人多刷几次
	// 就能把限额耗光，之后所有人看到的都是「限额已用尽」。
	checkCacheTTL = 20 * time.Minute

	// maxRollbackVersions 是回滚列表里最多展示几个版本。
	// 界面上是一个单选列表，超过三四个就没人会认真读了，
	// 而「回滚」这个动作本身也不该鼓励随便挑一个试试。
	maxRollbackVersions = 3

	// rollbackScanLimit 是回滚列表要拉取多少个 release 再筛选。
	// 要比 maxRollbackVersions 大：当前版本附近可能有若干预发布版、
	// 草稿版需要被过滤掉，只拉 3 条会筛出不足 3 个候选。
	rollbackScanLimit = 15

	// updateTimeout 是一次更新任务的总时限。
	//
	// 这个数字必须**大于**下载客户端的超时（10 分钟），否则会出现
	// 「下载还在正常跑，外层先超时把它掐了」——现象是更新偶尔在
	// 网络慢的时候莫名失败，而日志里只有一句 context deadline exceeded。
	updateTimeout = 20 * time.Minute
)

// Config 是更新功能的可配置项（存在 settings 表里）。
type Config struct {
	// Enabled 为 false 时，界面不提供「检测更新」入口。
	// 默认 true —— 这个功能的全部价值就在于能发现新版本，
	// 默认关掉等于默认不用。
	Enabled bool `json:"enabled"`
	// Repo 是发布源仓库，形如 owner/name
	Repo string `json:"repo"`
	// Token 是可选的 GitHub token（提升 API 限额）。
	// 只对 api.github.com 发送，绝不发给资源 CDN
	Token string `json:"token"`
	// Proxy 是可选的出站代理，给国内直连不通 GitHub 的部署用
	Proxy string `json:"proxy"`
}

// DefaultConfig 返回内置默认配置。
func DefaultConfig() Config {
	return Config{Enabled: true, Repo: defaultRepo}
}

// Info 是「检测更新」接口的响应。
type Info struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
	// HasUpdate 表示 latest 比 current 新
	HasUpdate bool `json:"has_update"`
	// BuildType 见 version 包的常量，前端据此决定显示哪种入口
	BuildType string `json:"build_type"`
	// CanApply 表示**当前形态下能否一键更新**。
	// 与 HasUpdate 是两个独立的问题：source 构建即便有新版本也不能自动更新，
	// docker 构建虽然有新版本但宿主侧 updater 不在场时同样不能。
	CanApply bool `json:"can_apply"`
	// ApplyMode 是即将采用的更新方式：binary / docker / manual
	ApplyMode string `json:"apply_mode"`
	// BlockedReason 在 CanApply 为 false 时说明原因，直接显示给用户
	BlockedReason string `json:"blocked_reason,omitempty"`
	// Release 是发布说明（更新日志），可能为空
	Release *ReleaseInfo `json:"release,omitempty"`
	// Warning 是「检测本身出了问题」时的说明（网络不通、限额用尽）。
	// 这种情况下 HasUpdate 为 false，但用户需要知道这不是「已是最新」
	Warning string `json:"warning,omitempty"`
	// Cached 表示这次返回的是缓存结果
	Cached bool `json:"cached"`
	// CheckedAt 是该结果的取得时刻（缓存时是首次取得的时刻）
	CheckedAt time.Time `json:"checked_at"`
}

// RollbackCandidate 是可回滚到的某个版本。
type RollbackCandidate struct {
	Version     string `json:"version"`
	PublishedAt string `json:"published_at"`
	HTMLURL     string `json:"html_url"`
}

// UpgradeRunner 是宿主侧更新器的抽象。
//
// 定义成接口而不是直接依赖具体类型，是为了让「宿主侧更新器不在场」
// 成为一个可测试的普通状态（返回 ErrUpdaterUnavailable），
// 而不是一个需要真去连 socket 才能触发的分支。
type UpgradeRunner interface {
	// Upgrade 请求宿主侧更新器执行一次升级，返回升级 ID 供查询进度。
	// 更新器不在场时返回 ErrUpdaterUnavailable（连接被拒会映射成它），
	// 所以不需要单独的 Available 预检 —— 那只是多一次往返。
	Upgrade(ctx context.Context) (string, error)
	// Progress 查询某次升级的进度
	Progress(ctx context.Context, id string) (*UpgradeProgress, error)
}

// UpgradeProgress 是宿主侧更新器上报的进度。
type UpgradeProgress struct {
	ID      string `json:"id"`
	Phase   string `json:"phase"`
	Percent int    `json:"percent"`
	Message string `json:"message"`
	Done    bool   `json:"done"`
	Failed  bool   `json:"failed"`
	// Logs 是最近若干行输出，供界面在失败时给出可排查的信息
	Logs []string `json:"logs,omitempty"`
}

// ErrUpdaterUnavailable 表示宿主侧更新器不在场。
//
// 这是 docker 部署下最常见的「不能一键更新」的原因，值得一个专门的错误：
// 它对应的用户动作是「去服务器上跑一次 install.sh」，
// 而其它错误对应的动作是「看日志、查网络」——两者不能混为一谈。
var ErrUpdaterUnavailable = errors.New("宿主侧更新器未运行，无法自动更新镜像")

// 业务性拒绝的哨兵错误。
//
// 它们的共同点：错误文案本身就是**给用户的结论**（读一句话就知道该干嘛），
// 而不是需要去看日志的故障。API 层靠 errors.Is把它们归为 409，
// 其余错误归为 500 —— 用类型而不是文案子串判断，改措辞不会让
// 分类静默漂移（动态信息用 %w 包住哨兵，见各构造点）。
var (
	// ErrTaskRunning 已有更新任务在跑（全局只允许一个）。
	ErrTaskRunning = errors.New("已有更新任务正在进行，请等它结束")
	// ErrModeUnsupported Apply 收到了不认识的 mode。
	ErrModeUnsupported = errors.New("不支持的更新方式")
	// ErrCannotApply 当前构建形态不允许一键更新（源码构建 / 无法定位自身）。
	ErrCannotApply = errors.New("当前部署形态不支持在线更新")
	// ErrBuildTypeRejected 当前构建形态不支持请求的动作（如 docker 形态的在线回滚）。
	ErrBuildTypeUnsupported = errors.New("当前构建类型不支持该操作")
)

// IsBusinessError 报告 err 是否属于「用户可理解的业务性拒绝」。
//
// 分类逻辑放在错误定义的旁边而不是 API 层：哪些错误是业务拒绝，
// 只有构造它们的地方知道；API 层只负责把结论翻译成 409/500。
func IsBusinessError(err error) bool {
	return errors.Is(err, ErrTaskRunning) ||
		errors.Is(err, ErrModeUnsupported) ||
		errors.Is(err, ErrCannotApply) ||
		errors.Is(err, ErrBuildTypeUnsupported)
}

// Service 是更新功能的核心。
type Service struct {
	db     *gorm.DB
	logger *slog.Logger

	// client 由配置（代理/token/仓库）构造，配置变更时重建。
	// 用 mu 保护：配置可以在运行时改，而检测更新可能正好在跑。
	mu     sync.RWMutex
	client *Client
	cfg    Config

	// cache 是检测结果的进程内缓存
	cacheMu   sync.Mutex
	cache     *Info
	cacheAt   time.Time
	cacheRepo string

	// runner 是宿主侧更新器（docker 形态），可能为 nil
	runner UpgradeRunner

	// task 是当前正在跑的更新任务（binary 形态的自我替换）。
	// 全局只允许一个：两个人同时点「更新」去做同一件事没有意义，
	// 而并发的文件替换会把备份文件搅乱，让回滚失去依据。
	taskMu sync.Mutex
	task   *Task
}

// Task 是一次更新任务的进度快照。
//
// 每个字段都必须有 json tag。这一点看起来是废话，但漏掉三个（Ended /
// Failed / Done）就会产生一个很隐蔽的故障：Go 默认把无 tag 的导出字段
// 序列化成**大写的原名**（"Done" 而不是 "done"），于是前端的
// `!task.done` 恒为 true —— 界面永远认为任务在运行，进度条卡住不消失，
// 「重启」按钮永远不出现。编译期没有任何提示，类型检查也发现不了
// （TS 的接口只是编译期约定，运行时 JSON 里少一个字段不会报错）。
type Task struct {
	ID      string     `json:"id"`
	Kind    string     `json:"kind"` // update / rollback
	Target  string     `json:"target"`
	Phase   string     `json:"phase"`
	Percent int        `json:"percent"`
	Message string     `json:"message"`
	Started time.Time  `json:"started_at"`
	Ended   *time.Time `json:"ended,omitempty"`
	Failed  bool       `json:"failed"`
	Done    bool       `json:"done"`

	// Logs 是给界面看的任务日志（失败时用它排查原因）。
	// 不加 omitempty：空数组与「字段不存在」在前端是两件事 ——
	// 前者是「当前没有日志」，后者会让 `task.logs?.length` 这类判断
	// 落到 undefined 上，而那个分支本该走「没有日志」。
	Logs []string `json:"logs"`

	// 内部字段不序列化
	cancel context.CancelFunc
	logs   []string
}

// Snapshot 返回可安全序列化的任务副本。
//
// 返回副本而不是原对象：任务在后台 goroutine 里持续被改写，
// 直接把指针交给 HTTP 层去序列化会与那些写入撞上数据竞争
// （`go test -race` 会报，而线上表现为偶发的进度数字错乱）。
func (t *Task) Snapshot() *Task {
	if t == nil {
		return (&Task{}).Normalized()
	}
	cp := *t
	cp.cancel = nil
	// logs 是内部累积的原始行，暴露成 Logs 供界面在失败时显示。
	// nil → 空切片的归一交给 Normalized 统一处理，这里只复制内容。
	cp.Logs = append([]string{}, t.logs...)
	cp.logs = nil
	return &cp
}

// NewService 构造更新服务。
//
// runner 可为 nil —— 那表示这个部署没有宿主侧更新器（bare 安装、
// 或者用户没装）。此时 docker 形态会明确报告「无法自动更新」，
// 而不是静默什么都不做。
func NewService(db *gorm.DB, runner UpgradeRunner, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{db: db, runner: runner, logger: logger}
	cfg, err := s.loadConfig()
	if err != nil {
		// 读配置失败不该让服务起不来：用默认值继续，界面上会显示
		// 「检测更新」的结果里带 Warning，用户能看到实际发生了什么
		logger.Warn("读取更新配置失败，使用默认值", "err", err)
		cfg = DefaultConfig()
	}
	s.applyConfig(cfg)
	return s
}

// GetConfig 返回当前配置（含从数据库读到的值）。
func (s *Service) GetConfig() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// SaveConfig 保存配置并重建客户端。
func (s *Service) SaveConfig(cfg Config) error {
	cfg.Repo = strings.TrimSpace(cfg.Repo)
	cfg.Token = strings.TrimSpace(cfg.Token)
	cfg.Proxy = strings.TrimSpace(cfg.Proxy)
	if cfg.Repo == "" {
		cfg.Repo = defaultRepo
	}
	if !strings.Contains(cfg.Repo, "/") {
		return fmt.Errorf("仓库必须形如 owner/name，当前: %q", cfg.Repo)
	}

	row := model.Setting{Key: settingKey, Value: model.JSONMap{
		"enabled": cfg.Enabled,
		"repo":    cfg.Repo,
		"token":   cfg.Token,
		"proxy":   cfg.Proxy,
	}}
	if err := s.db.Save(&row).Error; err != nil {
		return fmt.Errorf("保存更新配置失败: %w", err)
	}

	s.applyConfig(cfg)
	// 配置变了，缓存的检测结果就过期了（换了仓库还显示旧仓库的结果会误导）
	s.invalidateCache()
	return nil
}

// applyConfig 重建客户端。构造失败时保留旧客户端并把原因记下来，
// 由检测接口通过 Warning 暴露 —— 比让整个更新功能直接不可用要好。
func (s *Service) applyConfig(cfg Config) {
	client, err := NewClient(ClientOptions{Repo: cfg.Repo, Token: cfg.Token, ProxyURL: cfg.Proxy})
	if err != nil {
		s.logger.Warn("更新客户端构造失败，沿用上一次的配置", "err", err)
		s.mu.Lock()
		s.cfg = cfg
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.cfg = cfg
	s.client = client
	s.mu.Unlock()
}

// loadConfig 从 settings 表读配置。
func (s *Service) loadConfig() (Config, error) {
	cfg := DefaultConfig()
	if s.db == nil {
		return cfg, nil
	}
	var row model.Setting
	err := s.db.First(&row, "key = ?", settingKey).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	read := func(k string, dst *string) {
		if v, ok := row.Value[k].(string); ok {
			*dst = v
		}
	}
	if v, ok := row.Value["enabled"].(bool); ok {
		cfg.Enabled = v
	}
	read("repo", &cfg.Repo)
	read("token", &cfg.Token)
	read("proxy", &cfg.Proxy)
	if cfg.Repo == "" {
		cfg.Repo = defaultRepo
	}
	return cfg, nil
}

func (s *Service) invalidateCache() {
	s.cacheMu.Lock()
	s.cache = nil
	s.cacheAt = time.Time{}
	s.cacheMu.Unlock()
}

// Check 检测更新。
//
// force 为 true 时跳过缓存。缓存不只是省流量 —— 它决定了
// 「点开面板」这个动作会不会打一次 GitHub API，而限额是共享的。
//
// 检测失败时**不返回错误**，而是返回一个带 Warning 的正常响应：
// 这是界面上的一个角落，让整个面板报错弹出红条会盖住其它信息
// （尤其是「当前版本号」这个永远该可见的事实）。
func (s *Service) Check(ctx context.Context, force bool) *Info {
	cfg := s.GetConfig()

	base := s.currentState()

	if !cfg.Enabled {
		base.Warning = "更新检测已在系统设置中关闭"
		return base
	}

	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	if client == nil {
		base.Warning = "更新客户端不可用（配置有误），请检查系统设置里的更新配置"
		return base
	}

	if !force {
		if cached := s.fromCache(cfg.Repo); cached != nil {
			// 缓存里的 CanApply 依赖运行期状态（更新器是否在场），
			// 所以每次都要用当前状态重新算，不能连它一起缓存
			cached.CanApply, cached.ApplyMode, cached.BlockedReason = s.applyMode()
			return cached
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	rel, err := client.FetchLatestRelease(ctx)
	if err != nil {
		// 失败时若有缓存就退回去用，并说明数据可能不是最新的
		if cached := s.fromCache(cfg.Repo); cached != nil {
			cached.Cached = true
			cached.Warning = "无法连接 GitHub，以下为上次检测的结果：" + err.Error()
			cached.CanApply, cached.ApplyMode, cached.BlockedReason = s.applyMode()
			return cached
		}
		base.Warning = err.Error()
		return base
	}

	info := base
	info.Latest = strings.TrimPrefix(rel.TagName, "v")
	info.HasUpdate = version.Newer(info.Latest, info.Current)
	info.CheckedAt = time.Now().UTC()
	info.Release = &ReleaseInfo{
		TagName:     rel.TagName,
		Name:        rel.Name,
		Body:        rel.Body,
		PublishedAt: rel.PublishedAt,
		HTMLURL:     rel.HTMLURL,
		Prerelease:  rel.Prerelease,
	}

	s.toCache(info, cfg.Repo)
	return info
}

// currentState 组装与网络无关的那部分信息。
//
// 设置页的仓库/开关回显走 GET /update/config，不混在这里 ——
// 两份响应各自服务一个问题，字段才不会互相牵连。
func (s *Service) currentState() *Info {
	canApply, mode, reason := s.applyMode()
	return &Info{
		Current:       version.Version,
		Latest:        version.Version,
		BuildType:     version.BuildType,
		CanApply:      canApply,
		ApplyMode:     mode,
		BlockedReason: reason,
		CheckedAt:     time.Now().UTC(),
	}
}

// applyMode 判定「当前部署形态下能不能一键更新、走哪条路」。
//
// 三种形态的判定与理由是这套功能里最该被写清楚的一段：
//
//	source  不能更新。没有发布产物与之对应，自动更新只会用官方二进制
//	        覆盖掉开发者自己编译的那份，那不是「更新」而是「破坏工作区」。
//	docker  能，但必须经由宿主侧更新器。容器里的进程替换不了自己 ——
//	        镜像才是事实来源。更新器不在场时明确报告原因，而不是
//	        给一个点了没反应的按钮。
//	binary  能，直接自我替换。这是唯一一条「进程改自己」的路径。
func (s *Service) applyMode() (canApply bool, mode, reason string) {
	switch version.BuildType {
	case version.BuildSource:
		return false, "manual",
			"当前是源码构建（未使用官方发布产物），请用 git pull 后重新部署"
	case version.BuildDocker:
		if s.runner == nil {
			return false, "docker",
				"未检测到宿主侧更新器（llm-relay-updater）。请重新运行 deploy/install.sh 以安装它，之后即可一键更新"
		}
		// runner 是否真的活着要连一次 socket 才知道，
		// 那是网络操作，不放在这个纯判定函数里 —— Check 的调用方
		// 会在用户点「更新」时拿到真实的错误
		return true, "docker", ""
	case version.BuildBinary:
		if _, err := SelfPath(); err != nil {
			return false, "binary", err.Error()
		}
		return true, "binary", ""
	default:
		return false, "manual", "无法识别的构建类型: " + version.BuildType
	}
}

// 缓存读写。缓存的是「与网络有关的那部分」，运行期状态不缓存。

func (s *Service) fromCache(repo string) *Info {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.cache == nil || s.cacheRepo != repo {
		return nil
	}
	if time.Since(s.cacheAt) > checkCacheTTL {
		return nil
	}
	cp := *s.cache
	cp.Cached = true
	return &cp
}

func (s *Service) toCache(info *Info, repo string) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	cp := *info
	cp.Cached = false
	s.cache = &cp
	s.cacheAt = time.Now()
	s.cacheRepo = repo
}

// ---------------------------------------------------------------------------
// 应用更新
// ---------------------------------------------------------------------------

// Apply 启动一次更新。立即返回任务快照，真正的下载在后台跑。
//
// 为什么是后台任务而不是同步执行（见包注释第 2 点）：下载是分钟级的，
// 同步 HTTP 请求必然会被某一层掐断。返回任务 ID 后，界面轮询
// /system/update/progress 就能看到阶段与百分比，刷新页面也不丢。
//
// mode 参数允许前端显式指定走哪条路（"docker"/"binary"），
// 留空则按当前构建形态自动选。留这个口子是因为存在「容器里跑着
// 但想用 binary 方式」这类非常规部署，而报错让人无从下手。
func (s *Service) Apply(ctx context.Context, mode string) (*Task, error) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		_, auto, reason := s.applyMode()
		if reason != "" {
			// 这些原因（源码构建、无法定位自身……）都是结论而非故障，包上哨兵
			return nil, fmt.Errorf("%s: %w", reason, ErrCannotApply)
		}
		mode = auto
	}

	switch mode {
	case "docker":
		return s.applyDocker(ctx)
	case "binary":
		return s.applyBinary(ctx)
	default:
		return nil, fmt.Errorf("%w: %q", ErrModeUnsupported, mode)
	}
}

// applyDocker 把升级交给宿主侧更新器。
//
// 这个方法**不启动本地任务** —— 进度的真相在宿主侧，
// 本进程只是个转发者。返回的任务里带的是宿主侧给的升级 ID，
// 之后由 Progress 原样转发查询。
func (s *Service) applyDocker(ctx context.Context) (*Task, error) {
	if s.runner == nil {
		return nil, ErrUpdaterUnavailable
	}

	// 不做 Available 预检：Upgrade 连不上 socket 时会把连接拒绝
	// 映射成 ErrUpdaterUnavailable（见 runner 的 do），一次往返就能得到
	// 与「先探测再请求」完全相同的结论。
	id, err := s.runner.Upgrade(ctx)
	if err != nil {
		return nil, fmt.Errorf("请求宿主侧更新器失败: %w", err)
	}
	now := time.Now().UTC()
	return (&Task{
		ID:      id,
		Kind:    "update",
		Target:  "latest",
		Phase:   "requested",
		Percent: 0,
		Message: "已请求宿主侧更新器拉取并重建容器",
		Started: now,
	}).Normalized(), nil
}

// Normalized 保证可选的数组字段非 nil，然后返回自身。
//
// 存在的理由是一个很容易漏的细节：Go 里 nil 切片序列化成 JSON 的 `null`，
// 而前端期望数组 —— `task.logs.length` 会在 null 上抛错，
// 表现成「面板打开就白屏」，而根因只是某个返回路径忘了初始化这个字段。
//
// 与其在六处构造 Task 的地方各记一次，不如让**出口**统一过一道。
// 所有返回 Task 给 HTTP 层的地方都必须调用它。
func (t *Task) Normalized() *Task {
	if t.Logs == nil {
		t.Logs = []string{}
	}
	if t.cancel != nil {
		// 内部字段不该随响应出去（没有 json tag 本来就不会，
		// 但取消函数被带出去意味着调用方能中断任务，这里显式清掉）
		t.cancel = nil
	}
	return t
}

// applyBinary 下载发布产物并原子替换自身。
//
// 并发保护在这里：全局只允许一个任务。两个人同时点「更新」时，
// 第二个会拿到明确错误而不是两个人一起去改同一批文件。
func (s *Service) applyBinary(parent context.Context) (*Task, error) {
	// 从父 context 剥离取消信号再套超时。
	//
	// 直接用 c.Request.Context() 会让「用户关掉浏览器标签页」变成
	// 「更新中断」—— 一个已经下载了一半的归档被丢弃，下次还得重来。
	// 更新一旦开始就该跑完，它的生命周期不该与某一次 HTTP 请求绑定。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), updateTimeout)

	task := &Task{
		ID:      fmt.Sprintf("upd-%d", time.Now().UnixNano()),
		Kind:    "update",
		Target:  "latest",
		Phase:   "prepare",
		Percent: 0,
		Message: "准备中",
		Started: time.Now().UTC(),
		cancel:  cancel,
	}
	if err := s.claimTask(task); err != nil {
		cancel()
		return nil, err
	}

	go func() {
		defer cancel()
		s.runBinaryUpdate(ctx, task)
	}()

	return task.Snapshot().Normalized(), nil
}

// claimTask 占住全局任务槽：没有进行中的任务时把 t 记为当前任务并返回
// nil；已有任务在跑时返回 ErrTaskRunning（带上进行中任务的阶段信息，
// 让第二个人知道它跑到哪了）。
func (s *Service) claimTask(t *Task) error {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if s.task != nil && !s.task.Done {
		return fmt.Errorf("%w（%s，%s）", ErrTaskRunning, s.task.Phase, s.task.Message)
	}
	s.task = t
	return nil
}

// failTask 把任务置为失败并记一条服务端日志。
//
// 失败原因必须落日志：任务面板会被关掉，而排障的人手里只有日志。
func (s *Service) failTask(t *Task, format string, args ...any) {
	s.setTask(t, "failed", 100, fmt.Sprintf(format, args...))
	s.taskMu.Lock()
	t.Failed = true
	s.taskMu.Unlock()
	s.logger.Warn("更新任务失败", "task", t.ID, "kind", t.Kind, "reason", fmt.Sprintf(format, args...))
}

// runBinaryUpdate 是 binary 更新的实际流程。
//
// 分四步，每步都更新任务状态：prepare → checking → downloading →
// verifying → installing → done。之所以把阶段分这么细，是因为
// 用户在这个界面上的唯一问题是「它卡住了还是在干活」，
// 而阶段名就是答案。
func (s *Service) runBinaryUpdate(ctx context.Context, t *Task) {
	defer s.finishTask(t)

	// ---- 1. 定位自身 ----
	exePath, err := SelfPath()
	if err != nil {
		s.failTask(t, "无法定位当前可执行文件: %v", err)
		return
	}
	s.setTask(t, "checking", 5, "正在检测最新版本")

	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	if client == nil {
		s.failTask(t, "更新客户端不可用，请检查系统设置里的更新配置")
		return
	}

	// ---- 2. 取最新版本 ----
	// 这里强制不走缓存：用户点「更新」的意图就是「现在装最新的」，
	// 20 分钟前的缓存可能指向一个已经不是最新的版本
	rel, err := client.FetchLatestRelease(ctx)
	if err != nil {
		s.failTask(t, "获取最新版本失败: %v", err)
		return
	}
	target := strings.TrimPrefix(rel.TagName, "v")

	if !version.Newer(target, version.Version) {
		// 已经是最新：这不是错误，任务成功结束。
		// 报成失败会让用户以为出了问题，而实际情况是什么都不用做
		s.setTask(t, "done", 100, fmt.Sprintf("当前已是最新版本（v%s）", version.Version))
		return
	}

	s.taskMu.Lock()
	t.Target = target
	s.taskMu.Unlock()

	// ---- 3. 下载、校验并替换 ----
	if !s.installRelease(ctx, client, rel, target, exePath, t, 20) {
		return
	}

	s.setTask(t, "done", 100,
		fmt.Sprintf("已更新到 v%s，重启服务后生效（上一版本已备份，可回滚）", target))
	s.logger.Info("更新完成", "from", version.Version, "to", target, "exe", exePath)
}

// installRelease 完成「找平台产物 → 下载 → 校验 → 解压 → 替换」的安装尾巴。
//
// update 与 rollback 两条流程共用这一段：两边各写一份时，安全检查
// （大小上限、校验和强制）很容易在某一次改动中只被其中一侧继承。
// downloadingPercent 让两条流程保持各自的进度节奏。失败时把任务置为
// 失败并返回 false，成功返回 true。
func (s *Service) installRelease(ctx context.Context, client *Client, rel *Release, target, exePath string, t *Task, downloadingPercent int) bool {
	archiveName := ArchiveName(target, runtime.GOOS, runtime.GOARCH)
	asset, ok := FindAsset(rel.Assets, archiveName)
	if !ok {
		// 这条错误几乎总是发布流水线的问题，所以把期望的文件名写出来
		s.failTask(t, "本次发布里没有找到当前平台的产物 %s（%s/%s）", archiveName, runtime.GOOS, runtime.GOARCH)
		return false
	}
	checksumURL := ChecksumURL(rel.Assets)

	// 临时目录建在可执行文件同目录：保证后面的 rename 是同文件系统的原子操作
	tmpDir, err := TempDir(exePath)
	if err != nil {
		s.failTask(t, "%v", err)
		return false
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	archivePath := filepath.Join(tmpDir, archiveName)
	s.setTask(t, "downloading", downloadingPercent,
		fmt.Sprintf("正在下载 %s（%.1f MB）", archiveName, float64(asset.Size)/(1<<20)))
	if err := client.Download(ctx, asset.BrowserDownloadURL, archivePath, maxArchiveSize); err != nil {
		s.failTask(t, "下载失败: %v", err)
		return false
	}

	// 校验和缺失时**拒绝安装**。
	//
	// sub2api 在这里是「有就校验、没有就跳过」，我们选择更严的一侧：
	// 这个功能的本质是「从网络上下载一个可执行文件并让它以服务身份运行」，
	// 而这是我们能做的最后一道、也是唯一一道完整性检查。
	// 一个没有校验和的发布要么是流水线配错了（那就该修），
	// 要么是有人在上游做了手脚（那更不能装）。
	//
	// 代价是「发布时必须上传 checksums.txt」，而 goreleaser 默认就生成它。
	if checksumURL == "" {
		s.failTask(t, "本次发布没有提供 %s，出于安全考虑拒绝在无校验的情况下安装。"+
			"请检查发布流水线是否上传了校验和文件", ChecksumAssetName)
		return false
	}
	s.setTask(t, "verifying", 70, "正在校验文件完整性")

	sums, err := client.FetchChecksums(ctx, checksumURL)
	if err != nil {
		s.failTask(t, "获取校验和失败: %v", err)
		return false
	}
	if err := verifyChecksum(archivePath, sums); err != nil {
		s.failTask(t, "%v", err)
		return false
	}

	s.setTask(t, "installing", 85, "正在解压并替换程序文件")
	newBinary := filepath.Join(tmpDir, BinaryName(runtime.GOOS))
	if err := ExtractBinary(archivePath, newBinary, maxBinarySize); err != nil {
		s.failTask(t, "%v", err)
		return false
	}
	if err := ReplaceSelf(exePath, newBinary); err != nil {
		s.failTask(t, "替换程序文件失败: %v", err)
		return false
	}
	return true
}

// setTask 更新任务进度（线程安全）。
func (s *Service) setTask(t *Task, phase string, percent int, message string) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	t.Phase = phase
	t.Percent = percent
	t.Message = message
	t.logs = append(t.logs, fmt.Sprintf("[%s] %s", phase, message))
	// 只留最近 100 行：给界面看的日志不该无限增长
	if len(t.logs) > 100 {
		t.logs = t.logs[len(t.logs)-100:]
	}
}

// finishTask 标记任务结束。
func (s *Service) finishTask(t *Task) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	now := time.Now().UTC()
	t.Ended = &now
	t.Done = true
	if t.Percent < 100 {
		t.Percent = 100
	}
}

// Progress 查询更新进度。
//
// docker 形态的进度在宿主侧，这里转发查询；binary 形态读本地任务。
// 两种形态对界面是同一个接口 —— 前端不该关心「更新是谁在跑」。
func (s *Service) Progress(ctx context.Context, id string) (*Task, error) {
	s.taskMu.Lock()
	task := s.task
	s.taskMu.Unlock()

	// 有本地任务且 ID 对得上（或没传 ID）：用本地的
	if task != nil && (id == "" || task.ID == id) {
		return task.Snapshot().Normalized(), nil
	}

	// 否则问宿主侧更新器
	if s.runner != nil {
		p, err := s.runner.Progress(ctx, id)
		if err == nil && p != nil {
			return (&Task{
				ID:      p.ID,
				Kind:    "update",
				Target:  "latest",
				Phase:   p.Phase,
				Percent: p.Percent,
				Message: p.Message,
				Done:    p.Done,
				Failed:  p.Failed,
				Logs:    p.Logs,
			}).Normalized(), nil
		}
	}

	if id == "" {
		// 没有任务时返回一个结构完整的空任务：前端拿到它就知道
		// 「当前没有在跑的东西」，而不是去处理 null
		return (&Task{Phase: "idle"}).Normalized(), nil
	}
	return nil, fmt.Errorf("找不到更新任务 %s", id)
}

// ---------------------------------------------------------------------------
// 回滚
// ---------------------------------------------------------------------------

// RollbackCandidates 列出可回滚到的版本（比当前版本旧的最近几个）。
func (s *Service) RollbackCandidates(ctx context.Context) ([]RollbackCandidate, error) {
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	if client == nil {
		return nil, errors.New("更新客户端不可用")
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	releases, err := client.FetchReleases(ctx, rollbackScanLimit)
	if err != nil {
		return nil, err
	}
	return filterRollbackCandidates(releases), nil
}

// filterRollbackCandidates 是回滚白名单规则本体。
//
// 抽成纯函数是因为这份规则有两个使用方：列表接口（展示）与
// 下载式回滚（校验目标合法）。两侧必须是同一条规则 —— 分开写的话，
// 「列表里看得到」与「允许回滚到」总有一天会给出不同答案。
func filterRollbackCandidates(releases []Release) []RollbackCandidate {
	seen := make(map[string]bool, len(releases))
	out := make([]RollbackCandidate, 0, maxRollbackVersions)
	for _, r := range releases {
		// 草稿与预发布不进入候选：回滚到一个 rc 版本比不回滚更糟
		if r.Draft || r.Prerelease {
			continue
		}
		v := strings.TrimPrefix(strings.TrimSpace(r.TagName), "v")
		if v == "" || seen[v] {
			continue
		}
		// 只列**严格早于**当前版本的。含当前版本会让列表里出现
		// 一个「回滚到自己」的选项，那没有任何意义
		if !version.Newer(version.Version, v) {
			continue
		}
		seen[v] = true
		out = append(out, RollbackCandidate{Version: v, PublishedAt: r.PublishedAt, HTMLURL: r.HTMLURL})
	}

	// 新的在前
	sort.SliceStable(out, func(i, j int) bool {
		return version.Newer(out[i].Version, out[j].Version)
	})
	if len(out) > maxRollbackVersions {
		out = out[:maxRollbackVersions]
	}
	return out
}

// Rollback 回滚。
//
// 两种形态：
//
//	version == ""  → 本地回滚：把 .backup 换回来。不需要网络，
//	                 所以它是**唯一在网络不通时还能用**的救援手段。
//	version != ""  → 下载指定版本并安装。目标必须出现在
//	                 RollbackCandidates 里 —— 否则用户可以指定任意
//	                 tag，包括那些根本没通过发布验证的。
func (s *Service) Rollback(ctx context.Context, targetVersion string) (*Task, error) {
	targetVersion = strings.TrimSpace(targetVersion)

	if version.BuildType == version.BuildDocker {
		return nil, fmt.Errorf("%w：容器部署请通过宿主侧更新器回滚（在界面选择版本后会自动走该通道）",
			ErrBuildTypeUnsupported)
	}
	if version.BuildType != version.BuildBinary {
		return nil, fmt.Errorf("%w：当前构建类型不支持在线回滚", ErrBuildTypeUnsupported)
	}

	exePath, err := SelfPath()
	if err != nil {
		return nil, err
	}

	// 本地回滚（不指定版本）。
	//
	// 这条路径**完全不联网**，因此不需要 context 超时控制 ——
	// 它只是两次 rename，毫秒级完成。这也正是它值得单独存在的原因：
	// 网络不通、GitHub 被墙、发布产物被删，这些情况下它依然可用，
	// 是唯一还能把服务救回来的手段。
	if targetVersion == "" {
		if !HasBackup(exePath) {
			return nil, ErrNoBackup
		}
		t := &Task{
			ID:      fmt.Sprintf("rbk-%d", time.Now().UnixNano()),
			Kind:    "rollback",
			Target:  "local-backup",
			Phase:   "installing",
			Percent: 50,
			Message: "正在恢复上一版本",
			Started: time.Now().UTC(),
		}
		if err := s.claimTask(t); err != nil {
			return nil, err
		}

		go func() {
			if err := RestoreBackup(exePath); err != nil {
				s.failTask(t, "回滚失败: %v", err)
			} else {
				s.setTask(t, "done", 100, "已恢复到上一版本，重启服务后生效")
			}
			s.finishTask(t)
		}()
		return t.Snapshot().Normalized(), nil
	}

	// 下载指定版本
	return s.startDownloadRollback(ctx, exePath, targetVersion)
}

// startDownloadRollback 下载并安装一个指定的历史版本。
func (s *Service) startDownloadRollback(ctx context.Context, exePath, targetVersion string) (*Task, error) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), updateTimeout)
	t := &Task{
		ID:      fmt.Sprintf("rbk-%d", time.Now().UnixNano()),
		Kind:    "rollback",
		Target:  targetVersion,
		Phase:   "checking",
		Percent: 5,
		Message: fmt.Sprintf("正在准备回滚到 v%s", targetVersion),
		Started: time.Now().UTC(),
		cancel:  cancel,
	}
	if err := s.claimTask(t); err != nil {
		cancel()
		return nil, err
	}

	go func() {
		defer cancel()
		s.runDownloadRollback(rctx, t, exePath, targetVersion)
	}()
	return t.Snapshot().Normalized(), nil
}

// runDownloadRollback 是「下载指定版本并安装」的实际流程。
func (s *Service) runDownloadRollback(ctx context.Context, t *Task, exePath, targetVersion string) {
	defer s.finishTask(t)

	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	if client == nil {
		s.failTask(t, "更新客户端不可用")
		return
	}

	s.setTask(t, "checking", 10, fmt.Sprintf("正在查找 v%s 的发布产物", targetVersion))
	// 只拉一次版本列表：白名单校验与目标定位用的是同一份数据，
	// 拉两次只是多烧一次本就稀缺的 GitHub 匿名限额
	releases, err := client.FetchReleases(ctx, rollbackScanLimit)
	if err != nil {
		s.failTask(t, "获取版本列表失败: %v", err)
		return
	}

	// 先确认目标在允许列表里。这一步是安全边界：没有它，
	// 调用方可以传任意 tag，把一个未经发布流程的产物装到服务器上
	target := strings.TrimPrefix(targetVersion, "v")
	allowed := false
	for _, c := range filterRollbackCandidates(releases) {
		if c.Version == target {
			allowed = true
			break
		}
	}
	if !allowed {
		s.failTask(t, "版本 %s 不在可回滚列表里（只允许回滚到比当前版本旧的、且非预发布的正式版本）", targetVersion)
		return
	}

	var rel *Release
	for i := range releases {
		if strings.TrimPrefix(releases[i].TagName, "v") == target {
			rel = &releases[i]
			break
		}
	}
	if rel == nil {
		s.failTask(t, "没有找到 v%s 的发布记录", target)
		return
	}

	// 注意：安装走的是与更新同一个 installRelease（因此同一个 ReplaceSelf），
	// 所以回滚同样会留下 .backup ——「回滚」这个动作本身也是可回滚的
	if !s.installRelease(ctx, client, rel, target, exePath, t, 30) {
		return
	}

	s.setTask(t, "done", 100, fmt.Sprintf("已回滚到 v%s，重启服务后生效", target))
}

// Cancel 取消当前任务（目前只在 binary 形态有意义）。
func (s *Service) Cancel() error {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if s.task == nil || s.task.Done {
		return errors.New("没有正在进行的任务")
	}
	if s.task.cancel != nil {
		s.task.cancel()
	}
	return nil
}

// LocalBackupAvailable 报告是否存在可本地回滚的备份。
//
// 只有 binary 形态才谈得上本地备份 —— 容器里没有「自己的可执行文件」
// 这个概念（它是镜像的一部分，替换了也没意义）。其它形态一律返回 false，
// 而不是去探测一个在该形态下根本没有含义的文件。
func (s *Service) LocalBackupAvailable() bool {
	if version.BuildType != version.BuildBinary {
		return false
	}
	exe, err := SelfPath()
	if err != nil {
		return false
	}
	return HasBackup(exe)
}
