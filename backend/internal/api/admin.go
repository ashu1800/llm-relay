package api

import (
	"errors"
	"fmt"
	"net/http"
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
	// 整表替换是界面用主路径：白名单在表单里就是一张表，一次提交一整张
	r.PUT("/:id/models", s.replaceChannelModelsAPI)
	r.POST("/:id/models", s.bindChannelModel)
	r.DELETE("/:id/models/:bindingId", s.unbindChannelModel)
}

// whitelistItem 是渠道模型白名单的一行：客户端请求 PublicName，
// 转发时替换成 UpstreamName（留空则同名）。
type whitelistItem struct {
	PublicName   string `json:"public_name"`
	UpstreamName string `json:"upstream_name"`
	Enabled      *bool  `json:"enabled"`
}

// channelPayload 是渠道的写入载荷。
//
// Models 用指针接收整个白名单：nil 表示「这次不动白名单」，
// 空数组表示「清空白名单」—— 若用值类型，这两件事都是 len=0，分不开，
// 于是「把最后一条删掉」会静默失效。
type channelPayload struct {
	Name     string `json:"name"`
	GroupID  uint   `json:"group_id"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url"`
	APIKey   string `json:"api_key"`
	Weight   int    `json:"weight"`
	// ProxyID 走哪个出站代理；0 = 直连。
	//
	// 必须是指针：0 既是「直连」也是 uint 的零值，用值类型就分不出
	// 「改成直连」与「这次请求不提代理这件事」。实测踩过 ——
	// 用户把渠道从代理改回直连时传 proxy_id=0，后端当成「没传」忽略掉，
	// 界面上显示已保存、库里还指着那个代理。与模型白名单要用指针是同一类坑。
	ProxyID   *uint            `json:"proxy_id"`
	Enabled   *bool            `json:"enabled"`
	Slots     model.JSONList   `json:"available_slots"`
	ExtraConf model.JSONMap    `json:"extra_config"`
	CustomMap model.JSONMap    `json:"custom_mapping"`
	Monitor   string           `json:"monitor_type"`
	Models    *[]whitelistItem `json:"models"`
}

// normalizeWhitelist 校验并规整白名单：对外名必填、同渠道内不重复、上游名留空则同名。
func normalizeWhitelist(items []whitelistItem) ([]model.ChannelModel, error) {
	out := make([]model.ChannelModel, 0, len(items))
	seen := map[string]bool{}
	for _, it := range items {
		name := strings.TrimSpace(it.PublicName)
		if name == "" {
			return nil, errors.New("模型白名单里有空的对外模型名")
		}
		if seen[name] {
			return nil, errors.New("模型白名单里对外名重复: " + name)
		}
		seen[name] = true
		upstream := strings.TrimSpace(it.UpstreamName)
		if upstream == "" {
			upstream = name
		}
		enabled := true
		if it.Enabled != nil {
			enabled = *it.Enabled
		}
		out = append(out, model.ChannelModel{PublicName: name, UpstreamName: upstream, Enabled: enabled})
	}
	return out, nil
}

// replaceChannelModels 用给定白名单整体替换某个渠道的条目。
// 整体替换而不是逐条 diff：白名单在界面上就是一张表，一次提交一整张表，
// 不会出现「删了两条、加了一条，结果只生效一半」的中间状态。
func replaceChannelModels(tx *gorm.DB, channelID uint, items []model.ChannelModel) error {
	if err := tx.Where("channel_id = ?", channelID).Delete(&model.ChannelModel{}).Error; err != nil {
		return err
	}
	for i := range items {
		items[i].ID = 0
		items[i].ChannelID = channelID
		if err := tx.Create(&items[i]).Error; err != nil {
			return err
		}
	}
	return nil
}

// channelListItem 在渠道字段之外带上模型白名单摘要。
//
// 列表里直接能看到「这条渠道能跑哪些模型」很重要：白名单是模型存在的唯一依据，
// 而「渠道建好了但白名单是空的」正是最容易被忽略、又只表现为请求 502 的状态。
type channelListItem struct {
	model.Channel
	Models     []string `json:"models"`
	ModelCount int      `json:"model_count"`
}

