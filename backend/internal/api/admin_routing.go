package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/model"
)

// registerRoutingRoutes 挂载路由分析接口。
func registerRoutingRoutes(g *gin.RouterGroup, s *Server) {
	r := g.Group("/routing")
	r.GET("/analysis", s.routingAnalysis)
}

type channelStatRow struct {
	// GORM 的 Scan 按列名映射字段，不读 json tag，
	// 字段名与列名对不上时必须显式标 column，否则该字段会静默取零值。
	ChannelID      uint    `gorm:"column:channel_id" json:"channel_id"`
	ChannelName    string  `gorm:"column:channel_name" json:"channel_name"`
	Requests       int64   `gorm:"column:requests" json:"requests"`
	Errors         int64   `gorm:"column:errors" json:"errors"`
	Retries        int64   `gorm:"column:retries" json:"retries"`
	AvgMs          float64 `gorm:"column:avg_ms" json:"avg_ms"`
	AvgFirstByteMs float64 `gorm:"column:avg_first_byte_ms" json:"avg_first_byte_ms"`
	CachedTokens   int64   `gorm:"column:cached_tokens" json:"cached_tokens"`
	InputTokens    int64   `gorm:"column:input_tokens" json:"input_tokens"`
	PromptTokens   int64   `gorm:"column:prompt_tokens" json:"prompt_tokens"`
	CompletionToks int64   `gorm:"column:completion_tokens" json:"completion_tokens"`
	Cost           string  `gorm:"column:cost" json:"cost"`
}

type incidentRow struct {
	TraceID     string `json:"trace_id"`
	Model       string `json:"model"`
	ChannelName string `json:"channel_name"`
	StatusCode  int    `json:"status_code"`
	RetryCount  int    `json:"retry_count"`
	Error       string `json:"error"`
	TotalMs     int    `json:"total_ms"`
	CreatedAt   string `json:"created_at"`
}

