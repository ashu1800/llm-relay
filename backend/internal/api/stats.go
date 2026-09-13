package api

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"

	"llm-relay/internal/model"
)

// round2 保留两位小数，用于毫秒等展示型数值。
// 比例类字段一律返回原始浮点，格式化交给前端，避免各接口精度不一致。
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// costsByCurrency 是「币种 -> 金额」的累加器。
//
// 为什么需要它：estimated_cost 是带币种的量，SUM 只在同一币种内成立。
// 所以 SQL 按 (维度, cost_currency) 分组，同一个维度会散成若干行，这里负责
// 把它们折叠回一行 —— 折叠时只求和，**不做任何换算**：混币相加会得到一个
// 既不是人民币也不是美元的数，而且账面上看不出任何异常。
//
// 折叠的那个「切换点」正是早期设计里最容易出错的地方：只要有一处忘记按币种
// 分开，看板就会给出一个看起来很正常、实际没有意义的合计。
type costsByCurrency map[string]decimal.Decimal

// add 把一笔金额并进对应币种。
func (c costsByCurrency) add(currency string, v decimal.Decimal) {
	code := strings.ToUpper(strings.TrimSpace(currency))
	if code == "" {
		// 有默认值的列不会给出空串，真出现空值时也不能随手并进某个币种 ——
		// 那正是「把钱挂到错误的币种上」
		code = model.CurrencyUSD
	}
	c[code] = c[code].Add(v)
}

// json 输出给前端的形状：{"CNY":"12.34000000"}。
// 金额统一用字符串，与接口里其余金额一致（JSON 浮点会把 0.15 变成
// 0.14999999999999999，而金额是要拿去和账单对账的）。
func (c costsByCurrency) json() map[string]string {
	out := make(map[string]string, len(c))
	for code, v := range c {
		out[code] = v.StringFixed(8)
	}
	return out
}

func registerStatsRoutes(g *gin.RouterGroup, s *Server) {
	r := g.Group("/stats")
	r.GET("/summary", s.statsSummary)
	r.GET("/timeseries", s.statsTimeseries)
	r.GET("/models", s.statsModels)
	r.GET("/channels", s.statsChannels)
	r.GET("/heatmap", s.statsHeatmap)
}

// resolveRange 把时间范围关键字换算成区间与分桶粒度。
// 边界按容器本地时区计算，日志本身以 UTC 落库，比较时由驱动完成时区换算。
func resolveRange(key string) (time.Time, time.Time, string) {
	now := time.Now()
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "3d":
		return now.AddDate(0, 0, -3), now, "hour"
	case "7d":
		return now.AddDate(0, 0, -7), now, "day"
	case "30d":
		return now.AddDate(0, 0, -30), now, "day"
	default: // today
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), now, "hour"
	}
}

// localTZ 返回可用于 PostgreSQL AT TIME ZONE 的时区名。
func localTZ() string {
	name := time.Now().Location().String()
	if name == "Local" || name == "" {
		return "UTC"
	}
	return name
}

type summaryRow struct {
	Requests            int64   `gorm:"column:requests"`
	Success             int64   `gorm:"column:success"`
	Errors              int64   `gorm:"column:errors"`
	PromptTokens        int64   `gorm:"column:prompt_tokens"`
	CompletionTokens    int64   `gorm:"column:completion_tokens"`
	CachedTokens        int64   `gorm:"column:cached_tokens"`
	CacheCreationTokens int64   `gorm:"column:cache_creation_tokens"`
	ReasoningTokens     int64   `gorm:"column:reasoning_tokens"`
	AvgFirstByte        float64 `gorm:"column:avg_first_byte"`
	AvgTotal            float64 `gorm:"column:avg_total"`
}

func (s *Server) statsSummary(c *gin.Context) {
	start, end, _ := resolveRange(c.Query("range"))
	data, err := s.summarySnapshot(start, end)
	if err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	data["range"] = gin.H{
		"key": c.Query("range"), "start": start.Format(time.RFC3339), "end": end.Format(time.RFC3339),
	}
	c.JSON(http.StatusOK, data)
}

