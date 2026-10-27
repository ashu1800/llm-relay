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
	"llm-relay/internal/relay/convert"
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
// 表名、列名、索引名、以及**实际的列值**全在里面。管理接口虽有登录鉴权，
// 但错误回显本就不该扩大信息面（登录者也可能把报文截图发给别人求助），
// 把库结构与被拒的数据一起回显出去没有收益；而错误信息对用户定位问题
// 也没帮助 —— 用户要做的是「换个名字」或「先删掉那条」，这由后端翻译成人话更合适。
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
	// 图标：空 body 表示去上游抓一个，带 icon 表示设置成自定义值
	r.POST("/:id/icon", s.channelIcon)
	// 预览抓取：不要求渠道已保存（新建时还没有 ID），只抓取不写库。
	// 放在 channels 组里是因为它同样要求管理台登录
	r.POST("/icon/preview", s.channelIconPreview)
	// 复制渠道：连密钥密文与模型白名单一起复制，副本默认停用
	r.POST("/:id/duplicate", s.duplicateChannel)
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
	// ---- 渠道定价属性 ----
	// 单价已于 2026-10-27 迁到模型级（model.ModelPrice）：白名单行只保留
	// 倍率与时段规则，它们才是「这条渠道相对标价怎么算」的表达。
	// Multiplier 用指针区分「没传」与「显式填 0」；0 与不传都归一成 1
	Multiplier *float64       `json:"multiplier"`
	PeakRules  model.JSONList `json:"peak_rules"`
	// SupportedEfforts 是这一行对外声明的思考档位（/v1/models 用）。
	// 用指针区分「没传」与「显式清空」：整表提交时没传表示「这次不动它」，
	// 空数组表示「清掉、回落到按协议推导」—— 两者都归一成 nil 落库，
	// 但单条绑定接口（bindPayload）靠指针避免把已配的档位静默抹掉。
	SupportedEfforts *model.StringList `json:"supported_efforts"`
}

// channelPayload 是渠道的写入载荷。
//
// Models 用指针接收整个白名单：nil 表示「这次不动白名单」，
// 空数组表示「清空白名单」—— 若用值类型，这两件事都是 len=0，分不开，
// 于是「把最后一条删掉」会静默失效。
type channelPayload struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url"`
	// Currency 是这条渠道的记账币种（人民币渠道填 CNY）。它决定价格字段的
	// 单位，也决定这笔调用在日志与看板里算到哪个币种下面；空值按美元处理，
	// 与数据列的默认值保持一致（见 model.Channel.Currency）
	Currency string `json:"currency"`
	APIKey   string `json:"api_key"`
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

// validateDefaultModel 校验「默认模型映射」的配置配得上套。
//
// 前端也会拦（保存前 message.warning），但后端必须也拦：接口不是只有界面在用，
// 脚本与备份导入都在打。拦的是三种「配了但不会生效」的状态：
//
//  1. 开关开着但模型名空 —— 路由侧把它等同于没开（见 relay.ChannelDefaultModel），
//     保存成功等于给用户一个假象，而表现又是那个 2ms 的 502。
//  2. 指定的模型不在该渠道白名单里 —— 候选查询按
//     channel_models.public_name = extra_config->>'default_model' 做 JOIN 匹配，
//     匹配不到就不成为兜底候选。这条更隐蔽：白名单看着是配好的，只是没配这个名字。
//  3. 指定的模型在白名单里但那一行停用了 —— 候选查询的 JOIN 还要求
//     channel_models.enabled = true。同一张表单里「先选好兜底目标、再回白名单
//     把那一行停用」完全可能发生，前端下拉框只列启用行，但那只防住选择的
//     那一刻，防不住之后停用，所以必须在保存时按整表状态再拦一次。
//
// 判据复用 relay 侧的读取函数，与路由一字不差 —— 两边各写一份迟早漂移，
// 而漂移的表现正是「保存时说没问题、路由里不生效」。
func validateDefaultModel(extra map[string]any, whitelist []model.ChannelModel) error {
	if !relay.ChannelDefaultModelEnabled(extra) {
		// 开关关着（或压根没配）：这时 default_model 里残留什么都不影响路由，
		// ChannelDefaultModel 不会认它，不必拦住保存
		return nil
	}
	name, on := relay.ChannelDefaultModel(extra)
	if !on {
		return errors.New("开了「默认模型映射」但没填默认模型名；不填的话这个开关不会生效")
	}
	for _, it := range whitelist {
		if strings.TrimSpace(it.PublicName) != name {
			continue
		}
		if it.Enabled {
			return nil
		}
		return errors.New("默认模型 " + name + " 在白名单里但处于停用状态：停用的行不参与兜底，请启用那一行或另选模型")
	}
	return fmt.Errorf("默认模型 %s 不在这条渠道的白名单里；兜底请求靠它去匹配白名单行，配不上就不会生效", name)
}

