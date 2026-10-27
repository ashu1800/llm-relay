package pricing

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/relay"
)

// per1M 是单价的计价单位：每 100 万 token 的价格（币种跟着渠道走，见 Price.Currency）。
var per1M = decimal.NewFromInt(1000000)

// Price 是某一时刻对「某渠道的某个模型」生效的单价。
type Price struct {
	// ChannelID + ModelName 是价格的归属：同一个模型名在不同渠道成本不同，
	// 所以计费必须带上渠道，不能只按模型名查
	ChannelID       uint
	ModelName       string
	Currency        string
	InputPer1M      decimal.Decimal
	OutputPer1M     decimal.Decimal
	CacheReadPer1M  decimal.Decimal
	CacheWritePer1M decimal.Decimal
	// Multiplier 是最终生效的倍率（时段倍率优先，其次固定倍率，都没有则为 1）
	Multiplier decimal.Decimal
	// Source 说明倍率来自哪里：peak / fixed / none
	Source    string
	PeakLabel string
	// PeakApplied 只表示「时段规则命中」，固定倍率不算峰时
	PeakApplied bool
	Base        model.ChannelModel
	// Priced 表示这次计价真的取到了单价（模型在所属币种下配了价）。
	// false 时金额必然为 0，供界面区分「真实的 0 元」与「漏配价」。
	Priced bool
}

// 倍率来源，写进定价快照，事后能一眼看出这笔钱是按时段算的还是按固定倍率算的。
const (
	MultiplierSourcePeak  = "peak"
	MultiplierSourceFixed = "fixed"
	MultiplierSourceNone  = "none"
)

// Cost 按归一化后的用量计算预估费用。
// PromptTokens 是未命中缓存的输入，与 CachedTokens 互不重叠。
func (p Price) Cost(u relay.Usage) decimal.Decimal {
	total := decimal.Zero
	total = total.Add(scale(p.InputPer1M, u.PromptTokens))
	total = total.Add(scale(p.OutputPer1M, u.CompletionTokens))
	total = total.Add(scale(p.CacheReadPer1M, u.CachedTokens))
	// 缓存写单价是**独立配置项**：Anthropic 官方对 cache_creation 收输入价的
	// 1.25 倍（5m 档）/ 2 倍（1h 档），其它上游口径不一，所以不做隐式兜底 ——
	// 未配置时按 0 计（站主拍板：不需要为此提醒）。
	// （漏配的后果是这笔费用被记成 0；宁可显式为 0 也不要悄悄替用户按某个
	// 倍率算 —— 倍率填错比漏配更难发现。）
	total = total.Add(scale(p.CacheWritePer1M, u.CacheCreationTokens))
	return total
}

// Snapshot 是可写入日志的定价快照，保证历史账目不会因日后改价而漂移。
func (p Price) Snapshot(at time.Time) model.JSONMap {
	return model.JSONMap{
		// model_key 保留原键名：历史日志的解析与界面展示都按它取值，
		// 换名字只会让新旧日志长得不一样
		"model_key":          p.ModelName,
		"channel_id":         p.ChannelID,
		"currency":           p.Currency,
		"input_per_1m":       p.InputPer1M.String(),
		"output_per_1m":      p.OutputPer1M.String(),
		"cache_read_per_1m":  p.CacheReadPer1M.String(),
		"cache_write_per_1m": p.CacheWritePer1M.String(),
		"multiplier":         p.Multiplier.String(),
		"multiplier_source":  p.Source,
		"fixed_multiplier":   strconv.FormatFloat(p.Base.Multiplier, 'f', -1, 64),
		"peak_applied":       p.PeakApplied,
		"peak_label":         p.PeakLabel,
		// 带时区偏移的本地时间：时段规则是按本地时间判断的，
		// 只记 UTC 的话事后核对「到底算不算峰时」要自己换算
		"resolved_at": at.Format(time.RFC3339),
	}
}

// scale 计算 price * tokens / 1e6，先乘后除以保留精度。
func scale(price decimal.Decimal, tokens int) decimal.Decimal {
	if tokens <= 0 || price.IsZero() {
		return decimal.Zero
	}
	return price.Mul(decimal.NewFromInt(int64(tokens))).Div(per1M)
}

