package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	// 复用主程序的 semver 解析而不是再写一个子串比较器：
	// 两套判定会对同一对版本给出不同答案（见 versionMatches 的注释）
	"llm-relay/internal/version"
)

// 流程中的超时常量。拉镜像是最慢的一步（国内网络下可能几分钟），
// 后面每一步都必须有上限 —— 没有上限的等待会表现为「进度条卡在 60% 不动」，
// 而用户唯一能做的就是重启容器，那会让这次更新半途而废。
const (
	pullTimeout     = 15 * time.Minute
	recreateTimeout = 3 * time.Minute
	healthTimeout   = 2 * time.Minute
	healthInterval  = 2 * time.Second
	// buildTimeout 给「本地源码重新构建」这条路。它比 pull 慢得多：
	// 要装 npm 依赖（国内镜像源下第一次可能几分钟）再编译整个 Go 后端。
	// 20 分钟是实测的宽裕值，也给首次构建（无任何缓存）留了余量。
	buildTimeout = 20 * time.Minute
	// 回滚时的健康检查给更短的时间：回滚是救火动作，
	// 而它要退回去的那个镜像此前是能跑的，不需要那么长的启动余量
	rollbackHealthTimeout = 90 * time.Second
	// 源码归档的大小上限。本项目源码（不含 node_modules 与 .git）
	// 只有几 MB，200MB 给足余量又能在异常响应时保护磁盘
	maxSourceArchiveSize = 200 << 20
)

// logf 往当前任务的日志里追加一行（同时写进程日志）。
//
// 任务日志是给管理台看的：更新失败时它是唯一能解释「为什么」的东西，
// 而进程日志在宿主上、用户未必会去看。
func (r *runner) logf(u *upgrade, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.mu.Lock()
	u.Logs = append(u.Logs, fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), msg))
	// 只留最近 200 行：docker pull 的输出可能很长，
	// 而回传给管理台的响应不该有几百 KB
	if len(u.Logs) > 200 {
		u.Logs = u.Logs[len(u.Logs)-200:]
	}
	r.mu.Unlock()
	r.logger.Info(msg, "task", u.ID, "kind", u.Kind)
}

// setPhase 更新任务阶段。
func (r *runner) setPhase(u *upgrade, phase string, percent int, message string) {
	r.mu.Lock()
	u.Phase = phase
	u.Percent = percent
	u.Message = message
	r.mu.Unlock()
	r.logf(u, "%s", message)
}

// finish 标记任务结束并把快照存入历史。
func (r *runner) finish(u *upgrade, failed bool) {
	r.mu.Lock()
	u.Done = true
	u.Failed = failed
	if failed {
		u.Phase = phaseFailed
	}
	u.Percent = 100
	// 存副本而不是指针：历史是只读的，副本让「历史」与「当前任务」
	// 在类型上就是两个东西，避免将来有人往历史里写时意外改到活动任务。
	// 用 snapshotUpgrade 还顺带把 Logs 从 nil 归一成空切片 ——
	// nil 会被序列化成 JSON 的 null，而管理台期望一个数组。
	snap := snapshotUpgrade(u)
	r.history = append(r.history, &snap)
	if len(r.history) > 10 {
		r.history = r.history[len(r.history)-10:]
	}
	r.mu.Unlock()
}

// ---------------------------------------------------------------------------
// HTTP 处理
// ---------------------------------------------------------------------------

func (r *runner) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"image":  r.image,
		"pid":    os.Getpid(),
	})
}

// handleProgress 返回任务进度。
//
// 不带 id 时返回「最近一次任务」——这解决的是刷新页面后手上没有 ID
// 的问题：用户 F5 之后仍想看到刚才那次更新的结果。
func (r *runner) handleProgress(w http.ResponseWriter, req *http.Request) {
	id := strings.TrimSpace(req.URL.Query().Get("id"))

	r.mu.Lock()
	defer r.mu.Unlock()

	if id == "" {
		// 优先给正在跑的；没有就给最近一次结束的
		if r.current != nil {
			writeJSON(w, http.StatusOK, snapshotUpgrade(r.current))
			return
		}
		if len(r.history) > 0 {
			writeJSON(w, http.StatusOK, r.history[len(r.history)-1])
			return
		}
		writeJSON(w, http.StatusOK, &upgrade{
			Phase: phaseIdle, Message: "还没有执行过更新", Logs: []string{},
		})
		return
	}

	if r.current != nil && r.current.ID == id {
		writeJSON(w, http.StatusOK, snapshotUpgrade(r.current))
		return
	}
	for i := len(r.history) - 1; i >= 0; i-- {
		if r.history[i].ID == id {
			writeJSON(w, http.StatusOK, r.history[i])
			return
		}
	}
	writeError(w, http.StatusNotFound, fmt.Sprintf("找不到任务 %s（更新器可能重启过，内存中的记录已丢失）", id))
}

// snapshotUpgrade 返回任务的副本，并把 Logs 归一成非 nil。
//
// 必须复制而不是把 *upgrade 直接交给 json.Encoder：任务在后台 goroutine
// 里持续被 r.setPhase / r.logf 改写，而编码发生在 HTTP goroutine 上 ——
// 两个 goroutine 同时读写同一个 struct（尤其是那个 slice），
// 就是一次数据竞争（-race 会报，线上表现为偶发的日志串行）。
//
// Logs 归一成空切片而不是 nil：nil 序列化成 JSON 的 `null`，
// 而管理台期望一个数组（`task.logs.map` 会在 null 上抛错）。
func snapshotUpgrade(u *upgrade) upgrade {
	cp := *u
	cp.Logs = append([]string{}, u.Logs...)
	return cp
}

// handleUpgrade 启动一次升级。
func (r *runner) handleUpgrade(w http.ResponseWriter, req *http.Request) {
	r.startTask(w, "upgrade")
}

