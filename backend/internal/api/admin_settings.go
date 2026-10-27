package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/config"
)

// registerSettingsRoutes 挂载系统设置接口。
//
// 运行参数来自环境变量与 yaml，进程启动后不可变，因此这里只读展示，
// 页面会说明改哪个文件、需要重启。不提供改了不生效的假开关。
func registerSettingsRoutes(g *gin.RouterGroup, s *Server) {
	r := g.Group("/settings")
	r.GET("", s.getSettings)
	r.POST("/cleanup", s.cleanupData)
}

func (s *Server) getSettings(c *gin.Context) {
	cfg := s.deps.Config
	db := s.deps.Store.DB()

	var counts struct {
		Logs     int64 `json:"logs"`
		Payloads int64 `json:"payloads"`
		// Priced 是「已定价的模型数」，取自 model_prices 的行数。
		//
		// 曾经统计的是 channel_models 上四档单价非 0 的白名单行数。2026-10-27
		// 单价迁到模型级时，store.migratePricesToModelLevel 把那四列 DROP 掉了，
		// 这条 SQL 没跟着改 —— 升级后的库上整个接口直接 500（实测：
		// `column "input_per1_m" does not exist`），系统设置页整页打不开。
		// 现在与「模型定价」页同一个口径：有 model_prices 行就是定了价。
		Priced   int64 `json:"priced"`
		Channels int64 `json:"channels"`
		Models   int64 `json:"models"`
		Keys     int64 `json:"keys"`
	}
	if err := db.Raw(`SELECT
		(SELECT COUNT(*) FROM request_logs)::bigint AS logs,
		(SELECT COUNT(*) FROM request_payloads)::bigint AS payloads,
		(SELECT COUNT(*) FROM model_prices)::bigint AS priced,
		(SELECT COUNT(*) FROM channels)::bigint AS channels,
		(SELECT COUNT(DISTINCT public_name) FROM channel_models WHERE enabled = true)::bigint AS models,
		(SELECT COUNT(*) FROM api_keys)::bigint AS keys`).Scan(&counts).Error; err != nil {
		writeInternalError(c, err)
		return
	}

	var span struct {
		Earliest *time.Time `json:"earliest"`
		Latest   *time.Time `json:"latest"`
	}
	db.Raw("SELECT MIN(created_at) AS earliest, MAX(created_at) AS latest FROM request_logs").Scan(&span)

	// 数据库版本作为连通性证据；连不上时这里会是空串
	var dbVersion string
	db.Raw("SELECT version()").Scan(&dbVersion)
	if len(dbVersion) > 70 {
		dbVersion = dbVersion[:70] + "…"
	}

	c.JSON(http.StatusOK, gin.H{
		"runtime": gin.H{
			"listen":                 cfg.Server.Addr(),
			"mode":                   cfg.Server.Mode,
			"log_level":              cfg.Log.Level,
			"log_format":             cfg.Log.Format,
			"upstream_timeout_sec":   int(cfg.Relay.UpstreamTimeout.Seconds()),
			"first_byte_timeout_sec": int(cfg.Relay.FirstByteTimeout.Seconds()),
			"max_retries":            cfg.Relay.MaxRetries,
			"max_request_body_mb":    cfg.Relay.MaxRequestBodyMB,
			"log_retention_days":     cfg.Relay.LogRetentionDays,
			"payload_storage_mode":   cfg.Relay.PayloadStorageMode,
			"payload_max_kb":         cfg.Relay.PayloadMaxKB,
			"max_concurrency":        cfg.Relay.MaxConcurrency,
			"default_rpm":            cfg.Relay.DefaultRPM,
			// 毫秒而非秒：默认值 500ms 换算成整秒是 0，界面上会显示成
			// 「已关闭」——那是与实际相反的误导。
			"retry_same_upstream_delay_ms": int(cfg.Relay.RetrySameUpstreamDelay.Milliseconds()),
			// 登录鉴权状态与会话时长。密钥本身（哪怕哈希）永远不出接口；
			// auth_method 告诉前端当前是二选一里的哪一种（unset=待初始化）
			"console_auth_enabled": s.consoleAuthEnabled(),
			"console_auth_method":  string(s.authMethod()),
			"session_ttl_hours":    int(cfg.Security.SessionTTL.Hours()),
			"database_ok":          dbVersion != "",
			"database_version":     dbVersion,
		},
		"counts": gin.H{
			"logs": counts.Logs, "payloads": counts.Payloads, "priced": counts.Priced,
			"channels": counts.Channels, "models": counts.Models,
			"keys": counts.Keys,
		},
		"log_span": gin.H{"earliest": span.Earliest, "latest": span.Latest},
		// env_keys 是「每个运行时字段由哪个环境变量决定」的唯一事实来源。
		// 前端不再自己维护一份手写清单 —— 那份清单曾经 13 条里错 6 条，
		// 用户照着设一个不存在的变量不会有任何反应，比不提示更糟。
		"env_keys": config.EnvKeys(),
	})
}

// cleanupData 手动清理超出保留期的日志与孤立报文。
// 保留天数取自配置，页面上会显示实际值。
func (s *Server) cleanupData(c *gin.Context) {
	days := s.deps.Config.Relay.LogRetentionDays
	if days <= 0 {
		writeUpstreamError(c, http.StatusBadRequest, "未配置日志保留天数，已跳过清理", "invalid_request_error")
		return
	}
	out, err := s.runCleanup(days)
	if err != nil {
		writeInternalError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"deleted_logs":     out.DeletedLogs,
		"deleted_payloads": out.DeletedPayloads,
		"deleted_orphans":  out.DeletedOrphans,
		"retention_days":   days,
		"cutoff":           time.Now().AddDate(0, 0, -days).Format(time.RFC3339),
	})
}
