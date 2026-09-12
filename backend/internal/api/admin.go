package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/secure"
)

func parseID(c *gin.Context) (uint, bool) {
	raw := c.Param("id")
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		writeUpstreamError(c, http.StatusBadRequest, "非法的 ID: "+raw, "invalid_request_error")
		return 0, false
	}
	return uint(n), true
}

// ============================ 渠道 ============================

func registerChannelRoutes(g *gin.RouterGroup, s *Server) {
	r := g.Group("/channels")
	r.GET("", s.listChannels)
	r.POST("", s.createChannel)
	r.PUT("/:id", s.updateChannel)
	r.DELETE("/:id", s.deleteChannel)
	r.GET("/:id/models", s.listChannelModels)
	r.POST("/:id/models", s.bindChannelModel)
	r.DELETE("/:id/models/:bindingId", s.unbindChannelModel)
}

type channelPayload struct {
	Name       string         `json:"name"`
	GroupID    uint           `json:"group_id"`
	ProviderID uint           `json:"provider_id"`
	Protocol   string         `json:"protocol"`
	BaseURL    string         `json:"base_url"`
	APIKey     string         `json:"api_key"`
	Weight     int            `json:"weight"`
	Enabled    *bool          `json:"enabled"`
	Slots      model.JSONList `json:"available_slots"`
	ExtraConf  model.JSONMap  `json:"extra_config"`
	CustomMap  model.JSONMap  `json:"custom_mapping"`
	Monitor    string         `json:"monitor_type"`
}

func (s *Server) listChannels(c *gin.Context) {
	var items []model.Channel
	q := s.deps.Store.DB().Order("id DESC")
	if gid := c.Query("group_id"); gid != "" {
		q = q.Where("group_id = ?", gid)
	}
	if err := q.Find(&items).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

func (s *Server) createChannel(c *gin.Context) {
	var p channelPayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	if strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.BaseURL) == "" {
		writeUpstreamError(c, http.StatusBadRequest, "name 与 base_url 必填", "invalid_request_error")
		return
	}
	if p.Protocol == "" {
		p.Protocol = model.ProtocolOpenAIChat
	}
	if p.Weight <= 0 {
		p.Weight = 1
	}
	if p.GroupID == 0 {
		p.GroupID = defaultGroupID(s)
	}
	if p.ProviderID == 0 {
		p.ProviderID = 1
	}

	enc, err := s.deps.Cipher.Encrypt(strings.TrimSpace(p.APIKey))
	if err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, "密钥加密失败: "+err.Error(), "internal_error")
		return
	}

	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	ch := model.Channel{
		Name: p.Name, GroupID: p.GroupID, ProviderID: p.ProviderID,
		Protocol: p.Protocol, BaseURL: strings.TrimRight(strings.TrimSpace(p.BaseURL), "/"),
		APIKeyEnc: enc, APIKeyHint: secure.MaskKey(p.APIKey),
		Weight: p.Weight, Enabled: enabled, MonitorType: orDefault(p.Monitor, "none"),
		Slots: p.Slots, ExtraConfig: p.ExtraConf, CustomMap: p.CustomMap,
		HealthStatus: "unknown",
	}
	if err := s.deps.Store.DB().Create(&ch).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, ch)
}

func (s *Server) updateChannel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var p channelPayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}

	updates := map[string]any{}
	if p.Name != "" {
		updates["name"] = p.Name
	}
	if p.BaseURL != "" {
		updates["base_url"] = strings.TrimRight(p.BaseURL, "/")
	}
	if p.Protocol != "" {
		updates["protocol"] = p.Protocol
	}
	if p.GroupID != 0 {
		updates["group_id"] = p.GroupID
	}
	if p.ProviderID != 0 {
		updates["provider_id"] = p.ProviderID
	}
	if p.Weight > 0 {
		updates["weight"] = p.Weight
	}
	if p.Enabled != nil {
		updates["enabled"] = *p.Enabled
	}
	if p.Slots != nil {
		updates["slots"] = p.Slots
	}
	if p.ExtraConf != nil {
		updates["extra_config"] = p.ExtraConf
	}
	if p.CustomMap != nil {
		updates["custom_map"] = p.CustomMap
	}
	// 密钥留空表示不修改，避免误清空
	if strings.TrimSpace(p.APIKey) != "" {
		enc, err := s.deps.Cipher.Encrypt(strings.TrimSpace(p.APIKey))
		if err != nil {
			writeUpstreamError(c, http.StatusInternalServerError, "密钥加密失败: "+err.Error(), "internal_error")
			return
		}
		updates["api_key_enc"] = enc
		updates["api_key_hint"] = secure.MaskKey(p.APIKey)
	}
	if len(updates) == 0 {
		writeUpstreamError(c, http.StatusBadRequest, "没有需要更新的字段", "invalid_request_error")
		return
	}

	if err := s.deps.Store.DB().Model(&model.Channel{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated": len(updates)})
}

func (s *Server) deleteChannel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	db := s.deps.Store.DB()
	if err := db.Where("channel_id = ?", id).Delete(&model.ChannelModel{}).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	if err := db.Delete(&model.Channel{}, id).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "deleted": true})
}

