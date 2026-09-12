package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"llm-relay/internal/model"
)

// 价格来源与优先级：手工录入永远压过自动同步，避免被定时任务覆盖。
const (
	SourceManual   = "manual"
	SourceOfficial = "official"
	SourceLiteLLM  = "litellm"

	PriorityManual   = 100
	PriorityOfficial = 50
	PriorityLiteLLM  = 10
)

// SourceURL 是各自动源的地址。
const (
	LiteLLMURL         = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	DeepSeekPricingURL = "https://api-docs.deepseek.com/quick_start/pricing"
)

// Entry 是解析自外部源的一条定价，单价统一为「每 100 万 token 的美元价」。
type Entry struct {
	ModelKey        string
	InputPer1M      decimal.Decimal
	OutputPer1M     decimal.Decimal
	CacheReadPer1M  decimal.Decimal
	CacheWritePer1M decimal.Decimal
	PeakRules       model.JSONList
}

// Syncer 负责把外部价格同步进库。
type Syncer struct {
	db     *gorm.DB
	engine *Engine
	client *http.Client
	logger *slog.Logger
}

// NewSyncer 构造同步器。
func NewSyncer(db *gorm.DB, engine *Engine, logger *slog.Logger) *Syncer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Syncer{
		db:     db,
		engine: engine,
		logger: logger,
		client: &http.Client{
			Timeout: 90 * time.Second,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				TLSHandshakeTimeout: 15 * time.Second,
				ForceAttemptHTTP2:   true,
			},
		},
	}
}

// SyncResult 汇总一次同步的结果。
type SyncResult struct {
	Source        string    `json:"source"`
	Status        string    `json:"status"`
	Added         int       `json:"added"`
	Updated       int       `json:"updated"`
	Unchanged     int       `json:"unchanged"`
	SkippedManual int       `json:"skipped_manual"`
	Error         string    `json:"error,omitempty"`
	StartedAt     time.Time `json:"started_at"`
}

// SyncAll 依次同步全部自动源，并写入同步历史。
func (s *Syncer) SyncAll(ctx context.Context) []SyncResult {
	results := []SyncResult{
		s.runSource(ctx, SourceOfficial, PriorityOfficial, DeepSeekPricingURL, s.fetchDeepSeekOfficial),
		s.runSource(ctx, SourceLiteLLM, PriorityLiteLLM, LiteLLMURL, s.fetchLiteLLM),
	}
	// 官方源先写、LiteLLM 后写，靠优先级保护：低优先级不会覆盖高优先级
	s.engine.Invalidate()
	return results
}

type fetchFunc func(ctx context.Context) ([]Entry, error)

func (s *Syncer) runSource(ctx context.Context, source string, priority int, url string, fetch fetchFunc) SyncResult {
	res := SyncResult{Source: source, Status: "ok", StartedAt: time.Now().UTC()}

	entries, err := fetch(ctx)
	if err != nil {
		res.Status = "failed"
		res.Error = err.Error()
		s.logger.Warn("定价同步失败", "source", source, "err", err)
		s.recordHistory(res, url)
		return res
	}

	for _, e := range entries {
		added, updated, unchanged, skipped := s.upsert(e, source, priority, url)
		res.Added += added
		res.Updated += updated
		res.Unchanged += unchanged
		res.SkippedManual += skipped
	}
	s.logger.Info("定价同步完成", "source", source,
		"added", res.Added, "updated", res.Updated,
		"unchanged", res.Unchanged, "skipped", res.SkippedManual)
	s.recordHistory(res, url)
	return res
}

func (s *Syncer) recordHistory(res SyncResult, url string) {
	finished := time.Now().UTC()
	_ = s.db.Create(&model.PricingSyncLog{
		Source: res.Source, Status: res.Status,
		Added: res.Added, Updated: res.Updated,
		Unchanged: res.Unchanged, SkippedManual: res.SkippedManual,
		Error: res.Error, StartedAt: res.StartedAt, FinishedAt: &finished,
	}).Error
}