// handleRollback 回滚到本次会话里的上一个镜像。
func (r *runner) handleRollback(w http.ResponseWriter, req *http.Request) {
	r.startTask(w, "rollback")
}

// startTask 是所有写操作的统一入口：并发保护 + 后台执行。
//
// 并发保护是这里最重要的正确性保证：两次 docker compose up 同时跑
// 会互相拆台（一个在重建、另一个也在重建同一个容器），
// 结果可能是两个都失败，或者容器停在一个谁也没预期的状态。
func (r *runner) startTask(w http.ResponseWriter, kind string) {
	r.mu.Lock()
	if r.current != nil && !r.current.Done {
		busy := r.current
		r.mu.Unlock()
		writeError(w, http.StatusConflict,
			fmt.Sprintf("已有任务正在进行（%s，%s），请等它结束", busy.Phase, busy.Message))
		return
	}

	u := &upgrade{
		ID:      fmt.Sprintf("%s-%d", kind, time.Now().UnixNano()),
		Kind:    kind,
		Phase:   phaseIdle,
		Message: "已接受请求",
		Started: time.Now(),
		Logs:    []string{},
	}
	r.current = u
	r.mu.Unlock()

	// 后台执行：拉镜像可能几分钟，同步 HTTP 一定会被某一层掐断
	// （管理台那边还有自己的超时）。立即返回任务 ID，进度靠轮询。
	go func() {
		if kind == "rollback" {
			r.runRollback(u)
		} else {
			r.runUpgrade(u)
		}
	}()

	writeJSON(w, http.StatusOK, map[string]any{"id": u.ID, "kind": kind})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// ---------------------------------------------------------------------------
// 升级流程
// ---------------------------------------------------------------------------

// runUpgrade 执行一次完整升级：记录当前镜像 → 拉新镜像 → 重建 → 健康检查
// → 失败则自动回滚。
//
// 「失败则自动回滚」是这套流程里最重要的设计：一次坏的更新如果留在原地，
// 用户面对的是一个起不来的服务，而修复它需要登上服务器 ——
// 那正是这个功能想要避免的场景。自动回滚把「更新失败」的后果收敛成
// 「服务短暂中断后又回到了能用的状态」。
func (r *runner) runUpgrade(u *upgrade) {
	ctx := context.Background()

	// ---- 1. 记下当前镜像，并**给它打一个稳定的回滚标签** ----
	//
	// 这两件事必须一起做，而且必须在任何可能改动镜像的动作之前做。
	//
	// 为什么不满足于记下 content ID：实测（Docker 29 + containerd
	// snapshotter）发现，`docker compose build` 把 tag 指向新镜像之后，
	// 旧镜像会被**回收** —— 此后 `docker image inspect <那个 ID>` 直接
	// 失败，`RELAY_IMAGE=<旧 ID> docker compose up` 报 "No such image"。
	// 容器自己因为持有快照仍能继续跑，所以服务看着没事，但**回滚的依据
	// 已经没了**：更新失败时那句「正在自动回滚」注定失败。
	//
	// 打上标签就解决了：标签是对镜像的一次引用，有引用的镜像不会被回收，
	// 而且它是一个**稳定的名字**，不受后续 tag 重新指向的影响。
	// 用一个固定的标签名（而不是带时间戳的）是有意的 —— 每次更新覆盖它，
	// 于是它始终精确地表示「上一个版本」，也不会无限堆积镜像。
	r.setPhase(u, phasePull, 0, "正在记录当前镜像，作为回滚依据")
	previous, err := r.currentImageID(ctx)
	if err != nil {
		r.logf(u, "读取当前镜像 ID 失败：%v（将无法自动回滚）", err)
	}
	// curImageID 传给 build 策略读镜像里的版本号 —— 那一步发生在
	// 容器被重建之前，镜像没变过，不必再查一次
	curImageID := previous
	if previous != "" {
		r.logf(u, "当前镜像：%s", previous)
		if err := r.tagRollback(ctx, previous); err != nil {
			// 打标签失败不中止更新，但要如实说清后果 ——
			// 用户需要在「更新可能失败且无法回滚」与「放弃更新」之间自己决定
			r.logf(u, "警告：无法创建回滚标签 %s：%v（本次更新若失败将无法自动回滚）",
				r.rollbackTag, err)
			previous = ""
		} else {
			r.logf(u, "已创建回滚标签 %s，失败时可回退到它", r.rollbackTag)
			// 后续一律用这个标签作为回滚目标，而不是那个会被回收的 ID
			previous = r.rollbackTag
		}
	}
	r.mu.Lock()
	u.previousImage = previous
	r.mu.Unlock()

	// ---- 2. 取得新版本 ----
	//
	// 这一步是两种策略唯一分岔的地方，之后的「重建 + 健康检查 + 失败回滚」
	// 完全共用 —— 那部分才是容易出错的部分，不该抄两遍。
	//
	// 它同时回报「目标版本号」（build 策略才知道，image 策略为空），
	// 供第 5 步确认更新真的生效。
	var targetVersion string
	if r.strategy == "build" {
		var ok bool
		targetVersion, ok = r.runSourceBuild(u, previous, curImageID)
		if !ok {
			return
		}
	} else {
		if !r.runImagePull(u, previous) {
			return
		}
	}

	// ---- 3. 重建容器 ----
	r.setPhase(u, phaseRecreate, 45, "正在用新镜像重建容器")
	rcCtx, cancelRC := context.WithTimeout(ctx, recreateTimeout)
	defer cancelRC()
	if out, err := r.composeUp(rcCtx); err != nil {
		r.logf(u, "重建输出：%s", tail(out, 30))
		r.autoRollback(u, previous, "重建容器失败："+err.Error())
		return
	}

	// ---- 4. 健康检查 ----
	r.setPhase(u, phaseWait, 70, "正在等待服务就绪")
	if err := r.waitHealthy(ctx, healthTimeout); err != nil {
		r.logf(u, "健康检查失败：%v", err)
		r.autoRollback(u, previous, "健康检查未通过")
		return
	}

	// ---- 5. 确认版本真的换了 ----
	//
	// 这一步是「更新成功」这句话的证据。不加它，一个把版本号编错、
	// 或者根本没换成新镜像的更新，也会一路走到「更新完成」并显示
	// 那个纹丝不动的版本号 —— 用户唯一的感受是「点了没用」，
	// 而日志里全是成功。
	//
	// 判据同样是**离线读镜像标签**（见 runningImageVersion）。
	// 这里刻意不把「读不出标签」当成失败：老镜像（本次改动之前构建的）
	// 根本没有那个标签，把「问不出来」当成「更新失败」会让第一次
	// 升级到新版本的人被无故回滚。问不出来时只记一行日志。
	if r.strategy == "build" && targetVersion != "" {
		r.setPhase(u, phaseWait, 90, "正在确认新版本已生效")
		got := r.runningImageVersion(ctx)
		switch {
		case versionMatches(got, targetVersion):
			r.logf(u, "已确认版本生效：%s", got)
		case got == "":
			r.logf(u, "无法从镜像读出运行版本（该镜像可能构建于本功能之前），跳过版本确认")
		default:
			r.logf(u, "运行中的版本是 %q，不是目标 %q", got, targetVersion)
			r.autoRollback(u, previous,
				fmt.Sprintf("更新后运行的仍是 %s，而不是目标的 %s（新镜像可能没有正确注入版本号）",
					got, targetVersion))
			return
		}
	}

	r.setPhase(u, phaseDone, 100, "更新完成，服务已用新镜像启动并通过健康检查")
	r.finish(u, false)
}

// runSourceBuild 走「本地源码重新构建」这条路。
//
// 返回 (目标版本号, 是否继续往下走)。第二个返回值为 false 表示升级已经在
// 内部处理完（成功但无需重建，或失败），调用方应当直接返回而不要继续
// 往下走重建流程。
//
// 第一个返回值只在「确实构建了新镜像」时有意义 —— 它会被用来在重建后
// 确认新版本真的生效了。走「已是最新、无需重建」那条提前返回时给空串，
// 因为那时没有需要验证的新版本。
//
// # 为什么不用 git pull
//
// 安装目录里**没有 .git** —— install.sh 把源码 tar 过去时显式排除了它
// （见该脚本第 4 步的 --exclude='./.git'）。理由很实在：源码目录是要被
// docker build 的，带上一整个 .git 只会让构建上下文变大、缓存更容易失效。
//
// 所以这里的做法是：从 GitHub 下载目标版本的**源码归档**
// （官方自动生成的 tarball，形如 codeload.github.com/.../tar.gz/refs/tags/v1.2.3），
// 解出来覆盖到安装目录，然后重新构建镜像。
//
// 这个选择还有一个额外好处：tarball 是**不可变**的 —— 它对应一个确切的
// tag，内容由 GitHub 生成。而 git pull 拿到的是分支的最新提交，
// 「我更新到了哪一版」这个问题的答案会随时间变化。
func (r *runner) runSourceBuild(u *upgrade, previous, curImageID string) (string, bool) {
	ctx := context.Background()

	// ---- 先问 tag，再决定要不要下载 ----
	//
	// 顺序不能反：查 tag 是几十毫秒的 API 请求，而源码归档是几 MB 的
	// 下载 + 解压。「已是最新」是这条路的常态结果（用户多点几次更新），
	// 先比 tag 就能把常态路径的整段下载完全省掉。
	r.setPhase(u, phasePull, 10, "正在获取最新发布的源码")
	tag, err := r.latestTag(ctx, u)
	if err != nil {
		r.setPhase(u, phaseFailed, 100, err.Error())
		r.finish(u, true)
		return "", false
	}
	ver := strings.TrimPrefix(tag, "v")

	// 已经是最新版本就不必重建。判据是镜像里编进去的版本号，
	// 见 runningImageVersion 的注释（它是离线读的，不依赖服务在跑）。
	// 镜像 ID 复用第 1 步拿到的 —— 中间只发生过打标签，容器没变。
	current := r.imageVersionOf(ctx, curImageID)
	if versionMatches(current, ver) {
		r.setPhase(u, phaseDone, 100, fmt.Sprintf("已是该发布的最新版本（v%s），无需重新构建", ver))
		r.finish(u, false)
		return "", false
	}
	r.logf(u, "将更新到 v%s（当前 %s）", ver, orUnknown(current))

	srcDir, err := r.fetchSource(ctx, u, tag)
	if err != nil {
		r.setPhase(u, phaseFailed, 100, err.Error())
		r.finish(u, true)
		return "", false
	}
	// 解压到临时目录，成功之后再整体替换 —— 中途失败不会留下一个
	// 半新半旧的源码树（那会让下一次构建产出无法解释的东西）
	defer func() { _ = os.RemoveAll(srcDir) }()

	// 覆盖源码。只替换会被构建用到的目录，保留 deploy/.env、deploy/data、
	// backups —— 那些是运行期数据，覆盖掉等于把服务和数据库弄丢。
	r.setPhase(u, phasePull, 15, "正在更新源码文件")
	if err := r.syncSource(srcDir); err != nil {
		r.autoRollback(u, previous, "更新源码文件失败："+err.Error())
		return "", false
	}

	// 重新构建镜像。这一步很慢（要装 npm 依赖、编译 Go），
	// 所以进度给 15→40 这一段。
	//
	// 必须把**目标版本号显式传进去**，见 composeBuild 的注释。
	r.setPhase(u, phasePull, 20, "正在重新构建镜像（首次构建可能要几分钟）")
	buildCtx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	if out, err := r.composeBuild(buildCtx, ver); err != nil {
		r.logf(u, "构建输出：%s", tail(out, 40))
		r.autoRollback(u, previous, "镜像构建失败："+err.Error())
		return "", false
	}
	r.setPhase(u, phasePull, 40, "镜像构建完成")
	return ver, true
}

// repoOwner 与 repoName 从 -repo 参数解析（形如 owner/name）。
func (r *runner) repoParts() (string, string, error) {
	parts := strings.SplitN(strings.TrimSpace(r.repo), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("-repo 必须形如 owner/name，当前: %q", r.repo)
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}

// latestTag 问 GitHub API 最新发布的 tag（含 v 前缀）。
//
// 单独成函数而不是并进 fetchSource：调用方要先拿 tag 对比「是否已是
// 最新」，相同就不必下载归档 —— 查询与下载是两个量级的动作。
func (r *runner) latestTag(ctx context.Context, u *upgrade) (string, error) {
	owner, name, err := r.repoParts()
	if err != nil {
		return "", err
	}

	// 不用 "releases/latest" 的重定向是因为我们同时还需要 tag 名
	// （归档 URL 里要用它）。
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, name)
	body, err := r.httpGet(ctx, apiURL, "application/vnd.github+json")
	if err != nil {
		// 404 是首次使用最常见的情况，值得单独给一句能照着做的提示。
		// 原始报错（"HTTP 404"）对用户毫无指导性 —— 它既没说清是哪个
		// 地址，也没说下一步该干什么。
		if errors.Is(err, errHTTPNotFound) {
			return "", fmt.Errorf(
				"仓库 %s/%s 还没有发布任何版本，因此没有可更新的目标。"+
					"请在 GitHub 上打一个 tag（如 v0.1.2）触发发布，或确认「发布源仓库」设置是否正确",
				owner, name)
		}
		return "", fmt.Errorf("获取最新版本信息失败: %w", err)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rel); err != nil || rel.TagName == "" {
		return "", fmt.Errorf("解析发布信息失败（可能该仓库还没有发布任何版本）")
	}
	r.logf(u, "最新发布：%s", rel.TagName)
	return rel.TagName, nil
}

// fetchSource 下载并解开指定 tag 的源码归档，返回解压后的临时目录。
// 清理责任在调用方（defer os.RemoveAll）。
func (r *runner) fetchSource(ctx context.Context, u *upgrade, tag string) (string, error) {
	owner, name, err := r.repoParts()
	if err != nil {
		return "", err
	}

	tmp, err := os.MkdirTemp("", "llm-relay-src-*")
	if err != nil {
		return "", fmt.Errorf("创建临时目录失败: %w", err)
	}

	// 源码归档。codeload 是 GitHub 官方的归档服务，
	// 用 tag 而不是分支名，内容才能与版本号严格对应。
	archiveURL := fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/refs/tags/%s",
		owner, name, tag)
	archivePath := filepath.Join(tmp, "src.tar.gz")

	r.setPhase(u, phasePull, 12, fmt.Sprintf("正在下载 v%s 的源码", strings.TrimPrefix(tag, "v")))
	if err := r.httpDownload(ctx, archiveURL, archivePath, maxSourceArchiveSize); err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("下载源码失败: %w", err)
	}

	extractDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	// GitHub 的源码 tarball 会在最外层套一个 <repo>-<tag>/ 目录，
	// 用 --strip-components=1 剥掉它，让内容直接落在 extractDir 下
	if out, err := r.system(ctx, "tar", "-xzf", archivePath, "-C", extractDir, "--strip-components=1"); err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("解开源码归档失败: %v（%s）", err, tail(out, 5))
	}
	_ = os.Remove(archivePath)

	// 确认解出来的东西确实是一份源码：少了这个检查，
	// 一个空的或结构不对的归档会被「成功」同步过去，
	// 然后 docker build 在某个晦涩的地方失败
	if _, err := os.Stat(filepath.Join(extractDir, "backend", "go.mod")); err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("下载到的源码缺少 backend/go.mod，归档结构可能变了")
	}
	return extractDir, nil
}

