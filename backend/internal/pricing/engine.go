package pricing

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/relay"
)

// per1M 是单价的计价单位：每 100 万 token 的美元价。
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
	// 缓存写入按输入价计费（Anthropic 的写入溢价较高，官方未统一口径，这里取输入价）
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
// 缓存整张 channel_models（几百行）而不是按需查库：计费发生在每次请求结束时，
// 那时候再查一次库等于给转发链路加一跳。价格改了由调用方 Invalidate。
type Engine struct {
	db    *gorm.DB
	ttl   time.Duration
	mu    sync.RWMutex
	cache map[string]model.ChannelModel
	// names 记住每个渠道有哪些模型：不是用来查价的，而是给「未配价」
	// 这类统计用 —— 计价只需要 cache
	loadedAt time.Time
	lastErr  error
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
	return &Engine{db: db, ttl: ttl, cache: map[string]model.ChannelModel{}}
}

// Invalidate 在定价被修改或同步后强制刷新缓存。
func (e *Engine) Invalidate() {
	e.mu.Lock()
	e.loadedAt = time.Time{}
	e.mu.Unlock()
}

// Resolve 解析「某个渠道的某个模型」在 at 时刻的单价。
// 第二个返回值表示这条白名单记录是否存在且配了价。
//
// 两个都不满足时返回 false，调用方据此把费用记成 0 —— 这也是为什么
// 渠道列表要显式提示「未配价的模型」：漏配的后果在账面上看不出异常。
func (e *Engine) Resolve(ctx context.Context, channelID uint, modelName string, at time.Time) (Price, bool) {
	key := strings.TrimSpace(modelName)
	if key == "" || channelID == 0 {
		return Price{}, false
	}
	e.refresh(ctx)

	e.mu.RLock()
	defer e.mu.RUnlock()

	base, ok := e.cache[cacheKey(channelID, key)]
	if !ok {
		return Price{}, false
	}
	if base.InputPer1M.IsZero() && base.OutputPer1M.IsZero() &&
		base.CacheReadPer1M.IsZero() && base.CacheWritePer1M.IsZero() && base.Multiplier <= 1 {
		// 四个单价全 0 且没有倍率 = 这条模型还没配价。
		// 继续算下去会得到 0 元，与「配了价但单价是 0」分不开，不如如实说没价
		return Price{}, false
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
		// 币种恒定美元：原来只有 ModelPricing 带 currency 字段，
		// 而全站金额都按美元展示，留着那个字段只会让人以为能改
		Currency:        "USD",
		InputPer1M:      base.InputPer1M.Mul(dec),
		OutputPer1M:     base.OutputPer1M.Mul(dec),
		CacheReadPer1M:  base.CacheReadPer1M.Mul(dec),
		CacheWritePer1M: base.CacheWritePer1M.Mul(dec),
		Multiplier:      dec,
		Source:          source,
		PeakLabel:       label,
		PeakApplied:     source == MultiplierSourcePeak,
		Base:            base,
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
	cache := make(map[string]model.ChannelModel, len(rows))
	for _, r := range rows {
		cache[cacheKey(r.ChannelID, r.PublicName)] = r
	}
	e.cache = cache
	e.loadedAt = time.Now()
	e.lastErr = nil
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

// ValidatePrice 校验一行价格的合理性，供管理接口在保存渠道模型前调用。
//
// 时段规则的窗口合法性问题在 NormalizeRules 里查：窗口写错（起止相同、
// 时间格式不对）不会报错、只会永不命中，用户会以为「配了却没生效」。
func ValidatePrice(p model.ChannelModel) error {
	if p.InputPer1M.IsNegative() || p.OutputPer1M.IsNegative() ||
		p.CacheReadPer1M.IsNegative() || p.CacheWritePer1M.IsNegative() {
		return fmt.Errorf("单价不能为负")
	}
	if p.Multiplier < 0 {
		return fmt.Errorf("固定倍率不能为负")
	}
	if p.Multiplier > MaxMultiplier {
		return fmt.Errorf("固定倍率不能超过 %g", MaxMultiplier)
	}
	if _, err := NormalizeRules(p.PeakRules); err != nil {
		return err
	}
	return nil
}