func (s *Server) listChannels(c *gin.Context) {
	db := s.deps.Store.DB()
	var channels []model.Channel
	q := db.Order("id DESC")
	if gid := c.Query("group_id"); gid != "" {
		q = q.Where("group_id = ?", gid)
	}
	if err := q.Find(&channels).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}

	ids := make([]uint, 0, len(channels))
	for _, ch := range channels {
		ids = append(ids, ch.ID)
	}
	names := map[uint][]string{}
	if len(ids) > 0 {
		var rows []model.ChannelModel
		if err := db.Where("channel_id IN ?", ids).Order("public_name").Find(&rows).Error; err != nil {
			writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
			return
		}
		for _, r := range rows {
			names[r.ChannelID] = append(names[r.ChannelID], r.PublicName)
		}
	}

	items := make([]channelListItem, 0, len(channels))
	for _, ch := range channels {
		list := names[ch.ID]
		if list == nil {
			list = []string{}
		}
		items = append(items, channelListItem{Channel: ch, Models: list, ModelCount: len(list)})
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
	// 代理存在性在这里校验：填一个不存在的 id，转发时才发现的话，
	// 表现是「渠道莫名其妙不通」，而配置看起来完全正常
	proxyID := uint(0)
	if p.ProxyID != nil {
		proxyID = *p.ProxyID
	}
	if err := checkProxyExists(s, proxyID); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	// 白名单在这里就校验：等到写完渠道再报错，用户得重填一遍表单
	var whitelist []model.ChannelModel
	if p.Models != nil {
		items, err := normalizeWhitelist(*p.Models)
		if err != nil {
			writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
		whitelist = items
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
		Name: p.Name, GroupID: p.GroupID,
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
	// 白名单随渠道一起建：建渠道时就能把「这条渠道能跑哪些模型」填完，
	// 不用再回列表点一次「模型」
	if p.Models != nil {
		if err := replaceChannelModels(s.deps.Store.DB(), ch.ID, whitelist); err != nil {
			writeUpstreamError(c, http.StatusInternalServerError, "写入模型白名单失败: "+err.Error(), "internal_error")
			return
		}
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
	if p.Weight > 0 {
		updates["weight"] = p.Weight
	}
	if p.ProxyID != nil {
		if err := checkProxyExists(s, *p.ProxyID); err != nil {
			writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
		updates["proxy_id"] = *p.ProxyID
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
	// 只改白名单（改完模型点保存）也是合法请求，所以不能只看 updates 是否为空
	if len(updates) == 0 && p.Models == nil {
		writeUpstreamError(c, http.StatusBadRequest, "没有需要更新的字段", "invalid_request_error")
		return
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

	// 渠道字段与白名单放同一个事务：白名单是整表替换，
	// 中途失败留下「渠道改了、白名单没改」会让人以为保存没生效
	err := s.deps.Store.DB().Transaction(func(tx *gorm.DB) error {
		if len(updates) > 0 {
			if err := applyUpdates(tx, &model.Channel{}, id, updates); err != nil {
				return err
			}
		} else {
			// 一个渠道字段都不改时 applyUpdates 无事可做，但仍要确认渠道存在，
			// 否则会给一个不存在的 id 建出一堆孤儿白名单
			var n int64
			if err := tx.Model(&model.Channel{}).Where("id = ?", id).Count(&n).Error; err != nil {
				return err
			}
			if n == 0 {
				return gorm.ErrRecordNotFound
			}
		}
		if p.Models != nil {
			return replaceChannelModels(tx, id, whitelist)
		}
		return nil
	})
	if err != nil {
		writeUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated": len(updates), "models_replaced": p.Models != nil})
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
	var rows []model.ChannelModel
	if err := s.deps.Store.DB().Where("channel_id = ?", id).
		Order("public_name").Find(&rows).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows, "total": len(rows)})
}

type bindPayload struct {
	PublicName   string `json:"public_name"`
	UpstreamName string `json:"upstream_name"`
	Enabled      *bool  `json:"enabled"`
}

func (p bindPayload) whitelistItem() whitelistItem {
	return whitelistItem{PublicName: p.PublicName, UpstreamName: p.UpstreamName, Enabled: p.Enabled}
}

// replaceChannelModelsAPI 用请求体里的整张白名单替换该渠道的条目。
func (s *Server) replaceChannelModelsAPI(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var p struct {
		Items []whitelistItem `json:"items"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	items, err := normalizeWhitelist(p.Items)
	if err != nil {
		writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	db := s.deps.Store.DB()
	var ch model.Channel
	if err := db.First(&ch, id).Error; err != nil {
		writeUpstreamError(c, http.StatusNotFound, "渠道不存在", "not_found_error")
		return
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return replaceChannelModels(tx, id, items)
	}); err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"channel_id": id, "total": len(items)})
}

// bindChannelModel 给渠道加一条模型白名单。
//
// 这里不再需要「模型不存在就自动创建」：白名单就是模型在系统里的唯一登记处，
// 写进来即生效，不存在两处状态对不上的可能。
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
	items, err := normalizeWhitelist([]whitelistItem{p.whitelistItem()})
	if err != nil {
		writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	item := items[0]

	db := s.deps.Store.DB()
	var ch model.Channel
	if err := db.First(&ch, id).Error; err != nil {
		writeUpstreamError(c, http.StatusNotFound, "渠道不存在", "not_found_error")
		return
	}

	// 同渠道内对外名唯一：重复添加视为「改上游名/启用状态」，而不是塞第二条。
	//
	// 这里刻意不用 FirstOrCreate + Assign：Assign 的结构体走的是零值跳过逻辑，
	// enabled=false 会被当成「没传」而跳过，于是「把某条白名单停用」静默失效。
	// 显式分「新建」与「改已有」两条路，两个字段都写死在 map 里，不依赖零值语义。
	binding := model.ChannelModel{ChannelID: id, PublicName: item.PublicName}
	err = db.Where("channel_id = ? AND public_name = ?", id, item.PublicName).First(&binding).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		binding = model.ChannelModel{
			ChannelID: id, PublicName: item.PublicName,
			UpstreamName: item.UpstreamName, Enabled: item.Enabled,
		}
		if err := db.Create(&binding).Error; err != nil {
			writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
			return
		}
	case err != nil:
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	default:
		if err := db.Model(&binding).Updates(map[string]any{
			"upstream_name": item.UpstreamName,
			"enabled":       item.Enabled,
		}).Error; err != nil {
			writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
			return
		}
		binding.UpstreamName = item.UpstreamName
		binding.Enabled = item.Enabled
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

// groupCreatePayload 是新建分组的入参。
//
// 刻意不直接绑定实体：Enabled 用指针才能区分「没传」与「传 false」。
// 以前「不传就启用」是靠数据库列默认值兜的（实体带 gorm default 时，
// false 会被从 INSERT 里省掉，正好落成 true），而那个标签同时让
// 「显式传 false」也存不进去 —— 见 model.ChannelModel.Enabled 的注释。
// 去掉标签后两种语义必须在代码里分开，否则新建的分组会默认停用，
// 而停用的分组不参与任何路由，现象是「刚建的分组怎么调都不通」。
type groupCreatePayload struct {
	Name      string `json:"name"`
	Remark    string `json:"remark"`
	Strategy  string `json:"strategy"`
	IsDefault bool   `json:"is_default"`
	Enabled   *bool  `json:"enabled"`
	Color     string `json:"color"`
	RPM       int    `json:"rpm"`
	TPM       int    `json:"tpm"`
}

// normalizeGroupColor 校验并归一化分组颜色。
//
// 只接受 #rgb / #rrggbb：这个值会被前端直接写进内联样式，
// 放开格式就等于给「把任意字符串塞进 style」留了口子。
// 空串是合法的，表示「按分组名派生一个颜色」。
func normalizeGroupColor(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if !strings.HasPrefix(s, "#") {
		return "", errors.New("颜色要写成 #rgb 或 #rrggbb")
	}
	hex := s[1:]
	if len(hex) != 3 && len(hex) != 6 {
		return "", errors.New("颜色要写成 #rgb 或 #rrggbb")
	}
	for _, c := range hex {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return "", errors.New("颜色里只能出现 0-9 与 a-f")
		}
	}
	return strings.ToLower(s), nil
}

// validateGroupQuota 校验分组的每分钟额度。0 表示不限制，负值没有意义。
func validateGroupQuota(rpm, tpm int) error {
	if rpm < 0 || tpm < 0 {
		return errors.New("rpm / tpm 不能为负数（0 表示不限制）")
	}
	return nil
}

func (s *Server) createGroup(c *gin.Context) {
	var p groupCreatePayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	if strings.TrimSpace(p.Name) == "" {
		writeUpstreamError(c, http.StatusBadRequest, "name 必填", "invalid_request_error")
		return
	}
	strategy := p.Strategy
	if strategy == "" {
		strategy = model.StrategyWeighted
	}
	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	color, err := normalizeGroupColor(p.Color)
	if err != nil {
		writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	if err := validateGroupQuota(p.RPM, p.TPM); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	gr := model.ChannelGroup{
		Name: strings.TrimSpace(p.Name), Remark: p.Remark,
		Strategy: strategy, IsDefault: p.IsDefault, Enabled: enabled,
		Color: color, RPM: p.RPM, TPM: p.TPM,
	}
	db := s.deps.Store.DB()
	// 默认分组只能有一个：strategyFor(0) 取的是第一条 is_default=true 的记录，
	// 存在多个时选中哪个完全看返回顺序，行为不可预期。
	err = db.Transaction(func(tx *gorm.DB) error {
		if gr.IsDefault {
			if err := tx.Model(&model.ChannelGroup{}).
				Where("is_default = ?", true).
				Update("is_default", false).Error; err != nil {
				return err
			}
		}
		return tx.Create(&gr).Error
	})
	if err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, gr)
}

// groupUpdatePayload 是编辑分组的入参，所有字段可选。
//
// 不能像原来那样直接绑定实体再按「非零值」挑字段：布尔字段没有非零值可判，
// 于是 is_default / enabled 永远进不了 updates —— 传了返回 200 却不落库，
// 界面上开关拨过去又弹回来，看起来像「保存失败但没报错」。
// 字符串字段同样有问题：remark 传空串清不掉。
type groupUpdatePayload struct {
	Name      *string `json:"name"`
	Remark    *string `json:"remark"`
	Strategy  *string `json:"strategy"`
	IsDefault *bool   `json:"is_default"`
	Enabled   *bool   `json:"enabled"`
	// Color / RPM / TPM 同样必须是指针：0 与「没传」要能分开
	// （把 RPM 从 10 改回 0 = 取消限制，是常见操作）
	Color *string `json:"color"`
	RPM   *int    `json:"rpm"`
	TPM   *int    `json:"tpm"`
}

func (s *Server) updateGroup(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var p groupUpdatePayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	updates := map[string]any{}
	if p.Name != nil {
		name := strings.TrimSpace(*p.Name)
		if name == "" {
			writeUpstreamError(c, http.StatusBadRequest, "name 不能为空", "invalid_request_error")
			return
		}
		updates["name"] = name
	}
	if p.Strategy != nil {
		updates["strategy"] = *p.Strategy
	}
	if p.Remark != nil {
		updates["remark"] = *p.Remark
	}
	if p.IsDefault != nil {
		updates["is_default"] = *p.IsDefault
	}
	if p.Enabled != nil {
		updates["enabled"] = *p.Enabled
	}
	if p.Color != nil {
		color, err := normalizeGroupColor(*p.Color)
		if err != nil {
			writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
		// 显式传空串表示「恢复自动配色」，所以这里要能写进去
		updates["color"] = color
	}
	if p.RPM != nil || p.TPM != nil {
		// 只改其中一个时，另一个按当前值参与校验，免得单独改 TPM 被当成 rpm=0
		var cur model.ChannelGroup
		if err := s.deps.Store.DB().First(&cur, id).Error; err != nil {
			writeUpstreamError(c, http.StatusNotFound, "分组不存在", "not_found_error")
			return
		}
		rpm, tpm := cur.RPM, cur.TPM
		if p.RPM != nil {
			rpm = *p.RPM
		}
		if p.TPM != nil {
			tpm = *p.TPM
		}
		if err := validateGroupQuota(rpm, tpm); err != nil {
			writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
		updates["rpm"] = rpm
		updates["tpm"] = tpm
	}
	if len(updates) == 0 {
		writeUpstreamError(c, http.StatusBadRequest, "没有需要更新的字段", "invalid_request_error")
		return
	}
	// 把「设为默认」与「清掉其它分组的默认标记」放进同一个事务，
	// 否则中途失败会留下两个默认分组或零个默认分组
	err := s.deps.Store.DB().Transaction(func(tx *gorm.DB) error {
		if p.IsDefault != nil && *p.IsDefault {
			if err := tx.Model(&model.ChannelGroup{}).
				Where("id <> ?", id).
				Update("is_default", false).Error; err != nil {
				return err
			}
		}
		return applyUpdates(tx, &model.ChannelGroup{}, id, updates)
	})
	if err != nil {
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
	if s.deps.GroupLimit != nil {
		// 连同限流窗口一起删掉：分组没了，它的每分钟计数没有任何意义，
		// 留着只会在内存里慢慢堆积（而且 id 复用的话会把旧计数带给新分组）
		s.deps.GroupLimit.Reset(id)
	}
	deleteByID(c, db, &model.ChannelGroup{}, id, "分组不存在")
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
	// 必须注册在 /:id 之前，否则 "export" 会被当成日志 id
	r.GET("/export", s.exportLogs)
	r.GET("/:id", s.logDetail)
}

// logFilters 按查询参数拼出日志筛选条件。
//
// 单拎出来是为了让列表与导出用同一套条件：两边各写一遍迟早会不一致，
// 而「导出的和看到的不一样」是排查问题时最误导人的情况。
func (s *Server) logFilters(c *gin.Context) *gorm.DB {
	q := s.deps.Store.DB().Model(&model.RequestLog{})

	if m := strings.TrimSpace(c.Query("model")); m != "" {
		q = q.Where("model_requested = ?", m)
	}
	// trace_id 精确定位：从一条报错跳到完整链路的入口
	if tid := strings.TrimSpace(c.Query("trace_id")); tid != "" {
		q = q.Where("trace_id = ?", tid)
	}
	// 状态码：既支持精确值，也支持按类别看
	// （「只看失败的」是排查时最常用的，而精确匹配单个状态码做不到）
	if st := strings.TrimSpace(c.Query("status")); st != "" {
		q = q.Where("status_code = ?", st)
	}
	switch c.Query("status_class") {
	case "success":
		q = q.Where("status_code >= 200 AND status_code < 300")
	case "error":
		q = q.Where("status_code >= 400")
	}
	if cid := strings.TrimSpace(c.Query("channel_id")); cid != "" {
		q = q.Where("channel_id = ?", cid)
	}
	if kid := strings.TrimSpace(c.Query("key_id")); kid != "" {
		q = q.Where("api_key_id = ?", kid)
	}
	if since := c.Query("since"); since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			q = q.Where("created_at >= ?", t)
		}
	}
	if until := c.Query("until"); until != "" {
		if t, err := time.Parse(time.RFC3339, until); err == nil {
			q = q.Where("created_at <= ?", t)
		}
	}
	return q
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

	q := s.logFilters(c)

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

// maxExportRows 是单次导出的上限。日志可能很多，全量导出会把内存和响应撑爆；
// 到顶时会在响应头里说明被截断，而不是悄悄少给。
const maxExportRows = 20000

// exportLogs 导出当前筛选条件下的全部日志。
//
// 前端原来只导出当前页（默认 50 条），用户点「导出」拿到的文件却像是全部日志 ——
// 这种「看起来成功、实际只有一小部分」是最难发现的一类问题。
// 导出必须在服务端按同一套筛选条件做全量，并把实际条数写进响应头。
func (s *Server) exportLogs(c *gin.Context) {
	var total int64
	if err := s.logFilters(c).Count(&total).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	exported := total
	if exported > maxExportRows {
		exported = maxExportRows
	}

	var items []model.RequestLog
	if err := s.logFilters(c).Order("id DESC").Limit(int(exported)).Find(&items).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}

	c.Header("X-Total-Count", strconv.FormatInt(total, 10))
	c.Header("X-Exported-Count", strconv.Itoa(len(items)))
	c.Header("X-Exported-Truncated", strconv.FormatBool(total > int64(len(items))))
	c.Header("Content-Disposition", "attachment; filename=request-logs.csv")
	c.Header("Content-Type", "text/csv; charset=utf-8")

	var b strings.Builder
	// 加 BOM，Excel 才会按 UTF-8 识别。
	// 这里必须写转义序列：直接嵌入 BOM 字符会让 Go 源码在词法分析阶段就报错
	b.WriteString("\ufeff")
	b.WriteString("请求时间,模型,状态,密钥,渠道,输入Token,输出Token,缓存命中,缓存写入,推理Token,首包延迟(ms),完成时长(ms),费用USD,trace_id\n")
	for i := range items {
		r := &items[i]
		row := []string{
			r.CreatedAt.Format(time.RFC3339),
			r.ModelRequested, strconv.Itoa(r.StatusCode), r.APIKeyName, r.ChannelName,
			strconv.Itoa(r.PromptTokens), strconv.Itoa(r.CompletionTokens),
			strconv.Itoa(r.CachedTokens), strconv.Itoa(r.CacheCreationTokens), strconv.Itoa(r.ReasoningTokens),
			strconv.Itoa(r.FirstByteMs), strconv.Itoa(r.TotalMs), r.EstimatedCost.String(), r.TraceID,
		}
		for j, cell := range row {
			if j > 0 {
				b.WriteByte(',')
			}
			// CSV 转义：字段里有逗号、引号或换行时要用引号包起来
			if strings.ContainsAny(cell, ",\"\n\r") {
				b.WriteString(`"` + strings.ReplaceAll(cell, `"`, `""`) + `"`)
			} else {
				b.WriteString(cell)
			}
		}
		b.WriteByte('\n')
	}
	c.String(http.StatusOK, b.String())
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
