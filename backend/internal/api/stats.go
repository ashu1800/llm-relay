package api

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// round2 保留两位小数，用于毫秒等展示型数值。
// 比例类字段一律返回原始浮点，格式化交给前端，避免各接口精度不一致。
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func registerStatsRoutes(g *gin.RouterGroup, s *Server) {
	r := g.Group("/stats")
	r.GET("/summary", s.statsSummary)
	r.GET("/timeseries", s.statsTimeseries)
	r.GET("/models", s.statsModels)
	r.GET("/channels", s.statsChannels)
	r.GET("/heatmap", s.statsHeatmap)
}

// resolveRange 把时间范围关键字换算成区间与分桶粒度。
// 边界按容器本地时区计算，日志本身以 UTC 落库，比较时由驱动完成时区换算。
func resolveRange(key string) (time.Time, time.Time, string) {
	now := time.Now()
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "3d":
		return now.AddDate(0, 0, -3), now, "hour"
	case "7d":
		return now.AddDate(0, 0, -7), now, "day"
	case "30d":
		return now.AddDate(0, 0, -30), now, "day"
	default: // today
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), now, "hour"
	}
}

// localTZ 返回可用于 PostgreSQL AT TIME ZONE 的时区名。
func localTZ() string {
	name := time.Now().Location().String()
	if name == "Local" || name == "" {
		return "UTC"
	}
	return name
}

type summaryRow struct {
	Requests            int64           `gorm:"column:requests"`
	Success             int64           `gorm:"column:success"`
	Errors              int64           `gorm:"column:errors"`
	PromptTokens        int64           `gorm:"column:prompt_tokens"`
	CompletionTokens    int64           `gorm:"column:completion_tokens"`
	CachedTokens        int64           `gorm:"column:cached_tokens"`
	CacheCreationTokens int64           `gorm:"column:cache_creation_tokens"`
	ReasoningTokens     int64           `gorm:"column:reasoning_tokens"`
	Cost                decimal.Decimal `gorm:"column:cost"`
	AvgFirstByte        float64         `gorm:"column:avg_first_byte"`
	AvgTotal            float64         `gorm:"column:avg_total"`
}

func (s *Server) statsSummary(c *gin.Context) {
	start, end, _ := resolveRange(c.Query("range"))
	data, err := s.summarySnapshot(start, end)
	if err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	data["range"] = gin.H{
		"key": c.Query("range"), "start": start.Format(time.RFC3339), "end": end.Format(time.RFC3339),
	}
	c.JSON(http.StatusOK, data)
}

// summarySnapshot 算出某个区间的汇总。抽出来是为了让实时推送复用同一套口径 ——
// 看板上的数字与 WebSocket 推来的数字必须来自同一个查询，
// 否则「刚刷新是 A、两秒后自己变成 B」这种不一致会让人怀疑看板本身。
func (s *Server) summarySnapshot(start, end time.Time) (gin.H, error) {
	const q = `SELECT
		COUNT(*)::bigint AS requests,
		COUNT(*) FILTER (WHERE status_code >= 200 AND status_code < 300)::bigint AS success,
		COUNT(*) FILTER (WHERE status_code >= 400)::bigint AS errors,
		COALESCE(SUM(prompt_tokens),0)::bigint AS prompt_tokens,
		COALESCE(SUM(completion_tokens),0)::bigint AS completion_tokens,
		COALESCE(SUM(cached_tokens),0)::bigint AS cached_tokens,
		COALESCE(SUM(cache_creation_tokens),0)::bigint AS cache_creation_tokens,
		COALESCE(SUM(reasoning_tokens),0)::bigint AS reasoning_tokens,
		COALESCE(SUM(estimated_cost),0) AS cost,
		COALESCE(AVG(first_byte_ms) FILTER (WHERE first_byte_ms > 0),0) AS avg_first_byte,
		COALESCE(AVG(total_ms) FILTER (WHERE total_ms > 0),0) AS avg_total
	FROM request_logs WHERE created_at >= ? AND created_at <= ?`

	var row summaryRow
	if err := s.deps.Store.DB().Raw(q, start, end).Scan(&row).Error; err != nil {
		return nil, err
	}

	successRate := 0.0
	if row.Requests > 0 {
		successRate = float64(row.Success) / float64(row.Requests)
	}
	// 命中率分母为全部输入（未命中 + 命中 + 缓存写入），与日志页口径保持一致
	cacheDenom := row.PromptTokens + row.CachedTokens + row.CacheCreationTokens
	hitRate := 0.0
	if cacheDenom > 0 {
		hitRate = float64(row.CachedTokens) / float64(cacheDenom)
	}

	return gin.H{
		"requests":              row.Requests,
		"success":               row.Success,
		"errors":                row.Errors,
		"success_rate":          successRate,
		"prompt_tokens":         row.PromptTokens,
		"completion_tokens":     row.CompletionTokens,
		"cached_tokens":         row.CachedTokens,
		"cache_creation_tokens": row.CacheCreationTokens,
		"reasoning_tokens":      row.ReasoningTokens,
		"total_tokens":          row.PromptTokens + row.CompletionTokens + row.CachedTokens,
		"cache_hit_rate":        hitRate,
		"estimated_cost":        row.Cost.StringFixed(8),
		"avg_first_byte_ms":     row.AvgFirstByte,
		"avg_total_ms":          row.AvgTotal,
	}, nil
}