// syncSource 把新源码覆盖到安装目录。
//
// # 只替换该替换的
//
// 下面这份清单是「构建需要什么」，清单之外的一切都保持不动。
// 尤其不能碰的：
//
//	deploy/.env     数据库密码、加密主密钥、管理密钥 —— 覆盖掉服务就废了
//	deploy/data     postgres 的数据卷
//	backups         备份目录
//
// # 为什么先删后拷
//
// 新版本可能删掉了某个文件，而「只覆盖不删除」会把它留在原地。
// 对 Go 来说这通常表现为「编译报重复声明」—— 一个与真正原因
// 毫无关联的错误信息。所以先 rm -rf 再整目录拷进去。
func (r *runner) syncSource(srcDir string) error {
	// 会被构建用到、因而需要更新的顶层目录/文件
	items := []string{
		"backend", "frontend", "deploy/Dockerfile",
		"deploy/docker-compose.yml", "deploy/.env.example",
		"deploy/install.sh", "deploy/install-bare.sh",
		"deploy/llm-relay-updater.service", "deploy/Caddyfile",
		"Makefile", ".goreleaser.yaml",
		"backend/scripts", "README.md", "LICENSE",
	}

	for _, item := range items {
		src := filepath.Join(srcDir, item)
		if _, err := os.Stat(src); err != nil {
			// 新版本里没有这个文件是正常的（比如某个部署文件被合并了），
			// 跳过而不是报错
			continue
		}
		dst := filepath.Join(r.repoDir, item)

		if info, err := os.Stat(src); err == nil && info.IsDir() {
			if err := os.RemoveAll(dst); err != nil {
				return fmt.Errorf("清理旧目录 %s 失败: %w", item, err)
			}
			if err := copyTree(src, dst); err != nil {
				return fmt.Errorf("复制 %s 失败: %w", item, err)
			}
			continue
		}
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("复制 %s 失败: %w", item, err)
		}
	}
	return nil
}

