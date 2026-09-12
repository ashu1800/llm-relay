package pricing

import (
	"context"
	"fmt"
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

// Price 是某一时刻对某模型生效的单价。
type Price struct {
	ModelKey        string
	Currency        string
	InputPer1M      decimal.Decimal
	OutputPer1M     decimal.Decimal
	CacheReadPer1M  decimal.Decimal
	CacheWritePer1M decimal.Decimal
	Multiplier      decimal.Decimal
	PeakLabel       string
	PeakApplied     bool
	Base            model.ModelPricing
}

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
		"model_key":          p.ModelKey,
		"currency":           p.Currency,
		"input_per_1m":       p.InputPer1M.String(),
		"output_per_1m":      p.OutputPer1M.String(),
		"cache_read_per_1m":  p.CacheReadPer1M.String(),
		"cache_write_per_1m": p.CacheWritePer1M.String(),
		"multiplier":         p.Multiplier.String(),
		"peak_applied":       p.PeakApplied,
		"peak_label":         p.PeakLabel,
		"resolved_at":        at.UTC().Format(time.RFC3339),
	}
}

// scale 计算 price * tokens / 1e6，先乘后除以保留精度。
func scale(price decimal.Decimal, tokens int) decimal.Decimal {
	if tokens <= 0 || price.IsZero() {
		return decimal.Zero
	}
	return price.Mul(decimal.NewFromInt(int64(tokens))).Div(per1M)
}

// Engine 负责把「模型名 + 时刻」解析成单价，带进程内缓存。
type Engine struct {
	db    *gorm.DB
	ttl   time.Duration
	mu    sync.RWMutex
	cache map[string]model.ModelPricing
	// prefixes 保存以通配方式配置的规则，按模型名长度倒序，保证最长匹配优先
	prefixes []model.ModelPricing
	loadedAt time.Time
	lastErr  error
}

// NewEngine 构造计价引擎。
func NewEngine(db *gorm.DB, ttl time.Duration) *Engine {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &Engine{db: db, ttl: ttl, cache: map[string]model.ModelPricing{}}
}

// Invalidate 在定价被修改或同步后强制刷新缓存。
func (e *Engine) Invalidate() {
	e.mu.Lock()
	e.loadedAt = time.Time{}
	e.mu.Unlock()
}

// Resolve 解析模型在 at 时刻的单价。第二个返回值表示是否命中定价配置。
func (e *Engine) Resolve(ctx context.Context, modelKey string, at time.Time) (Price, bool) {
	key := strings.TrimSpace(modelKey)
	if key == "" {
		return Price{}, false
	}
	e.refresh(ctx)

	e.mu.RLock()
	defer e.mu.RUnlock()

	base, ok := e.lookupLocked(key)
	if !ok {
		return Price{}, false
	}

	mult, label := MatchPeak(base.PeakRules, at)
	m := decimal.NewFromFloat(mult)
	if m.LessThan(decimal.NewFromInt(1)) {
		m = decimal.NewFromInt(1)
	}

	return Price{
		ModelKey:        base.ModelKey,
		Currency:        base.Currency,
		InputPer1M:      base.InputPer1M.Mul(m),
		OutputPer1M:     base.OutputPer1M.Mul(m),
		CacheReadPer1M:  base.CacheReadPer1M.Mul(m),
		CacheWritePer1M: base.CacheWritePer1M.Mul(m),
		Multiplier:      m,
		PeakLabel:       label,
		PeakApplied:     m.GreaterThan(decimal.NewFromInt(1)),
		Base:            base,
	}, true
}

// lookupLocked 先精确匹配模型名，再按前缀规则做最长匹配。
func (e *Engine) lookupLocked(key string) (model.ModelPricing, bool) {
	if p, ok := e.cache[key]; ok {
		return p, true
	}
	for _, p := range e.prefixes {
		if strings.HasPrefix(key, p.ModelKey) {
			return p, true
		}
	}
	return model.ModelPricing{}, false
}

// refresh 在缓存过期时重新载入定价表。
func (e *Engine) refresh(ctx context.Context) {
	e.mu.RLock()
	fresh := time.Since(e.loadedAt) < e.ttl && e.loadedAt != (time.Time{})
	e.mu.RUnlock()
	if fresh {
		return
	}

	var rows []model.ModelPricing
	err := e.db.WithContext(ctx).Where("active = ?", true).Find(&rows).Error

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
	cache := make(map[string]model.ModelPricing, len(rows))
	var prefixes []model.ModelPricing
	for _, r := range rows {
		if r.MatchType == "prefix" {
			prefixes = append(prefixes, r)
			continue
		}
		cache[r.ModelKey] = r
	}
	// 长前缀优先，避免 gpt- 规则抢占 gpt-5- 规则
	sortByKeyLenDesc(prefixes)
	e.cache = cache
	e.prefixes = prefixes
	e.loadedAt = time.Now()
	e.lastErr = nil
}

// LastError 返回最近一次加载定价表的错误，供系统页展示。
func (e *Engine) LastError() error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lastErr
}

func sortByKeyLenDesc(rows []model.ModelPricing) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && len(rows[j].ModelKey) > len(rows[j-1].ModelKey); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

// FormatCost 统一金额展示精度。
func FormatCost(d decimal.Decimal) string {
	return d.Round(8).String()
}

// Validate 校验定价行的合理性，供管理接口调用。
func Validate(p model.ModelPricing) error {
	if strings.TrimSpace(p.ModelKey) == "" {
		return fmt.Errorf("模型名不能为空")
	}
	if p.InputPer1M.IsNegative() || p.OutputPer1M.IsNegative() ||
		p.CacheReadPer1M.IsNegative() || p.CacheWritePer1M.IsNegative() {
		return fmt.Errorf("单价不能为负")
	}
	return nil
}
