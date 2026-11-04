package pricing

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"llm-relay/internal/model"
)

// 参考价库（价格目录）：LiteLLM 上游持续维护的「数百个模型官方价格表」。
//
// 它在 llm-relay 里的角色被刻意限制为**建议价源**：
//   - 管理台定价页用它做「从参考价库填充」（美元档），用户确认后才落库；
//   - 漏配价不会被它静默兜底 —— 记账仍然只认 model_prices（见 engine.go
//     Resolve），漏配照旧在界面上点名。这是站主拍板的哲学：参考价可能有误
//     （中转站、折扣、口径差异），自动兜底会让错误的价格无声地进入账本。
//
// 数据有两个来源，解析走**同一段代码**（ParseLiteLLMRaw）：
//   1. 编译期内嵌：catalog_data/litellm_prices.json（上游原文件，
//      go:embed；更新用 scripts/update-price-catalog.py / make update-prices）；
//   2. 运行期下载：管理台「更新参考价库」拉上游原文件 → 解析裁剪 →
//      整份存进 settings 表（key=price_catalog_v1）。
// 查找优先级：DB 副本 > 内嵌。DB 副本坏了（手工改坏/版本回退）回落到内嵌，
// 不让一次坏数据把搜索功能整个带走。

// CatalogSettingKey 是参考价库在 settings 表里的行键。
// 带 v1：将来裁剪口径变了（加字段/换单位），换 v2 并在读取时放弃旧键，
// 避免新旧结构混用。
const CatalogSettingKey = "price_catalog_v1"

// CatalogEntry 是裁剪后的单个模型条目。字段名刻意用短 json 名：
// 整份目录以 jsonb 存进 settings，4526 条 × 长字段名的膨胀不可忽略。
//
// 单位约定（与 model.ModelPrice 的口径一一对应，填充时不做任何换算）：
//
//	InputPer1M / OutputPer1M / CacheReadPer1M / CacheWritePer1M —— 每 100 万 token；
//	PerImage —— 每张；PerSecond —— 每秒音频；PerCharacter —— 每字符。
//
// 上游字段是「每 1 token」的小数（如 2.5e-6），这里在解析时统一乘 1e6
// 折成「每百万」；三个非 token 维度上游本来就是每单位口径，照单全收。
type CatalogEntry struct {
	Mode            string  `json:"mode"`
	Provider        string  `json:"provider"`
	InputPer1M      float64 `json:"in,omitempty"`
	OutputPer1M     float64 `json:"out,omitempty"`
	CacheReadPer1M  float64 `json:"cr,omitempty"`
	CacheWritePer1M float64 `json:"cw,omitempty"`
	PerImage        float64 `json:"img,omitempty"`
	PerSecond       float64 `json:"sec,omitempty"`
	PerCharacter    float64 `json:"chr,omitempty"`
	MaxInput        int64   `json:"max_in,omitempty"`
	MaxOutput       int64   `json:"max_out,omitempty"`
}

// HasPrice 报告这个条目是否带任何价格信息。目录里存在少量只有元数据
// （上下文窗口、模式）没有价格的条目，它们对「填充」毫无用处，
// 搜索接口会把它们过滤掉，免得用户选中一个填不出任何价格的候选。
func (e CatalogEntry) HasPrice() bool {
	return e.InputPer1M > 0 || e.OutputPer1M > 0 || e.CacheReadPer1M > 0 || e.CacheWritePer1M > 0 ||
		e.PerImage > 0 || e.PerSecond > 0 || e.PerCharacter > 0
}

// Catalog 是裁剪后的整份目录，外加匹配用的归一化索引。
type Catalog struct {
	FetchedAt string                  `json:"fetched_at"`
	Source    string                  `json:"source"`
	Entries   map[string]CatalogEntry `json:"entries"`

	// normIndex 把「归一化名 -> 原始键列表」建在内存里（jsonb 持久化的是
	// Entries，这个索引每次加载时重建 —— 它是派生数据，不该进库）。
	normIndex map[string][]string
	// providers 是目录里出现过的全部 litellm_provider，用于归一化时
	// 判断「前缀是不是供应商前缀」（openai/gpt-4o 的 openai 要剥，
	// foo-bar/gpt-4o 的 foo-bar 不在目录里就不能剥）。
	providers map[string]bool
}