// imageVersion 读出某个镜像里编入的版本号（来自镜像标签）。
//
// # 为什么读镜像而不是问服务
//
// 最初的实现是 GET http://127.0.0.1:8888/api/admin/system/version ——
// 两个问题，第二个是致命的：
//
//  1. 那个接口**需要登录**。更新器没有会话，实测返回 401。
//     也就是说这个判断从来没生效过（「已是最新，无需重建」那条捷径
//     永远走不到，每次都白重建一遍）。
//  2. 更要紧的是：它靠 HTTP 问一个**可能正躺着**的服务。而更新器最需要
//     知道「现在是哪个版本」的时刻，恰恰是服务起不来的时候 ——
//     那时 HTTP 必然不通，判断必然失败。
//
// 改成读镜像标签（见 deploy/Dockerfile 的 org.opencontainers.image.version）：
// 完全离线、不依赖服务是否健康、也不需要凭据。只要镜像还在本地就能问。
//
// 拿不到标签时返回空串 + nil（表示「问不出来」），而不是 false：
// 调用方需要区分「确认不是目标版本」与「无法判断」——
// 后者不该触发回滚，那会把一次成功的更新误判成失败。
func (r *runner) imageVersion(ctx context.Context, imageRef string) (string, error) {
	out, err := r.docker(ctx, "image", "inspect", "-f",
		`{{index .Config.Labels "org.opencontainers.image.version"}}`, imageRef)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// runningImageVersion 取当前运行容器所用镜像里编入的版本号。
func (r *runner) runningImageVersion(ctx context.Context) string {
	imageID, err := r.currentImageID(ctx)
	if err != nil || imageID == "" {
		return ""
	}
	return r.imageVersionOf(ctx, imageID)
}

// imageVersionOf 读镜像版本，任何失败（含空引用）都返回空串。
// 调用方需要区分「确认不是目标版本」与「无法判断」，空串表示后者。
func (r *runner) imageVersionOf(ctx context.Context, imageRef string) string {
	if imageRef == "" {
		return ""
	}
	v, err := r.imageVersion(ctx, imageRef)
	if err != nil {
		return ""
	}
	return v
}

// versionMatches 判断某个版本号字符串是否就是目标版本。
//
// 判据是 version.Parse 后的主三段 + 预发布标识相等：构建时注入的
// 版本号可能带 git describe 尾巴（v1.2.3-7-g3f9a1c-dirty），与发布
// tag 的 v1.2.3 不会字面相等，但指的是同一版；尾巴不参与判等。
//
// 不用 strings.Contains：子串匹配会把 1.2.30 当成 1.2.3（前缀），
// 也会把 0.1.1-rc1 当成 0.1.1 —— 前者把失败报告成成功，后者在
// rc 还没升到正式版时拒绝重建。semver 包就在同一个 module 里，
// 它就是为这些形状写的（见其包注释）。
func versionMatches(current, target string) bool {
	if current == "" || target == "" {
		return false
	}
	a, b := version.Parse(current), version.Parse(target)
	return a.Major == b.Major && a.Minor == b.Minor && a.Patch == b.Patch && a.Pre == b.Pre
}

// orUnknown 给日志用：空串显示成「未知」而不是一片空白。
func orUnknown(s string) string {
	if s == "" {
		return "未知"
	}
	return s
}

// runImagePull 走「拉取远端镜像」这条路。
func (r *runner) runImagePull(u *upgrade, previous string) bool {
	ctx := context.Background()

	r.setPhase(u, phasePull, 10, "正在拉取最新镜像（国内网络下可能需要几分钟）")
	pullCtx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()
	out, err := r.docker(pullCtx, "pull", r.image)
	if err != nil {
		r.logf(u, "拉取输出：%s", tail(out, 20))
		// 拉取失败不影响正在跑的服务（旧镜像还在），所以不触发回滚 ——
		// 回滚会把一个本来好好的服务重建一遍，纯属自找麻烦
		r.setPhase(u, phaseFailed, 100, "拉取镜像失败："+err.Error())
		r.finish(u, true)
		return false
	}
	r.logf(u, "拉取输出：%s", tail(out, 10))

	// 拉完之后立刻确认镜像真的变了。
	//
	// 这一步不是多余的：docker pull 在没有新版本时会成功返回但什么都不做，
	// 于是「重建容器」其实是在重建一个一模一样的容器 —— 用户会看到
	// 「更新成功、重启完成」，而版本号纹丝不动。
	// 提前把这种情况识别出来，就能给出一句明确的「已是最新，无需更新」。
	newID, err := r.imageIDOf(ctx, r.image)
	if err == nil && previous != "" && newID == previous {
		r.setPhase(u, phaseDone, 100, "镜像已是最新版本，无需重建容器")
		r.finish(u, false)
		return false
	}
	return true
}

// composeBuild 在源码目录里重新构建镜像，并把**目标版本号**编进去。
//
// # 为什么必须显式传 VERSION
//
// compose 文件里 app.build.args 写的是 `VERSION: ${VERSION:-dev}`，
// 插值来源是安装目录下的 .env —— 而那个文件是 install.sh 写的，
// 更新器刻意不动它（deploy/.env 里还有数据库密码与加密主密钥，
// 见 syncSource 的注释）。于是默认路径下会是这样：
//
//	构建出的镜像里编的还是**更新前**的版本号
//	→ 界面显示的版本号纹丝不动
//	→ imageVersion 读出的永远是旧版本，versionMatches 永远不匹配
//	→ 用户每次点「更新」都会真的重建一遍（几分钟），却看不出任何变化
//
// 这条路径在仓库还没有任何 Release 之前测不到（更新在第一步就失败了），
// 所以它是一个「只有发布之后才会暴露」的缺陷 —— 而那时用户已经
// 在真实使用它了。
//
// 解法是用环境变量覆盖插值（compose 的 env 优先级：进程环境 > .env 文件），
// 而不是去改 .env —— 那个文件不该被一个后台服务改写。
func (r *runner) composeBuild(ctx context.Context, version string) (string, error) {
	return r.run(ctx, r.buildEnv(version), "docker",
		"compose", "-f", r.composeFile, "build", r.service)
}

// buildEnv 给出构建时要覆盖的环境变量。
//
// 只覆盖 VERSION 一个，这是**实测确认过的最小集合**：
//
//   - COMMIT 不需要覆盖。compose 文件没把它声明成 build arg，
//     传了也会被 compose 丢掉（静默无效）；而 Dockerfile 自己的
//     默认值就是 "docker"，与 install.sh 部署出来的完全一致。
//   - DATE 也不需要。Dockerfile 在 DATE 为空时会取构建时刻
//     （`${DATE:-$(date -u …)}`），所以只要真的重新构建了，
//     时间戳自然是新的 —— 反倒不该由更新器从外面塞一个时间进去。
//
// 检查过这一点是因为「传了一个被忽略的参数」会让人以为它生效了，
// 下次改这行的人就会基于一个错误的假设做决定。
func (r *runner) buildEnv(version string) []string {
	if version == "" {
		return nil
	}
	return []string{"VERSION=" + version}
}

// run 执行一条命令并统一做错误归一化：超时说超时，有输出取最后
// 一行非空输出做摘要（「no space left on device」比「exit status 1」
// 有用得多），没输出就带回原始错误。
//
// 参数以切片传入、用 exec.CommandContext 直接执行，**不经过 shell** ——
// 这个进程以 root 运行，任何一处字符串拼接进 sh -c 都是一个提权面。
// 目前所有参数都来自启动时的 flag，但把「不拼 shell」定成硬规矩，
// 将来加参数时才不会有人顺手破例。
//
// extraEnv 以 KEY=VALUE 的独立元素追加（而不是拼进一个字符串再由
// shell 展开），所以值里出现空格、引号都不会引起解析问题 ——
// 版本号来自 GitHub 的 tag 名，虽然当前形状可控，
// 但「值恰好安全」是会随时间失效的性质。
func (r *runner) run(ctx context.Context, extraEnv []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if ctx.Err() != nil {
			return text, fmt.Errorf("命令超时（%v）", ctx.Err())
		}
		if text != "" {
			return text, fmt.Errorf("%s", lastLine(text))
		}
		return text, err
	}
	return text, nil
}

