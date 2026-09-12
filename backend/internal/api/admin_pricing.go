package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/model"
	"llm-relay/internal/pricing"
)

func registerPricingRoutes(g *gin.RouterGroup, s *Server) {
	r := g.Group("/pricing")
	r.GET("", s.listPricing)
	r.POST("", s.createPricing)
	r.PUT("/:id", s.updatePricing)
	r.DELETE("/:id", s.deletePricing)
	r.POST("/sync", s.syncPricing)
	r.GET("/history", s.pricingHistory)
	r.POST("/resolve", s.resolvePricing)
}

func (s *Server) listPricing(c *gin.Context) {
	q := s.deps.Store.DB().Model(&model.ModelPricing{})
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		q = q.Where("model_key ILIKE ?", "%"+kw+"%")
	}
	if src := c.Query("source"); src != "" {
		q = q.Where("source = ?", src)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}

	page, _ := strconv.Atoi(orDefault(c.Query("page"), "1"))
	size, _ := strconv.Atoi(orDefault(c.Query("page_size"), "50"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 500 {
		size = 50
	}

	var items []model.ModelPricing
	if err := q.Order("model_key").Offset((page - 1) * size).Limit(size).Find(&items).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

type pricingPayload struct {
	ModelKey        string         `json:"model_key"`
	MatchType       string         `json:"match_type"`
	InputPer1M      string         `json:"input_per_1m"`
	OutputPer1M     string         `json:"output_per_1m"`
	CacheReadPer1M  string         `json:"cache_read_per_1m"`
	CacheWritePer1M string         `json:"cache_write_per_1m"`
	PeakRules       model.JSONList `json:"peak_rules"`
}

// createPricing 手工录入价格。手工来源优先级最高，不会被自动同步覆盖。
func (s *Server) createPricing(c *gin.Context) {
	var p pricingPayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	row, err := buildPricingRow(p)
	if err != nil {
		writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	row.Source = pricing.SourceManual
	row.Priority = pricing.PriorityManual
	row.Currency = "USD"
	row.Active = true
	if row.MatchType == "" {
		row.MatchType = "exact"
	}
	if err := s.deps.Store.DB().Create(&row).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	s.refreshPricing()
	c.JSON(http.StatusOK, row)
}

func (s *Server) updatePricing(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var p pricingPayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	updates := map[string]any{}
	if p.ModelKey != "" {
		updates["model_key"] = p.ModelKey
	}
	if p.MatchType != "" {
		updates["match_type"] = p.MatchType
	}
	for k, v := range map[string]string{
		"input_per_1m": p.InputPer1M, "output_per_1m": p.OutputPer1M,
		"cache_read_per_1m": p.CacheReadPer1M, "cache_write_per_1m": p.CacheWritePer1M,
	} {
		if strings.TrimSpace(v) == "" {
			continue
		}
		d, err := parseDecimal(v)
		if err != nil {
			writeUpstreamError(c, http.StatusBadRequest, k+" 不是合法数字", "invalid_request_error")
			return
		}
		updates[k] = d
	}
	if p.PeakRules != nil {
		updates["peak_rules"] = p.PeakRules
	}
	// 手工改动后提升为最高优先级，避免下次同步把人工修正冲掉
	updates["source"] = pricing.SourceManual
	updates["priority"] = pricing.PriorityManual

	if err := s.deps.Store.DB().Model(&model.ModelPricing{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	s.refreshPricing()
	c.JSON(http.StatusOK, gin.H{"id": id, "updated": len(updates)})
}

func (s *Server) deletePricing(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := s.deps.Store.DB().Delete(&model.ModelPricing{}, id).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	s.refreshPricing()
	c.JSON(http.StatusOK, gin.H{"id": id, "deleted": true})
}

// syncPricing 触发一次全量同步。耗时取决于外网速度，给足超时。
func (s *Server) syncPricing(c *gin.Context) {
	if s.deps.Syncer == nil {
		writeUpstreamError(c, http.StatusServiceUnavailable, "定价同步器未启用", "internal_error")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()

	results := s.deps.Syncer.SyncAll(ctx)
	s.refreshPricing()
	c.JSON(http.StatusOK, gin.H{"results": results})
}

func (s *Server) pricingHistory(c *gin.Context) {
	var items []model.PricingSyncLog
	if err := s.deps.Store.DB().Order("id DESC").Limit(30).Find(&items).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type resolvePayload struct {
	Model string `json:"model"`
	At    string `json:"at"`
}

// resolvePricing 演示某模型在指定时刻实际生效的单价，便于核对峰谷与来源。
func (s *Server) resolvePricing(c *gin.Context) {
	var p resolvePayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	if s.deps.Pricing == nil {
		writeUpstreamError(c, http.StatusServiceUnavailable, "计价引擎未启用", "internal_error")
		return
	}
	at := time.Now().UTC()
	if p.At != "" {
		t, err := time.Parse(time.RFC3339, p.At)
		if err != nil {
			writeUpstreamError(c, http.StatusBadRequest, "at 需为 RFC3339 时间", "invalid_request_error")
			return
		}
		at = t
	}
	price, ok := s.deps.Pricing.Resolve(c.Request.Context(), p.Model, at)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"found": false, "model": p.Model, "at": at.Format(time.RFC3339)})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"found": true, "at": at.Format(time.RFC3339), "snapshot": price.Snapshot(at),
	})
}

func (s *Server) refreshPricing() {
	if s.deps.Pricing != nil {
		s.deps.Pricing.Invalidate()
	}
}