// embed 指向仓库内的上游原文件快照。编译期必须存在：
// 没有它 go build 直接失败 —— 这是刻意的，缺数据比静默降级更显眼。
//
//go:embed catalog_data/litellm_prices.json
var embeddedCatalogRaw []byte

// MaxCatalogRawBytes 是运行期下载原文件的大小上限（上游约 3.2MB，
// 给到 32MB 是为了容忍上游格式变化，同时挡住一个被劫持的响应）。
// 导出给 api 层做下载上限（update.FetchFile 的 maxSize 参数）。
const MaxCatalogRawBytes = 32 << 20

// perMillion 是「每 token 单价 -> 每百万 token 单价」的折算系数。
var perMillion = 1_000_000.0

// ParseLiteLLMRaw 解析上游原文件（完整版 model_prices_and_context_window.json），
// 裁剪成 Catalog。构建期内嵌文件与运行期下载的副本走这同一段代码。
//
// 裁剪规则（只保留记账与展示需要的字段，4526 条 3.2MB → 落库约 0.6MB）：
//   - 跳过非对象条目与 sample_spec（上游的格式说明样例）；
//   - token 四档 ×1e6 折成「每百万」；
//   - 图像取 output_cost_per_image，没有则退 input_cost_per_image
//     （gpt-image-1 按 token 计价、dall-e 系按张计价，后者才是「每张」口径；
//     按 token 计价的图像模型这里就没有 img 字段，属预期行为）；
//   - 音频转写取 input_cost_per_second，TTS 取 input_cost_per_character；
//   - 上下文窗口只留展示用，不参与任何逻辑。
//
// 忽略的 variant 档（*_batches / *_priority / *_flex / above_200k 等）是
// LiteLLM 的批量/优先级计价变体，llm-relay 的记账没有这些口径，取标准档。
func ParseLiteLLMRaw(raw []byte) (*Catalog, error) {
	var rawEntries map[string]map[string]any
	if err := json.Unmarshal(raw, &rawEntries); err != nil {
		return nil, fmt.Errorf("参考价库不是合法 JSON: %w", err)
	}
	cat := &Catalog{
		Entries:   make(map[string]CatalogEntry, len(rawEntries)),
		normIndex: map[string][]string{},
		providers: map[string]bool{},
	}
	for key, raw := range rawEntries {
		if raw == nil {
			continue
		}
		provider := strField(raw, "litellm_provider")
		if provider == "sample_spec" || strings.HasPrefix(key, "mode:") ||
			strings.HasPrefix(key, "sample_spec") {
			continue
		}
		entry := CatalogEntry{
			Mode:            strField(raw, "mode"),
			Provider:        provider,
			InputPer1M:      perMillionField(raw, "input_cost_per_token"),
			OutputPer1M:     perMillionField(raw, "output_cost_per_token"),
			CacheReadPer1M:  perMillionField(raw, "cache_read_input_token_cost"),
			CacheWritePer1M: perMillionField(raw, "cache_creation_input_token_cost"),
			PerImage:        unitField(raw, "output_cost_per_image", "input_cost_per_image"),
			PerSecond:       unitField(raw, "input_cost_per_second"),
			PerCharacter:    unitField(raw, "input_cost_per_character"),
			MaxInput:        intField(raw, "max_input_tokens"),
			MaxOutput:       intField(raw, "max_output_tokens"),
		}
		cat.providers[provider] = true
		cat.Entries[key] = entry
		norm := NormalizeModelName(key, cat.providers)
		cat.normIndex[norm] = append(cat.normIndex[norm], key)
	}
	if len(cat.Entries) == 0 {
		return nil, fmt.Errorf("参考价库解析后是空的")
	}
	// 归一化桶内的键排序：MatchBest 取「同族最新」依赖稳定顺序
	// （日期后缀相同时取字典序最大者），map 遍历顺序不可依赖。
	for _, keys := range cat.normIndex {
		sort.Strings(keys)
	}
	return cat, nil
}

// strField 取字符串字段，缺失/类型不符返回空串。
func strField(raw map[string]any, key string) string {
	if v, ok := raw[key].(string); ok {
		return v
	}
	return ""
}

// perMillionField 取「每 token 单价」并折成每百万；缺失/非正数返回 0。
func perMillionField(raw map[string]any, key string) float64 {
	return numField(raw, key) * perMillion
}