// upsert 按优先级写入单条定价。
// 返回 added / updated / unchanged / skippedManual。
func (s *Syncer) upsert(e Entry, source string, priority int, url string) (int, int, int, int) {
	if strings.TrimSpace(e.ModelKey) == "" {
		return 0, 0, 0, 0
	}

	var existing model.ModelPricing
	err := s.db.Where("model_key = ?", e.ModelKey).First(&existing).Error

	if err == gorm.ErrRecordNotFound {
		row := model.ModelPricing{
			ProviderID: 1, ModelKey: e.ModelKey, MatchType: "exact", Currency: "USD",
			InputPer1M: e.InputPer1M, OutputPer1M: e.OutputPer1M,
			CacheReadPer1M: e.CacheReadPer1M, CacheWritePer1M: e.CacheWritePer1M,
			PeakRules: e.PeakRules, Source: source, SourceURL: url,
			Priority: priority, Active: true,
		}
		if err := s.db.Create(&row).Error; err != nil {
			s.logger.Warn("写入定价失败", "model", e.ModelKey, "err", err)
			return 0, 0, 0, 0
		}
		return 1, 0, 0, 0
	}
	if err != nil {
		s.logger.Warn("查询定价失败", "model", e.ModelKey, "err", err)
		return 0, 0, 0, 0
	}

	// 手工录入或更高优先级的来源不被覆盖
	if existing.Priority >= PriorityManual || existing.Priority > priority {
		return 0, 0, 0, 1
	}

	if existing.InputPer1M.Equal(e.InputPer1M) &&
		existing.OutputPer1M.Equal(e.OutputPer1M) &&
		existing.CacheReadPer1M.Equal(e.CacheReadPer1M) &&
		existing.CacheWritePer1M.Equal(e.CacheWritePer1M) {
		return 0, 0, 1, 0
	}

	updates := map[string]any{
		"input_per_1m":       e.InputPer1M,
		"output_per_1m":      e.OutputPer1M,
		"cache_read_per_1m":  e.CacheReadPer1M,
		"cache_write_per_1m": e.CacheWritePer1M,
		"peak_rules":         e.PeakRules,
		"source":             source,
		"source_url":         url,
		"priority":           priority,
	}
	if err := s.db.Model(&model.ModelPricing{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
		s.logger.Warn("更新定价失败", "model", e.ModelKey, "err", err)
		return 0, 0, 0, 0
	}
	return 0, 1, 0, 0
}

// ============================ DeepSeek 官方 ============================

var (
	tableRe = regexp.MustCompile("(?s)<table.*?</table>")
	rowRe   = regexp.MustCompile("(?s)<tr.*?</tr>")
	cellRe  = regexp.MustCompile("(?s)<t[hd][^>]*>.*?</t[hd]>")
	tagRe   = regexp.MustCompile("(?s)<[^>]*>")
	priceRe = regexp.MustCompile("\\$([0-9]+(?:\\.[0-9]+)?)")
	// 官方表头会把脚注编号跟在模型名后，例如 deepseek-flash(1)
	footnoteRe = regexp.MustCompile("\\([0-9]+\\)$")
)

// fetchDeepSeekOfficial 抓取并解析 DeepSeek 官方定价表。
// 表结构为「指标 + OFF-PEAK 行 + PEAK 行」，按状态机逐行读取。
func (s *Syncer) fetchDeepSeekOfficial(ctx context.Context) ([]Entry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DeepSeekPricingURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; llm-relay-pricing-sync/1.0)")
	req.Header.Set("Accept-Language", "en")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 DeepSeek 定价页失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DeepSeek 定价页返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return parseDeepSeekTable(body)
}

// parseDeepSeekTable 解析官方定价表。抽成纯函数以便用固定样本测试。
func parseDeepSeekTable(body []byte) ([]Entry, error) {
	table := tableRe.Find(body)
	if table == nil {
		return nil, fmt.Errorf("DeepSeek 定价页未找到表格")
	}

	var rows [][]string
	for _, r := range rowRe.FindAll(table, -1) {
		var cells []string
		for _, c := range cellRe.FindAll(r, -1) {
			txt := html.UnescapeString(tagRe.ReplaceAllString(string(c), " "))
			cells = append(cells, strings.Join(strings.Fields(txt), " "))
		}
		if len(cells) > 0 {
			rows = append(rows, cells)
		}
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("DeepSeek 定价表行数不足")
	}

	// 表头：MODEL | deepseek-flash | deepseek-v4-pro
	header := rows[0]
	if len(header) < 2 || !strings.EqualFold(header[0], "MODEL") {
		return nil, fmt.Errorf("DeepSeek 定价表表头异常: %v", header)
	}
	names := make([]string, 0, len(header)-1)
	for _, n := range header[1:] {
		names = append(names, strings.TrimSpace(footnoteRe.ReplaceAllString(n, "")))
	}

	type acc struct {
		offInput, peakInput   decimal.Decimal
		offOutput, peakOutput decimal.Decimal
		offCache, peakCache   decimal.Decimal
	}
	buckets := make([]acc, len(names))

	metric := ""
	mode := ""
	for _, cells := range rows[1:] {
		joined := strings.Join(cells, " ")
		upper := strings.ToUpper(joined)

		if strings.Contains(upper, "CACHE HIT") {
			metric = "cache"
		} else if strings.Contains(upper, "CACHE MISS") {
			metric = "input"
		} else if strings.Contains(upper, "OUTPUT TOKENS") {
			metric = "output"
		}

		if strings.Contains(upper, "OFF-PEAK") {
			mode = "off"
		} else if strings.HasPrefix(strings.TrimSpace(cells[0]), "PEAK") || strings.Contains(upper, "PEAK") {
			if mode == "off" {
				mode = "peak"
			}
		} else {
			continue
		}

		vals := parsePrices(joined)
		if len(vals) < len(names) {
			continue
		}
		for i := range names {
			v := vals[i]
			switch {
			case metric == "cache" && mode == "off":
				buckets[i].offCache = v
			case metric == "cache" && mode == "peak":
				buckets[i].peakCache = v
			case metric == "input" && mode == "off":
				buckets[i].offInput = v
			case metric == "input" && mode == "peak":
				buckets[i].peakInput = v
			case metric == "output" && mode == "off":
				buckets[i].offOutput = v
			case metric == "output" && mode == "peak":
				buckets[i].peakOutput = v
			}
		}
	}

	var entries []Entry
	for i, name := range names {
		b := buckets[i]
		// 基准价取谷时价；峰时通过 PeakRules 的倍率还原
		if b.offInput.IsZero() && b.offOutput.IsZero() {
			continue
		}
		rules := DeepSeekPeakRules()
		if !b.peakInput.IsZero() && !b.offInput.IsZero() && !b.peakInput.Equal(b.offInput) {
			// 用官方实际倍率替换硬编码值，价格调整时自动跟随
			mult, _ := b.peakInput.Div(b.offInput).Float64()
			rules = scaleRules(rules, mult)
		}
		entries = append(entries, Entry{
			ModelKey:        name,
			InputPer1M:      b.offInput,
			OutputPer1M:     b.offOutput,
			CacheReadPer1M:  b.offCache,
			CacheWritePer1M: b.offInput,
			PeakRules:       rules,
		})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("DeepSeek 定价表解析结果为空")
	}
	return entries, nil
}

