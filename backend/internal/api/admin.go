package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/relay"
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

// writeConflictOrInternal 把写库失败翻译成响应：唯一名冲突回 409（人话），
// 其余回 500（不带库细节）。所有 create/update 出口共用这一个分类点 ——
// 下一个带唯一约束的表接入时不会再有人手抄这五行的机会。
func writeConflictOrInternal(c *gin.Context, err error, dupMsg string) {
	if isUniqueViolation(err) {
		writeUpstreamError(c, http.StatusConflict, dupMsg, "invalid_request_error")
		return
	}
	writeInternalError(c, err)
}

// writeUpdateError 把更新失败翻译成响应；找不到记录时给 404 而不是 500。
func writeUpdateError(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeUpstreamError(c, http.StatusNotFound, "记录不存在", "not_found_error")
		return
	}
	writeConflictOrInternal(c, err, "名称已存在")
}

// writeInternalError 回一个不含内部细节的 500，同时把完整错误写进服务端日志。
//
// 为什么不能直接把 err.Error() 交给客户端：Postgres 的约束错误里带着数据。
// 例如唯一索引冲突的报文是
//
//	ERROR: duplicate key value violates unique constraint "idx_channel_model"
//	DETAIL: Key (channel_id, public_name)=(3, gpt-4o) already exists.
//
// 表名、列名、索引名、以及**实际的列值**全在里面。这个管理接口没有鉴权
// （设计如此，靠同源中间件兜底），把库结构与被拒的数据一起回显出去，
// 等于额外给出一个信息面；而错误信息对用户定位问题也没帮助 ——
// 用户要做的是「换个名字」或「先删掉那条」，这由后端翻译成人话更合适。
//
// 完整错误进 slog：排障时看服务端日志，那里本来就有。
//
// 措辞对读写都成立（`/v1/models` 这类只读接口也会用到它），
// 所以用「本次请求未生效」而不是「操作未被应用」。
func writeInternalError(c *gin.Context, err error) {
	slog.Error("接口内部错误",
		"path", c.Request.URL.Path,
		"method", c.Request.Method,
		"err", err,
	)
	writeUpstreamError(c, http.StatusInternalServerError,
		"服务端处理失败，本次请求未生效；详情见服务端日志", "internal_error")
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
	// 往渠道发一句 "hi" 看通不通。与真实转发走同一套代码，
	// 所以它验证的是「中继实际会发出去的那个请求」，不是另拼一个
	r.POST("/:id/test", s.testChannel)
	// 整组重排优先级（拖动列表后提交）。放在 /:id 之前：gin 的路由树里
	// 静态段与参数段不冲突，但把固定路径写在前面更好读
	r.PUT("/order", s.reorderChannels)
	// 图标：空 body 表示去上游抓一个，带 icon 表示设置成自定义值
	r.POST("/:id/icon", s.channelIcon)
}

// whitelistItem 是渠道模型白名单的一行：客户端请求 PublicName，
// 转发时替换成 UpstreamName（留空则同名）。
type whitelistItem struct {
	PublicName   string `json:"public_name"`
	UpstreamName string `json:"upstream_name"`
	Enabled      *bool  `json:"enabled"`
	// ProxyID 让这一个模型走自己的代理；0 = 跟随渠道。
	// 用值类型即可：白名单是整表提交，0 的语义就是「跟随渠道」，不存在歧义。
	ProxyID uint `json:"proxy_id"`
	// ---- 价格 ----
	// 金额用字符串传：JSON 的浮点会把 0.15 变成 0.14999999999999999，
	// 而单价要落进 numeric(18,8) 的列里
	InputPer1M      string `json:"input_per_1m"`
	OutputPer1M     string `json:"output_per_1m"`
	CacheReadPer1M  string `json:"cache_read_per_1m"`
	CacheWritePer1M string `json:"cache_write_per_1m"`
	// Multiplier 用指针区分「没传」与「显式填 0」；0 与不传都归一成 1
	Multiplier *float64       `json:"multiplier"`
	PeakRules  model.JSONList `json:"peak_rules"`
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
	// Currency 是这条渠道的记账币种（人民币渠道填 CNY）。它决定价格字段的
	// 单位，也决定这笔调用在日志与看板里算到哪个币种下面；空值按美元处理，
	// 与数据列的默认值保持一致（见 model.Channel.Currency）
	Currency string `json:"currency"`
	APIKey   string `json:"api_key"`
	// 这里刻意没有 weight：权重是**组内优先级序号**，由渠道在分组里的位置决定，
	// 不再由客户端指定。新建的排到末尾；调整顺序走 PUT /channels/order。
	// 旧脚本仍在 body 里带 weight —— Go 会忽略不认识的字段，所以不会解析失败，
	// 只是那个值不再生效。
	// ProxyID 走哪个出站代理；0 = 直连。
	//
	// 必须是指针：0 既是「直连」也是 uint 的零值，用值类型就分不出
	// 「改成直连」与「这次请求不提代理这件事」。实测踩过 ——
	// 用户把渠道从代理改回直连时传 proxy_id=0，后端当成「没传」忽略掉，
	// 界面上显示已保存、库里还指着那个代理。与模型白名单要用指针是同一类坑。
	ProxyID *uint `json:"proxy_id"`
	// Icon 是渠道图标；传空字符串表示「清空，回到默认图标」，
	// 所以同样要用指针才能区分「清空」与「这次不提图标」
	Icon      *string          `json:"icon"`
	Enabled   *bool            `json:"enabled"`
	Slots     model.JSONList   `json:"available_slots"`
	ExtraConf model.JSONMap    `json:"extra_config"`
	CustomMap model.JSONMap    `json:"custom_mapping"`
	Monitor   string           `json:"monitor_type"`
	Models    *[]whitelistItem `json:"models"`
}