// unitField 按顺序取第一个为正的字段（每单位口径，不折算）。
func unitField(raw map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if v := numField(raw, k); v > 0 {
			return v
		}
	}
	return 0
}

// numField 取数值字段。上游有个别条目把价格写成字符串（如 "0"），
// 只接受数字或可解析为数字的字符串，其余当缺失。
func numField(raw map[string]any, key string) float64 {
	v, ok := raw[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%g", &f); err == nil {
			return f
		}
	}
	return 0
}

// intField 取整数字段（上下文窗口），缺失或非法返回 0。
func intField(raw map[string]any, key string) int64 {
	if f := numField(raw, key); f > 0 {
		return int64(f)
	}
	return 0
}

// dateSuffixRe 匹配模型名里的日期后缀：claude-3-5-sonnet-20241022 与
// gpt-4o-2024-08-06 两种写法。版本后缀（-002、-latest）不在列 ——
// 它们是模型身份的一部分，剥掉会把不同版本混到同一个归一化桶里。
var dateSuffixRe = regexp.MustCompile(`[-@]20\d{2}-?\d{2}-?\d{2}$`)

// NormalizeModelName 归一化模型名：小写、剥已知的供应商前缀、剥日期后缀。
// 这是匹配与搜索共用的唯一口径 —— 改口径时搜索排序与「一键匹配」会一起变。
func NormalizeModelName(name string, providers map[string]bool) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if i := strings.IndexByte(s, '/'); i > 0 {
		prefix := s[:i]
		if providers[prefix] {
			s = s[i+1:]
		}
	}
	s = dateSuffixRe.ReplaceAllString(s, "")
	return s
}

// normalize 用目录自己的供应商集合归一化（内部便捷封装）。
func (c *Catalog) normalize(name string) string {
	return NormalizeModelName(name, c.providers)
}

// CatalogHit 是搜索/匹配的一条结果：目录键 + 裁剪后的条目。
type CatalogHit struct {
	Key   string       `json:"key"`
	Entry CatalogEntry `json:"entry"`
	// Score 供调试/排序展示用，越小越相关（0 = 键完全一致）。
	Score int `json:"score"`
}

// Search 按关键词搜索目录，返回至多 limit 条（按相关度升序）。
//
// 相关度分级：键精确 > 归一化相等 > 键前缀 > 键包含。
// mode 非空时过滤（chat / image_generation / audio_transcription /
// audio_speech / embedding …，取上游的原始 mode 词表）。
// q 为空时按 mode 过滤后原序返回（目录键本身近似按供应商分族）。
func (c *Catalog) Search(q, mode string, limit int) []CatalogHit {
	if limit <= 0 {
		limit = 20
	}
	query := strings.ToLower(strings.TrimSpace(q))
	type scored struct {
		key   string
		entry CatalogEntry
		score int
	}
	var hits []scored
	for key, entry := range c.Entries {
		if mode != "" && entry.Mode != mode {
			continue
		}
		if !entry.HasPrice() {
			continue
		}
		lower := strings.ToLower(key)
		var score int
		switch {
		case query == "":
			score = 100
		case lower == query:
			score = 0
		case c.normalize(lower) == c.normalize(query):
			score = 1
		case strings.HasPrefix(lower, query):
			score = 2
		case strings.Contains(lower, query):
			score = 3
		default:
			continue
		}
		hits = append(hits, scored{key, entry, score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score < hits[j].score
		}
		return hits[i].key < hits[j].key
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]CatalogHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, CatalogHit{Key: h.key, Entry: h.entry, Score: h.score})
	}
	return out
}