// system 执行一条普通的系统命令（tar 等）。同样不经 shell，理由见 run。
func (r *runner) system(ctx context.Context, name string, args ...string) (string, error) {
	return r.run(ctx, nil, name, args...)
}

// rollbackTag 是回滚标签的默认后缀形式，仅在没有 -image 可推导时使用。
//
// 正常情况下标签名由 `-image` 推导（见 deriveRollbackTag），
// 这样一个镜像名就对应一个回滚标签，同机多份部署不会互相覆盖。
const rollbackTag = "llm-relay:rollback"

// deriveRollbackTag 由镜像名推导回滚标签名。
//
// 从 -image 推导而不是写死 llm-relay:rollback：同一台机器上跑两份
// llm-relay（比如 staging 与 prod）时，写死的标签会互相覆盖，
// 于是一次回滚会把另一个部署的镜像换上去 —— 那是很难排查的故障。
//
// 推导规则是「把 tag 部分换成 rollback」：
//
//	llm-relay:local                      → llm-relay:rollback
//	ghcr.io/ashu1800/llm-relay:latest    → ghcr.io/ashu1800/llm-relay:rollback
//	llm-relay                            → llm-relay:rollback
//
// 镜像名里带端口（registry:5000/foo）时不能简单按最后一个冒号切，
// 所以用「最后一个 / 之后是否还有冒号」来判断 tag 从哪里开始。
func deriveRollbackTag(explicit, image string) string {
	if explicit != "" {
		return explicit
	}
	if image == "" {
		return rollbackTag
	}
	// tag 位于最后一个 '/' 之后的第一个 ':' 处
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon > slash {
		return image[:colon] + ":rollback"
	}
	return image + ":rollback"
}

