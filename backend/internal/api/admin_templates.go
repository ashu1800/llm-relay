package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/secure"
)

// registerTemplateRoutes 挂载渠道模板接口。
//
// 模板的作用是免去手抄：常见厂商的 base_url 与协议固定，
// 每建一个渠道都要重填一遍既容易写错也不必要。
func registerTemplateRoutes(g *gin.RouterGroup, s *Server) {
	r := g.Group("/channel-templates")
	r.GET("", s.listTemplates)
	r.POST("", s.createTemplate)
	r.PUT("/:id", s.updateTemplate)
	r.DELETE("/:id", s.deleteTemplate)
	// 用模板一键建渠道
	r.POST("/:id/apply", s.applyTemplate)
}

type templatePayload struct {
	Name      string        `json:"name"`
	Protocol  string        `json:"protocol"`
	BaseURL   string        `json:"base_url"`
	GroupID   uint          `json:"group_id"`
	ExtraConf model.JSONMap `json:"extra_config"`
	CustomMap model.JSONMap `json:"custom_mapping"`
}

func (s *Server) listTemplates(c *gin.Context) {
	var items []model.ChannelTemplate
	if err := s.deps.Store.DB().Order("id").Find(&items).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// validateTemplate 校验并归一化模板字段。
func validateTemplate(p *templatePayload) string {
	p.Name = strings.TrimSpace(p.Name)
	p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if p.Name == "" {
		return "name 必填"
	}
	if p.Protocol == "" {
		p.Protocol = model.ProtocolOpenAIChat
	}
	return ""
}

// isUniqueViolation 判断是否为唯一约束冲突，用于把数据库错误翻译成人能看懂的话。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate key") || strings.Contains(msg, "unique constraint")
}

func (s *Server) createTemplate(c *gin.Context) {
	var p templatePayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	if msg := validateTemplate(&p); msg != "" {
		writeUpstreamError(c, http.StatusBadRequest, msg, "invalid_request_error")
		return
	}
	tpl := model.ChannelTemplate{
		Name: p.Name, Protocol: p.Protocol, BaseURL: p.BaseURL,
		GroupID: p.GroupID, ExtraConfig: p.ExtraConf, CustomMap: p.CustomMap,
	}
	if err := s.deps.Store.DB().Create(&tpl).Error; err != nil {
		if isUniqueViolation(err) {
			writeUpstreamError(c, http.StatusConflict, "模板名已存在: "+p.Name, "invalid_request_error")
			return
		}
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, tpl)
}

func (s *Server) updateTemplate(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var p templatePayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	if msg := validateTemplate(&p); msg != "" {
		writeUpstreamError(c, http.StatusBadRequest, msg, "invalid_request_error")
		return
	}
	var tpl model.ChannelTemplate
	if err := s.deps.Store.DB().First(&tpl, id).Error; err != nil {
		writeUpstreamError(c, http.StatusNotFound, "模板不存在", "not_found_error")
		return
	}
	// 列名由 GORM 从 Go 字段名推导：CustomMap 对应 custom_map，
	// 与 API 上的 custom_mapping 不是一回事。写成 JSON 名会报列不存在。
	err := s.deps.Store.DB().Model(&tpl).Updates(map[string]any{
		"name": p.Name, "protocol": p.Protocol, "base_url": p.BaseURL,
		"group_id": p.GroupID, "extra_config": p.ExtraConf, "custom_map": p.CustomMap,
	}).Error
	if err != nil {
		if isUniqueViolation(err) {
			writeUpstreamError(c, http.StatusConflict, "模板名已存在: "+p.Name, "invalid_request_error")
			return
		}
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated": true})
}

func (s *Server) deleteTemplate(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	res := s.deps.Store.DB().Delete(&model.ChannelTemplate{}, id)
	if res.Error != nil {
		writeUpstreamError(c, http.StatusInternalServerError, res.Error.Error(), "internal_error")
		return
	}
	if res.RowsAffected == 0 {
		writeUpstreamError(c, http.StatusNotFound, "模板不存在", "not_found_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": res.RowsAffected})
}

// applyTemplatePayload 是「用模板建渠道」的入参。
// 模板只提供协议与 base_url，密钥必须由调用方现填——模板里不该存凭据。
type applyTemplatePayload struct {
	Name    string `json:"name"`
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url"`
	GroupID uint   `json:"group_id"`
	Weight  int    `json:"weight"`
}

func (s *Server) applyTemplate(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var p applyTemplatePayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}

	var tpl model.ChannelTemplate
	if err := s.deps.Store.DB().First(&tpl, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeUpstreamError(c, http.StatusNotFound, "模板不存在", "not_found_error")
			return
		}
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}

	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = tpl.Name
	}
	if strings.TrimSpace(p.APIKey) == "" {
		writeUpstreamError(c, http.StatusBadRequest, "api_key 必填", "invalid_request_error")
		return
	}
	baseURL := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if baseURL == "" {
		baseURL = tpl.BaseURL
	}
	if baseURL == "" {
		writeUpstreamError(c, http.StatusBadRequest, "模板未提供 base_url，请手动填写", "invalid_request_error")
		return
	}
	// 同名渠道会让日志与路由分析难以区分，直接拒绝
	var exists int64
	s.deps.Store.DB().Model(&model.Channel{}).Where("name = ?", name).Count(&exists)
	if exists > 0 {
		writeUpstreamError(c, http.StatusConflict, "已存在同名渠道: "+name, "invalid_request_error")
		return
	}

	enc, err := s.deps.Cipher.Encrypt(p.APIKey)
	if err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, "加密渠道密钥失败: "+err.Error(), "internal_error")
		return
	}

	groupID := p.GroupID
	if groupID == 0 {
		groupID = tpl.GroupID
	}
	if groupID == 0 {
		groupID = defaultGroupID(s)
	}
	weight := p.Weight
	if weight <= 0 {
		weight = 1
	}

	ch := model.Channel{
		Name: name, GroupID: groupID, ProviderID: 1,
		Protocol: tpl.Protocol, BaseURL: baseURL,
		APIKeyEnc: enc, APIKeyHint: secure.MaskKey(p.APIKey),
		Weight: weight, Enabled: true, MonitorType: "none",
		ExtraConfig: tpl.ExtraConfig, CustomMap: tpl.CustomMap,
		HealthStatus: "unknown",
	}
	if err := s.deps.Store.DB().Create(&ch).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"channel": ch, "template_id": tpl.ID})
}