func scaleRules(in model.JSONList, mult float64) model.JSONList {
	outRules := make(model.JSONList, 0, len(in))
	for _, r := range in {
		cp := model.JSONMap{}
		for k, v := range r {
			cp[k] = v
		}
		cp["multiplier"] = mult
		outRules = append(outRules, cp)
	}
	return outRules
}

func parsePrices(s string) []decimal.Decimal {
	matches := priceRe.FindAllStringSubmatch(s, -1)
	vals := make([]decimal.Decimal, 0, len(matches))
	for _, m := range matches {
		d, err := decimal.NewFromString(m[1])
		if err == nil {
			vals = append(vals, d)
		}
	}
	return vals
}

// ============================ LiteLLM ============================

// fetchLiteLLM 拉取 LiteLLM 社区维护的价格表。
//
// 重要：该表对 DeepSeek 记录的是「峰时价」（已与官方页面核对一致），
// 因此这里统一除以 2 还原为谷时基准价，并附加官方峰时规则，
// 否则谷时段的费用会被高估一倍。
func (s *Syncer) fetchLiteLLM(ctx context.Context) ([]Entry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, LiteLLMURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 LiteLLM 价格表失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LiteLLM 价格表返回 %d", resp.StatusCode)
	}

	var raw map[string]map[string]any
	dec := json.NewDecoder(io.LimitReader(resp.Body, 64<<20))
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("解析 LiteLLM 价格表失败: %w", err)
	}

	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var entries []Entry
	for _, k := range keys {
		item := raw[k]
		if item == nil {
			continue
		}
		// 只收对话类模型，跳过 embedding / 图像 / 音频，避免污染定价表
		if mode, ok := item["mode"].(string); ok && mode != "" && mode != "chat" && mode != "completion" {
			continue
		}
		in := perTokenToPer1M(item["input_cost_per_token"])
		out := perTokenToPer1M(item["output_cost_per_token"])
		if in.IsZero() && out.IsZero() {
			continue
		}
		cacheRead := perTokenToPer1M(item["cache_read_input_token_cost"])
		cacheWrite := perTokenToPer1M(item["cache_creation_input_token_cost"])

		provider, _ := item["litellm_provider"].(string)
		rules := model.JSONList(nil)
		if provider == "deepseek" {
			two := decimal.NewFromInt(2)
			in = in.Div(two)
			out = out.Div(two)
			cacheRead = cacheRead.Div(two)
			if !cacheWrite.IsZero() {
				cacheWrite = cacheWrite.Div(two)
			}
			rules = DeepSeekPeakRules()
		}
		if cacheWrite.IsZero() {
			cacheWrite = in
		}

		// 同时写入带 provider 前缀与裸名两种键，便于用任一种写法命中
		entries = append(entries, Entry{
			ModelKey: k, InputPer1M: in, OutputPer1M: out,
			CacheReadPer1M: cacheRead, CacheWritePer1M: cacheWrite, PeakRules: rules,
		})
		if idx := strings.Index(k, "/"); idx > 0 {
			bare := k[idx+1:]
			entries = append(entries, Entry{
				ModelKey: bare, InputPer1M: in, OutputPer1M: out,
				CacheReadPer1M: cacheRead, CacheWritePer1M: cacheWrite, PeakRules: rules,
			})
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("LiteLLM 价格表解析结果为空")
	}
	return entries, nil
}

func perTokenToPer1M(v any) decimal.Decimal {
	f, ok := v.(float64)
	if !ok || f <= 0 {
		return decimal.Zero
	}
	// 每 token 价 -> 每 100 万 token 价
	return decimal.NewFromFloat(f).Mul(decimal.NewFromInt(1000000)).Round(8)
}