// rollbackTagName 已并入 r.rollbackTag：main 构造 runner 时必然经由
// deriveRollbackTag 赋值且永不返回空，不存在需要兜底推导的路径。

// tagRollback 把当前镜像标记成回滚目标。
//
// 这一步的意义不只是「记下它是谁」，更是**保住它不被回收** ——
// 详见 runUpgrade 里第 1 步的注释。
func (r *runner) tagRollback(ctx context.Context, image string) error {
	_, err := r.docker(ctx, "tag", image, r.rollbackTag)
	return err
}

// rollbackTargetExists 判断回滚标签当前是否可用。
func (r *runner) rollbackTargetExists(ctx context.Context) bool {
	_, err := r.imageVersion(ctx, r.rollbackTag)
	return err == nil
}

// errHTTPNotFound 表示目标不存在（HTTP 404）。
//
// 单独定义它而不是让调用方比对字符串：调用方需要据此给出**不同的建议**
// （「仓库还没发布过」vs「网络出问题了」），而字符串比对会在某次
// 改措辞时静默失效 —— 那正是最需要它继续工作的时候。
var errHTTPNotFound = errors.New("HTTP 404")

// httpGet 发一次 GET 并返回响应体（限 4MB）。
func (r *runner) httpGet(ctx context.Context, url, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "llm-relay-updater")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errHTTPNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// httpDownload 下载文件到 path，带大小上限。
//
// 上限是必须的：这个地址来自 GitHub API 的响应，虽然可信，
// 但一个无界的写入就能把服务器的磁盘填满 —— 而磁盘满之后
// 最先崩的是数据库。
func (r *runner) httpDownload(ctx context.Context, url, path string, maxSize int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "llm-relay-updater")

	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxSize {
		return fmt.Errorf("文件过大：%d 字节（上限 %d）", resp.ContentLength, maxSize)
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	// 多读 1 字节以便区分「正好等于上限」与「被截断」
	written, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxSize+1))
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(path)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return closeErr
	}
	if written > maxSize {
		_ = os.Remove(path)
		return fmt.Errorf("下载超过上限 %d 字节", maxSize)
	}
	if written == 0 {
		_ = os.Remove(path)
		return fmt.Errorf("下载到的文件是空的")
	}
	return nil
}

