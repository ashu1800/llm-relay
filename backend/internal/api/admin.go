package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/secure"
)

// applyUpdates 按 id 执行局部更新，没有任何行被命中时返回 ErrRecordNotFound。
//
// 不能用 len(updates) 当成功标志：像定价那样总有几个无条件字段，
// 即使 id 不存在也会返回「更新成功」，把「改错了对象」这件事掩盖过去。
func applyUpdates(db *gorm.DB, dest any, id uint, updates map[string]any) error {
	res := db.Model(dest).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// writeUpdateError 把更新失败翻译成响应；找不到记录时给 404 而不是 500。
func writeUpdateError(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeUpstreamError(c, http.StatusNotFound, "记录不存在", "not_found_error")
		return
	}
	writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
}

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

	if err := applyUpdates(s.deps.Store.DB(), &model.Channel{}, id, updates); err != nil {
		writeUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated": len(updates)})
}

// modelAllowed 判断模型是否在密钥白名单内。白名单为空表示不限制。
func modelAllowed(list model.StringList, name string) bool {
	if len(list) == 0 {
		return true
	}
	for _, m := range list {
		if m == name {
			return true
		}
	}
	return false
}

// resolveGroupWhitelist 把密钥上的分组白名单解析成分组 ID。
//
// 白名单里写 ID 或分组名都认：纯数字按 ID，否则按名字查。
// 一条都解析不出来时返回错误而不是放行 —— 放行等于「删掉那个分组就能绕过限制」，
// 那这份白名单也就没有存在的意义了。调用方据此回 403 并说明原因。
func (s *Server) resolveGroupWhitelist(list model.StringList) ([]uint, error) {
	if len(list) == 0 {
		return nil, nil
	}
	db := s.deps.Store.DB()
	ids := make([]uint, 0, len(list))
	var unresolved []string
	for _, raw := range list {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		if n, err := strconv.ParseUint(item, 10, 32); err == nil {
			ids = append(ids, uint(n))
			continue
		}
		var g model.ChannelGroup
		if err := db.Where("name = ?", item).First(&g).Error; err != nil {
			unresolved = append(unresolved, item)
			continue
		}
		ids = append(ids, g.ID)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("密钥的分组白名单 %v 无法解析（分组可能已被删除或改名）", unresolved)
	}
	return ids, nil
}

// deleteByID 删除主表行，并按 RowsAffected 区分「删掉了」与「本来就没有」。
//
// 原来五个删除接口对不存在的 ID 一律返回 {"deleted":true}，调用方（含前端）
// 分不清「删除成功」和「这个 ID 根本不存在」：幂等重试、并发删除、
// 传错 ID 全被当成成功，问题被静默吞掉。六个接口里只有 deleteTemplate 做对了，
// 这里统一成一致的行为。
func deleteByID(c *gin.Context, db *gorm.DB, dest any, id uint, notFound string) bool {
	res := db.Delete(dest, id)
	if res.Error != nil {
		writeUpstreamError(c, http.StatusInternalServerError, res.Error.Error(), "internal_error")
		return false
	}
	if res.RowsAffected == 0 {
		writeUpstreamError(c, http.StatusNotFound, notFound, "not_found_error")
		return false
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "deleted": true})
	return true
}