func (s *Server) listChannelModels(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var rows []struct {
		model.ChannelModel
		PublicName string `json:"public_name"`
	}
	err := s.deps.Store.DB().Table("channel_models").
		Select("channel_models.*, models.public_name AS public_name").
		Joins("JOIN models ON models.id = channel_models.model_id").
		Where("channel_models.channel_id = ?", id).
		Scan(&rows).Error
	if err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows})
}

type bindPayload struct {
	PublicName   string `json:"public_name"`
	UpstreamName string `json:"upstream_name"`
}

// bindChannelModel 绑定模型到渠道。若对外模型不存在则自动创建（中转场景很常见）。
func (s *Server) bindChannelModel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var p bindPayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	if strings.TrimSpace(p.PublicName) == "" {
		writeUpstreamError(c, http.StatusBadRequest, "public_name 必填", "invalid_request_error")
		return
	}
	if p.UpstreamName == "" {
		p.UpstreamName = p.PublicName
	}

	db := s.deps.Store.DB()
	var m model.Model
	if err := db.Where("public_name = ?", p.PublicName).First(&m).Error; err != nil {
		if err != gorm.ErrRecordNotFound {
			writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
			return
		}
		m = model.Model{PublicName: p.PublicName, ProviderID: 1, Enabled: true}
		if err := db.Create(&m).Error; err != nil {
			writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
			return
		}
	}

	binding := model.ChannelModel{ChannelID: id, ModelID: m.ID, UpstreamName: p.UpstreamName, Enabled: true}
	if err := db.Where(model.ChannelModel{ChannelID: id, ModelID: m.ID}).
		Assign(model.ChannelModel{UpstreamName: p.UpstreamName, Enabled: true}).
		FirstOrCreate(&binding).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, binding)
}

func (s *Server) unbindChannelModel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	bid, err := strconv.ParseUint(c.Param("bindingId"), 10, 64)
	if err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "非法的绑定 ID", "invalid_request_error")
		return
	}
	if err := s.deps.Store.DB().Where("id = ? AND channel_id = ?", bid, id).
		Delete(&model.ChannelModel{}).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// ============================ 分组 ============================

func registerGroupRoutes(g *gin.RouterGroup, s *Server) {
	r := g.Group("/groups")
	r.GET("", s.listGroups)
	r.POST("", s.createGroup)
	r.PUT("/:id", s.updateGroup)
	r.DELETE("/:id", s.deleteGroup)
}

func (s *Server) listGroups(c *gin.Context) {
	var items []model.ChannelGroup
	if err := s.deps.Store.DB().Order("id").Find(&items).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

func (s *Server) createGroup(c *gin.Context) {
	var p model.ChannelGroup
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	if strings.TrimSpace(p.Name) == "" {
		writeUpstreamError(c, http.StatusBadRequest, "name 必填", "invalid_request_error")
		return
	}
	if p.Strategy == "" {
		p.Strategy = model.StrategyWeighted
	}
	p.ID = 0
	if err := s.deps.Store.DB().Create(&p).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, p)
}

func (s *Server) updateGroup(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var p model.ChannelGroup
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	updates := map[string]any{}
	if p.Name != "" {
		updates["name"] = p.Name
	}
	if p.Strategy != "" {
		updates["strategy"] = p.Strategy
	}
	if p.Remark != "" {
		updates["remark"] = p.Remark
	}
	if len(updates) == 0 {
		writeUpstreamError(c, http.StatusBadRequest, "没有需要更新的字段", "invalid_request_error")
		return
	}
	if err := s.deps.Store.DB().Model(&model.ChannelGroup{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated": len(updates)})
}

func (s *Server) deleteGroup(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var count int64
	if err := s.deps.Store.DB().Model(&model.Channel{}).Where("group_id = ?", id).Count(&count).Error; err == nil && count > 0 {
		writeUpstreamError(c, http.StatusConflict, "该分组下仍有渠道，请先迁移", "invalid_request_error")
		return
	}
	if err := s.deps.Store.DB().Delete(&model.ChannelGroup{}, id).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "deleted": true})
}

// ============================ 模型 ============================

func registerModelRoutes(g *gin.RouterGroup, s *Server) {
	r := g.Group("/models")
	r.GET("", s.listModelsAdmin)
	r.POST("", s.createModel)
	r.PUT("/:id", s.updateModel)
	r.DELETE("/:id", s.deleteModel)
	r.GET("/providers", s.listProviders)
}