// summarySnapshot 算出某个区间的汇总。抽出来是为了让实时推送复用同一套口径 ——
// 看板上的数字与 WebSocket 推来的数字必须来自同一个查询，
// 否则「刚刷新是 A、两秒后自己变成 B」这种不一致会让人怀疑看板本身。
func (s *Server) summarySnapshot(start, end time.Time) (gin.H, error) {
	return s.summarySnapshotCtx(context.Background(), start, end)
}

// summarySnapshotCtx 是带 ctx 的版本：实时推送循环要用它，
// 这样关停与单次超时都能真的打断查询（没有 ctx 的查询只能干等）。
func (s *Server) summarySnapshotCtx(ctx context.Context, start, end time.Time) (gin.H, error) {
	const q = `SELECT
                COUNT(*)::bigint AS requests,
                COUNT(*) FILTER (WHERE status_code >= 200 AND status_code < 300)::bigint AS success,
                COUNT(*) FILTER (WHERE status_code >= 400)::bigint AS errors,
                COALESCE(SUM(prompt_tokens),0)::bigint AS prompt_tokens,
                COALESCE(SUM(completion_tokens),0)::bigint AS completion_tokens,
                COALESCE(SUM(cached_tokens),0)::bigint AS cached_tokens,
                COALESCE(SUM(cache_creation_tokens),0)::bigint AS cache_creation_tokens,
                COALESCE(SUM(reasoning_tokens),0)::bigint AS reasoning_tokens,
                COALESCE(AVG(first_byte_ms) FILTER (WHERE first_byte_ms > 0),0) AS avg_first_byte,
                COALESCE(AVG(total_ms) FILTER (WHERE total_ms > 0),0) AS avg_total
        FROM request_logs WHERE created_at >= ? AND created_at <= ?`

	var row summaryRow
	if err := s.deps.Store.DB().WithContext(ctx).Raw(q, start, end).Scan(&row).Error; err != nil {
		return nil, err
	}

	// 金额按币种分开查，而不是在主查询里加 COALESCE(SUM(...) FILTER (WHERE ...))：
	// 币种集合由数据决定，写死几种就会在将来多出一种时静默漏掉一笔钱。
	// 单开一条查询也顺带避开了「对平均值再求平均」——AVG 没法按币种折叠。
	var costRows []struct {
		Currency string          `gorm:"column:cost_currency"`
		Cost     decimal.Decimal `gorm:"column:cost"`
	}
	if err := s.deps.Store.DB().WithContext(ctx).Raw(
		`SELECT cost_currency, COALESCE(SUM(estimated_cost),0) AS cost
                 FROM request_logs WHERE created_at >= ? AND created_at <= ?
                 GROUP BY cost_currency`, start, end).Scan(&costRows).Error; err != nil {
		return nil, err
	}
	costs := costsByCurrency{}
	for _, r := range costRows {
		costs.add(r.Currency, r.Cost)
	}

	successRate := 0.0
	if row.Requests > 0 {
		successRate = float64(row.Success) / float64(row.Requests)
	}
	// 命中率分母为全部输入（未命中 + 命中 + 缓存写入），与日志页口径保持一致
	cacheDenom := row.PromptTokens + row.CachedTokens + row.CacheCreationTokens
	hitRate := 0.0
	if cacheDenom > 0 {
		hitRate = float64(row.CachedTokens) / float64(cacheDenom)
	}

	return gin.H{
		"requests":              row.Requests,
		"success":               row.Success,
		"errors":                row.Errors,
		"success_rate":          successRate,
		"prompt_tokens":         row.PromptTokens,
		"completion_tokens":     row.CompletionTokens,
		"cached_tokens":         row.CachedTokens,
		"cache_creation_tokens": row.CacheCreationTokens,
		"reasoning_tokens":      row.ReasoningTokens,
		"total_tokens":          row.PromptTokens + row.CompletionTokens + row.CachedTokens,
		"cache_hit_rate":        hitRate,
		"costs":                 costs.json(),
		"avg_first_byte_ms":     row.AvgFirstByte,
		"avg_total_ms":          row.AvgTotal,
	}, nil
}