// Engine 负责把「渠道 + 模型名 + 时刻」解析成单价，带进程内缓存。
//
// 缓存整张 model_prices（几十到几百行）而不是按需查库：计费发生在每次请求
// 结束时，那时候再查一次库等于给转发链路加一跳。价格改了由调用方 Invalidate。
//
// 2026-10-27 起单价是**模型级**的（model.ModelPrice），渠道只提供币种与
// 倍率。缓存因此拆成两张表的内容：模型价 → 按模型名查；币种/倍率/时段规则
// → 按「渠道 × 模型」查（它们仍在 channel_models 上）。
type Engine struct {
	db    *gorm.DB
	ttl   time.Duration
	mu    sync.RWMutex
	cache map[string]model.ModelPrice
	// rows 是白名单行：提供倍率、时段规则与「这条模型在该渠道是否启用」。
	// 单价不再从这里取 —— 那部分已经搬到 model_prices。
	rows map[string]model.ChannelModel
	// currencies 是「渠道 ID -> 记账币种」。金额带币种，算价时就要用到它。
	currencies map[uint]string
	loadedAt   time.Time
	lastErr    error
}

// cacheKey 拼出缓存键。用 NUL 分隔而不是 ":" 或 "-"：
// 模型名里本来就可能有这些字符（claude-3-5-sonnet），拼起来会撞键。
func cacheKey(channelID uint, model string) string {
	return strconv.FormatUint(uint64(channelID), 10) + "\x00" + model
}

// NewEngine 构造计价引擎。
func NewEngine(db *gorm.DB, ttl time.Duration) *Engine {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &Engine{
		db:         db,
		ttl:        ttl,
		cache:      map[string]model.ModelPrice{},
		rows:       map[string]model.ChannelModel{},
		currencies: map[uint]string{},
	}
}

// Invalidate 在定价被修改或同步后强制刷新缓存。
func (e *Engine) Invalidate() {
	e.mu.Lock()
	e.loadedAt = time.Time{}
	e.mu.Unlock()
}

// CurrencyOf 返回某个渠道的记账币种。
//
// 与 Resolve 分开是因为「这笔账是哪个币种」和「这个模型配没配价」是两件事：
// 没配价的调用金额是 0，但币种仍要如实记下来，否则同一页日志里会出现
// 「有的行有币种、有的行空着」，只能靠人猜。
func (e *Engine) CurrencyOf(ctx context.Context, channelID uint) string {
	if channelID == 0 {
		return model.CurrencyUSD
	}
	e.refresh(ctx)

	e.mu.RLock()
	defer e.mu.RUnlock()
	if c := e.currencies[channelID]; c != "" {
		return c
	}
	return model.CurrencyUSD
}

// Resolve 解析「某个渠道的某个模型」在 at 时刻的单价。
// 第二个返回值表示这条白名单记录存在、且该模型**在所属渠道的币种下**配了价。
//
// 取价顺序（2026-10-27 模型级定价）：
//  1. 白名单行必须存在且启用 —— 没上白名单的模型不该计费；
//  2. 单价按**模型名**从 model_prices 取，再按渠道币种选美元或人民币那一档；
//  3. 倍率仍来自白名单行（时段规则优先，其次固定倍率）。
//
// 不明码标价时返回 false，调用方据此把费用记成 0 —— 这也是为什么渠道列表
// 仍要提示「未配价的模型」：漏配的后果在账面上看不出异常。
func (e *Engine) Resolve(ctx context.Context, channelID uint, modelName string, at time.Time) (Price, bool) {
	key := strings.TrimSpace(modelName)
	if key == "" || channelID == 0 {
		return Price{}, false
	}
	e.refresh(ctx)

	e.mu.RLock()
	defer e.mu.RUnlock()

	base, ok := e.rows[cacheKey(channelID, key)]
	if !ok {
		return Price{}, false
	}

	// 币种先定下来：它决定取哪一档单价，也决定这条模型到底「有没有配价」
	currency := e.currencies[channelID]
	if currency == "" {
		// 加列之前的老库或渠道被手工改坏时兜底，不让金额挂到空币种上
		currency = model.CurrencyUSD
	}

	row, priced := e.cache[base.PublicName]
	var unit model.PriceSet
	if priced {
		unit = row.PriceFor(currency)
	}
	// 该币种四档全 0 = 这个模型没配这一档价。与「配成 0 元」在账面等价，
	// 但语义不同：前者会让整笔费用记 0 且不标价，后者同样是 0。
	// 两者都记 0，所以这里只要求「有白名单行」即可继续 —— 未配价时
	// 金额自然是 0（见下面 Price.Cost），但倍率与快照仍有意义。
	if !priced || unit.IsZero() {
		// 没有价格也没有倍率：如实说「没价」，不让调用方以为这里有账可看
		if base.Multiplier <= 1 && len(base.PeakRules) == 0 {
			return Price{}, false
		}
	}

	// 时段规则优先，其次固定倍率。
	//
	// 这里刻意不把两者相乘：用户的心智是「这个时段按 2 倍算」，
	// 固定倍率是「平时也按 1.5 倍算」，相乘会得到 3 倍，没人预期得到。
	//
	// 也刻意不再把倍率夹到 >= 1：倍率可以是折扣（0.5）。
	// 原来的夹取会让打折规则静默失效 —— 配了没反应比报错更难查。
	mult, label, peakHit := MatchPeak(base.PeakRules, at)
	m, source := 1.0, MultiplierSourceNone
	switch {
	case peakHit:
		m, source = mult, MultiplierSourcePeak
	case base.Multiplier > 0 && base.Multiplier != 1:
		m, source = base.Multiplier, MultiplierSourceFixed
	}
	dec := decimal.NewFromFloat(m)

	return Price{
		ChannelID: channelID,
		ModelName: base.PublicName,
		// 币种跟着渠道走：人民币渠道取人民币那一档单价。
		//
		// 这里曾经写死 USD，理由是「全站金额都按美元展示，留个能改的字段
		// 只会让人以为能改」—— 那在当时是对的（那时确实只有美元一种口径），
		// 现在上游有人民币账单，写死就变成了「把人民币数字标成美元」。
		// 换算仍然不做：币种只用来分别展示、分别统计（见 model/currency.go）。
		Currency:        currency,
		InputPer1M:      unit.InputPer1M.Mul(dec),
		OutputPer1M:     unit.OutputPer1M.Mul(dec),
		CacheReadPer1M:  unit.CacheReadPer1M.Mul(dec),
		CacheWritePer1M: unit.CacheWritePer1M.Mul(dec),
		Multiplier:      dec,
		Source:          source,
		PeakLabel:       label,
		PeakApplied:     source == MultiplierSourcePeak,
		Base:            base,
		// Priced 区分「配了价但算出来 0」与「压根没配价」：
		// 前者是真实的 0 元订单，后者是漏配 —— 界面上要能分别指出。
		Priced: priced && !unit.IsZero(),
	}, true
}