// copyTree 递归复制目录。
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		// 符号链接按链接本身复制：源码归档里的链接都是正常的
		// （例如 deploy 下指向别处的脚本），跟随它反而会把内容重复一份
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		}
		return copyFile(path, target)
	})
}

// copyFile 复制单个文件并保留权限位。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// runRollback 回滚到本次会话记录的上一个镜像。
func (r *runner) runRollback(u *upgrade) {
	ctx := context.Background()

	// 回滚目标按可靠性排序取（见 findRollbackTarget）：回滚标签最可靠，
	// 因为它不依赖更新器进程的内存，也不受镜像回收影响。
	// 都拿不到就只能明确拒绝 ——「猜一个镜像名去回滚」比不回滚更危险。
	target := r.findRollbackTarget(ctx)
	if target == "" {
		r.setPhase(u, phaseFailed, 100,
			"没有可回滚的目标：还没有执行过一次更新，因此不存在上一个版本的镜像。"+
				"（回滚依据由每次更新在动手前创建的 "+r.rollbackTag+" 标签提供）")
		r.finish(u, true)
		return
	}

	r.mu.Lock()
	u.previousImage = target
	r.mu.Unlock()
	r.setPhase(u, phasePull, 10, fmt.Sprintf("正在准备回滚到镜像 %s", target))

	// 目标镜像可能已被本地清理（docker image prune），先确认它在不在
	if _, err := r.imageIDOf(ctx, target); err != nil {
		r.setPhase(u, phasePull, 20, "本地没有该镜像，正在拉取")
		pullCtx, cancel := context.WithTimeout(ctx, pullTimeout)
		defer cancel()
		if out, err := r.docker(pullCtx, "pull", target); err != nil {
			r.logf(u, "拉取输出：%s", tail(out, 20))
			r.setPhase(u, phaseFailed, 100, "拉取回滚镜像失败："+err.Error())
			r.finish(u, true)
			return
		}
	}

	// 用目标镜像重建容器。这里通过临时覆盖 compose 的 image 字段实现 ——
	// 见 updateImageAndRecreate 的说明
	r.setPhase(u, phaseRecreate, 50, "正在用目标镜像重建容器")
	rcCtx, cancelRC := context.WithTimeout(ctx, recreateTimeout)
	defer cancelRC()
	if out, err := r.updateImageAndRecreate(rcCtx, target); err != nil {
		r.logf(u, "重建输出：%s", tail(out, 30))
		r.setPhase(u, phaseFailed, 100, "回滚时重建容器失败："+err.Error())
		r.finish(u, true)
		return
	}

	r.setPhase(u, phaseWait, 75, "正在等待服务就绪")
	if err := r.waitHealthy(ctx, rollbackHealthTimeout); err != nil {
		r.setPhase(u, phaseFailed, 100, "回滚后健康检查仍未通过："+err.Error())
		r.finish(u, true)
		return
	}

	r.setPhase(u, phaseDone, 100, "已回滚，服务已通过健康检查")
	r.finish(u, false)
}

// autoRollback 在升级失败后尝试把服务恢复到上一个镜像。
//
// 它是「尽力而为」：任何一个环节失败都只记录，不改变「升级已经失败」
// 这个结论。把回滚失败也包装成成功会掩盖一个更糟的状态
// （服务既不在新版也不在旧版），而那种状态必须让人知道。
func (r *runner) autoRollback(u *upgrade, previous, reason string) {
	if previous == "" {
		r.setPhase(u, phaseFailed, 100, reason+"，且没有可回滚的镜像记录，请手动处理")
		r.finish(u, true)
		return
	}

	r.setPhase(u, phaseRollingBack, 85, reason+"，正在自动回滚到上一个镜像")
	ctx := context.Background()
	rcCtx, cancel := context.WithTimeout(ctx, recreateTimeout)
	defer cancel()

	if out, err := r.updateImageAndRecreate(rcCtx, previous); err != nil {
		r.logf(u, "回滚重建失败：%v，输出：%s", err, tail(out, 30))
		r.setPhase(u, phaseFailed, 100,
			fmt.Sprintf("%s，且自动回滚也失败（%v）。服务可能处于不可用状态，请登录服务器检查", reason, err))
		r.finish(u, true)
		return
	}

	if err := r.waitHealthy(ctx, rollbackHealthTimeout); err != nil {
		r.setPhase(u, phaseFailed, 100,
			fmt.Sprintf("%s；已回滚到上一个镜像，但健康检查仍未通过（%v）。请登录服务器检查日志", reason, err))
		r.finish(u, true)
		return
	}

	r.setPhase(u, phaseFailed, 100,
		fmt.Sprintf("%s，已自动回滚到上一个镜像，服务恢复正常。请检查更新失败的原因后重试", reason))
	r.finish(u, true)
}

// ---------------------------------------------------------------------------
// docker 交互
// ---------------------------------------------------------------------------