// MatchBest 返回「这个模型名在目录里最可能对应的条目」。
//
// 三级匹配：键精确 → 归一化相等 → 同族日期取最新（gpt-4o 的对外名往往
// 不带日期，目录里却只有带日期的 gpt-4o-2024-08-06 与不带日期的 gpt-4o
// 两份 —— 取日期最新的那份才贴近「现价」）。第二返回值 false = 没有可信
// 的匹配，调用方应当让用户走搜索，而不是硬凑一个。
func (c *Catalog) MatchBest(name string) (CatalogHit, bool) {
	name = strings.TrimSpace(name)
	if name == "" || len(c.Entries) == 0 {
		return CatalogHit{}, false
	}
	if e, ok := c.Entries[name]; ok && e.HasPrice() {
		return CatalogHit{Key: name, Entry: e}, true
	}
	norm := c.normalize(name)
	if keys := c.normIndex[norm]; len(keys) > 0 {
		// 归一化桶内取字典序最大：日期后缀是唯一让字典序与「新旧」同向的
		// 存量特征（2024-08-06 > 2024-05-13 逐字符成立）。
		for i := len(keys) - 1; i >= 0; i-- {
			if e := c.Entries[keys[i]]; e.HasPrice() {
				return CatalogHit{Key: keys[i], Entry: e}, true
			}
		}
		return CatalogHit{}, false
	}
	// 最后一级：把查询名也按「候选键前缀」匹配 —— gpt-4o-2024-08-06 对着
	// 目录里只有 gpt-4o 的情形反着走一遍。命中多个时取字典序最小（最短名）。
	if hits := c.Search(name, "", 2); len(hits) > 0 && hits[0].Score <= 2 {
		return hits[0], true
	}
	return CatalogHit{}, false
}

// CatalogStore 是 Server 持有的目录缓存：懒加载一次，刷新时显式替换。
// 搜索发生在定价页的每次键入（a-input 远程搜索），不能每次都解析 3MB JSON，
// 也不能在启动时无谓付出这份成本 —— 目录是管理台低频功能。
type CatalogStore struct {
	db *gorm.DB

	mu      sync.RWMutex
	cat     *Catalog
	loaded  bool
	lastErr error
	// builtinFlag 记录内存目录的来源（true = 内嵌快照）。它与 Entries 里的
	// Source 字段并存的原因：Source 是持久化数据（写进 settings 的下载地址），
	// builtinFlag 是运行时状态 ——「这份目录是从哪条路径加载的」不该靠
	// 展示字段反推。
	builtinFlag bool
}

// NewCatalogStore 构造目录缓存。db 为 nil 时只用内嵌副本（测试用）。
func NewCatalogStore(db *gorm.DB) *CatalogStore {
	return &CatalogStore{db: db}
}

// Get 返回当前目录；没有 DB 副本或副本解析失败时回落到内嵌快照。
// lastErr 非空表示 DB 副本存在但坏了 —— 管理台应当提醒用户重新更新，
// 而不是让「表面正常的内嵌旧价」无声顶替。
func (s *CatalogStore) Get(ctx context.Context) (*Catalog, error) {
	s.mu.RLock()
	if s.loaded {
		cat, err := s.cat, s.lastErr
		s.mu.RUnlock()
		return cat, err
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return s.cat, s.lastErr
	}
	cat, builtin, err := s.load(ctx)
	if cat != nil {
		s.cat = cat
		s.builtinFlag = builtin
	}
	s.lastErr = err
	s.loaded = true
	return s.cat, s.lastErr
}

// load 从 settings 读 DB 副本；第二返回值表示是否回落到了内嵌快照。
func (s *CatalogStore) load(ctx context.Context) (*Catalog, bool, error) {
	if s.db != nil {
		var row model.Setting
		err := s.db.WithContext(ctx).Where("key = ?", CatalogSettingKey).First(&row).Error
		if err == nil && row.Value != nil {
			raw, mErr := json.Marshal(row.Value)
			if mErr == nil {
				cat, pErr := parseCatalogDocument(raw)
				if pErr == nil {
					return cat, false, nil
				}
				return s.embedded(), true, fmt.Errorf("settings 里的参考价库副本解析失败（已回落内嵌版）: %w", pErr)
			}
			return s.embedded(), true, fmt.Errorf("settings 里的参考价库副本无法序列化（已回落内嵌版）: %w", mErr)
		}
		if err != nil && err != gorm.ErrRecordNotFound {
			return s.embedded(), true, fmt.Errorf("读取参考价库副本失败（已回落内嵌版）: %w", err)
		}
	}
	return s.embedded(), true, nil
}

// embedded 解析编译期内嵌的快照。解析失败是构建期事故（数据文件坏了），
// 上抛 —— 调用方返回 500 比静默返回空目录更诚实。
func (s *CatalogStore) embedded() *Catalog {
	cat, err := ParseLiteLLMRaw(embeddedCatalogRaw)
	if err != nil {
		panic("内嵌参考价库解析失败: " + err.Error())
	}
	if cat.Source == "" {
		cat.Source = "builtin snapshot"
	}
	return cat
}