func (s *Server) deleteChannel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	db := s.deps.Store.DB()
	// 先确认主表行存在，再动关联表：否则会删掉绑定却没删渠道，
	// 调用方还收到一个「成功」
	var ch model.Channel
	if err := db.First(&ch, id).Error; err != nil {
		writeUpstreamError(c, http.StatusNotFound, "渠道不存在", "not_found_error")
		return
	}
	// 关联行与主表行必须在同一个事务里：中途失败会留下没有归属的绑定
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("channel_id = ?", id).Delete(&model.ChannelModel{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Channel{}, id).Error
	}); err != nil {
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
	// 渠道不存在时直接 404，而不是回一个空列表。
	// 空列表会被读成「这个渠道一条绑定都没有」，与「渠道根本不存在」是两回事，
	// 排查故障时很容易被误导。
	var ch model.Channel
	if err := s.deps.Store.DB().First(&ch, id).Error; err != nil {
		writeUpstreamError(c, http.StatusNotFound, "渠道不存在", "not_found_error")
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
	if err := applyUpdates(s.deps.Store.DB(), &model.ChannelGroup{}, id, updates); err != nil {
		writeUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated": len(updates)})
}

func (s *Server) deleteGroup(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	db := s.deps.Store.DB()

	// 原来写的是 err == nil && count > 0：查询失败时条件为假，
	// 于是「查不出来」被当成「没有渠道占用」，直接把一个仍在使用的分组删掉。
	// 查不出来就不该往下删。
	var count int64
	if err := db.Model(&model.Channel{}).Where("group_id = ?", id).Count(&count).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	if count > 0 {
		writeUpstreamError(c, http.StatusConflict, "该分组下仍有渠道，请先迁移", "invalid_request_error")
		return
	}
	// 模板也带 group_id，漏掉它会留下指向已删分组的模板
	var tplCount int64
	if err := db.Model(&model.ChannelTemplate{}).Where("group_id = ?", id).Count(&tplCount).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	if tplCount > 0 {
		writeUpstreamError(c, http.StatusConflict, "该分组下仍有模板，请先迁移", "invalid_request_error")
		return
	}
	deleteByID(c, db, &model.ChannelGroup{}, id, "分组不存在")
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

// modelPayload 是模型的写入载荷。
//
// 除名称外用指针接收：只有客户端真的传了某个字段才更新它。
// 若直接用值类型，未传的字段会以零值参与更新——只改个名字就会把
// enabled 悄悄改成 false，或者把模型商改成 0。
type modelPayload struct {
	PublicName  string  `json:"public_name"`
	Description *string `json:"description"`
	ProviderID  *uint   `json:"provider_id"`
	Enabled     *bool   `json:"enabled"`
}

// modelUpdates 把「显式提供」的字段转成更新映射。
// 抽成纯函数是为了能直接测——这里漏掉一个字段不会报错，
// 只会表现为界面上改了却没生效。
func modelUpdates(p modelPayload) map[string]any {
	updates := map[string]any{}
	if name := strings.TrimSpace(p.PublicName); name != "" {
		updates["public_name"] = name
	}
	if p.Description != nil {
		updates["description"] = *p.Description
	}
	if p.ProviderID != nil {
		updates["provider_id"] = *p.ProviderID
	}
	if p.Enabled != nil {
		updates["enabled"] = *p.Enabled
	}
	return updates
}

// providerExists 校验模型商存在，避免模型挂到一个不存在的模型商上。
func (s *Server) providerExists(id uint) bool {
	var n int64
	s.deps.Store.DB().Model(&model.Provider{}).Where("id = ?", id).Count(&n)
	return n > 0
}

func (s *Server) createModel(c *gin.Context) {
	var p modelPayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	name := strings.TrimSpace(p.PublicName)
	if name == "" {
		writeUpstreamError(c, http.StatusBadRequest, "public_name 必填", "invalid_request_error")
		return
	}
	m := model.Model{PublicName: name, ProviderID: 1, Enabled: true}
	if p.Description != nil {
		m.Description = *p.Description
	}
	if p.ProviderID != nil {
		m.ProviderID = *p.ProviderID
	}
	if p.Enabled != nil {
		m.Enabled = *p.Enabled
	}
	if !s.providerExists(m.ProviderID) {
		writeUpstreamError(c, http.StatusBadRequest,
			"模型商不存在: "+strconv.FormatUint(uint64(m.ProviderID), 10), "invalid_request_error")
		return
	}
	if err := s.deps.Store.DB().Create(&m).Error; err != nil {
		if isUniqueViolation(err) {
			writeUpstreamError(c, http.StatusConflict, "模型名已存在: "+name, "invalid_request_error")
			return
		}
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, m)
}

func (s *Server) updateModel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var p modelPayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	updates := modelUpdates(p)
	if len(updates) == 0 {
		writeUpstreamError(c, http.StatusBadRequest, "没有需要更新的字段", "invalid_request_error")
		return
	}
	if p.ProviderID != nil && !s.providerExists(*p.ProviderID) {
		writeUpstreamError(c, http.StatusBadRequest,
			"模型商不存在: "+strconv.FormatUint(uint64(*p.ProviderID), 10), "invalid_request_error")
		return
	}
	if err := applyUpdates(s.deps.Store.DB(), &model.Model{}, id, updates); err != nil {
		if isUniqueViolation(err) {
			writeUpstreamError(c, http.StatusConflict, "模型名已存在", "invalid_request_error")
			return
		}
		writeUpdateError(c, err)
		return
	}
	// 如实回报改了哪些字段，便于排查「界面改了没生效」
	changed := make([]string, 0, len(updates))
	for k := range updates {
		changed = append(changed, k)
	}
	sort.Strings(changed)
	c.JSON(http.StatusOK, gin.H{"id": id, "updated": len(updates), "fields": changed})
}

func (s *Server) deleteModel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	db := s.deps.Store.DB()
	// 同 deleteChannel：先确认主表行存在，关联行与主表行同一个事务
	var m model.Model
	if err := db.First(&m, id).Error; err != nil {
		writeUpstreamError(c, http.StatusNotFound, "模型不存在", "not_found_error")
		return
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("model_id = ?", id).Delete(&model.ChannelModel{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Model{}, id).Error
	}); err != nil {
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
	// 用指针区分「没传」与「显式设为 0」
	RateLimitRPM *int `json:"rate_limit_rpm"`
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
	if p.RateLimitRPM != nil {
		k.RateLimitRPM = *p.RateLimitRPM
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
	if p.RateLimitRPM != nil {
		updates["rate_limit_rpm"] = *p.RateLimitRPM
	}
	// 白名单此前只有创建路径会写，更新路径静默丢弃：
	// 传 {enabled:true, allowed_models:[...]} 会返回 updated:1 但白名单纹丝不动。
	// 用 nil 判断「没传」—— 空数组是有效值（表示清空白名单），不能当成没传。
	if p.AllowedModels != nil {
		updates["allowed_models"] = p.AllowedModels
	}
	if p.AllowedGroups != nil {
		updates["allowed_groups"] = p.AllowedGroups
	}
	if len(updates) == 0 {
		writeUpstreamError(c, http.StatusBadRequest, "没有需要更新的字段", "invalid_request_error")
		return
	}
	if err := applyUpdates(s.deps.Store.DB(), &model.APIKey{}, id, updates); err != nil {
		writeUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated": len(updates)})
}

func (s *Server) deleteKey(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	deleteByID(c, s.deps.Store.DB(), &model.APIKey{}, id, "密钥不存在")
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
	// 未留存时报文返回 null，前端据此区分「没有留存」与「留存了空内容」
	var stored model.RequestPayload
	var payload *model.RequestPayload
	if err := s.deps.Store.DB().Where("log_id = ?", id).First(&stored).Error; err == nil {
		payload = &stored
	}
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