type seriesRow struct {
	Bucket           time.Time       `gorm:"column:bucket"`
	Requests         int64           `gorm:"column:requests"`
	Errors           int64           `gorm:"column:errors"`
	PromptTokens     int64           `gorm:"column:prompt_tokens"`
	CompletionTokens int64           `gorm:"column:completion_tokens"`
	CachedTokens     int64           `gorm:"column:cached_tokens"`
	Cost             decimal.Decimal `gorm:"column:cost"`
	Currency         string          `gorm:"column:cost_currency"`
}

func (s *Server) statsTimeseries(c *gin.Context) {
	start, end, bucket := resolveRange(c.Query("range"))
	// 分桶粒度只允许白名单值，避免拼接进 SQL 造成注入
	if bucket != "hour" && bucket != "day" {
		bucket = "hour"
	}

	tz := localTZ()
	sql := `SELECT date_trunc(?, created_at AT TIME ZONE ?) AT TIME ZONE ? AS bucket,
                COUNT(*)::bigint AS requests,
                COUNT(*) FILTER (WHERE status_code >= 400)::bigint AS errors,
                COALESCE(SUM(prompt_tokens),0)::bigint AS prompt_tokens,
                COALESCE(SUM(completion_tokens),0)::bigint AS completion_tokens,
                COALESCE(SUM(cached_tokens),0)::bigint AS cached_tokens,
                cost_currency,
                COALESCE(SUM(estimated_cost),0) AS cost
        FROM request_logs WHERE created_at >= ? AND created_at <= ?
        GROUP BY bucket, cost_currency ORDER BY bucket`

	var rows []seriesRow
	if err := s.deps.Store.DB().Raw(sql, bucket, tz, tz, start, end).Scan(&rows).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}

	// 同一个时间桶会按币种散成多行，这里折叠回一条：请求数、词元是各币种
	// 子集的和，直接相加就是总数；金额各自挂到自己的币种下面。
	type bucketAgg struct {
		bucket           time.Time
		requests         int64
		errors           int64
		promptTokens     int64
		completionTokens int64
		cachedTokens     int64
		costs            costsByCurrency
	}
	aggs := make([]*bucketAgg, 0, len(rows))
	byBucket := map[string]*bucketAgg{}
	for _, r := range rows {
		// 键用格式化后的时间串而不是 time.Time：后者内部带 Location 指针，
		// 直接当 map 键要依赖「每次扫描返回的 Location 是同一个」这个前提
		key := r.Bucket.UTC().Format(time.RFC3339Nano)
		agg := byBucket[key]
		if agg == nil {
			agg = &bucketAgg{bucket: r.Bucket, costs: costsByCurrency{}}
			byBucket[key] = agg
			aggs = append(aggs, agg)
		}
		agg.requests += r.Requests
		agg.errors += r.Errors
		agg.promptTokens += r.PromptTokens
		agg.completionTokens += r.CompletionTokens
		agg.cachedTokens += r.CachedTokens
		agg.costs.add(r.Currency, r.Cost)
	}

	items := make([]gin.H, 0, len(aggs))
	for _, a := range aggs {
		items = append(items, gin.H{
			"ts":                a.bucket.Local().Format(time.RFC3339),
			"requests":          a.requests,
			"errors":            a.errors,
			"prompt_tokens":     a.promptTokens,
			"completion_tokens": a.completionTokens,
			"cached_tokens":     a.cachedTokens,
			"costs":             a.costs.json(),
		})
	}
	c.JSON(http.StatusOK, gin.H{"bucket": bucket, "items": items})
}

