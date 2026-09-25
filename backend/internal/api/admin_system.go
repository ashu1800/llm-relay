package api

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/update"
	"llm-relay/internal/version"
)

// registerSystemRoutes 挂载系统版本与更新接口。
//
// 全部挂在 admin 组下（sameOriginOnly + requireConsoleAuth + limitAdminBody），
// 这里不再单独加鉴权 —— 它们能替换正在运行的程序文件，
// 而管理台的其它接口能改渠道密钥，两者的权限等级本就相同。
//
// 路径沿用 /system/* 前缀，与既有的 /system/info 归在一处：
// 界面上它们都在「系统设置」页与侧栏的版本徽标里，接口也放在一起便于对照。
func registerSystemRoutes(g *gin.RouterGroup, s *Server) {
	r := g.Group("/system")
	// 除 /version 外全部依赖更新服务；没启用时统一回 501，
	// 不在 8 个 handler 里各写一遍守卫。
	needUpdate := func(h gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) {
			if s.deps.Update == nil {
				writeUpstreamError(c, http.StatusNotImplemented, "更新功能未启用", "not_implemented")
				return
			}
			h(c)
		}
	}
	{
		// 版本信息：只读，无网络请求，永远可用
		r.GET("/version", s.getVersion)
		// 检测更新：会访问 GitHub（有 20 分钟缓存）
		r.GET("/update/check", needUpdate(s.checkUpdate))
		// 启动一次更新（异步，立即返回任务）
		r.POST("/update", needUpdate(s.startUpdate))
		// 轮询更新进度
		r.GET("/update/progress", needUpdate(s.updateProgress))
		// 取消正在进行的任务
		r.POST("/update/cancel", needUpdate(s.cancelUpdate))
		// 可回滚版本列表
		r.GET("/update/rollback-versions", needUpdate(s.rollbackVersions))
		// 执行回滚（不带版本 = 本地回退到上一版，不联网）
		r.POST("/update/rollback", needUpdate(s.rollback))
		// 重启服务以应用更新
		r.POST("/restart", s.restartService)

		// 更新相关配置（仓库 / token / 代理）
		r.GET("/update/config", needUpdate(s.getUpdateConfig))
		r.PUT("/update/config", needUpdate(s.saveUpdateConfig))
	}
}

// getVersion 返回当前版本信息。
//
// 这个接口刻意**不碰网络**：版本徽标要在任何情况下都能显示出来，
// 包括 GitHub 完全不可达的时候。「能不能连上 GitHub」是另一个问题，
// 由 /update/check 回答 —— 把两件事混在一起会让最基本的信息
// （我现在跑的是哪一版）依赖于最容易失败的那个环节。
func (s *Server) getVersion(c *gin.Context) {
	// 内嵌 version.Info：字段与 json tag 只在 version 包定义一处。
	// 这里手抄一份 gin.H 的话，每加一个字段都要同步两边。
	resp := struct {
		version.Info
		// is_release 与 build_type 一起给：前端判分支时用前者更直观，
		// 而 build_type 用来决定显示哪种更新方式
		IsRelease bool `json:"is_release"`
	}{Info: version.Current(), IsRelease: version.IsRelease()}
	c.JSON(http.StatusOK, resp)
}

// checkUpdate 检测是否有新版本。
//
// 失败时**不返回 5xx**：这个接口的响应里除了「有没有新版本」，
// 还有「当前版本号」这个永远该可见的事实。用错误码回绝会让整个
// 面板变成一条红色错误，把版本号一起盖掉。所以失败信息放在
// warning 字段里，200 返回。
func (s *Server) checkUpdate(c *gin.Context) {
	force := c.Query("force") == "true" || c.Query("force") == "1"
	info := s.deps.Update.Check(c.Request.Context(), force)
	c.JSON(http.StatusOK, info)
}

// startUpdate 启动更新。
//
// 请求体可带 {"mode":"binary"} 显式指定方式，留空则自动判定。
// 立即返回任务对象（含 id 与初始阶段），真正的下载在后台跑 ——
// 前端之后轮询 /update/progress?task=<id> 拿进度。
//
// 为什么要异步：跨洋下载归档是分钟级操作，
// 同步 HTTP 一定会被浏览器或反向代理掐断（见 update 包注释）。
func (s *Server) startUpdate(c *gin.Context) {
	var body struct {
		Mode string `json:"mode"`
	}
	// 请求体可选：不带 body 的 POST 是合法的调用方式
	_ = c.ShouldBindJSON(&body)

	task, err := s.deps.Update.Apply(c.Request.Context(), body.Mode)
	if err != nil {
		s.writeUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, task)
}

// updateProgress 查询进度。
//
// task 参数是任务 id；不传时返回当前/最近一次任务 ——
// 用户 F5 之后手上没有 id，仍该看到刚才那次更新跑到哪了。
func (s *Server) updateProgress(c *gin.Context) {
	task, err := s.deps.Update.Progress(c.Query("task"))
	if err != nil {
		writeUpstreamError(c, http.StatusNotFound, err.Error(), "not_found_error")
		return
	}
	c.JSON(http.StatusOK, task)
}

// cancelUpdate 取消当前任务。
func (s *Server) cancelUpdate(c *gin.Context) {
	if err := s.deps.Update.Cancel(); err != nil {
		writeUpstreamError(c, http.StatusConflict, err.Error(), "conflict_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已请求取消，正在进行的下载会在下一个检查点中止"})
}