// routingAnalysis 汇总渠道权重与实际分流情况，用于回答「策略是否按预期生效」。
//
// 关键指标是期望占比与实际占比的偏差：权重配好了但流量没按权重走，
// 说明有渠道在失败重试，或者被 least_latency 之类的策略改写了排序。
func (s *Server) routingAnalysis(c *gin.Context) {
	start, end, _ := resolveRange(c.Query("range"))
	db := s.deps.Store.DB()

	var rows []channelStatRow
	statSQL := `SELECT channel_id, channel_name,
		COUNT(*)::bigint AS requests,
		COUNT(*) FILTER (WHERE status_code >= 400)::bigint AS errors,
		COALESCE(SUM(retry_count),0)::bigint AS retries,
		COALESCE(AVG(total_ms) FILTER (WHERE total_ms > 0),0) AS avg_ms,
		COALESCE(AVG(first_byte_ms) FILTER (WHERE first_byte_ms > 0),0) AS avg_first_byte_ms,
		COALESCE(SUM(cached_tokens),0)::bigint AS cached_tokens,
		COALESCE(SUM(prompt_tokens) + SUM(cached_tokens) + SUM(cache_creation_tokens),0)::bigint AS input_tokens,
		COALESCE(SUM(prompt_tokens),0)::bigint AS prompt_tokens,
		COALESCE(SUM(completion_tokens),0)::bigint AS completion_tokens,
		COALESCE(SUM(estimated_cost),0) AS cost
		FROM request_logs
		WHERE created_at >= ? AND created_at <= ? AND channel_name <> ''
		GROUP BY channel_id, channel_name ORDER BY requests DESC`
	if err := db.Raw(statSQL, start, end).Scan(&rows).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}

	// 期望占比按「已启用渠道」的权重算，而不是按实际有流量的渠道算。
	// 否则某个渠道全挂导致它没流量时，偏差反而显示为 0，看不出问题。
	var channels []model.Channel
	if err := db.Where("enabled = ?", true).Find(&channels).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	totalWeight := 0
	weightByID := map[uint]int{}
	for _, ch := range channels {
		w := ch.Weight
		if w < 1 {
			w = 1
		}
		totalWeight += w
		weightByID[ch.ID] = w
	}

	var totalRequests int64
	for _, r := range rows {
		totalRequests += r.Requests
	}

	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		actual := 0.0
		if totalRequests > 0 {
			actual = float64(r.Requests) / float64(totalRequests)
		}
		expected := 0.0
		if w, ok := weightByID[r.ChannelID]; ok && totalWeight > 0 {
			expected = float64(w) / float64(totalWeight)
		}
		successRate := 1.0
		if r.Requests > 0 {
			successRate = float64(r.Requests-r.Errors) / float64(r.Requests)
		}
		hitRate := 0.0
		if r.InputTokens > 0 {
			hitRate = float64(r.CachedTokens) / float64(r.InputTokens)
		}
		items = append(items, gin.H{
			"channel_id": r.ChannelID, "channel_name": r.ChannelName,
			"weight":         weightByID[r.ChannelID],
			"expected_share": expected,
			"actual_share":   actual,
			"deviation":      actual - expected,
			"requests":       r.Requests, "errors": r.Errors, "retries": r.Retries,
			"success_rate":      successRate,
			"avg_ms":            round2(r.AvgMs),
			"avg_first_byte_ms": round2(r.AvgFirstByteMs),
			"cache_hit_rate":    hitRate,
			"tokens":            r.PromptTokens + r.CompletionToks,
			"cost":              r.Cost,
			"idle":              false,
		})
	}

	// 未被使用的已启用渠道也要列出来，否则「配了但没流量」会被静默忽略
	used := map[uint]bool{}
	for _, r := range rows {
		used[r.ChannelID] = true
	}
	for _, ch := range channels {
		if used[ch.ID] {
			continue
		}
		expected := 0.0
		if totalWeight > 0 {
			expected = float64(weightByID[ch.ID]) / float64(totalWeight)
		}
		items = append(items, gin.H{
			"channel_id": ch.ID, "channel_name": ch.Name,
			"weight":         weightByID[ch.ID],
			"expected_share": expected, "actual_share": 0.0,
			"deviation": -expected,
			"requests":  0, "errors": 0, "retries": 0,
			"success_rate": 0.0, "avg_ms": 0, "avg_first_byte_ms": 0,
			"cache_hit_rate": 0.0, "tokens": 0, "cost": "0",
			"idle": true,
		})
	}

	// 模型到渠道的分布：看某个模型是否被固定路由到了单一渠道
	type pairRow struct {
		Model       string `json:"model"`
		ChannelName string `json:"channel_name"`
		Requests    int64  `json:"requests"`
	}
	var pairs []pairRow
	pairSQL := `SELECT model_requested AS model, channel_name, COUNT(*)::bigint AS requests
		FROM request_logs WHERE created_at >= ? AND created_at <= ? AND channel_name <> ''
		GROUP BY model_requested, channel_name ORDER BY model_requested, requests DESC`
	if err := db.Raw(pairSQL, start, end).Scan(&pairs).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	var modelOrder []string
	modelSum := map[string]int64{}
	modelMap := map[string][]gin.H{}
	for _, p := range pairs {
		if _, ok := modelMap[p.Model]; !ok {
			modelOrder = append(modelOrder, p.Model)
		}
		modelMap[p.Model] = append(modelMap[p.Model], gin.H{
			"channel_name": p.ChannelName, "requests": p.Requests,
		})
		modelSum[p.Model] += p.Requests
	}
	models := make([]gin.H, 0, len(modelOrder))
	for _, name := range modelOrder {
		models = append(models, gin.H{
			"model": name, "requests": modelSum[name], "channels": modelMap[name],
		})
	}

	// 重试与失败记录：排查「为什么流量跑到别的渠道去了」的直接证据
	var incidents []incidentRow
	incSQL := `SELECT trace_id, model_requested AS model, channel_name,
		status_code, retry_count, error, total_ms,
		to_char(created_at, 'YYYY-MM-DD HH24:MI:SS') AS created_at
		FROM request_logs
		WHERE created_at >= ? AND created_at <= ? AND (retry_count > 0 OR status_code >= 400)
		ORDER BY created_at DESC LIMIT 30`
	if err := db.Raw(incSQL, start, end).Scan(&incidents).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}

	var agg struct {
		Requests  int64   `gorm:"column:requests"`
		Errors    int64   `gorm:"column:errors"`
		Retries   int64   `gorm:"column:retries"`
		AvgMs     float64 `gorm:"column:avg_ms"`
		FirstByte float64 `gorm:"column:avg_first_byte_ms"`
		Cached    int64   `gorm:"column:cached_tokens"`
		InputToks int64   `gorm:"column:input_tokens"`
		Cost      string  `gorm:"column:cost"`
	}
	sumSQL := `SELECT COUNT(*)::bigint AS requests,
		COUNT(*) FILTER (WHERE status_code >= 400)::bigint AS errors,
		COALESCE(SUM(retry_count),0)::bigint AS retries,
		COALESCE(AVG(total_ms) FILTER (WHERE total_ms > 0),0) AS avg_ms,
		COALESCE(AVG(first_byte_ms) FILTER (WHERE first_byte_ms > 0),0) AS avg_first_byte_ms,
		COALESCE(SUM(cached_tokens),0)::bigint AS cached_tokens,
		COALESCE(SUM(prompt_tokens) + SUM(cached_tokens) + SUM(cache_creation_tokens),0)::bigint AS input_tokens,
		COALESCE(SUM(estimated_cost),0) AS cost
		FROM request_logs WHERE created_at >= ? AND created_at <= ?`
	if err := db.Raw(sumSQL, start, end).Scan(&agg).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	hitRate := 0.0
	if agg.InputToks > 0 {
		hitRate = float64(agg.Cached) / float64(agg.InputToks)
	}
	retryRate := 0.0
	if agg.Requests > 0 {
		retryRate = float64(agg.Retries) / float64(agg.Requests)
	}

	c.JSON(http.StatusOK, gin.H{
		"summary": gin.H{
			"requests": agg.Requests, "errors": agg.Errors, "retries": agg.Retries,
			"retry_rate": retryRate, "avg_ms": round2(agg.AvgMs),
			"avg_first_byte_ms": round2(agg.FirstByte), "cache_hit_rate": hitRate,
			"cost": agg.Cost,
		},
		"channels":  items,
		"models":    models,
		"incidents": incidents,
	})
}