type groupRow struct {
	Name             string          `gorm:"column:name"`
	Currency         string          `gorm:"column:cost_currency"`
	Requests         int64           `gorm:"column:requests"`
	PromptTokens     int64           `gorm:"column:prompt_tokens"`
	CompletionTokens int64           `gorm:"column:completion_tokens"`
	CachedTokens     int64           `gorm:"column:cached_tokens"`
	Tokens           int64           `gorm:"column:tokens"`
	Cost             decimal.Decimal `gorm:"column:cost"`
	Errors           int64           `gorm:"column:errors"`
	// 每一行都是「某个维度 x 某个币种」的子集，所以耗时只能取总和与条数，
	// 折叠时再相除：AVG 没法把两个币种的平均值合成一个平均值
	MsSum   int64 `gorm:"column:ms_sum"`
	MsCount int64 `gorm:"column:ms_n"`
}

func (s *Server) statsModels(c *gin.Context) {
	s.groupStats(c, "model_requested")
}

func (s *Server) statsChannels(c *gin.Context) {
	s.groupStats(c, "channel_name")
}

// groupStats 按维度聚合调用量与费用，维度只接受内部白名单字段名。
func (s *Server) groupStats(c *gin.Context, column string) {
	if column != "model_requested" && column != "channel_name" {
		writeUpstreamError(c, http.StatusBadRequest, "不支持的聚合维度", "invalid_request_error")
		return
	}
	start, end, _ := resolveRange(c.Query("range"))
	limit, _ := strconv.Atoi(orDefault(c.Query("limit"), "10"))
	if limit < 1 || limit > 100 {
		limit = 10
	}

	sql := `SELECT ` + column + ` AS name,
                COUNT(*)::bigint AS requests,
                COUNT(*) FILTER (WHERE status_code >= 400)::bigint AS errors,
                COALESCE(SUM(prompt_tokens),0)::bigint AS prompt_tokens,
                COALESCE(SUM(completion_tokens),0)::bigint AS completion_tokens,
                COALESCE(SUM(cached_tokens),0)::bigint AS cached_tokens,
                COALESCE(SUM(total_tokens),0)::bigint AS tokens,
                cost_currency,
                COALESCE(SUM(estimated_cost),0) AS cost,
                COALESCE(SUM(total_ms) FILTER (WHERE total_ms > 0),0)::bigint AS ms_sum,
                COUNT(*) FILTER (WHERE total_ms > 0)::bigint AS ms_n
        FROM request_logs WHERE created_at >= ? AND created_at <= ? AND ` + column + ` <> ''
        GROUP BY ` + column + `, cost_currency`

	var rows []groupRow
	if err := s.deps.Store.DB().Raw(sql, start, end).Scan(&rows).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}

	// 折叠：同一个维度按币种散成多行，合并回一行，金额挂到各自币种下面。
	// （排序与截断也挪到合并之后 —— 见下面的说明）
	type groupAgg struct {
		name                                                                   string
		requests, errors, promptTokens, completionTokens, cachedTokens, tokens int64
		costs                                                                  costsByCurrency
		msSum                                                                  int64
		msCount                                                                int64
	}
	aggs := make([]*groupAgg, 0, len(rows))
	byName := map[string]*groupAgg{}
	for _, r := range rows {
		agg := byName[r.Name]
		if agg == nil {
			agg = &groupAgg{name: r.Name, costs: costsByCurrency{}}
			byName[r.Name] = agg
			aggs = append(aggs, agg)
		}
		agg.requests += r.Requests
		agg.errors += r.Errors
		agg.promptTokens += r.PromptTokens
		agg.completionTokens += r.CompletionTokens
		agg.cachedTokens += r.CachedTokens
		agg.tokens += r.Tokens
		agg.msSum += r.MsSum
		agg.msCount += r.MsCount
		agg.costs.add(r.Currency, r.Cost)
	}

	// 排序与截断必须在折叠之后：按币种分组会把同一个模型散成多行，
	// 若在 SQL 里按单行请求数排序取前 N，「人民币 10 次 + 美元 8 次」这种
	// 总量最大的模型反而会被挤出去（而榜单上看不出少了谁）。
	sort.SliceStable(aggs, func(i, j int) bool {
		if aggs[i].requests != aggs[j].requests {
			return aggs[i].requests > aggs[j].requests
		}
		// 请求数相同时按名字定序：否则榜单顺序会在两次刷新之间自己跳动
		return aggs[i].name < aggs[j].name
	})
	if len(aggs) > limit {
		aggs = aggs[:limit]
	}

	items := make([]gin.H, 0, len(aggs))
	for _, a := range aggs {
		var avgMs float64
		if a.msCount > 0 {
			avgMs = float64(a.msSum) / float64(a.msCount)
		}
		items = append(items, gin.H{
			"name": a.name, "requests": a.requests, "errors": a.errors,
			"prompt_tokens": a.promptTokens, "completion_tokens": a.completionTokens,
			"cached_tokens": a.cachedTokens, "tokens": a.tokens,
			"costs": a.costs.json(), "avg_ms": avgMs,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type heatRow struct {
	Day      string          `gorm:"column:day"`
	Hour     int             `gorm:"column:hour"`
	Currency string          `gorm:"column:cost_currency"`
	Requests int64           `gorm:"column:requests"`
	Cost     decimal.Decimal `gorm:"column:cost"`
	Tokens   int64           `gorm:"column:tokens"`
}

// statsHeatmap 返回近 N 天按「日期 x 小时」分布的热力数据。
// 日期与小时按容器本地时区切分，否则跨时区看会整体错位。
func (s *Server) statsHeatmap(c *gin.Context) {
	days, _ := strconv.Atoi(orDefault(c.Query("days"), "30"))
	if days < 1 || days > 180 {
		days = 30
	}
	since := time.Now().AddDate(0, 0, -days)

	// 悬浮提示要显示消费与 Token（与参考站的提示一致），所以一并聚合。
	// COALESCE 是必需的：某个小时里只要有一行 estimated_cost 为 NULL，
	// SUM 整体就会是 NULL，扫进 decimal 会直接报错。
	const q = `SELECT to_char(created_at AT TIME ZONE ?, 'YYYY-MM-DD') AS day,
                EXTRACT(HOUR FROM created_at AT TIME ZONE ?)::int AS hour,
                cost_currency,
                COUNT(*)::bigint AS requests,
                COALESCE(SUM(estimated_cost), 0) AS cost,
                COALESCE(SUM(total_tokens), 0)::bigint AS tokens
        FROM request_logs WHERE created_at >= ?
        GROUP BY day, hour, cost_currency ORDER BY day, hour`

	tz := localTZ()
	var rows []heatRow
	if err := s.deps.Store.DB().Raw(q, tz, tz, since).Scan(&rows).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}

	// 与看板其余部分同一个口径：一个（日期,小时）格子下可能有两种币种的账，
	// 折叠成一条，金额按币种分开挂着（悬浮提示里会逐个币种列出来）
	type heatAgg struct {
		day      string
		hour     int
		requests int64
		tokens   int64
		costs    costsByCurrency
	}
	aggs := make([]*heatAgg, 0, len(rows))
	byCell := map[string]*heatAgg{}
	for _, r := range rows {
		// 键里同时带上日期与小时：只用小时会把 30 天里同一个钟点并成一条
		key := r.Day + "#" + strconv.Itoa(r.Hour)
		agg := byCell[key]
		if agg == nil {
			agg = &heatAgg{day: r.Day, hour: r.Hour, costs: costsByCurrency{}}
			byCell[key] = agg
			aggs = append(aggs, agg)
		}
		agg.requests += r.Requests
		agg.tokens += r.Tokens
		agg.costs.add(r.Currency, r.Cost)
	}

	items := make([]gin.H, 0, len(aggs))
	for _, a := range aggs {
		items = append(items, gin.H{
			"day": a.day, "hour": a.hour, "requests": a.requests,
			"costs":  a.costs.json(),
			"tokens": a.tokens,
		})
	}
	c.JSON(http.StatusOK, gin.H{"days": days, "timezone": tz, "items": items})
}
