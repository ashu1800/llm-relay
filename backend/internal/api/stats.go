package api

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"

	"llm-relay/internal/model"
	"llm-relay/internal/relay"
)

// totalTokensOf 是 interface{} 形态的 relay.UsageTotal，供聚合行使用。
//
// 聚合结果是 int64。公式本体在 relay 包只有一处定义，这里用 int64 重算
// 而不是转 int 再转回来 —— 巨量 token（>2^31）在 32 位平台上会溢出，
// 虽然当前只发 64 位构建，口径先站稳不用留坑。
func totalTokensOf(prompt, completion, cached, cacheCreation int64) int64 {
	return prompt + completion + cached + cacheCreation
}

// cacheHitRateOf 同上，是 relay.CacheHitRateOf 的 int64 版本。
func cacheHitRateOf(prompt, cached, cacheCreation int64) float64 {
	return relay.CacheHitRateOf(int(prompt), int(cached), int(cacheCreation))
}

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
	r.GET("/daily-report", s.statsDailyReport)
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

// statsZoneName 返回可用于 PostgreSQL AT TIME ZONE 的时区名。
//
// 为什么不能直接拿 time.Now().Location().String()：Go 在 **TZ 未设置** 时
// 会加载 /etc/localtime，但把名字硬写成 "Local"（见标准库 zoneinfo_unix.go
// 的 initLocal）。原来的实现在这种情况下静默返回 "UTC"，于是分桶整体偏 8 小时
// （北京时间 00:00-08:00 的请求被算进前一天）。裸机部署脚本没有写 TZ，
// 必然命中这条路径；容器编排里设了 TZ，所以只有裸机用户会看到这个偏差。
//
// 解析顺序：TZ 环境变量 -> Go 已解析出的具名时区 -> /etc/timezone
// （Debian/Ubuntu）-> /etc/localtime 符号链接。全都拿不到时返回空串，
// 由 statsZoneExpr 退化成固定偏移，并且只告警一次 —— 宁可口径明确，
// 也不要悄悄换一个时区。
func statsZoneName() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc.String()
		}
	}
	if name := time.Now().Location().String(); name != "Local" && name != "" {
		return name
	}
	// Debian/Ubuntu 把时区名单独放在 /etc/timezone
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if name := strings.TrimSpace(string(b)); name != "" {
			if loc, err := time.LoadLocation(name); err == nil {
				return loc.String()
			}
		}
	}
	// /etc/localtime 通常是指向 /usr/share/zoneinfo/<Area>/<City> 的符号链接
	if dst, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(dst, "zoneinfo/"); i >= 0 {
			if loc, err := time.LoadLocation(dst[i+len("zoneinfo/"):]); err == nil {
				return loc.String()
			}
		}
	}
	return ""
}

var (
	statsZoneOnce sync.Once
	// statsZoneExpr 是 SQL 里 AT TIME ZONE 后面那段，可能是 "?" 也可能是 "?::interval"
	statsZoneExpr string
	// statsZoneArg 是对应的参数值，SQL 里会出现两次
	statsZoneArg any
)

// statsZone 解析一次时区并缓存。返回值直接拼进 SQL 的 AT TIME ZONE 之后，
// 参数按 statsZoneArg 传入（每条 SQL 用两次）。
func statsZone() (string, any) {
	statsZoneOnce.Do(func() {
		if name := statsZoneName(); name != "" {
			statsZoneExpr, statsZoneArg = "?", name
			return
		}
		// 退化路径：按当前 UTC 偏移做固定偏移。这对没有夏令时的时区
		// （如 Asia/Shanghai）完全等价，对有夏令时的时区在切换日附近会有偏差，
		// 所以必须告警而不是静默采用。
		_, offset := time.Now().Zone()
		slog.Warn("无法确定服务器时区名，统计分桶将按固定 UTC 偏移计算；" +
			"如需精确分桶请设置 TZ 环境变量（例如 TZ=Asia/Shanghai）")
		// PostgreSQL 的 AT TIME ZONE 接受 interval；用秒数避免符号歧义
		// （POSIX 风格 "UTC+8" 的含义与 ISO 8601 相反，不采用）
		statsZoneExpr, statsZoneArg = "?::interval", fmt.Sprintf("%d seconds", offset)
	})
	return statsZoneExpr, statsZoneArg
}