// findRollbackTarget 找出「应该回滚到哪个镜像」。
//
// 来源按可靠性排序：
//
//  1. 回滚标签（llm-relay:rollback）—— **最可靠**：它由上一次更新在动手
//     之前打上，是对旧镜像的一次真实引用，因而不会被镜像回收影响；
//     而且它跨更新器重启依然存在（内存里的历史会丢，标签不会）。
//  2. 当前任务的 previousImage —— 同一次更新内使用，正常情况下就是那个标签。
//  3. 最近历史里的 previousImage —— 更新完成、用户事后想退回去时走这条。
//  4. 都没有 → 返回空串，由调用方明确拒绝。
//
// 刻意**不做**「猜一个镜像名」这种兜底（比如假设镜像一定叫
// llm-relay:previous）：猜错的结果是把容器指向一个不存在或更糟的镜像，
// 而用户以为自己在「恢复」。宁可明确说「做不到，请手动处理」。
func (r *runner) findRollbackTarget(ctx context.Context) string {
	// 1. 回滚标签：这是唯一一个「即使更新器重启也还在」的依据
	if r.rollbackTargetExists(ctx) {
		return r.rollbackTag
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// 2. 当前正在跑的任务（升级失败后的自动回滚走这条）
	if r.current != nil && r.current.previousImage != "" {
		return r.current.previousImage
	}
	// 3. 最近的历史记录，从新到旧找第一个有记录的
	for i := len(r.history) - 1; i >= 0; i-- {
		if img := r.history[i].previousImage; img != "" {
			return img
		}
	}
	return ""
}

// docker 执行一条 docker 命令（不经 shell，理由与输出摘要见 run）。
func (r *runner) docker(ctx context.Context, args ...string) (string, error) {
	return r.run(ctx, nil, "docker", args...)
}

// composeUp 用 compose 文件重建应用服务容器。
//
// --no-deps 是关键：它让 compose 只处理 app 这一个服务，
// 数据库容器不会被停、不会被删、也不会被重建。没有它，
// 一次「换个应用镜像」会顺带重启数据库 —— 那是不必要的停机和风险。
func (r *runner) composeUp(ctx context.Context) (string, error) {
	return r.docker(ctx, "compose", "-f", r.composeFile, "up", "-d", "--no-deps", r.service)
}

// updateImageAndRecreate 用指定的镜像 ID 重建容器（回滚路径）。
//
// 这里**不修改 compose 文件**，而是用 `docker compose run` 之外的另一条路：
// 直接把容器指向目标镜像并重建。
//
// 为什么不改文件：compose 文件是站主维护的配置，一个后台服务去改它，
// 会让「文件内容」与「谁改的」变得难以追溯；而且回滚改完之后，
// 文件里就会永久留下一个旧版本号，下次有人看文件会以为那就是要跑的版本。
//
// 具体做法是 docker compose 支持的 `--project-directory` + 环境变量插值：
// compose 文件里 image 写成了 ${RELAY_IMAGE:-...}，这里通过环境变量覆盖。
// 若站主的 compose 文件没有用这个变量（老部署），则退回到
// `docker compose up -d --no-deps --force-recreate <service>` 并提示
// 需要在文件里把 image 改成目标版本。
func (r *runner) updateImageAndRecreate(ctx context.Context, image string) (string, error) {
	// 先看 compose 文件是否支持用环境变量指定镜像
	raw, err := os.ReadFile(r.composeFile)
	if err != nil {
		return "", fmt.Errorf("读取 compose 文件失败: %w", err)
	}
	if strings.Contains(string(raw), "RELAY_IMAGE") || strings.Contains(string(raw), "${IMAGE") {
		return r.run(ctx, []string{"RELAY_IMAGE=" + image}, "docker",
			"compose", "-f", r.composeFile, "up", "-d", "--no-deps", "--force-recreate", r.service)
	}

	// 老部署：compose 文件里写死了镜像名。此时没法在不动文件的前提下
	// 指定版本，只能明确报错并给出该怎么做 —— 总好过悄悄改用户的文件
	return "", fmt.Errorf(
		"compose 文件 %s 里的 image 字段没有使用 ${RELAY_IMAGE} 变量，无法在不修改文件的前提下指定回滚版本。"+
			"请把该字段改成 image: ${RELAY_IMAGE:-llm-relay:local}，然后重试；"+
			"或登录服务器手动把镜像版本改好后执行 docker compose up -d --no-deps %s",
		r.composeFile, r.service)
}

// currentImageID 取当前运行容器的镜像 ID。
func (r *runner) currentImageID(ctx context.Context) (string, error) {
	// 先按容器名查（compose 的 container_name），拿不到再按 compose 服务名查
	for _, args := range [][]string{
		{"inspect", "-f", "{{.Image}}", "llm-relay"},
		{"compose", "-f", r.composeFile, "ps", "-q", r.service},
	} {
		if out, err := r.docker(ctx, args...); err == nil {
			if v := strings.TrimSpace(out); v != "" {
				return v, nil
			}
		}
	}
	return "", errors.New("无法确定当前容器使用的镜像")
}

// imageIDOf 取某个镜像名的本地 ID。
func (r *runner) imageIDOf(ctx context.Context, image string) (string, error) {
	out, err := r.docker(ctx, "image", "inspect", "-f", "{{.Id}}", image)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// waitHealthy 轮询健康检查地址直到通过或超时。
func (r *runner) waitHealthy(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 3 * time.Second}

	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("健康检查返回 %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(healthInterval)
	}
	if lastErr == nil {
		lastErr = errors.New("超时")
	}
	return fmt.Errorf("等待 %v 后仍未就绪：%w", timeout, lastErr)
}

// tail 取输出的最后 n 行，用于把 docker 的长输出压进任务日志。
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return strings.Join(lines, " | ")
	}
	return strings.Join(lines[len(lines)-n:], " | ")
}

// lastLine 取最后一行非空输出，作为错误摘要。
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return "命令失败且没有输出"
}