// validateDefaultModelOnUpdate 用「更新之后」的状态校验默认模型映射。
//
// 更新是部分字段语义：本次没传的那一半要取库里现有的，否则两种漏检都会发生 ——
// 只交白名单时（把默认模型那一行删掉）不报错、只交 extra_config 时
// （指定一个白名单里没有的模型）也不报错，两者都让兜底静默失效。
func (s *Server) validateDefaultModelOnUpdate(id uint, p channelPayload, whitelist []model.ChannelModel) error {
	// 两边都传了就不必查库 —— 绝大多数保存走这条，省一次往返
	if p.Models != nil && p.ExtraConf != nil {
		return validateDefaultModel(p.ExtraConf, whitelist)
	}

	var existing model.Channel
	if err := s.deps.Store.DB().Select("id", "extra_config").First(&existing, id).Error; err != nil {
		// 渠道不存在由后面的事务路径统一报错（它本来就要查一次），这里不抢答
		return nil
	}
	extra := effectiveDefaultModelExtra(existing.ExtraConfig, p.ExtraConf)

	// 校验要用**更新之后**的白名单整行（含启用状态，见 validateDefaultModel
	// 对停用行的拦截）。本次没交白名单就取库里现有的
	if p.Models == nil {
		var rows []model.ChannelModel
		if err := s.deps.Store.DB().Model(&model.ChannelModel{}).
			Where("channel_id = ?", id).Find(&rows).Error; err != nil {
			return nil
		}
		whitelist = rows
	}
	return validateDefaultModel(extra, whitelist)
}