// localTZ 保留给需要「时区名」而非 SQL 片段的调用方。
func localTZ() string {
	if name := statsZoneName(); name != "" {
		return name
	}
	return "UTC"
}

// statsFilter 是看板的筛选条件：按分组 / 按渠道 / 按模型，0 或空串表示不筛选。
//
// 三个条件都落在 request_logs 自带的列上（channel_id / group_id 建表时就有索引），
// 所以筛选只是加一个 WHERE：不需要改表，也**不去 join channels** —— join 会把
// 「渠道后来换了分组」算到历史账上，而日志里的归属是当时那一刻的快照
// （与 CostCurrency、PricingSnapshot 同一个道理）。
//
// Model 是后来加的：请求日志并入看板之后，那一页的「模型」下拉要同时作用于
// 卡片与列表，否则同一个页面上会出现两套口径（选了模型，卡片纹丝不动）。
// model_requested 上没有索引，但窗口最长 30 天、且通常还带着分组/渠道条件，
// 先不建索引 —— 真慢了再补，而不是先加一个可能永远用不上的索引。
type statsFilter struct {
	GroupID   uint
	ChannelID uint
	Model     string
}

// parseStatsFilter 解析 group_id / channel_id / model。
//
// 不传 = 不筛选；传了就必须是正整数（或非空模型名），判据与请求日志的筛选一致
// （/logs 用的是同一套）。非法值**不能**静默当成「不筛选」：那会得到一份
// 看起来很正常、其实是全站的数字，而界面上明明选着某个分组 ——
// 「静默变全量」比直接报错难查得多。
func parseStatsFilter(c *gin.Context) (statsFilter, error) {
	var f statsFilter
	for _, p := range []struct {
		name string
		dst  *uint
	}{{"group_id", &f.GroupID}, {"channel_id", &f.ChannelID}} {
		raw := strings.TrimSpace(c.Query(p.name))
		if raw == "" {
			continue
		}
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || v == 0 {
			return f, fmt.Errorf("参数 %s 非法: %s", p.name, raw)
		}
		*p.dst = uint(v)
	}
	// 模型名不校验格式：它是上游模型标识，各家的写法不受本项目约束
	// （/logs 的 model 筛选也是这么处理的）。空串＝不筛选。
	f.Model = strings.TrimSpace(c.Query("model"))
	return f, nil
}

// statsFilterOf 解析筛选；非法时已经把 400 写回去了，调用方看 ok 决定是否继续。
func statsFilterOf(c *gin.Context) (statsFilter, bool) {
	f, err := parseStatsFilter(c)
	if err != nil {
		writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return f, false
	}
	return f, true
}

// where 返回可以直接拼在时间条件之后的片段与参数。
//
// 按需拼接，而不是写成 (? = 0 OR group_id = ?)：后者会让规划器放弃
// group_id / channel_id 上的索引，退化成全表扫。
func (f statsFilter) where() (string, []any) {
	var sb strings.Builder
	args := make([]any, 0, 3)
	if f.GroupID > 0 {
		sb.WriteString(" AND group_id = ?")
		args = append(args, f.GroupID)
	}
	if f.ChannelID > 0 {
		sb.WriteString(" AND channel_id = ?")
		args = append(args, f.ChannelID)
	}
	if f.Model != "" {
		sb.WriteString(" AND model_requested = ?")
		args = append(args, f.Model)
	}
	return sb.String(), args
}

