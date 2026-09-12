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

// templateUpdatePayload 是编辑模板的入参。
//
// 与创建不同，这里每个字段都是可选的：只写客户端真正提供的字段。
// 原实现无条件写 6 个字段，而前端只发 3 个（name/protocol/base_url），
// 于是「只改个名字」会把 group_id 清成 0、extra_config 与 custom_map 清成 {} ——
// 而 custom_map 是自定义鉴权渠道的唯一来源，extra_config 决定并发上限。
// 更麻烦的是 JSONMap 的 Valuer 把 nil map 序列化成 "{}" 而不是 NULL，
// 事后从数据上看不出被清过。
type templateUpdatePayload struct {
	Name      *string        `json:"name"`
	Protocol  *string        `json:"protocol"`
	BaseURL   *string        `json:"base_url"`
	GroupID   *uint          `json:"group_id"`
	ExtraConf *model.JSONMap `json:"extra_config"`
	CustomMap *model.JSONMap `json:"custom_mapping"`
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
	// group_id=0 不是合法分组。创建渠道时本来就有这个兜底，
	// 模板这边漏了：加上外键之后，不兜底会直接写不进去。
	groupID := p.GroupID
	if groupID == 0 {
		groupID = defaultGroupID(s)
	}
	tpl := model.ChannelTemplate{
		Name: p.Name, Protocol: p.Protocol, BaseURL: p.BaseURL,
		GroupID: groupID, ExtraConfig: p.ExtraConf, CustomMap: p.CustomMap,
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
	var p templateUpdatePayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}

	var tpl model.ChannelTemplate
	if err := s.deps.Store.DB().First(&tpl, id).Error; err != nil {
		writeUpstreamError(c, http.StatusNotFound, "模板不存在", "not_found_error")
		return
	}

	// 用「库里现有值 + 本次提供的字段」合成一份完整载荷走同一套校验，
	// 这样「只改名字」也能校验出与其它字段冲突的组合，且复用归一化逻辑
	merged := templatePayload{
		Name: tpl.Name, Protocol: tpl.Protocol, BaseURL: tpl.BaseURL,
		GroupID: tpl.GroupID, ExtraConf: tpl.ExtraConfig, CustomMap: tpl.CustomMap,
	}
	if p.Name != nil {
		merged.Name = *p.Name
	}
	if p.Protocol != nil {
		merged.Protocol = *p.Protocol
	}
	if p.BaseURL != nil {
		merged.BaseURL = *p.BaseURL
	}
	if p.GroupID != nil {
		merged.GroupID = *p.GroupID
	}
	if p.ExtraConf != nil {
		merged.ExtraConf = *p.ExtraConf
	}
	if p.CustomMap != nil {
		merged.CustomMap = *p.CustomMap
	}
	if merged.GroupID == 0 {
		// 显式传 0 与不传都要落到真实分组上，不能留下悬挂引用
		merged.GroupID = defaultGroupID(s)
	}
	if msg := validateTemplate(&merged); msg != "" {
		writeUpstreamError(c, http.StatusBadRequest, msg, "invalid_request_error")
		return
	}

	// 只写客户端真正提供的字段；写归一化后的值（merged），不是原始入参
	//
	// 列名由 GORM 从 Go 字段名推导：CustomMap 对应 custom_map，
	// 与 API 上的 custom_mapping 不是一回事。写成 JSON 名会报列不存在。
	updates := map[string]any{}
	if p.Name != nil {
		updates["name"] = merged.Name
	}
	if p.Protocol != nil {
		updates["protocol"] = merged.Protocol
	}
	if p.BaseURL != nil {
		updates["base_url"] = merged.BaseURL
	}
	if p.GroupID != nil {
		updates["group_id"] = merged.GroupID
	}
	if p.ExtraConf != nil {
		updates["extra_config"] = merged.ExtraConf
	}
	if p.CustomMap != nil {
		updates["custom_map"] = merged.CustomMap
	}
	if len(updates) == 0 {
		writeUpstreamError(c, http.StatusBadRequest, "没有需要更新的字段", "invalid_request_error")
		return
	}

	if err := applyUpdates(s.deps.Store.DB(), &model.ChannelTemplate{}, id, updates); err != nil {
		if isUniqueViolation(err) {
			writeUpstreamError(c, http.StatusConflict, "模板名已存在: "+merged.Name, "invalid_request_error")
			return
		}
		writeUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated": len(updates)})
}

func (s *Server) deleteTemplate(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	// 这是六个删除接口里唯一原本就检查 RowsAffected 的；改用共用助手后
	// 连同返回体也统一成 {"id":..,"deleted":true}
	deleteByID(c, s.deps.Store.DB(), &model.ChannelTemplate{}, id, "模板不存在")
}

// applyTemplatePayload 是「用模板建渠道」的入参。
// 模板只提供协议与 base_url，密钥必须由调用方现填——模板里不该存凭据。
// Models 是随渠道一起写入的模型白名单，可不传。
type applyTemplatePayload struct {
	Name    string           `json:"name"`
	APIKey  string           `json:"api_key"`
	BaseURL string           `json:"base_url"`
	GroupID uint             `json:"group_id"`
	Weight  int              `json:"weight"`
	Models  *[]whitelistItem `json:"models"`
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

	var whitelist []model.ChannelModel
	if p.Models != nil {
		items, err := normalizeWhitelist(*p.Models)
		if err != nil {
			writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
		whitelist = items
	}

	ch := model.Channel{
		Name: name, GroupID: groupID,
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
	// 模板可以带一份默认白名单（如「DeepSeek 官方」带 deepseek-chat 的映射），
	// 这样用模板建的渠道建完就能用，不必再逐个添模型
	if p.Models != nil {
		if err := replaceChannelModels(s.deps.Store.DB(), ch.ID, whitelist); err != nil {
			writeUpstreamError(c, http.StatusInternalServerError, "写入模型白名单失败: "+err.Error(), "internal_error")
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"channel": ch, "template_id": tpl.ID})
}