// effectiveDefaultModelExtra 算出这条渠道在本次更新**之后**的 extra_config。
//
// 更新是部分字段语义：没传 extra_config 表示「这次不动它」，那就得拿库里
// 现成的那份来校验 —— 否则「只改白名单、不动默认映射」这种常见操作会校验不到，
// 于是用户把默认模型那一行从白名单里删掉时不会报错，兜底静默失效。
func effectiveDefaultModelExtra(existing model.JSONMap, incoming model.JSONMap) map[string]any {
	if incoming != nil {
		return incoming
	}
	return existing
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
		// 档位：整表提交时「没传」与「空数组」都是「回落到按协议推导」，
		// 归一成 nil 落库（读侧把 nil 与空数组一视同仁，见 relay.SupportedEfforts）
		if it.SupportedEfforts != nil {
			row.SupportedEfforts = convert.NormalizeEfforts(*it.SupportedEfforts)
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

// bindWhitelist 是「规整 + 校验代理引用」的唯一入口。
//
// 这两步此前在四处（建渠道 / 改渠道 / 整表 PUT / 单条绑定）各抄了一遍，
// 连同两条一模一样的 400 错误串。合并成一处的意义不在省行数：白名单的
// 校验规则以后要是再加一条（比如「同一个上游名只能绑一次」），漏改一处
// 就是一个能存进库的坏数据，而且没有任何信号。写失败时它自己回 400 并
// 返回 false，调用方只写 `if !ok { return }`。
func (s *Server) bindWhitelist(c *gin.Context, items []whitelistItem) ([]model.ChannelModel, bool) {
	rows, err := normalizeWhitelist(items)
	if err != nil {
		writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return nil, false
	}
	if verr := s.validateWhitelistProxies(rows); verr != nil {
		writeUpstreamError(c, http.StatusBadRequest, verr.Error(), "invalid_request_error")
		return nil, false
	}
	return rows, true
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

// wipedPrices 找出 next 相对 old 被清零（非 0 → 0）的价格属性，返回人话描述。
//
// 刻意做成纯函数：它直接决定 WARN 日志的内容，误报（原值就是 0）与漏报
// （改值没清零）都会毁掉这条排查线索，所以拉出来单独测。
//
// 2026-10-27 起单价已迁到模型级，本函数只剩倍率一项 —— 它的作用从
// 「保住价格」变成「保住倍率」：倍率被清零意味着这条渠道的加价规则消失，
// 同样属于「界面上点了保存、账单口径变了却没人知道」的那类事。
func wipedPrices(oldRow, next model.ChannelModel) []string {
	out := make([]string, 0, 1)
	if oldRow.Multiplier != 0 && next.Multiplier == 0 {
		out = append(out, fmt.Sprintf("固定倍率 %g→0", oldRow.Multiplier))
	}
	return out
}

// replaceChannelModels 用给定白名单整体替换某个渠道的条目。
// 整体替换而不是逐条 diff：白名单在界面上就是一张表，一次提交一整张表，
// 不会出现「删了两条、加了一条，结果只生效一半」的中间状态。
func replaceChannelModels(tx *gorm.DB, channelID uint, items []model.ChannelModel) error {
	// DELETE 会把旧行连同价格一起带走，所以替换前先读一遍旧价：
	// 「原来有价、这次提交变 0」的都要点名 —— 清空价格是合法操作，不拦，
	// 但这类变化曾以「输入价莫名变 0」的事故形态出现过（2026-09-20），
	// 当时只能去请求日志的定价快照里逐条倒查，有这行日志一眼定位
	var old []model.ChannelModel
	if err := tx.Where("channel_id = ?", channelID).Find(&old).Error; err != nil {
		return err
	}
	oldBy := make(map[string]model.ChannelModel, len(old))
	for _, r := range old {
		oldBy[r.PublicName] = r
	}

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

	// 清零日志放在写库成功之后：事务回滚时没有变化，报「被清零」是误报。
	// 渠道名拿不到就退化成只带 id，不为它让保存失败
	var chName string
	if err := tx.Model(&model.Channel{}).Select("name").Where("id = ?", channelID).
		Scan(&chName).Error; err != nil {
		chName = ""
	}
	for i := range items {
		o, ok := oldBy[items[i].PublicName]
		if !ok {
			continue
		}
		if wiped := wipedPrices(o, items[i]); len(wiped) > 0 {
			slog.Warn("渠道模型单价被清零，请确认是否有意操作",
				"channel", chName, "channel_id", channelID,
				"model", items[i].PublicName, "cleared", strings.Join(wiped, "、"))
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
	// FallbackModel 是这条渠道「默认模型映射」指定的模型名（没开则为空）。
	//
	// 由 relay.ChannelDefaultModel 算，与路由侧同一出口 —— 列表显示的和
	// 实际生效的必须是同一个判断，否则会出现「界面上标着兜底、路由里没兜底」。
	// 它回答的是「客户端的模型名没命中白名单时，这条渠道会用什么接单」。
	FallbackModel string `json:"fallback_model"`
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
	// State 注入时每行都有（没进快照的渠道兜零值 = 一切正常）；
	// State 未注入时为 nil
	Runtime *relay.ChannelRuntime `json:"runtime,omitempty"`
}

func (s *Server) listChannels(c *gin.Context) {
	db := s.deps.Store.DB()
	var channels []model.Channel
	if err := db.Order("id DESC").Find(&channels).Error; err != nil {
		writeInternalError(c, err)
		return
	}

	ids := make([]uint, 0, len(channels))
	for _, ch := range channels {
		ids = append(ids, ch.ID)
	}
	names := map[uint][]string{}
	enabledNames := map[uint]map[string]bool{}
	unpriced := map[uint]int{}
	if len(ids) > 0 {
		var rows []model.ChannelModel
		if err := db.Where("channel_id IN ?", ids).Order("public_name").Find(&rows).Error; err != nil {
			writeInternalError(c, err)
			return
		}
		// 「没配价」的判定要查模型级价格表（2026-10-27 起单价不再挂在
		// 白名单行上）。一次把全表读成 map，比每行查一次库省事。
		// 判定口径与该渠道币种有关：人民币渠道要看人民币那一档配没配。
		pricedModels := map[string]model.ModelPrice{}
		var priceRows []model.ModelPrice
		if err := db.Find(&priceRows).Error; err == nil {
			for _, p := range priceRows {
				pricedModels[p.ModelName] = p
			}
		}
		// 渠道币种也提前建 map：下面每一行都要用，而 currencyOfChannel
		// 是线性扫描 —— 白名单几百行 × 渠道几十条就是上万次比较，
		// 全在同一层循环里重复做。
		currencyByChannel := make(map[uint]string, len(channels))
		for _, ch := range channels {
			cur := model.CurrencyUSD
			if c, ok := model.NormalizeCurrency(ch.Currency); ok {
				cur = c
			}
			currencyByChannel[ch.ID] = cur
		}
		for _, r := range rows {
			names[r.ChannelID] = append(names[r.ChannelID], r.PublicName)
			if r.Enabled {
				if enabledNames[r.ChannelID] == nil {
					enabledNames[r.ChannelID] = map[string]bool{}
				}
				enabledNames[r.ChannelID][r.PublicName] = true
			}
			// 「没配价」= 该模型在该渠道币种下四档全 0，且没有倍率。
			// 列表上要显式提示条数 —— 漏配价的后果是这笔调用被记成 0 元，
			// 账面上完全看不出异常，只能靠这里点名
			noPrice := true
			if p, ok := pricedModels[r.PublicName]; ok {
				noPrice = p.PriceFor(currencyByChannel[r.ChannelID]).IsZero()
			}
			if noPrice && r.Multiplier <= 1 {
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
	// 运行期快照一次取全（内存读，O(渠道数)），再按 id 分发到各行。
	// 没进快照的渠道兜一份零值：列表这一列要回答的是「此刻能不能被路由到」，
	// 「从来没接过活」与「冷却已结束」在界面上都是同一句话（一切正常），
	// 留 nil 反而让前端要写两种分支
	runtimeByID := map[uint]relay.ChannelRuntime{}
	if s.deps.State != nil {
		runtimeByID = s.deps.State.Snapshot(time.Now())
	}
	for _, ch := range channels {
		list := names[ch.ID]
		if list == nil {
			list = []string{}
		}
		rt, ok := runtimeByID[ch.ID]
		if !ok {
			rt = relay.ChannelRuntime{}
		}
		items = append(items, channelListItem{
			Channel: ch, Models: list, ModelCount: len(list),
			UnpricedCount: unpriced[ch.ID],
			FallbackModel: fallbackModelOf(ch, enabledNames[ch.ID]),
			LastUsedAt:    lastUsed[ch.ID], Runtime: &rt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// fallbackModelOf 取这条渠道实际生效的默认模型名（没开则为空串）。
//
// enabledNames 是该渠道白名单里**启用中**的对外名集合（与路由 JOIN 的
// public_name + enabled 条件同口径）。目标行不在其中时返回空串 ——
// 那是「开了但配不上」的半残状态，兜底不会生效，亮着「兜底」胶囊
// 只会让人以为它在工作。半残状态可能来自备份导入（导入路径不走
// validateDefaultModel，靠事后剥离兜底，见 admin_backup.go）或直接改库，
// 「保存时拦住了、库里不可能有」这个假设并不成立；真出现时，
// 失败提示里的 brokenFallbackCount 会点名。
func fallbackModelOf(ch model.Channel, enabledNames map[string]bool) string {
	name, on := relay.ChannelDefaultModel(ch.ExtraConfig)
	if !on {
		return ""
	}
	if !enabledNames[name] {
		return ""
	}
	return name
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
		items, ok := s.bindWhitelist(c, *p.Models)
		if !ok {
			return
		}
		whitelist = items
	}
	// 默认模型映射要对着**本次提交的白名单**校验：指定的模型必须在白名单里
	// 且启用，否则候选查询的 JOIN 匹配不到它，兜底不会生效（见 validateDefaultModel）
	if verr := validateDefaultModel(p.ExtraConf, whitelist); verr != nil {
		writeUpstreamError(c, http.StatusBadRequest, verr.Error(), "invalid_request_error")
		return
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
		Name:     p.Name,
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
	// 白名单必须和渠道在**同一个事务**里写：
	// 原来白名单写失败时渠道已经落库，界面上报「创建失败」，用户重试
	// 就多出一条同名渠道（channels.name 没有唯一索引），而多出来的那条
	// 还会参与路由。updateChannel 一直是同一事务，这里保持一致。
	if err := s.deps.Store.DB().Transaction(func(tx *gorm.DB) error {
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
	// 只改白名单（改完模型点保存）也是合法请求，所以不能只看 updates 是否为空
	if len(updates) == 0 && p.Models == nil {
		writeUpstreamError(c, http.StatusBadRequest, "没有需要更新的字段", "invalid_request_error")
		return
	}
	var whitelist []model.ChannelModel
	if p.Models != nil {
		items, ok := s.bindWhitelist(c, *p.Models)
		if !ok {
			return
		}
		whitelist = items
	}
	// 默认模型映射的校验要用**更新之后**的状态：本次没传的字段取库里现有的。
	// 少取一边就会出现两种漏检 —— 只改白名单时（把默认模型那行删掉）不报错、
	// 或只改 extra_config 时（指定一个白名单里没有的模型）不报错，
	// 两者都让兜底静默失效。
	if verr := s.validateDefaultModelOnUpdate(id, p, whitelist); verr != nil {
		writeUpstreamError(c, http.StatusBadRequest, verr.Error(), "invalid_request_error")
		return
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

// KeyRoutesOf 解析密钥的每模型渠道路由（model_channels jsonb → map）。
//
// 解析失败（坏 JSON、条目形状不对）按空路由处理：转发侧拿到空列表会
// 如实报「没有可用渠道」，而不是带着错误的范围继续转发。回填与保存
// 路径写出的都是规整形状，这里主要是对直接改库的兜底。
//
// 返回的 map 键是模型名，值是渠道 ID 列表（**保持存储顺序**，即优先级序）。
func KeyRoutesOf(k *model.APIKey) map[string][]uint {
	out := map[string][]uint{}
	if k == nil || k.ModelRoutes == nil {
		return out
	}
	for name, raw := range k.ModelRoutes {
		arr, ok := raw.([]any)
		if !ok {
			continue
		}
		ids := make([]uint, 0, len(arr))
		for _, item := range arr {
			switch v := item.(type) {
			case float64:
				if v > 0 {
					ids = append(ids, uint(v))
				}
			case string:
				if n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 32); err == nil && n > 0 {
					ids = append(ids, uint(n))
				}
			}
		}
		out[name] = ids
	}
	return out
}

// KeyChannelScope 返回路由结果里出现过的全部渠道 ID（去重）。
// 供 /v1/models 之类的「这个密钥都能看到什么」查询收窄候选集。
// 返回空切片表示「路由没有任何渠道」—— 调用方应据此直接回空结果。
// 入参是 KeyRoutesOf 的产出（调用方多处复用同一次解析，见 /v1/models）。
func KeyChannelScope(routes map[string][]uint) []uint {
	seen := map[uint]bool{}
	ids := make([]uint, 0, len(routes)*2)
	for _, list := range routes {
		for _, id := range list {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
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
		return tx.Delete(&model.Channel{}, id).Error
	}); err != nil {
		writeInternalError(c, err)
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
	// 渠道定价属性与 whitelistItem 一致，见那里的说明
	Multiplier *float64       `json:"multiplier"`
	PeakRules  model.JSONList `json:"peak_rules"`
	// 与 whitelistItem 同名字段同语义：nil = 这次不动它
	SupportedEfforts *model.StringList `json:"supported_efforts"`
}

func (p bindPayload) whitelistItem() whitelistItem {
	return whitelistItem{
		PublicName: p.PublicName, UpstreamName: p.UpstreamName,
		Enabled: p.Enabled, ProxyID: p.ProxyID,
		Multiplier: p.Multiplier, PeakRules: p.PeakRules,
		SupportedEfforts: p.SupportedEfforts,
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
	items, ok := s.bindWhitelist(c, p.Items)
	if !ok {
		return
	}
	db := s.deps.Store.DB()
	var ch model.Channel
	if err := db.First(&ch, id).Error; err != nil {
		writeUpstreamError(c, http.StatusNotFound, "渠道不存在", "not_found_error")
		return
	}
	// 兜底目标校验：这里是「整表替换」那条路，也是渠道列表那个白名单编辑器
	// 实际提交的端点（frontend 的 PUT /channels/:id/models）。在这一路上把兜底
	// 目标删掉或停用，与在渠道表单里做同一件事同因同果 —— 兜底的候选查询按
	// public_name JOIN 白名单，匹配不到就不生效。只在表单那条路拦，等于把
	// 刚修好的兜底又从侧门打开一次（第三轮审查 B-中1）。
	if verr := validateDefaultModel(ch.ExtraConfig, items); verr != nil {
		writeUpstreamError(c, http.StatusBadRequest, verr.Error(), "invalid_request_error")
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
	items, ok := s.bindWhitelist(c, []whitelistItem{p.whitelistItem()})
	if !ok {
		return
	}
	item := items[0]

	db := s.deps.Store.DB()
	var ch model.Channel
	if err := db.First(&ch, id).Error; err != nil {
		writeUpstreamError(c, http.StatusNotFound, "渠道不存在", "not_found_error")
		return
	}

	// 兜底目标校验，按「这次绑定之后」的整表算（第三轮审查 B-中1）：
	// 单条 POST 是唯一能把某行 enabled 从 true 改成 false 的入口（见下面的零值注释），
	// 把兜底目标那一行停用同样会让兜底静默失效，所以判据必须用改动后的形态，
	// 不能拿库里的现状糊弄过去
	var rows []model.ChannelModel
	if err := db.Where("channel_id = ?", id).Find(&rows).Error; err != nil {
		writeInternalError(c, err)
		return
	}
	if verr := validateDefaultModel(ch.ExtraConfig, whitelistAfterBind(rows, item)); verr != nil {
		writeUpstreamError(c, http.StatusBadRequest, verr.Error(), "invalid_request_error")
		return
	}

	// 同渠道内对外名唯一：重复添加视为「改上游名/启用状态」，而不是塞第二条。
	//
	// 这里刻意不用 FirstOrCreate + Assign：Assign 的结构体走的是零值跳过逻辑，
	// enabled=false 会被当成「没传」而跳过，于是「把某条白名单停用」静默失效。
	// 显式分「新建」与「改已有」两条路，两个字段都写死在 map 里，不依赖零值语义。
	binding := model.ChannelModel{ChannelID: id, PublicName: item.PublicName}
	err := db.Where("channel_id = ? AND public_name = ?", id, item.PublicName).First(&binding).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		// 倍率、时段规则、代理都从 item 整块拷过来：item 已经过 normalizeWhitelist
		// （规则在那里校验并归一），漏拷任何一项都会表现为
		// 「接口返回了新的绑定，但那一项没保存」——ProxyID 就曾经这样丢过
		binding = model.ChannelModel{
			ChannelID: id, PublicName: item.PublicName,
			UpstreamName: item.UpstreamName, Enabled: item.Enabled,
			ProxyID:    item.ProxyID,
			Multiplier: item.Multiplier, PeakRules: item.PeakRules,
			SupportedEfforts: item.SupportedEfforts,
		}
		if err := db.Create(&binding).Error; err != nil {
			writeInternalError(c, err)
			return
		}
	case err != nil:
		writeInternalError(c, err)
		return
	default:
		updates := map[string]any{
			"upstream_name": item.UpstreamName,
			"enabled":       item.Enabled,
			// proxy_id 也要写：0 表示跟随渠道，是合法值而非「没传」，
			// 不补这行的话改绑定代理会被静默丢弃（价格不同：空载荷
			// 等于 0，写它会清掉用户配好的价，所以那边刻意不动）
			"proxy_id": item.ProxyID,
		}
		// 档位只在载荷真的带了才写：nil 表示「这次不动它」，
		// 与价格同一个道理 —— 空载荷不该把用户配好的档位抹掉
		if p.SupportedEfforts != nil {
			updates["supported_efforts"] = item.SupportedEfforts
		}
		if err := db.Model(&binding).Updates(updates).Error; err != nil {
			writeInternalError(c, err)
			return
		}
		binding.UpstreamName = item.UpstreamName
		binding.Enabled = item.Enabled
		binding.ProxyID = item.ProxyID
		if p.SupportedEfforts != nil {
			binding.SupportedEfforts = item.SupportedEfforts
		}
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
	db := s.deps.Store.DB()
	// 删之前先按「删完之后」的整表校验兜底目标（第三轮审查 B-中1）：白名单是
	// 兜底目标的唯一登记处，从这儿删掉那一行，与在渠道表单里删掉同因同果 ——
	// 候选查询 JOIN 不上就不再兜底。
	//
	// 这里刻意不「顺手把 extra_config 里的兜底配置剥掉」：静默改用户的配置正是
	// 这轮修复要根除的那类毛病（与导入路径不同，导入必须放行才剥键，见
	// stripBrokenFallbackConfigs）。删不掉时把话说明白，让用户自己改兜底或关开关。
	var ch model.Channel
	switch err := db.First(&ch, id).Error; {
	case err == nil:
		var rows []model.ChannelModel
		if err := db.Where("channel_id = ?", id).Find(&rows).Error; err != nil {
			writeInternalError(c, err)
			return
		}
		if verr := validateDefaultModel(ch.ExtraConfig, whitelistAfterUnbind(rows, uint(bid))); verr != nil {
			writeUpstreamError(c, http.StatusBadRequest, verr.Error(), "invalid_request_error")
			return
		}
	case errors.Is(err, gorm.ErrRecordNotFound):
		// 渠道不存在：下面那条 DELETE 本来也删不到东西，保持既有的宽容语义（200）
	default:
		writeInternalError(c, err)
		return
	}

	if err := db.Where("id = ? AND channel_id = ?", bid, id).
		Delete(&model.ChannelModel{}).Error; err != nil {
		writeInternalError(c, err)
		return
	}
	s.invalidatePricing()
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// whitelistAfterBind 算出「这条绑定写进去之后」的白名单整表（只在内存里，不落库）。
//
// 单条 POST 对已存在的对外名是「改这一条」，对新的对外名是「加一条」，
// 与落库那段的两分支同一语义；这里只要形态对得上，validateDefaultModel
// 就能拿它当判据 —— 判据本身不在这里重写一遍。
func whitelistAfterBind(rows []model.ChannelModel, item model.ChannelModel) []model.ChannelModel {
	out := make([]model.ChannelModel, 0, len(rows)+1)
	replaced := false
	for _, r := range rows {
		if r.PublicName == item.PublicName {
			// 同渠道内对外名唯一，所以就地改启用状态即可（上游名/价格不影响兜底判定）
			r.Enabled = item.Enabled
			replaced = true
		}
		out = append(out, r)
	}
	if !replaced {
		out = append(out, item)
	}
	return out
}

// whitelistAfterUnbind 算出「这条绑定删掉之后」的白名单整表。
//
// 只按主键剔除，不判断它是不是兜底目标：「谁是兜底目标、删了还配不配得上套」
// 全交给 validateDefaultModel，免得这个判据在文件里长出第二份、日后各改各的。
func whitelistAfterUnbind(rows []model.ChannelModel, bindingID uint) []model.ChannelModel {
	out := make([]model.ChannelModel, 0, len(rows))
	for _, r := range rows {
		if r.ID == bindingID {
			continue
		}
		out = append(out, r)
	}
	return out
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
	Name    string `json:"name"`
	Enabled *bool  `json:"enabled"`
	// AllowedModels 是该密钥可调用的模型集合，**必填非空**（创建与编辑都拦）：
	// 新模型下路由完全由密钥决定，没有模型的密钥什么都调不了，允许创建
	// 只会制造一把「看起来正常、一发请求就 502」的死钥匙。
	AllowedModels model.StringList `json:"allowed_models"`
	// ModelChannels 是每个模型的候选渠道优先级：{"模型名": [渠道ID有序]}。
	// 键集合必须与 AllowedModels 一致，且每条渠道都存在、白名单里有该模型
	//（normalizeKeyRoutes 校验）。
	ModelChannels map[string][]uint `json:"model_channels"`
	// 用指针区分「没传」与「显式设为 0」
	RateLimitRPM *int `json:"rate_limit_rpm"`
}

// normalizeKeyRoutes 校验并规整「每模型的候选渠道路由」。
//
// 规则（创建与更新共用这一个出口，漂移的表现是「界面存得进、调用时 403/502」）：
//   - models 为空 → 报错（新模型下密钥必须至少声明一个模型）；
//   - routes 缺某个模型、或该模型的列表为空 → 报错；
//   - routes 里的渠道不存在 → 报错（保存时发现比调用时发现 403 便宜得多）；
//   - 渠道存在但白名单里没有这个模型 → 报错（否则路由静默收窄，界面上看不出来）；
//   - 渠道重复条目去重、保留首次出现的顺序；模型名去空白，与白名单同名对待。
//
// 返回规整后的 models（去空白、去重、与路由键对齐）与 routes。
// 查库分两把：渠道存在性一次 IN 查全，白名单匹配一次 IN 查全 ——
// 逐条查在几十个模型 × 几条渠道时是上百次往返，而这是每次保存密钥都走的路径。
// db 由调用方传入：updateKey 传事务（校验与写入同一快照），createKey 传普通连接。
func (s *Server) normalizeKeyRoutes(db *gorm.DB, models model.StringList, routes map[string][]uint) (model.StringList, model.JSONMap, error) {
	// 先去空白、去重（保序）
	cleanModels := make(model.StringList, 0, len(models))
	seenModel := map[string]bool{}
	for _, raw := range models {
		name := strings.TrimSpace(raw)
		if name == "" || seenModel[name] {
			continue
		}
		seenModel[name] = true
		cleanModels = append(cleanModels, name)
	}
	if len(cleanModels) == 0 {
		return nil, nil, errors.New("至少要为密钥选择一个模型")
	}

	cleanRoutes := make(model.JSONMap, len(cleanModels))
	for _, name := range cleanModels {
		ids, ok := routes[name]
		if !ok || len(ids) == 0 {
			return nil, nil, fmt.Errorf("模型 %s 没有指定任何候选渠道", name)
		}
		seenCh := map[uint]bool{}
		list := make([]uint, 0, len(ids))
		for _, id := range ids {
			if id == 0 || seenCh[id] {
				continue
			}
			seenCh[id] = true
			list = append(list, id)
		}
		if len(list) == 0 {
			return nil, nil, fmt.Errorf("模型 %s 没有指定任何候选渠道", name)
		}
		cleanRoutes[name] = list
	}
	// 路由里多出来的键（models 没选它）：视为脏数据拦下，
	// 否则「模型列表和路由各改了一半」会静默留下永远不生效的条目
	for name := range routes {
		if !seenModel[name] {
			return nil, nil, fmt.Errorf("模型 %s 不在密钥的模型列表里，请重新保存", name)
		}
	}

	// 渠道存在性：一次 IN 查全
	chIDs := make([]uint, 0, len(cleanRoutes)*2)
	for _, v := range cleanRoutes {
		for _, id := range v.([]uint) {
			chIDs = append(chIDs, id)
		}
	}
	var existIDs []uint
	if err := db.Model(&model.Channel{}).Where("id IN ?", chIDs).Pluck("id", &existIDs).Error; err != nil {
		return nil, nil, err
	}
	exist := make(map[uint]bool, len(existIDs))
	for _, id := range existIDs {
		exist[id] = true
	}
	for name, v := range cleanRoutes {
		for _, id := range v.([]uint) {
			if !exist[id] {
				return nil, nil, fmt.Errorf("模型 %s 的候选渠道 #%d 不存在（可能已被删除）", name, id)
			}
		}
	}

	// 白名单匹配：一次 JOIN 查全「(channel_id, public_name) 已启用」的行
	var bindings []struct {
		ChannelID  uint
		PublicName string
	}
	if err := db.Model(&model.ChannelModel{}).
		Select("channel_id, public_name").
		Where("enabled = true AND channel_id IN ?", chIDs).
		Scan(&bindings).Error; err != nil {
		return nil, nil, err
	}
	hasModel := map[string]bool{}
	for _, b := range bindings {
		hasModel[strconv.FormatUint(uint64(b.ChannelID), 10)+"\x00"+b.PublicName] = true
	}
	for name, v := range cleanRoutes {
		for _, id := range v.([]uint) {
			key := strconv.FormatUint(uint64(id), 10) + "\x00" + name
			if !hasModel[key] {
				return nil, nil, fmt.Errorf("渠道 #%d 的模型白名单里没有 %s（或那一行已停用），不能作为它的候选渠道", id, name)
			}
		}
	}
	return cleanModels, cleanRoutes, nil
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
	// 模型与路由在这里一并校验：不选模型不允许创建，每模型的候选渠道
	// 必须真实存在且白名单覆盖 —— 这里不拦，调用时才会以 403/502 暴露。
	models, routes, err := s.normalizeKeyRoutes(s.deps.Store.DB(), p.AllowedModels, p.ModelChannels)
	if err != nil {
		writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
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
		Enabled: true, AllowedModels: models, ModelRoutes: routes,
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
	// 白名单与路由是**一对**数据：更新时必须同时提交（模型集合与路由键
	// 要对得上，单改一边必然产生「有模型没路由」或「路由里有幽灵模型」）。
	// 用 nil 判断「没传」—— 编辑表单总是整份提交，单独改名字/限额的请求
	// 不带这两个字段，不受影响。
	if p.AllowedModels != nil || p.ModelChannels != nil {
		// 只传了一边：取库里现有的另一边凑成整份再校验 —— 校验逻辑要求
		// 两者一致，直接报错会逼着所有调用方一次带全（界面本来就是这么发的），
		// 但脚本友好起见这里补齐后再走同一出口。
		//
		// 读另一边、校验、写，全在一个事务里：这几步不在同一事务的话，
		// 并发的整份编辑可以交错出「请求 A 用 B 已改过的另一半补齐并写回」
		// 的组合 —— 双方都没审过的配置落了库（2026-10-26 评审 O1）。
		// 单管理员个人自用碰不上，事务没有额外代价，一并收口。
		err := s.deps.Store.DB().Transaction(func(tx *gorm.DB) error {
			models, routesIn := p.AllowedModels, p.ModelChannels
			if models == nil {
				var cur model.APIKey
				if err := tx.Select("allowed_models").First(&cur, id).Error; err != nil {
					return err
				}
				models = cur.AllowedModels
			}
			if routesIn == nil {
				// 重建走 KeyRoutesOf —— 同一解析口径（字符串数字条目照样
				// 认得），别在这里再手写一份只认 float64 的循环（CLAUDE.md 14）
				var cur model.APIKey
				if err := tx.Select("model_channels").First(&cur, id).Error; err != nil {
					return err
				}
				routesIn = KeyRoutesOf(&cur)
			}
			cleanModels, cleanRoutes, nerr := s.normalizeKeyRoutes(tx, models, routesIn)
			if nerr != nil {
				return nerr
			}
			updates["allowed_models"] = cleanModels
			updates["model_channels"] = cleanRoutes
			return applyUpdates(tx, &model.APIKey{}, id, updates)
		})
		if err != nil {
			writeUpdateError(c, err)
			return
		}
	} else {
		if len(updates) == 0 {
			writeUpstreamError(c, http.StatusBadRequest, "没有需要更新的字段", "invalid_request_error")
			return
		}
		if err := applyUpdates(s.deps.Store.DB(), &model.APIKey{}, id, updates); err != nil {
			writeUpdateError(c, err)
			return
		}
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
	// 渠道筛选。日志里的归属是当时那一刻的快照。
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

// 日志列表固定「最新在前」（id DESC）。
//
// 曾经支持 sort= 参数（按耗时/费用/状态排），2026-10-27 站主要求移除表头
// 排序按钮，于是那条链路（logSortClause 白名单表 + orderLogs）一并删掉 ——
// 留着无人调用的参数等于给后来的人一个「这里还能排序」的错误印象。
//
// id DESC 是列表的既有语义：最新入库的在最前。用主键而不是 created_at，
// 是因为同一毫秒入库的多条按时间排不出全序，翻页时同一行可能出现两次。
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
	// 与列表接口同一套排序（含 sort 参数）：导出的顺序该与屏幕上看到的一致，
	// 否则「按耗时排完再导出」拿到的文件是另一个次序（P1-9 的配套）
	if err := filters.Order("id DESC").Limit(int(exported)).Find(&items).Error; err != nil {
		writeInternalError(c, err)
		return
	}

	// 客户端显示映射（key→当前叫法）：CSV 的客户端列与界面同一优先级 ——
	// 映射名 > 落库快照名。映射表只有几十到几百行、且此处一次性加载，
	// 对一次最多两万行的导出不构成负担。加载失败不挡导出：顶多这一列
	// 退回快照名，比整个导出 500 更符合「CSV 拿到手最重要」。
	aliasNames := map[string]string{}
	var aliases []model.ClientAlias
	if err := s.deps.Store.DB().Find(&aliases).Error; err == nil {
		for _, a := range aliases {
			if a.Key != "" && a.Name != "" {
				aliasNames[a.Key] = a.Name
			}
		}
	}
	clientCell := func(r *model.RequestLog) string {
		if name, ok := aliasNames[relay.ClientKey(r.ClientID, r.ClientName)]; ok {
			return name
		}
		return r.ClientName
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
	// 而现在同一份导出里可能同时有人民币和美元的账。
	// 「客户端」导的是显示名（映射优先、快照兜底，见上面的 clientCell；
	// 不是 id、更不是 UA 原文）：CSV 面向人，只放短名 —— UA 有逗号括号
	// 分号，塞进平铺的 CSV 会把表格撑乱，它的查看点在详情抽屉里。
	b.WriteString("请求时间,模型,思考,状态,密钥,客户端,渠道,输入Token,输出Token,缓存命中,缓存写入,推理Token,首字耗时(ms),总共耗时(ms),费用,币种,trace_id\n")
	for i := range items {
		r := &items[i]
		row := []string{
			r.CreatedAt.Format(time.RFC3339),
			r.ModelRequested, r.ThinkingLevel, strconv.Itoa(r.StatusCode), r.APIKeyName, clientCell(r), r.ChannelName,
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
// 提前回 400，不把外键拒绝伪装成 500。代理与密钥路由的引用方共用一份。
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