// refresh 在缓存过期时重新载入价格。
func (e *Engine) refresh(ctx context.Context) {
	e.mu.RLock()
	fresh := time.Since(e.loadedAt) < e.ttl && e.loadedAt != (time.Time{})
	e.mu.RUnlock()
	if fresh {
		return
	}

	// 只取启用的白名单条目：模型停用之后它就不该再参与计费，
	// 否则日志里会给一个根本没转发的模型算钱
	var rows []model.ChannelModel
	err := e.db.WithContext(ctx).Where("enabled = ?", true).Find(&rows).Error

	// 单价按模型名存（model_prices），币种按渠道存，三张表在同一次刷新里读回来
	var prices []model.ModelPrice
	errPrice := e.db.WithContext(ctx).Find(&prices).Error

	var chans []struct {
		ID       uint
		Currency string
	}
	errCurrency := e.db.WithContext(ctx).Model(&model.Channel{}).Select("id, currency").Find(&chans).Error

	e.mu.Lock()
	defer e.mu.Unlock()
	// 双检：并发刷新时以先到者为准
	if time.Since(e.loadedAt) < e.ttl && e.loadedAt != (time.Time{}) {
		return
	}
	if err != nil {
		e.lastErr = err
		return
	}
	cache := make(map[string]model.ModelPrice, len(prices))
	for _, p := range prices {
		cache[p.ModelName] = p
	}
	e.cache = cache

	rowMap := make(map[string]model.ChannelModel, len(rows))
	for _, r := range rows {
		rowMap[cacheKey(r.ChannelID, r.PublicName)] = r
	}
	e.rows = rowMap

	// 价格表读不回来是硬错误（会把所有模型记成 0 元），必须记下来
	if errPrice != nil {
		e.lastErr = errPrice
	}
	if errCurrency == nil {
		currencies := make(map[uint]string, len(chans))
		for _, ch := range chans {
			currencies[ch.ID] = ch.Currency
		}
		e.currencies = currencies
	} else if errPrice == nil {
		// 币种读不回来会影响「取哪一档单价」，比丢金额轻一些，但也不能一声不吭：
		// 系统页会显示这个错误
		e.lastErr = errCurrency
	}
	e.loadedAt = time.Now()
	if errPrice == nil && errCurrency == nil {
		e.lastErr = nil
	}
}

// LastError 返回最近一次加载定价表的错误，供系统页展示。
func (e *Engine) LastError() error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lastErr
}

// FormatCost 统一金额展示精度。
func FormatCost(d decimal.Decimal) string {
	return d.Round(8).String()
}

// 价格的合法性校验在 api.applyPriceFields 里随写入一起做：单价非负
// （parseDecimal 拒绝负数）、倍率区间、时段规则（NormalizeRules）。
// 刻意不在这里再包一层 Validate —— 两份规则迟早会不一致，
// 而漏掉的那一条的表现是「保存成功但规则永不命中」。