// json 把实际生效的筛选回显出去。
// 接口的约定是「不传即全量」，把生效值写进响应，「参数没生效」才不会被
// 读成「界面上那个筛选框没用」。
func (f statsFilter) json() gin.H {
	return gin.H{"group_id": f.GroupID, "channel_id": f.ChannelID, "model": f.Model}
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

// 概览的两条查询。写成常量是因为筛选条件要拼在时间条件之后
// （见 statsFilter.where）：两条查询都得带上，少一条就会出现
// 「请求数是筛选后的、金额却是全站的」。
//
// 金额那条拆成前后两段：它的 WHERE 后面还有 GROUP BY，条件必须插在
// 两者**之间** —— 拼到整条语句末尾会变成
// `GROUP BY cost_currency AND group_id = ?`，Postgres 报的是
// 「AND 的参数必须是 boolean」（实测踩过）。
const (
	summarySQL = `SELECT
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

	costSQLHead = `SELECT cost_currency, COALESCE(SUM(estimated_cost),0) AS cost
                 FROM request_logs WHERE created_at >= ? AND created_at <= ?`
	costSQLTail = ` GROUP BY cost_currency`
)

func (s *Server) statsSummary(c *gin.Context) {
	f, ok := statsFilterOf(c)
	if !ok {
		return
	}
	start, end, _ := resolveRange(c.Query("range"))
	data, err := s.summarySnapshotCtx(c.Request.Context(), start, end, f)
	if err != nil {
		writeInternalError(c, err)
		return
	}
	data["range"] = gin.H{
		"key": c.Query("range"), "start": start.Format(time.RFC3339), "end": end.Format(time.RFC3339),
	}
	data["filter"] = f.json()
	c.JSON(http.StatusOK, data)
}

// summarySnapshot 算出某个区间的汇总。抽出来是为了让实时推送复用同一套口径 ——
// 看板上的数字与 WebSocket 推来的数字必须来自同一个查询，
// 否则「刚刷新是 A、两秒后自己变成 B」这种不一致会让人怀疑看板本身。
//
// 实时推送传零值筛选（今天 + 全站），与它推给前端的视角一致。
func (s *Server) summarySnapshot(start, end time.Time, f statsFilter) (gin.H, error) {
	return s.summarySnapshotCtx(context.Background(), start, end, f)
}

// summarySnapshotCtx 是带 ctx 的版本：实时推送循环要用它，
// 这样关停与单次超时都能真的打断查询（没有 ctx 的查询只能干等）。
func (s *Server) summarySnapshotCtx(ctx context.Context, start, end time.Time, f statsFilter) (gin.H, error) {
	cond, filterArgs := f.where()

	var row summaryRow
	if err := s.deps.Store.DB().WithContext(ctx).Raw(
		summarySQL+cond, append([]any{start, end}, filterArgs...)...).Scan(&row).Error; err != nil {
		return nil, err
	}

	// 金额按币种分开查，而不是在主查询里加 COALESCE(SUM(...) FILTER (WHERE ...))：
	// 币种集合由数据决定，写死几种就会在将来多出一种时静默漏掉一笔钱。
	// 单开一条查询也顺带避开了「对平均值再求平均」——AVG 没法按币种折叠。
	//
	// 筛选条件同样要带上：只给主查询加、忘了这条，会出现
	// 「请求数是筛选后的、金额却是全站的」——两个数字都像是真的。
	var costRows []struct {
		Currency string          `gorm:"column:cost_currency"`
		Cost     decimal.Decimal `gorm:"column:cost"`
	}
	if err := s.deps.Store.DB().WithContext(ctx).Raw(
		costSQLHead+cond+costSQLTail, append([]any{start, end}, filterArgs...)...).Scan(&costRows).Error; err != nil {
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
	// 命中率分母为全部输入（未命中 + 命中 + 缓存写入），与日志页口径保持一致。
	// 公式与 relay 包同源，避免两处各写一遍后漂移。
	hitRate := cacheHitRateOf(row.PromptTokens, row.CachedTokens, row.CacheCreationTokens)

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
		"total_tokens":          totalTokensOf(row.PromptTokens, row.CompletionTokens, row.CachedTokens, row.CacheCreationTokens),
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
	f, ok := statsFilterOf(c)
	if !ok {
		return
	}
	start, end, bucket := resolveRange(c.Query("range"))
	// 分桶粒度只允许白名单值，避免拼接进 SQL 造成注入
	if bucket != "hour" && bucket != "day" {
		bucket = "hour"
	}

	cond, filterArgs := f.where()
	zoneExpr, zoneArg := statsZone()
	sql := `SELECT date_trunc(?, created_at AT TIME ZONE ` + zoneExpr + `) AT TIME ZONE ` + zoneExpr + ` AS bucket,
                COUNT(*)::bigint AS requests,
                COUNT(*) FILTER (WHERE status_code >= 400)::bigint AS errors,
                COALESCE(SUM(prompt_tokens),0)::bigint AS prompt_tokens,
                COALESCE(SUM(completion_tokens),0)::bigint AS completion_tokens,
                COALESCE(SUM(cached_tokens),0)::bigint AS cached_tokens,
                cost_currency,
                COALESCE(SUM(estimated_cost),0) AS cost
        FROM request_logs WHERE created_at >= ? AND created_at <= ?` + cond + `
        GROUP BY bucket, cost_currency ORDER BY bucket`

	var rows []seriesRow
	if err := s.deps.Store.DB().WithContext(c.Request.Context()).Raw(sql,
		append([]any{bucket, zoneArg, zoneArg, start, end}, filterArgs...)...).Scan(&rows).Error; err != nil {
		writeInternalError(c, err)
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
	f, ok := statsFilterOf(c)
	if !ok {
		return
	}
	start, end, _ := resolveRange(c.Query("range"))
	limit, _ := strconv.Atoi(orDefault(c.Query("limit"), "10"))
	if limit < 1 || limit > 100 {
		limit = 10
	}

	cond, filterArgs := f.where()
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
        FROM request_logs WHERE created_at >= ? AND created_at <= ?` + cond + ` AND ` + column + ` <> ''
        GROUP BY ` + column + `, cost_currency`

	var rows []groupRow
	if err := s.deps.Store.DB().WithContext(c.Request.Context()).Raw(sql, append([]any{start, end}, filterArgs...)...).Scan(&rows).Error; err != nil {
		writeInternalError(c, err)
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
	f, ok := statsFilterOf(c)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(orDefault(c.Query("days"), "30"))
	if days < 1 || days > 180 {
		days = 30
	}
	since := time.Now().AddDate(0, 0, -days)

	// 悬浮提示要显示消费与 Token（与参考站的提示一致），所以一并聚合。
	// COALESCE 是必需的：某个小时里只要有一行 estimated_cost 为 NULL，
	// SUM 整体就会是 NULL，扫进 decimal 会直接报错。
	//
	// 筛选同样生效：热力图的时间轴是固定的近 N 天（不跟时间范围走），
	// 但「看哪条渠道」这个条件没有理由不生效 —— 否则同一屏上
	// 卡片按渠道筛过、热力图还是全站，两个数字对不上。
	cond, filterArgs := f.where()
	zoneExpr, zoneArg := statsZone()
	q := `SELECT to_char(created_at AT TIME ZONE ` + zoneExpr + `, 'YYYY-MM-DD') AS day,
                EXTRACT(HOUR FROM created_at AT TIME ZONE ` + zoneExpr + `)::int AS hour,
                cost_currency,
                COUNT(*)::bigint AS requests,
                COALESCE(SUM(estimated_cost), 0) AS cost,
                COALESCE(SUM(total_tokens), 0)::bigint AS tokens
        FROM request_logs WHERE created_at >= ? AND created_at <= ?` + cond + `
        GROUP BY day, hour, cost_currency ORDER BY day, hour`

	// 上界与其它聚合保持一致：少了它，热力图会把「未来时间戳」的行也算进来
	// （导入的历史数据或时钟偏斜都可能是这种行），而卡片上又看不到它们。
	var rows []heatRow
	if err := s.deps.Store.DB().WithContext(c.Request.Context()).Raw(q,
		append([]any{zoneArg, zoneArg, since, time.Now()}, filterArgs...)...).Scan(&rows).Error; err != nil {
		writeInternalError(c, err)
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
	c.JSON(http.StatusOK, gin.H{"days": days, "timezone": localTZ(), "items": items})
}

// statsDailyReport 昨日战报：看板每天第一次打开时弹的那张小卡片的数据源。
//
// 复用 summarySnapshot 拿总量（与看板卡片完全同一口径，数字必须对得上），
// 另聚两样有「故事感」的：最忙的模型（按对外请求名）、最贵的一单
// （模型 + 渠道 + 金额）。date 参数可回看任意一天（YYYY-MM-DD），
// 默认昨天 —— 顺带让前端不必为「补看」做任何特殊处理。
func (s *Server) statsDailyReport(c *gin.Context) {
	loc := time.Now().Location()
	day := strings.TrimSpace(c.Query("date"))
	var start time.Time
	if day == "" {
		y, m, d := time.Now().AddDate(0, 0, -1).Date()
		start = time.Date(y, m, d, 0, 0, 0, 0, loc)
	} else {
		parsed, err := time.ParseInLocation("2006-01-02", day, loc)
		if err != nil {
			writeUpstreamError(c, http.StatusBadRequest, "date 要写成 YYYY-MM-DD", "invalid_request_error")
			return
		}
		start = parsed
	}
	end := start.AddDate(0, 0, 1)
	if end.After(time.Now()) {
		// 当天还没过完：上界收到此刻，战报只统计已经发生的
		end = time.Now()
	}

	summary, err := s.summarySnapshot(start, end, statsFilter{})
	if err != nil {
		writeInternalError(c, err)
		return
	}

	// 最忙模型：按对外请求名。空模型名（极端的脏数据）参与排名无妨 ——
	// 它确实被请求过，只是名字是空的。
	//
	// 用 Limit(1).Scan 而不是 First：First 会自动追加主键排序
	// （ORDER BY ..., request_logs.id），而 id 不在 GROUP BY 里，
	// Postgres 直接拒绝聚合查询里出现裸的主键列 —— 这正是线上 500 的原因
	var topModel struct {
		Model    string
		Requests int64
	}
	if err := s.deps.Store.DB().Model(&model.RequestLog{}).
		Select("model_requested AS model, COUNT(*)::bigint AS requests").
		Where("created_at >= ? AND created_at <= ?", start, end).
		Group("model_requested").
		Order("requests DESC").
		Limit(1).
		Scan(&topModel).Error; err != nil {
		writeInternalError(c, err)
		return
	}

	// 最贵一单：按币种各取一条。不同币种的金额不能直接比大小（100 CNY 会
	// 压过 99 USD，README 的多币种铁律），DISTINCT ON 让每种币各自选出
	// 自己的最贵，前端并列展示、不合成一个"全场最贵"
	var priciest []struct {
		Model       string
		ChannelName string
		Cost        decimal.Decimal
		Currency    string
	}
	if err := s.deps.Store.DB().Model(&model.RequestLog{}).
		Select("DISTINCT ON (cost_currency) model_requested AS model, channel_name AS channel_name, estimated_cost AS cost, cost_currency AS currency").
		Where("created_at >= ? AND created_at <= ?", start, end).
		Order("cost_currency, estimated_cost DESC").
		Scan(&priciest).Error; err != nil {
		writeInternalError(c, err)
		return
	}
	priciestOut := make([]gin.H, 0, len(priciest))
	for _, p := range priciest {
		if p.Currency == "" {
			continue
		}
		priciestOut = append(priciestOut, gin.H{
			"model": p.Model, "channel": p.ChannelName,
			"cost": p.Cost.StringFixed(8), "currency": p.Currency,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"day":       start.Format("2006-01-02"),
		"summary":   summary,
		"top_model": gin.H{"model": topModel.Model, "requests": topModel.Requests},
		// 每币种一条（单币种站点只有一条），前端并列展示
		"priciest": priciestOut,
		// 全表累计请求数：给前端的里程碑彩蛋当标尺（破 1 千/1 万/10 万）。
		// 战报一天最多弹一次，这条 COUNT 也就一天跑一次 —— 单独开一个
		// 每次进页面都要调的端点反而不划算
		"lifetime_requests": func() int64 {
			var n int64
			if err := s.deps.Store.DB().Model(&model.RequestLog{}).Count(&n).Error; err != nil {
				return 0 // 数不出来就没有里程碑，不该拖垮整张战报
			}
			return n
		}(),
	})
}