func (s *Server) listModelsAdmin(c *gin.Context) {
	var items []model.Model
	if err := s.deps.Store.DB().Order("public_name").Find(&items).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

func (s *Server) listProviders(c *gin.Context) {
	var items []model.Provider
	if err := s.deps.Store.DB().Order("id").Find(&items).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) createModel(c *gin.Context) {
	var p model.Model
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	if strings.TrimSpace(p.PublicName) == "" {
		writeUpstreamError(c, http.StatusBadRequest, "public_name 必填", "invalid_request_error")
		return
	}
	p.ID = 0
	if p.ProviderID == 0 {
		p.ProviderID = 1
	}
	if err := s.deps.Store.DB().Create(&p).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, p)
}

func (s *Server) updateModel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var p model.Model
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	updates := map[string]any{}
	if p.PublicName != "" {
		updates["public_name"] = p.PublicName
	}
	if p.Description != "" {
		updates["description"] = p.Description
	}
	updates["enabled"] = p.Enabled
	if err := s.deps.Store.DB().Model(&model.Model{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated": len(updates)})
}

func (s *Server) deleteModel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	db := s.deps.Store.DB()
	if err := db.Where("model_id = ?", id).Delete(&model.ChannelModel{}).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	if err := db.Delete(&model.Model{}, id).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "deleted": true})
}

// ============================ 密钥 ============================

func registerKeyRoutes(g *gin.RouterGroup, s *Server) {
	r := g.Group("/keys")
	r.GET("", s.listKeys)
	r.POST("", s.createKey)
	r.PUT("/:id", s.updateKey)
	r.DELETE("/:id", s.deleteKey)
}

func (s *Server) listKeys(c *gin.Context) {
	var items []model.APIKey
	if err := s.deps.Store.DB().Order("id DESC").Find(&items).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

type keyPayload struct {
	Name          string           `json:"name"`
	Enabled       *bool            `json:"enabled"`
	AllowedModels model.StringList `json:"allowed_models"`
	AllowedGroups model.StringList `json:"allowed_groups"`
}

// createKey 只在创建时返回一次明文，之后只保留哈希与掩码。
func (s *Server) createKey(c *gin.Context) {
	var p keyPayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	if strings.TrimSpace(p.Name) == "" {
		writeUpstreamError(c, http.StatusBadRequest, "name 必填", "invalid_request_error")
		return
	}
	plain, err := secure.GenerateAPIKey()
	if err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	k := model.APIKey{
		Name: p.Name, KeyHash: secure.HashKey(plain), KeyPrefix: plain[:11],
		Enabled: true, AllowedModels: p.AllowedModels, AllowedGroups: p.AllowedGroups,
	}
	if p.Enabled != nil {
		k.Enabled = *p.Enabled
	}
	if err := s.deps.Store.DB().Create(&k).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id": k.ID, "name": k.Name, "key": plain,
		"notice": "请立即保存，明文不会再次展示",
	})
}

func (s *Server) updateKey(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var p keyPayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	updates := map[string]any{}
	if p.Name != "" {
		updates["name"] = p.Name
	}
	if p.Enabled != nil {
		updates["enabled"] = *p.Enabled
	}
	if len(updates) == 0 {
		writeUpstreamError(c, http.StatusBadRequest, "没有需要更新的字段", "invalid_request_error")
		return
	}
	if err := s.deps.Store.DB().Model(&model.APIKey{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated": len(updates)})
}

func (s *Server) deleteKey(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := s.deps.Store.DB().Delete(&model.APIKey{}, id).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "deleted": true})
}

// ============================ 日志 ============================

func registerLogRoutes(g *gin.RouterGroup, s *Server) {
	r := g.Group("/logs")
	r.GET("", s.listLogs)
	r.GET("/:id", s.logDetail)
}

func (s *Server) listLogs(c *gin.Context) {
	page, _ := strconv.Atoi(orDefault(c.Query("page"), "1"))
	size, _ := strconv.Atoi(orDefault(c.Query("page_size"), "50"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}

	q := s.deps.Store.DB().Model(&model.RequestLog{})
	if m := c.Query("model"); m != "" {
		q = q.Where("model_requested = ?", m)
	}
	if st := c.Query("status"); st != "" {
		q = q.Where("status_code = ?", st)
	}
	if cid := c.Query("channel_id"); cid != "" {
		q = q.Where("channel_id = ?", cid)
	}
	if since := c.Query("since"); since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			q = q.Where("created_at >= ?", t)
		}
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	var items []model.RequestLog
	if err := q.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&items).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

func (s *Server) logDetail(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var entry model.RequestLog
	if err := s.deps.Store.DB().First(&entry, id).Error; err != nil {
		writeUpstreamError(c, http.StatusNotFound, "日志不存在", "not_found_error")
		return
	}
	var payload model.RequestPayload
	_ = s.deps.Store.DB().Where("log_id = ?", id).First(&payload).Error
	c.JSON(http.StatusOK, gin.H{"log": entry, "payload": payload})
}

// ---------- 小工具 ----------

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func defaultGroupID(s *Server) uint {
	var g model.ChannelGroup
	if err := s.deps.Store.DB().Where("is_default = ?", true).First(&g).Error; err == nil {
		return g.ID
	}
	return 1
}
