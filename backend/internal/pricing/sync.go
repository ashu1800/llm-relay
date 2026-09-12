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
	// ProviderID 是这条价格所属的模型商。为 0 表示不属于当前已接入的模型商
	// （LiteLLM 覆盖数百家，绝大多数我们并没有接入）。
	// 此前这个字段是写死的 1，导致 DeepSeek 官方价被标成了 OpenAI。
	ProviderID      uint
	ModelKey        string
	InputPer1M      decimal.Decimal
	OutputPer1M     decimal.Decimal
	CacheReadPer1M  decimal.Decimal
	CacheWritePer1M decimal.Decimal
	PeakRules       model.JSONList
}

// providerIDs 返回「模型商编码 -> id」的映射，用于给定价标注归属。
// 查表而不是写死 id，避免模型商 id 变动后静默标错。
func (s *Syncer) providerIDs() map[string]uint {
	var rows []model.Provider
	if err := s.db.Find(&rows).Error; err != nil {
		s.logger.Warn("读取模型商列表失败，定价归属将留空", "err", err)
		return map[string]uint{}
	}
	out := make(map[string]uint, len(rows))
	for _, p := range rows {
		out[strings.ToLower(strings.TrimSpace(p.Code))] = p.ID
	}
	return out
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
	Source        string `json:"source"`
	Status        string `json:"status"`
	Added         int    `json:"added"`
	Updated       int    `json:"updated"`
	Unchanged     int    `json:"unchanged"`
	SkippedManual int    `json:"skipped_manual"`
	// Pruned 是本次清理掉的过时裸名条数，见 pruneOrphanBareNames
	Pruned    int       `json:"pruned,omitempty"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
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
	if source == SourceLiteLLM {
		res.Pruned = s.pruneOrphanBareNames()
	}
	s.logger.Info("定价同步完成", "source", source,
		"added", res.Added, "updated", res.Updated,
		"unchanged", res.Unchanged, "skipped", res.SkippedManual,
		"pruned", res.Pruned)
	s.recordHistory(res, url)
	return res
}

// pruneOrphanBareNames 删除「无前缀、且不属于已接入模型商」的自动源定价行。
//
// 这些行的来历：LiteLLM 一份表里有十几家托管商提供同名模型，早期实现把
// 「provider/model」额外拆成裸名也写一遍，同一裸名被反复覆盖，最后留下的是
// 字典序最大的那家（实测 deepseek-v4-flash 被 15 个来源争抢，tencent 胜出）。
//
// 现在裸名只由已接入的模型商（provider_id > 0）写，剩下的 provider_id=0 裸名
// 再也不会被刷新 —— 留着只会变成冻结的假数据：看起来有价，实际停在某次同步的
// 瞬时值上，而且会盖住用户后来新建的同名模型。
//
// 只删自动源的裸名行：手工录入（source=manual）与带前缀的全名都不受影响。
func (s *Syncer) pruneOrphanBareNames() int {
	res := s.db.Exec(
		"DELETE FROM model_pricings WHERE source = ? AND provider_id = 0 AND model_key NOT LIKE '%/%'",
		SourceLiteLLM)
	if res.Error != nil {
		s.logger.Warn("清理过时裸名失败", "err", res.Error)
		return 0
	}
	if res.RowsAffected > 0 {
		s.logger.Info("已清理不再维护的裸名定价", "条数", res.RowsAffected)
	}
	return int(res.RowsAffected)
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
			ProviderID: e.ProviderID, ModelKey: e.ModelKey, MatchType: "exact", Currency: "USD",
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

	// 归属也要纳入「是否变化」：价格没变但模型商标错了（例如官方价此前
	// 统一记成了 OpenAI），重跑一次同步就应该把它纠正过来。
	unchanged := existing.InputPer1M.Equal(e.InputPer1M) &&
		existing.OutputPer1M.Equal(e.OutputPer1M) &&
		existing.CacheReadPer1M.Equal(e.CacheReadPer1M) &&
		existing.CacheWritePer1M.Equal(e.CacheWritePer1M) &&
		existing.ProviderID == e.ProviderID
	if unchanged {
		return 0, 0, 1, 0
	}

	// 列名必须用 model 里的常量：GORM 推导出的是 per1_m 而不是 per_1m，
	// 照 json 名写会报「列不存在」，而且只在更新已有行时才暴露
	updates := map[string]any{
		"provider_id":                   e.ProviderID,
		model.ColPricingInputPer1M:      e.InputPer1M,
		model.ColPricingOutputPer1M:     e.OutputPer1M,
		model.ColPricingCacheReadPer1M:  e.CacheReadPer1M,
		model.ColPricingCacheWritePer1M: e.CacheWritePer1M,
		"peak_rules":                    e.PeakRules,
		"source":                        source,
		"source_url":                    url,
		"priority":                      priority,
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
	// 脚注正文形如「(1) Use deepseek-flash as the model name. The legacy names
	// deepseek-v4-flash and deepseek-v4-flash-vision-exp are still accepted, ...」
	footnoteItemRe = regexp.MustCompile("\\((\\d{1,2})\\)\\s*([^()]{20,800})")
	legacyNamesRe  = regexp.MustCompile("(?i)legacy names?\\s+(.+?)\\s+are still accepted")
	modelNameRe    = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._-]*$")
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
	entries, err := parseDeepSeekTable(body)
	if err != nil {
		return nil, err
	}
	// 这个页面只覆盖 DeepSeek，归属固定；查表取 id 而不是写死数字
	id := s.providerIDs()["deepseek"]
	for i := range entries {
		entries[i].ProviderID = id
	}
	return entries, nil
}

// footnoteAliases 返回「表头第几列 -> 该列模型的遗留别名」。
//
// 为什么需要它：官方给 deepseek-flash 挂了脚注 (1)，正文写明
// 「legacy names deepseek-v4-flash and deepseek-v4-flash-vision-exp are still
// accepted ... billed at the Flash price」——旧名字仍然可用且按同一价格计费。
// 不把别名一并生成定价行的话，调用方用旧名字请求就会匹配不到价格，
// 费用静默记成 0（或落到 LiteLLM 里某家无关托管商的报价上）。
//
// 抽不到就返回空，不影响主流程。
func footnoteAliases(body []byte, header []string) map[int][]string {
	text := html.UnescapeString(tagRe.ReplaceAllString(string(body), " "))
	text = strings.Join(strings.Fields(text), " ")

	// 同一个编号可能出现多次（表头里的「deepseek-flash (1)」也算一次），
	// 取最长的那段正文 —— 真正的脚注是一长句，表头那次只会捕获到相邻的模型名。
	notes := map[string]string{}
	for _, m := range footnoteItemRe.FindAllStringSubmatch(text, -1) {
		if len(m[2]) > len(notes[m[1]]) {
			notes[m[1]] = m[2]
		}
	}

	outMap := map[int][]string{}
	for i, h := range header[1:] {
		fn := footnoteRe.FindString(h)
		if fn == "" {
			continue
		}
		num := strings.Trim(fn, "()")
		note, ok := notes[num]
		if !ok {
			continue
		}
		m := legacyNamesRe.FindStringSubmatch(note)
		if m == nil {
			continue
		}
		// 名字列表用 and / or / 逗号分隔，按分隔符切开。
		// 不能逐词取 —— 那样 "and" 本身会被当成一个模型名写进定价表。
		phrase := strings.ReplaceAll(m[1], " or ", ", ")
		phrase = strings.ReplaceAll(phrase, " and ", ", ")
		for _, part := range strings.Split(phrase, ",") {
			name := strings.TrimSpace(strings.Trim(part, ".,;"))
			if modelNameRe.MatchString(name) {
				outMap[i] = append(outMap[i], name)
			}
		}
	}
	return outMap
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

	aliasMap := footnoteAliases(body, header)
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
		base := Entry{
			ModelKey:        name,
			InputPer1M:      b.offInput,
			OutputPer1M:     b.offOutput,
			CacheReadPer1M:  b.offCache,
			CacheWritePer1M: b.offInput,
			PeakRules:       rules,
		}
		entries = append(entries, base)

		// 官方脚注承认的遗留别名按同一价格计费，同样生成定价行
		for _, alias := range aliasMap[i] {
			if alias == name {
				continue
			}
			a := base
			a.ModelKey = alias
			entries = append(entries, a)
		}
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

	ids := s.providerIDs()
	// 裸名的归属登记，见下方写入处的说明
	bareOwner := map[string]string{}
	bareConflicts := 0
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
		// 只有已接入的模型商才标 id，其余留 0 表示「不属于已接入的模型商」，
		// 而不是一律算到 OpenAI 头上
		pid := ids[strings.ToLower(provider)]
		entries = append(entries, Entry{
			ProviderID: pid, ModelKey: k, InputPer1M: in, OutputPer1M: out,
			CacheReadPer1M: cacheRead, CacheWritePer1M: cacheWrite, PeakRules: rules,
		})
		// 裸名只在「该模型商已接入」时才写。
		//
		// LiteLLM 一份表里有十几家托管商提供同名模型（实测 deepseek-v4-flash
		// 有 15 个来源，输入价从 0.088 到 0.25），而 upsert 只按 model_key 定位、
		// 同优先级后写覆盖先写 —— 不设限时最终留下的是「字典序最大的那家」，
		// 与模型真正的来源无关。线上就是这样把 DeepSeek 的价写成了 tencent 的。
		//
		// 已接入的模型商之间若仍撞名，用 bareOwner 记住先写入者（键已排序，
		// 结果确定），后来者跳过，避免同一轮同步里自相覆盖。
		if idx := strings.Index(k, "/"); idx > 0 && pid > 0 {
			bare := k[idx+1:]
			if owner, taken := bareOwner[bare]; !taken {
				bareOwner[bare] = k
				entries = append(entries, Entry{
					ProviderID: pid, ModelKey: bare, InputPer1M: in, OutputPer1M: out,
					CacheReadPer1M: cacheRead, CacheWritePer1M: cacheWrite, PeakRules: rules,
				})
			} else if owner != k {
				bareConflicts++
			}
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("LiteLLM 价格表解析结果为空")
	}
	if bareConflicts > 0 {
		s.logger.Info("LiteLLM 裸名冲突已按来源保留首个",
			"跳过", bareConflicts, "说明", "同一裸名被多个已接入模型商提供")
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