type seriesRow struct {
	Bucket           time.Time       `gorm:"column:bucket"`
	Requests         int64           `gorm:"column:requests"`
	Errors           int64           `gorm:"column:errors"`
	PromptTokens     int64           `gorm:"column:prompt_tokens"`
	CompletionTokens int64           `gorm:"column:completion_tokens"`
	CachedTokens     int64           `gorm:"column:cached_tokens"`
	Cost             decimal.Decimal `gorm:"column:cost"`
}

func (s *Server) statsTimeseries(c *gin.Context) {
	start, end, bucket := resolveRange(c.Query("range"))
	// 分桶粒度只允许白名单值，避免拼接进 SQL 造成注入
	if bucket != "hour" && bucket != "day" {
		bucket = "hour"
	}

	sql := `SELECT date_trunc('` + bucket + `', created_at) AS bucket,
		COUNT(*)::bigint AS requests,
		COUNT(*) FILTER (WHERE status_code >= 400)::bigint AS errors,
		COALESCE(SUM(prompt_tokens),0)::bigint AS prompt_tokens,
		COALESCE(SUM(completion_tokens),0)::bigint AS completion_tokens,
		COALESCE(SUM(cached_tokens),0)::bigint AS cached_tokens,
		COALESCE(SUM(estimated_cost),0) AS cost
	FROM request_logs WHERE created_at >= ? AND created_at <= ?
	GROUP BY bucket ORDER BY bucket`

	var rows []seriesRow
	if err := s.deps.Store.DB().Raw(sql, start, end).Scan(&rows).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}

	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		items = append(items, gin.H{
			"ts":                r.Bucket.UTC().Format(time.RFC3339),
			"requests":          r.Requests,
			"errors":            r.Errors,
			"prompt_tokens":     r.PromptTokens,
			"completion_tokens": r.CompletionTokens,
			"cached_tokens":     r.CachedTokens,
			"cost":              r.Cost.StringFixed(8),
		})
	}
	c.JSON(http.StatusOK, gin.H{"bucket": bucket, "items": items})
}

type groupRow struct {
	Name             string          `gorm:"column:name"`
	Requests         int64           `gorm:"column:requests"`
	PromptTokens     int64           `gorm:"column:prompt_tokens"`
	CompletionTokens int64           `gorm:"column:completion_tokens"`
	CachedTokens     int64           `gorm:"column:cached_tokens"`
	Tokens           int64           `gorm:"column:tokens"`
	Cost             decimal.Decimal `gorm:"column:cost"`
	AvgMs            float64         `gorm:"column:avg_ms"`
	Errors           int64           `gorm:"column:errors"`
}

func (s *Server) statsModels(c *gin.Context) {
	s.groupStats(c, "model_requested")
}

