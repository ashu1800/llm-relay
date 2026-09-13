package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
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
		// Priced 是「已配价的渠道模型条数」：价格表下线后，最能说明
		// 「定价配置是否完整」的就是它，而模型总数另有 models 一项
		Priced   int64 `json:"priced"`
		Channels int64 `json:"channels"`
		Models   int64 `json:"models"`
		Keys     int64 `json:"keys"`
		Groups   int64 `json:"groups"`
	}
	if err := db.Raw(`SELECT
		(SELECT COUNT(*) FROM request_logs)::bigint AS logs,
		(SELECT COUNT(*) FROM request_payloads)::bigint AS payloads,
		(SELECT COUNT(*) FROM channel_models
			WHERE input_per1_m <> 0 OR output_per1_m <> 0
			   OR cache_read_per1_m <> 0 OR cache_write_per1_m <> 0)::bigint AS priced,
		(SELECT COUNT(*) FROM channels)::bigint AS channels,
		(SELECT COUNT(DISTINCT public_name) FROM channel_models WHERE enabled = true)::bigint AS models,
		(SELECT COUNT(*) FROM api_keys)::bigint AS keys,
		(SELECT COUNT(*) FROM channel_groups)::bigint AS groups`).Scan(&counts).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
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
			"database_ok":            dbVersion != "",
			"database_version":       dbVersion,
		},
		"counts": gin.H{
			"logs": counts.Logs, "payloads": counts.Payloads, "priced": counts.Priced,
			"channels": counts.Channels, "models": counts.Models,
			"keys": counts.Keys, "groups": counts.Groups,
		},
		"log_span": gin.H{"earliest": span.Earliest, "latest": span.Latest},
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
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
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