// rollbackVersions 列出可回滚的版本。
func (s *Server) rollbackVersions(c *gin.Context) {
	versions, err := s.deps.Update.RollbackCandidates(c.Request.Context())
	if err != nil {
		writeUpstreamError(c, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	// has_backup 告诉界面「能否不联网退回上一版」。
	// 这条路径比下载指定版本可靠得多（不需要网络），所以界面应当
	// 在它可用时优先推荐它。
	hasBackup := false
	if version.BuildType == version.BuildBinary {
		hasBackup = s.deps.Update.LocalBackupAvailable()
	}
	c.JSON(http.StatusOK, gin.H{
		"versions":   versions,
		"has_backup": hasBackup,
	})
}

// rollback 执行回滚。
//
// 不带 version 时是**本地回滚**（把 .backup 换回来），完全不联网 ——
// 因此在 GitHub 不可达时它依然可用，是唯一还能救回服务的手段。
func (s *Server) rollback(c *gin.Context) {
	var body struct {
		Version string `json:"version"`
	}
	_ = c.ShouldBindJSON(&body)

	task, err := s.deps.Update.Rollback(c.Request.Context(), body.Version)
	if err != nil {
		s.writeUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, task)
}

// restartService 重启服务以应用更新。
//
// # 为什么这里不直接 os.Exit
//
// 进程自己退出、靠 systemd 的 Restart=always 拉起来，是最简洁的做法，
// 但它只在 systemd 托管下成立 —— 官方部署形态（install.sh）正是如此，
// 而开发者的 source 构建本来就不该由网页远程重启。
//
// 所以走一条在 systemd 托管下成立的路：**先回响应、再退出**。
// 进程退出后 Restart=always 会用（更新流程刚刚原子替换好的）可执行文件
// 把服务拉起来。
//
// 之前必须先把 HTTP 响应写出去：直接 os.Exit 会让前端拿到一个
// 连接被重置的错误，用户看到的是「重启失败」，而实际上重启成功了。
func (s *Server) restartService(c *gin.Context) {
	// 用 202 而不是 200：语义上是「已接受，正在执行」，
	// 而连接随时可能断，前端不该把它当成一次普通的成功响应
	c.JSON(http.StatusAccepted, gin.H{
		"message": "正在重启服务，页面会在服务回来后自动刷新",
	})

	// 把响应刷给客户端之后再退出。
	//
	// gin 的 c.JSON 只是写进了 ResponseWriter 的缓冲，
	// 真正的 flush 发生在 handler 返回之后 —— 所以这里要显式 Flush，
	// 否则 os.Exit 会在数据发出前把进程干掉，前端拿到的是连接重置。
	if f, ok := c.Writer.(http.Flusher); ok {
		f.Flush()
	}

	// 稍等一下再退，给 TCP 一点时间把上面那个响应送出去。
	// 200ms 是实测够用的值（同一个机房/本机的往返通常 < 5ms，
	// 经反代也不过几十毫秒），同时短到用户察觉不到。
	go func() {
		time.Sleep(200 * time.Millisecond)
		slog.Info("收到重启请求，进程即将退出以触发自动拉起")
		os.Exit(0)
	}()
}

// writeUpdateError 把更新相关的错误翻译成合适的响应。
//
// 逐个错误单独映射，因为它们的「下一步该做什么」完全不同：
//
//	ErrNoBackup           → 没有备份可回滚（说明现状）
//	业务性拒绝            → 409，用户读那句话就够了
//	其余                  → 500，去看日志
//
// 一律回 500 会让「本来就不允许更新」这种一眼能懂的问题看起来像程序崩溃。
func (s *Server) writeUpdateError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, update.ErrNoBackup):
		writeUpstreamError(c, http.StatusConflict, err.Error(), "no_backup")
	case update.IsBusinessError(err):
		writeUpstreamError(c, http.StatusConflict, err.Error(), "conflict_error")
	default:
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
	}
}

// getUpdateConfig 读取更新配置。
func (s *Server) getUpdateConfig(c *gin.Context) {
	cfg := s.deps.Update.GetConfig()
	// has_token 只回「有没有配」，**绝不回显原值** ——
	// 它是一个能访问 GitHub 的凭据，而管理台的接口响应
	// 可能被截图、被日志采集。
	//
	// 把这条注释写在 map 外面而不是夹在键之间：夹在中间会让
	// gofmt 的对齐计算被注释打断，键名那一列就会歪掉。
	c.JSON(http.StatusOK, gin.H{
		"enabled":      cfg.Enabled,
		"repo":         cfg.Repo,
		"proxy":        cfg.Proxy,
		"has_token":    cfg.Token != "",
		"repo_default": update.DefaultConfig().Repo,
	})
}

// saveUpdateConfig 保存更新配置。
func (s *Server) saveUpdateConfig(c *gin.Context) {
	var body struct {
		Enabled bool   `json:"enabled"`
		Repo    string `json:"repo"`
		Proxy   string `json:"proxy"`
		// Token 用指针：区分「没传这个字段」与「传了值」。
		//
		// 语义是三态：
		//   字段缺失 → 不修改（界面上是个不回显的输入框，用户没动它）
		//   空串     → 清除已保存的 token
		//   非空     → 设置为该值
		//
		// 注意 JSON 的 null 与「字段缺失」在 Go 里解析结果相同（都是 nil），
		// 所以「清除」必须用空串表达 —— 前端那边也是这么发的。见
		// frontend/src/api/version.ts 的 saveConfig 与
		// admin_system_test.go 的 TestSaveUpdateConfigTokenTriState。
		Token *string `json:"token"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求格式错误: "+err.Error(), "invalid_request_error")
		return
	}

	cfg := s.deps.Update.GetConfig()
	cfg.Enabled = body.Enabled
	if body.Repo != "" {
		cfg.Repo = body.Repo
	}
	cfg.Proxy = body.Proxy
	if body.Token != nil {
		cfg.Token = *body.Token
	}

	if err := s.deps.Update.SaveConfig(cfg); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "更新配置已保存，下次检测即刻生效"})
}