// validateWhitelistProxies 校验白名单里引用的代理都存在。
//
// 刻意不塞进 normalizeWhitelist：那是个纯函数（有单测直接调），
// 不该为了查库把单测也拖成数据库测试。
func (s *Server) validateWhitelistProxies(items []model.ChannelModel) error {
	// 去重后一次 IN 查询：逐条 COUNT 在白名单几十条时是几十次 DB 往返，
	// 而这是每次渠道保存都要走的路径
	ids := map[uint]bool{}
	for _, it := range items {
		if it.ProxyID != 0 {
			ids[it.ProxyID] = true
		}
	}
	if len(ids) == 0 {
		return nil
	}
	idList := make([]uint, 0, len(ids))
	for id := range ids {
		idList = append(idList, id)
	}
	var found []uint
	if err := s.deps.Store.DB().Model(&model.Proxy{}).
		Where("id IN ?", idList).Pluck("id", &found).Error; err != nil {
		return err
	}
	exist := make(map[uint]bool, len(found))
	for _, id := range found {
		exist[id] = true
	}
	for _, it := range items {
		if it.ProxyID != 0 && !exist[it.ProxyID] {
			return fmt.Errorf("模型 %s 指定的代理 #%d 不存在", it.PublicName, it.ProxyID)
		}
	}
	return nil
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
		row := model.ChannelModel{
			PublicName: name, UpstreamName: upstream, Enabled: enabled, ProxyID: it.ProxyID,
		}
		// 价格与模型一起提交（见 applyPriceFields）：校验失败时整表都不写，
		// 免得出现「模型加进去了、价格没保存」这种半截状态
		if err := applyPriceFields(&row, it); err != nil {
			return nil, fmt.Errorf("模型 %s 的价格有误: %w", name, err)
		}
		out = append(out, row)
	}
	return out, nil
}