// parseCatalogDocument 解析 settings 里存的裁剪版目录（与 Catalog 的 json
// 结构一致），并重建归一化索引。
func parseCatalogDocument(raw []byte) (*Catalog, error) {
	var cat Catalog
	if err := json.Unmarshal(raw, &cat); err != nil {
		return nil, err
	}
	if len(cat.Entries) == 0 {
		return nil, fmt.Errorf("目录条目为空")
	}
	rebuildIndex(&cat)
	return &cat, nil
}

// rebuildIndex 从 Entries 重建 normIndex 与 providers（派生数据不进库，
// 每次加载后补建 —— 与 ParseLiteLLMRaw 末尾同一段逻辑的收敛）。
func rebuildIndex(cat *Catalog) {
	cat.normIndex = map[string][]string{}
	cat.providers = map[string]bool{}
	for _, e := range cat.Entries {
		cat.providers[e.Provider] = true
	}
	for key := range cat.Entries {
		norm := cat.normalize(key)
		cat.normIndex[norm] = append(cat.normIndex[norm], key)
	}
	for _, keys := range cat.normIndex {
		sort.Strings(keys)
	}
}

// ReplaceCatalog 解析一份新下载的上游原文件，存进 settings 并热替换内存副本。
// fetchedAt/source 只作展示。落库成功但索引重建失败不可能发生（同一段代码），
// 所以成功返回后内存与 DB 是一致的。
func (s *CatalogStore) ReplaceCatalog(ctx context.Context, raw []byte, fetchedAt, source string) (*Catalog, error) {
	cat, err := ParseLiteLLMRaw(raw)
	if err != nil {
		return nil, err
	}
	cat.FetchedAt = fetchedAt
	cat.Source = source

	if s.db != nil {
		doc, err := json.Marshal(cat)
		if err != nil {
			return nil, err
		}
		var stored map[string]any
		if err := json.Unmarshal(doc, &stored); err != nil {
			return nil, err
		}
		// upsert settings 行。Setting 主键是 key，First-then-Save 在并发下
		// 会主键冲突；这里只有管理台单管理员触发，Create-or-Update 足够。
		var row model.Setting
		err = s.db.WithContext(ctx).Where("key = ?", CatalogSettingKey).First(&row).Error
		if err == gorm.ErrRecordNotFound {
			row = model.Setting{Key: CatalogSettingKey, Value: stored}
			if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		} else {
			row.Value = stored
			row.UpdatedAt = time.Now().UTC()
			if err := s.db.WithContext(ctx).Save(&row).Error; err != nil {
				return nil, err
			}
		}
	}

	s.mu.Lock()
	s.cat = cat
	s.loaded = true
	s.lastErr = nil
	s.builtinFlag = false
	s.mu.Unlock()
	return cat, nil
}

// Status 描述目录的当前来源，供管理台展示新鲜度。
// DB 副本存在但坏掉时 Broken 非空 —— 界面据此提示重新更新。
type CatalogStatus struct {
	// Source 是内嵌/DB 副本的来源说明（DB 副本存的是更新时的下载地址）
	Source string `json:"source"`
	// FetchedAt 是 DB 副本的抓取时间；内嵌快照没有可靠的抓取时间
	// （它随版本发布），留空由界面显示「随版本内置」。
	FetchedAt string `json:"fetched_at"`
	Entries   int    `json:"entries"`
	// Builtin 表示当前生效的是内嵌快照（true）还是 DB 副本（false）。
	Builtin bool   `json:"builtin"`
	Broken  string `json:"broken,omitempty"`
}

// Status 返回当前生效目录的元信息。
func (s *CatalogStore) Status(ctx context.Context) (CatalogStatus, error) {
	cat, err := s.Get(ctx)
	if cat == nil {
		return CatalogStatus{}, err
	}
	st := CatalogStatus{
		Source:    cat.Source,
		FetchedAt: cat.FetchedAt,
		Entries:   len(cat.Entries),
		Builtin:   s.isBuiltin(),
	}
	if err != nil {
		st.Broken = err.Error()
	}
	return st, nil
}

// isBuiltin 报告内存里的目录是否来自内嵌快照。
func (s *CatalogStore) isBuiltin() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.loaded || s.cat == nil {
		return true
	}
	return s.builtinFlag
}