func (s *Server) statsChannels(c *gin.Context) {
	s.groupStats(c, "channel_name")
}

// groupStats 按维度聚合调用量与费用，维度只接受内部白名单字段名。
func (s *Server) groupStats(c *gin.Context, column string) {
	if column != "model_requested" && column != "channel_name" {
		writeUpstreamError(c, http.StatusBadRequest, "不支持的聚合维度", "invalid_request_error")
		return
	}
	start, end, _ := resolveRange(c.Query("range"))
	limit, _ := strconv.Atoi(orDefault(c.Query("limit"), "10"))
	if limit < 1 || limit > 100 {
		limit = 10
	}

	sql := `SELECT ` + column + ` AS name,
		COUNT(*)::bigint AS requests,
		COUNT(*) FILTER (WHERE status_code >= 400)::bigint AS errors,
		COALESCE(SUM(prompt_tokens),0)::bigint AS prompt_tokens,
		COALESCE(SUM(completion_tokens),0)::bigint AS completion_tokens,
		COALESCE(SUM(cached_tokens),0)::bigint AS cached_tokens,
		COALESCE(SUM(total_tokens),0)::bigint AS tokens,
		COALESCE(SUM(estimated_cost),0) AS cost,
		COALESCE(AVG(total_ms) FILTER (WHERE total_ms > 0),0) AS avg_ms
	FROM request_logs WHERE created_at >= ? AND created_at <= ? AND ` + column + ` <> ''
	GROUP BY ` + column + ` ORDER BY requests DESC LIMIT ?`

	var rows []groupRow
	if err := s.deps.Store.DB().Raw(sql, start, end, limit).Scan(&rows).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}

	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		items = append(items, gin.H{
			"name": r.Name, "requests": r.Requests, "errors": r.Errors,
			"prompt_tokens": r.PromptTokens, "completion_tokens": r.CompletionTokens,
			"cached_tokens": r.CachedTokens, "tokens": r.Tokens,
			"cost": r.Cost.StringFixed(8), "avg_ms": r.AvgMs,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type heatRow struct {
	Day      string          `gorm:"column:day"`
	Hour     int             `gorm:"column:hour"`
	Requests int64           `gorm:"column:requests"`
	Cost     decimal.Decimal `gorm:"column:cost"`
	Tokens   int64           `gorm:"column:tokens"`
}

// statsHeatmap 返回近 N 天按「日期 x 小时」分布的热力数据。
// 日期与小时按容器本地时区切分，否则跨时区看会整体错位。
func (s *Server) statsHeatmap(c *gin.Context) {
	days, _ := strconv.Atoi(orDefault(c.Query("days"), "30"))
	if days < 1 || days > 180 {
		days = 30
	}
	since := time.Now().AddDate(0, 0, -days)

	// 悬浮提示要显示消费与 Token（与参考站的提示一致），所以一并聚合。
	// COALESCE 是必需的：某个小时里只要有一行 estimated_cost 为 NULL，
	// SUM 整体就会是 NULL，扫进 decimal 会直接报错。
	const q = `SELECT to_char(created_at AT TIME ZONE ?, 'YYYY-MM-DD') AS day,
		EXTRACT(HOUR FROM created_at AT TIME ZONE ?)::int AS hour,
		COUNT(*)::bigint AS requests,
		COALESCE(SUM(estimated_cost), 0) AS cost,
		COALESCE(SUM(total_tokens), 0)::bigint AS tokens
	FROM request_logs WHERE created_at >= ?
	GROUP BY day, hour ORDER BY day, hour`

	tz := localTZ()
	var rows []heatRow
	if err := s.deps.Store.DB().Raw(q, tz, tz, since).Scan(&rows).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}

	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		items = append(items, gin.H{
			"day": r.Day, "hour": r.Hour, "requests": r.Requests,
			// String() 会去掉多余的尾随零，前端可直接显示
			"cost":   r.Cost.String(),
			"tokens": r.Tokens,
		})
	}
	c.JSON(http.StatusOK, gin.H{"days": days, "timezone": tz, "items": items})
}