// invalidatePricing 让计价引擎丢掉缓存。
//
// 价格随渠道模型一起改，所以任何动到白名单的接口都必须喊一声：
// 引擎缓存 5 分钟，不主动失效的话「改完价发一次请求发现还是老价钱」，
// 用户会以为保存没成功而反复点保存。
func (s *Server) invalidatePricing() {
	if s.deps.Pricing != nil {
		s.deps.Pricing.Invalidate()
	}
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
	}
	// 批量插入：整表替换常见几十条，逐条 Create 是几十次往返；
	// Postgres 的参数上限对这里的量级（百级参数）毫无压力
	if len(items) > 0 {
		if err := tx.CreateInBatches(items, 100).Error; err != nil {
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
	// UnpricedCount 是白名单里还没配价的条数。由服务端算而不是前端遍历：
	// 判据（四个单价全 0 且没有倍率）必须与计价引擎一字不差，
	// 两边各写一份迟早会不一致
	UnpricedCount int `json:"unpriced_count"`
	// LastUsedAt 是这条渠道最近一次实际承接请求的时间（没有则为 null）。
	//
	// 取自请求日志而不是渠道行上的字段：日志里记的是**最终承接这次请求的渠道**
	// （故障转移跳过的那些尝试只进 Trail，不落 channel_id），所以它回答的是
	// 「这条渠道最近一次真的干活是什么时候」，而不是「最近一次被尝试」。
	// 也正因为如此，这里不需要在转发链路上加写库动作 ——
	// 健康状态那两处 Update 是有意节流的（只在状态变化时写）
	LastUsedAt *time.Time `json:"last_used_at"`
	// Runtime 是渠道的运行期状态（冷却剩余 / 失败连击 / 平滑延迟 / 在途数）。
	// health_status 只回答「最近一次成功或失败」，这一块回答「此刻能不能
	// 被路由到」—— 冷却中的渠道即使 health_status 还是 healthy 也不会接活。
	// 没有运行期数据（State 未注入）时为 nil
	Runtime *relay.ChannelRuntime `json:"runtime,omitempty"`
}

func (s *Server) listChannels(c *gin.Context) {
	db := s.deps.Store.DB()
	var channels []model.Channel
	q := db.Order("id DESC")
	if gid := c.Query("group_id"); gid != "" {
		q = q.Where("group_id = ?", gid)
	}
	if err := q.Find(&channels).Error; err != nil {
		writeInternalError(c, err)
		return
	}

	ids := make([]uint, 0, len(channels))
	for _, ch := range channels {
		ids = append(ids, ch.ID)
	}
	names := map[uint][]string{}
	unpriced := map[uint]int{}
	if len(ids) > 0 {
		var rows []model.ChannelModel
		if err := db.Where("channel_id IN ?", ids).Order("public_name").Find(&rows).Error; err != nil {
			writeInternalError(c, err)
			return
		}
		for _, r := range rows {
			names[r.ChannelID] = append(names[r.ChannelID], r.PublicName)
			// 「没配价」的判定与计价引擎一致：四个单价全 0 且没有倍率。
			// 列表上要显式提示条数 —— 漏配价的后果是这笔调用被记成 0 元，
			// 而账面上完全看不出异常，只能靠这里点名
			if r.InputPer1M.IsZero() && r.OutputPer1M.IsZero() &&
				r.CacheReadPer1M.IsZero() && r.CacheWritePer1M.IsZero() && r.Multiplier <= 1 {
				unpriced[r.ChannelID]++
			}
		}
	}

	// 最近调用时间：一次聚合查出这批渠道各自最后一次被用上的时刻。
	// 用 id 列表约束范围，避免全表聚合 —— 日志表是唯一会无界增长的表
	lastUsed := map[uint]*time.Time{}
	if len(ids) > 0 {
		var rows []struct {
			ChannelID uint
			LastUsed  time.Time
		}
		if err := db.Model(&model.RequestLog{}).
			Select("channel_id, MAX(created_at) AS last_used").
			Where("channel_id IN ?", ids).
			Group("channel_id").
			Scan(&rows).Error; err != nil {
			writeInternalError(c, err)
			return
		}
		for _, r := range rows {
			t := r.LastUsed
			lastUsed[r.ChannelID] = &t
		}
	}

	items := make([]channelListItem, 0, len(channels))
	// 运行期快照一次取全（内存读，O(渠道数)），再按 id 分发到各行
	var runtimeByID map[uint]relay.ChannelRuntime
	if s.deps.State != nil {
		runtimeByID = s.deps.State.Snapshot(time.Now())
	}
	for _, ch := range channels {
		list := names[ch.ID]
		if list == nil {
			list = []string{}
		}
		var rt *relay.ChannelRuntime
		if r, ok := runtimeByID[ch.ID]; ok {
			rt = &r
		}
		items = append(items, channelListItem{
			Channel: ch, Models: list, ModelCount: len(list), UnpricedCount: unpriced[ch.ID],
			LastUsedAt: lastUsed[ch.ID], Runtime: rt,
		})
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
	currency, ok := model.NormalizeCurrency(p.Currency)
	if !ok {
		if strings.TrimSpace(p.Currency) != "" {
			writeUpstreamError(c, http.StatusBadRequest, "不支持的币种："+p.Currency+"（可选 "+strings.Join(model.SupportedCurrencies, " / ")+"）", "invalid_request_error")
			return
		}
		// 没传币种 = 按历史口径（美元）处理：这里的默认值必须与数据列的
		// 默认值一致，否则同一个渠道经界面创建和经脚本创建会是两种币种
		currency = model.CurrencyUSD
	}
	if p.GroupID == 0 {
		// 没传分组就落到默认分组。默认分组可能被用户删掉或不设（见 store.Seed），
		// 那种情况下不能瞎指一个 id：写死 1 的通病是「要么外键报错、要么进错组」
		gid, ok := defaultGroupID(s)
		if !ok {
			writeUpstreamError(c, http.StatusBadRequest,
				"没有默认分组，请在请求里指定 group_id，或先在分组管理里把一个分组设为默认",
				"invalid_request_error")
			return
		}
		p.GroupID = gid
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
	// 显式指定的分组同样预检：靠外键拒绝的话回的是 500，前端只能显示
	// 「服务端处理失败」（与代理预检对称，都回 400 + 人话）
	if err := checkGroupExists(s, p.GroupID); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "分组有问题: "+err.Error(), "invalid_request_error")
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
		if verr := s.validateWhitelistProxies(items); verr != nil {
			writeUpstreamError(c, http.StatusBadRequest, verr.Error(), "invalid_request_error")
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
		Currency:  currency,
		APIKeyEnc: enc, APIKeyHint: secure.MaskKey(p.APIKey),
		Enabled: enabled, MonitorType: orDefault(p.Monitor, "none"),
		Slots: p.Slots, ExtraConfig: p.ExtraConf, CustomMap: p.CustomMap,
		// 建渠道时就把代理带上：漏了它的话，界面上选了代理、保存也成功，
		// 但库里还是 0（直连）—— 表现为「配了代理却不走代理」（实测踩过）
		ProxyID:      proxyID,
		Icon:         strings.TrimSpace(derefString(p.Icon)),
		HealthStatus: "unknown",
	}
	// 新渠道排在分组末尾，返回的 JSON 里也要带上它实际拿到的优先级，
	// 否则界面拿到的是 0，与库里不一致。
	//
	// 白名单必须和渠道在**同一个事务**里写：
	// 原来白名单写失败时渠道已经落库并占掉了 weight 序号，界面上报
	// 「创建失败」，用户重试就多出一条同名渠道（channels.name 没有唯一索引），
	// 而多出来的那条还会参与路由。updateChannel 早就是同一事务，这里补齐。
	if err := s.deps.Store.DB().Transaction(func(tx *gorm.DB) error {
		w, err := appendChannelToGroup(tx, ch.GroupID)
		if err != nil {
			return err
		}
		ch.Weight = w
		if err := tx.Create(&ch).Error; err != nil {
			return err
		}
		// 白名单随渠道一起建：建渠道时就能把「这条渠道能跑哪些模型」填完，
		// 不用再回列表点一次「模型」
		if p.Models != nil {
			return replaceChannelModels(tx, ch.ID, whitelist)
		}
		return nil
	}); err != nil {
		writeInternalError(c, err)
		return
	}
	s.invalidatePricing()
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
	if p.Currency != "" {
		currency, ok := model.NormalizeCurrency(p.Currency)
		if !ok {
			writeUpstreamError(c, http.StatusBadRequest, "不支持的币种："+p.Currency+"（可选 "+strings.Join(model.SupportedCurrencies, " / ")+"）", "invalid_request_error")
			return
		}
		// 改币种只影响之后的账：历史日志各自带着当时的币种快照，
		// 不会因为这次改动被重新解释（也**不会**自动换算已有的价格）
		updates["currency"] = currency
	}
	// 换分组不在这里写：它要连带算出新分组末尾的序号，且必须与 group_id
	// 在同一条 UPDATE 里落库（见下面事务里的说明），所以单独处理
	groupChange := uint(p.GroupID)
	// 换组预检目标分组存在：靠外键拒绝只会得到 500（与代理预检对称）
	if groupChange != 0 {
		if err := checkGroupExists(s, groupChange); err != nil {
			writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
	}
	if p.ProxyID != nil {
		if err := checkProxyExists(s, *p.ProxyID); err != nil {
			writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
		updates["proxy_id"] = *p.ProxyID
	}
	if p.Icon != nil {
		updates["icon"] = strings.TrimSpace(*p.Icon)
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
	// 只改白名单（改完模型点保存）、只换分组都是合法请求，所以不能只看 updates 是否为空
	if len(updates) == 0 && groupChange == 0 && p.Models == nil {
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
		if verr := s.validateWhitelistProxies(items); verr != nil {
			writeUpstreamError(c, http.StatusBadRequest, verr.Error(), "invalid_request_error")
			return
		}
		whitelist = items
	}

	// 渠道字段与白名单放同一个事务：白名单是整表替换，
	// 中途失败留下「渠道改了、白名单没改」会让人以为保存没生效
	err := s.deps.Store.DB().Transaction(func(tx *gorm.DB) error {
		// 换分组要动两边的序号，所以先把原分组记下来
		var before model.Channel
		if err := tx.Select("id", "group_id").First(&before, id).Error; err != nil {
			return err
		}

		if groupChange != 0 && groupChange != before.GroupID {
			// 新分组末尾的序号，必须在**这条还没进去**的时候算：
			// 先换组再算的话，它自己带过来的旧序号会被当成组内最大值，
			// 新序号白白多跳一格，新分组里就留下一个空洞（实测踩过：
			// 目标组只有序号 1 的一条，本该补到 2，结果补成了 3）
			w, err := appendChannelToGroup(tx, groupChange)
			if err != nil {
				return err
			}
			// 换组与赋序号必须在同一条 UPDATE 里：分两步会短暂出现
			// 「已经在新分组、却还带着旧序号」的中间状态，那个旧序号
			// 可能正好撞上新分组里已有的序号，被唯一索引当场拒绝
			updates["group_id"] = groupChange
			updates["weight"] = w
			if err := applyUpdates(tx, &model.Channel{}, id, updates); err != nil {
				return err
			}
			// 最后给原分组补位（它空出了一格）
			if err := renumberGroupChannels(tx, before.GroupID); err != nil {
				return err
			}
		} else if len(updates) > 0 {
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
	s.invalidatePricing()
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
//
// 数字 ID 也要查库确认存在。原来只做 ParseUint 就收下，于是一个不存在的
// 分组 ID（例如手滑多打一位）会被当成「解析成功」，len(ids) 非 0，下面那道
// 「一条都没解析出来就报错」的保护就失效了。结果是白名单静默匹配不到任何渠道，
// 请求全部 403，而界面上完全看不出原因 —— 排查时不会有人想到是白名单。
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
		var g model.ChannelGroup
		if n, err := strconv.ParseUint(item, 10, 32); err == nil {
			if err := db.First(&g, uint(n)).Error; err != nil {
				unresolved = append(unresolved, item+" (ID 不存在)")
				continue
			}
			ids = append(ids, g.ID)
			continue
		}
		if err := db.Where("name = ?", item).First(&g).Error; err != nil {
			unresolved = append(unresolved, item)
			continue
		}
		ids = append(ids, g.ID)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("密钥的分组白名单 %v 无法解析（分组可能已被删除或改名）", unresolved)
	}
	// 部分解析失败也要报出来：静默丢掉一项，用户会以为整份白名单都生效了
	if len(unresolved) > 0 {
		return nil, fmt.Errorf("密钥的分组白名单里有 %v 无法解析（分组可能已被删除或改名）", unresolved)
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
		writeInternalError(c, res.Error)
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
		if err := tx.Delete(&model.Channel{}, id).Error; err != nil {
			return err
		}
		// 删掉一条就把同组后面的序号补齐：留空洞的话，故障转移链上会多出
		// 一个「不存在的优先级」，而序号本身也不再能表示「第几个被尝试」
		return renumberGroupChannels(tx, ch.GroupID)
	}); err != nil {
		writeInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "deleted": true})
}

// reorderChannels 按给定的顺序重排某个分组内渠道的优先级。
//
// 整组全量提交（而不是「把某条移到第 N 位」）：拖拽得到的本来就是一份完整顺序，
// 全量提交是幂等的 —— 重复提交同一份顺序不会产生新变化，也不需要前端算差值。
// 代价是必须与库里的成员完全对上，对不上就报错让前端刷新（见 orderValidationError）。
func (s *Server) reorderChannels(c *gin.Context) {
	var p struct {
		GroupID uint   `json:"group_id"`
		IDs     []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	if p.GroupID == 0 {
		writeUpstreamError(c, http.StatusBadRequest, "group_id 必填", "invalid_request_error")
		return
	}

	db := s.deps.Store.DB()
	if err := db.Transaction(func(tx *gorm.DB) error {
		var groupChannels []model.Channel
		if err := tx.Select("id").Where("group_id = ?", p.GroupID).Find(&groupChannels).Error; err != nil {
			return err
		}
		if verr := orderValidationError(groupChannels, p.IDs); verr != nil {
			// 校验放在事务里：读成员与写序号之间不能有别的写入插进来，
			// 否则「校验时是全量、写的时候已经不是了」
			return orderInvalidError{msg: verr.Error()}
		}
		return applyChannelOrder(tx, p.GroupID, p.IDs)
	}); err != nil {
		// 成员对不上是用户输入问题（多半是界面上的列表过期了），
		// 与数据库故障要分开回，否则前端只会看到一句 500
		var invalid orderInvalidError
		if errors.As(err, &invalid) {
			writeUpstreamError(c, http.StatusBadRequest, invalid.Error(), "invalid_request_error")
			return
		}
		writeInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"group_id": p.GroupID, "ordered": len(p.IDs)})
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
		writeInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows, "total": len(rows)})
}

type bindPayload struct {
	PublicName   string `json:"public_name"`
	UpstreamName string `json:"upstream_name"`
	Enabled      *bool  `json:"enabled"`
	ProxyID      uint   `json:"proxy_id"`
	// 价格字段与 whitelistItem 一致，见那里的说明
	InputPer1M      string         `json:"input_per_1m"`
	OutputPer1M     string         `json:"output_per_1m"`
	CacheReadPer1M  string         `json:"cache_read_per_1m"`
	CacheWritePer1M string         `json:"cache_write_per_1m"`
	Multiplier      *float64       `json:"multiplier"`
	PeakRules       model.JSONList `json:"peak_rules"`
}

func (p bindPayload) whitelistItem() whitelistItem {
	return whitelistItem{
		PublicName: p.PublicName, UpstreamName: p.UpstreamName,
		Enabled: p.Enabled, ProxyID: p.ProxyID,
		InputPer1M: p.InputPer1M, OutputPer1M: p.OutputPer1M,
		CacheReadPer1M: p.CacheReadPer1M, CacheWritePer1M: p.CacheWritePer1M,
		Multiplier: p.Multiplier, PeakRules: p.PeakRules,
	}
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
	if verr := s.validateWhitelistProxies(items); verr != nil {
		writeUpstreamError(c, http.StatusBadRequest, verr.Error(), "invalid_request_error")
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
		writeInternalError(c, err)
		return
	}
	s.invalidatePricing()
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
	if verr := s.validateWhitelistProxies(items); verr != nil {
		writeUpstreamError(c, http.StatusBadRequest, verr.Error(), "invalid_request_error")
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
		// 价格与代理都从 item 整块拷过来：item 已经过 normalizeWhitelist
		// （价格在那里校验并归一），漏拷任何一项都会表现为
		// 「接口返回了新的绑定，但那一项没保存」——ProxyID 就曾经这样丢过
		binding = model.ChannelModel{
			ChannelID: id, PublicName: item.PublicName,
			UpstreamName: item.UpstreamName, Enabled: item.Enabled,
			ProxyID:    item.ProxyID,
			InputPer1M: item.InputPer1M, OutputPer1M: item.OutputPer1M,
			CacheReadPer1M: item.CacheReadPer1M, CacheWritePer1M: item.CacheWritePer1M,
			Multiplier: item.Multiplier, PeakRules: item.PeakRules,
		}
		if err := db.Create(&binding).Error; err != nil {
			writeInternalError(c, err)
			return
		}
	case err != nil:
		writeInternalError(c, err)
		return
	default:
		if err := db.Model(&binding).Updates(map[string]any{
			"upstream_name": item.UpstreamName,
			"enabled":       item.Enabled,
			// proxy_id 也要写：0 表示跟随渠道，是合法值而非「没传」，
			// 不补这行的话改绑定代理会被静默丢弃（价格不同：空载荷
			// 等于 0，写它会清掉用户配好的价，所以那边刻意不动）
			"proxy_id": item.ProxyID,
		}).Error; err != nil {
			writeInternalError(c, err)
			return
		}
		binding.UpstreamName = item.UpstreamName
		binding.Enabled = item.Enabled
		binding.ProxyID = item.ProxyID
		// 这里刻意不动价格：载荷里没传价格时是空字符串，按 applyPriceFields
		// 的语义等于 0，会把用户配好的价格悄悄清掉。改价走整表提交那条路
	}
	s.invalidatePricing()
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
		writeInternalError(c, err)
		return
	}
	s.invalidatePricing()
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

// groupListItem 是分组列表行：实体之外带「今日各币种花费」。
type groupListItem struct {
	model.ChannelGroup
	// TodaySpent 是今日各币种已花费金额（与 daily_budget 同一口径逐币对照）。
	// 只在分组配了预算时才算（一次聚合查询），没配预算的分组带 nil ——
	// 没有预算的花费数字没有读者，白付一次聚合
	TodaySpent map[string]float64 `json:"today_spent"`
}

func (s *Server) listGroups(c *gin.Context) {
	var items []model.ChannelGroup
	if err := s.deps.Store.DB().Order("id").Find(&items).Error; err != nil {
		writeInternalError(c, err)
		return
	}

	// 今日花费只对配了预算的分组聚合（同一口子：today + resolveRange）
	budgeted := map[uint]bool{}
	for _, g := range items {
		if len(normalizeBudgetAmounts(g.DailyBudget)) > 0 {
			budgeted[g.ID] = true
		}
	}
	spentByGroup := map[uint]map[string]float64{}
	if len(budgeted) > 0 {
		start, end, _ := resolveRange("today")
		var rows []struct {
			GroupID      uint
			CostCurrency string
			Spent        float64
		}
		if err := s.deps.Store.DB().Model(&model.RequestLog{}).
			Select("group_id, cost_currency, COALESCE(SUM(estimated_cost), 0) AS spent").
			Where("created_at >= ? AND created_at <= ? AND group_id IN ?", start, end, mapKeys(budgeted)).
			Group("group_id, cost_currency").
			Scan(&rows).Error; err != nil {
			writeInternalError(c, err)
			return
		}
		for _, r := range rows {
			if r.CostCurrency == "" {
				continue
			}
			if spentByGroup[r.GroupID] == nil {
				spentByGroup[r.GroupID] = map[string]float64{}
			}
			spentByGroup[r.GroupID][r.CostCurrency] = r.Spent
		}
	}

	out := make([]groupListItem, 0, len(items))
	for _, g := range items {
		item := groupListItem{ChannelGroup: g}
		if budgeted[g.ID] {
			item.TodaySpent = spentByGroup[g.ID]
			if item.TodaySpent == nil {
				item.TodaySpent = map[string]float64{}
			}
		}
		out = append(out, item)
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "total": len(out)})
}

// mapKeys 把集合 map 转成切片（Go 没有内建的 keys 提取）。
func mapKeys[V any](m map[uint]V) []uint {
	ks := make([]uint, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
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
	// DailyBudget 按币种的日预算（{"CNY": 50}）。创建时可选；
	// 空表/缺省 = 不设预算（见 normalizeDailyBudget 的清除语义）
	DailyBudget map[string]float64 `json:"daily_budget"`
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
	strategy := normalizeStrategy(p.Strategy)
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
	budget, err := normalizeDailyBudget(p.DailyBudget)
	if err != nil {
		writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	gr := model.ChannelGroup{
		Name: strings.TrimSpace(p.Name), Remark: p.Remark,
		Strategy: strategy, IsDefault: p.IsDefault, Enabled: enabled,
		Color: color, RPM: p.RPM, TPM: p.TPM, DailyBudget: budget,
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
		// 撞唯一名回 409 而不是 500，让前端能显示「分组名已存在」
		// 而不是「服务端处理失败」
		writeConflictOrInternal(c, err, "分组名已存在")
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
	// DailyBudget 用指针区分「没传」（保持原值）与「传空对象」（清除预算）。
	// 与 RPM 直接改成 0 不同，预算的「取消」在语义上就是空对象 ——
	// 不用指针的话用户永远删不掉预算
	DailyBudget *map[string]float64 `json:"daily_budget"`
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
		updates["strategy"] = normalizeStrategy(*p.Strategy)
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
	if p.DailyBudget != nil {
		budget, err := normalizeDailyBudget(*p.DailyBudget)
		if err != nil {
			writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
		if budget == nil {
			// 空对象 = 清除预算：写成 SQL NULL，而不是空 jsonb
			updates["daily_budget"] = gorm.Expr("NULL")
		} else {
			updates["daily_budget"] = budget
		}
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
	s.invalidateKeyCache()
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
		writeInternalError(c, err)
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
	// 单独一个接口而不是让列表带上明文：列表一次取全部密钥，
	// 而用户一次只看一把。按需解密，明文在最少的场合出现
	r.GET("/:id/reveal", s.revealKey)
}

func (s *Server) listKeys(c *gin.Context) {
	var items []model.APIKey
	if err := s.deps.Store.DB().Order("id DESC").Find(&items).Error; err != nil {
		writeInternalError(c, err)
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

// createKey 创建密钥。明文随响应返回一份（方便立刻复制走），
// 同时加密存一份供日后查看 —— 见 model.APIKey 里关于「为什么不只存哈希」的说明。
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
		writeInternalError(c, err)
		return
	}
	enc, err := s.deps.Cipher.Encrypt(plain)
	if err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, "加密密钥失败: "+err.Error(), "internal_error")
		return
	}
	k := model.APIKey{
		Name: p.Name, KeyHash: secure.HashKey(plain), KeyEnc: enc, KeyPrefix: plain[:11],
		Enabled: true, AllowedModels: p.AllowedModels, AllowedGroups: p.AllowedGroups,
	}
	if p.Enabled != nil {
		k.Enabled = *p.Enabled
	}
	if p.RateLimitRPM != nil {
		k.RateLimitRPM = *p.RateLimitRPM
	}
	if err := s.deps.Store.DB().Create(&k).Error; err != nil {
		writeInternalError(c, err)
		return
	}
	s.invalidateKeyCache()
	c.JSON(http.StatusOK, gin.H{
		"id": k.ID, "name": k.Name, "key": plain,
		"notice": "密钥已保存，之后可在列表里随时查看或复制",
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
	s.invalidateKeyCache()
	c.JSON(http.StatusOK, gin.H{"id": id, "updated": len(updates)})
}

func (s *Server) deleteKey(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if deleteByID(c, s.deps.Store.DB(), &model.APIKey{}, id, "密钥不存在") {
		s.invalidateKeyCache()
	}
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
// 第二个返回值为 false 表示参数非法（已写过 400 响应），调用方应立即返回；
// 数字型参数不校验直接进 SQL，非法值会在数据库层报错、客户端拿到 500。
func (s *Server) logFilters(c *gin.Context) (*gorm.DB, bool) {
	q := s.deps.Store.DB().Model(&model.RequestLog{})
	bad := func(name, val string) (*gorm.DB, bool) {
		writeUpstreamError(c, http.StatusBadRequest, "参数 "+name+" 非法: "+val, "invalid_request_error")
		return nil, false
	}

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
		n, err := strconv.Atoi(st)
		if err != nil || n < 100 || n > 599 {
			return bad("status", st)
		}
		q = q.Where("status_code = ?", n)
	}
	switch c.Query("status_class") {
	case "success":
		q = q.Where("status_code >= 200 AND status_code < 300")
	case "error":
		q = q.Where("status_code >= 400")
	}
	// 分组筛选：界面上「按分组看」是常态视角。
	// 不 join channels 取它现在的分组 —— 日志里的归属是当时那一刻的快照，
	// 渠道后来换了分组不该把历史账挪到新分组去（与 cost_currency 同一个道理）。
	if gid := strings.TrimSpace(c.Query("group_id")); gid != "" {
		v, err := strconv.ParseUint(gid, 10, 64)
		if err != nil || v == 0 {
			return bad("group_id", gid)
		}
		q = q.Where("group_id = ?", v)
	}
	if cid := strings.TrimSpace(c.Query("channel_id")); cid != "" {
		v, err := strconv.ParseUint(cid, 10, 64)
		if err != nil || v == 0 {
			return bad("channel_id", cid)
		}
		q = q.Where("channel_id = ?", v)
	}
	if kid := strings.TrimSpace(c.Query("key_id")); kid != "" {
		v, err := strconv.ParseUint(kid, 10, 64)
		if err != nil || v == 0 {
			return bad("key_id", kid)
		}
		q = q.Where("api_key_id = ?", v)
	}
	// 时间范围：range 是「看板那四档」（today/3d/7d/30d），由 resolveRange
	// 与统计接口**共用同一段代码**换算 —— 请求日志并入看板之后，上面卡片的数字
	// 与下面列表的行必须落在同一个窗口里，各算各的迟早会出现
	// 「卡片说今天 200 次，列表只有 180 条」这种没人能解释的偏差。
	//
	// 也支持 since/until 绝对时刻（排障脚本与「从某个时刻往后」的用法）。
	// 两者**不允许同时出现**：叠加时没人看得出哪个生效，宁可报错。
	rng := strings.TrimSpace(c.Query("range"))
	since := c.Query("since")
	until := c.Query("until")
	if rng != "" && (since != "" || until != "") {
		return bad("range", rng+"（不能与 since/until 同时使用）")
	}
	switch rng {
	case "":
		// 不筛时间
	case "today", "3d", "7d", "30d":
		start, end, _ := resolveRange(rng)
		q = q.Where("created_at >= ? AND created_at <= ?", start, end)
	default:
		// 不静默退回「今天」：界面上选着「近 7 天」而结果其实是今天，
		// 会让人得出错误结论（与统计接口「非法值不静默」同一条约定）
		return bad("range", rng)
	}
	if since != "" {
		t, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return bad("since", since)
		}
		q = q.Where("created_at >= ?", t)
	}
	if until != "" {
		t, err := time.Parse(time.RFC3339, until)
		if err != nil {
			return bad("until", until)
		}
		q = q.Where("created_at <= ?", t)
	}
	return q, true
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

	q, ok := s.logFilters(c)
	if !ok {
		return
	}

	var total int64
	// 无时间下界时的 COUNT 兜底：request_logs 是唯一持续增长的表，日志按
	// 保留期滚动清理（cleanup 任务），COUNT 语义上只该数保留期内的行 ——
	// 不给下界的话每次日志页加载都是一次全表 COUNT。idx_log_created 的
	// 首列就是 created_at，带下界后走索引。range（四档都带 start）与
	// since 已提供下界；保留期配置为非正数（= 不清理）时保持原样。
	countQ := q
	if strings.TrimSpace(c.Query("range")) == "" && c.Query("since") == "" &&
		s.deps.Config.Relay.LogRetentionDays > 0 {
		countQ = countQ.Where("created_at >= ?",
			time.Now().UTC().AddDate(0, 0, -s.deps.Config.Relay.LogRetentionDays))
	}
	if err := countQ.Count(&total).Error; err != nil {
		writeInternalError(c, err)
		return
	}
	var items []model.RequestLog
	if err := q.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&items).Error; err != nil {
		writeInternalError(c, err)
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
	filters, ok := s.logFilters(c)
	if !ok {
		return
	}
	var total int64
	if err := filters.Count(&total).Error; err != nil {
		writeInternalError(c, err)
		return
	}
	exported := total
	if exported > maxExportRows {
		exported = maxExportRows
	}

	var items []model.RequestLog
	if err := filters.Order("id DESC").Limit(int(exported)).Find(&items).Error; err != nil {
		writeInternalError(c, err)
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
	// 表头文案与界面保持一致（界面「任务耗时」列里的两行：首字 / 总耗时）：
	// 同一个数在页面叫一个名字、导出来又叫另一个名字，对不上账时最难查。
	// CSV 是平铺的两列，挂不住「任务耗时」这一层列头，
	// 所以把被合并掉的「耗时」二字补回列名，并带上单位（毫秒）。
	// 费用拆成「金额 + 币种」两列：金额离开币种就没意义，
	// 而现在同一份导出里可能同时有人民币和美元的账
	b.WriteString("请求时间,模型,状态,密钥,渠道,输入Token,输出Token,缓存命中,缓存写入,推理Token,首字耗时(ms),总共耗时(ms),费用,币种,trace_id\n")
	for i := range items {
		r := &items[i]
		row := []string{
			r.CreatedAt.Format(time.RFC3339),
			r.ModelRequested, strconv.Itoa(r.StatusCode), r.APIKeyName, r.ChannelName,
			strconv.Itoa(r.PromptTokens), strconv.Itoa(r.CompletionTokens),
			strconv.Itoa(r.CachedTokens), strconv.Itoa(r.CacheCreationTokens), strconv.Itoa(r.ReasoningTokens),
			strconv.Itoa(r.FirstByteMs), strconv.Itoa(r.TotalMs), r.EstimatedCost.String(), r.CostCurrency, r.TraceID,
		}
		for j, cell := range row {
			if j > 0 {
				b.WriteByte(',')
			}
			cell = csvNeutralize(cell)
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

// csvNeutralize 阻止导出的 CSV 被 Excel / WPS 当成公式执行。
//
// 原来只转义了逗号引号换行，但以 = + - @ 开头的单元格会被表格软件
// 当作公式求值 —— 而 model_requested、channel_name、api_key_name、trace_id
// 都是可控输入（模型名来自上游返回、渠道名与密钥名由用户填）。
// 典型利用是 =HYPERLINK(...) 或 =cmd|'/c calc'!A0。
//
// 处理方式遵循 OWASP 建议：前置一个单引号，表格软件会按文本显示，
// 单元格内容本身不变。制表符与回车也一并处理（同样是公式起始字符）。
func csvNeutralize(cell string) string {
	if cell == "" {
		return cell
	}
	switch cell[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + cell
	}
	return cell
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
	// 未留存时报文返回 null，前端据此区分「没有留存」与「留存了空内容」。
	// 查询失败不能也装成 null —— 那与「未留存」不可区分，排障时会被
	// 带偏方向；只有「确实没有这一行」才算未留存。
	var stored model.RequestPayload
	var payload *model.RequestPayload
	err := s.deps.Store.DB().Where("log_id = ?", id).First(&stored).Error
	switch {
	case err == nil:
		payload = &stored
	case errors.Is(err, gorm.ErrRecordNotFound):
		// 未留存
	default:
		writeInternalError(c, err)
		return
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

// checkRefExists 校验要被引用的记录存在（id 为 0 表示「不引用」，直接放行）；
// 提前回 400，不把外键拒绝伪装成 500。分组与代理两个引用方共用一份。
func checkRefExists(s *Server, id uint, m any, label string) error {
	if id == 0 {
		return nil
	}
	var n int64
	if err := s.deps.Store.DB().Model(m).Where("id = ?", id).Count(&n).Error; err != nil {
		return errors.New("校验" + label + "失败: " + err.Error())
	}
	if n == 0 {
		return errors.New("指定的" + label + "不存在")
	}
	return nil
}

func checkGroupExists(s *Server, id uint) error {
	return checkRefExists(s, id, &model.ChannelGroup{}, "分组")
}

// defaultGroupID 取被标为默认分组的 id。
//
// 取不到时返回 ok=false，不再退到写死的 1。以前能这么退，是因为 Seed
// 每次启动都会把「默认分组」建回来，这里几乎总是查得到；现在默认分组是
// 用户可以不设、也可以删掉的（见 store.Seed），再返回 1 就可能落到一个
// 不存在的分组上（外键拒绝，渠道建不出来），或者另一个不相干的分组上
// （渠道静默进了错组）。取不到时由调用方决定怎么办，别在这里猜。
func defaultGroupID(s *Server) (uint, bool) {
	var g model.ChannelGroup
	if err := s.deps.Store.DB().Where("is_default = ?", true).First(&g).Error; err != nil {
		return 0, false
	}
	return g.ID, true
}
