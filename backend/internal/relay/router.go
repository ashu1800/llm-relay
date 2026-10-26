package relay

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/secure"
)

// Candidate 是一个可用于本次请求的渠道。
type Candidate struct {
	Channel     model.Channel
	Binding     model.ChannelModel
	APIKeyPlain string
	// Available 表示当前时刻是否在渠道的可用时段内。
	Available bool
	// Saturated 表示该渠道的在途请求已达自身上限。它只是被排到后面，
	// 不出局——全部渠道都饱和时仍然要能发出去。
	Saturated bool
	// Fallback 表示这条候选是靠渠道的「默认模型映射」接下的：
	// 客户端请求的模型名没有精确命中该渠道的白名单，于是改用渠道配置的
	// 默认模型。这类候选永远排在精确命中之后（见 sortCandidates）。
	//
	// 注意 Binding 已被改写成默认模型那一行：PublicName 是计费键
	// （计价引擎按白名单行的对外名查价），UpstreamName 是真正发给上游的名字。
	Fallback bool
	// keyOrder 是这条候选在密钥路由列表里的下标（越小越优先）。
	// 不在列表里的候选取一个排它之后的大值（见 sortOrderOf）。
	keyOrder int
}

// EgressProxyID 返回这次请求该走哪个代理：模型条目上的设置优先于渠道级，
// 都是 0 表示直连。
func (c Candidate) EgressProxyID() uint {
	if c.Binding.ProxyID != 0 {
		return c.Binding.ProxyID
	}
	return c.Channel.ProxyID
}

// Router 负责按密钥的模型渠道路由挑选候选渠道，并给出故障转移顺序。
type Router struct {
	db     *gorm.DB
	cipher *secure.Cipher
	// logger 用于记录「渠道密钥解不开被剔除」这类需要人看见的异常
	logger *slog.Logger

	// state 为运行期渠道状态，可为空（未接入时限流能力自动关闭）
	state *ChannelState
}

// SetChannelState 注入渠道运行期状态，用于冷却过滤与并发饱和判定。
func (r *Router) SetChannelState(st *ChannelState) { r.state = st }

// SetLogger 覆盖默认日志器，传 nil 不生效。
func (r *Router) SetLogger(l *slog.Logger) {
	if l != nil {
		r.logger = l
	}
}

// NewRouter 构造路由器。logger 默认 slog.Default()，main 里可换成全局那一份。
func NewRouter(db *gorm.DB, cipher *secure.Cipher) *Router {
	return &Router{db: db, cipher: cipher, logger: slog.Default()}
}

// CandidateQuery 是一次候选渠道查询的条件。
type CandidateQuery struct {
	PublicModel string
	// AllowedChannels 是密钥为该模型配置的候选渠道（有序，越小越优先）。
	// 空表示不按密钥限制（探测、部分测试路径）——候选取全部能跑这个模型的渠道。
	// 非空时只在列表内的渠道中选，且排序以列表顺序为第一优先（见 sortCandidates）。
	AllowedChannels []uint
	// Exclude 里的渠道 ID 会被跳过，用于重试时避免再次命中同一渠道
	Exclude []uint
}

// candidateRow 是一次候选查询取回的一行。
type candidateRow struct {
	model.Channel
	PublicName   string
	UpstreamName string
	BindingID    uint
	// BindingProxyID 是该模型条目自己的代理（0 = 跟随渠道）
	BindingProxyID uint
	// DefaultModel 是渠道配置的默认模型名（用 COALESCE 取，没配则为空串）。
	// 两轮查询都会带上它，但它**不能**用来判断这一行从哪一轮来 ——
	// 见 buildCandidates 的 fromFallbackQuery。
	DefaultModel string
	// fromFallbackQuery 标记这一行由兜底查询取回。
	//
	// 必须显式带上，不能靠「public_name == default_model」去猜：精确查询
	// 也带着 default_model 列，当客户端请求的模型名恰好就是渠道的默认模型名时，
	// 猜法会把一次精确命中误判成兜底 —— 日志标错、候选还被排到末尾。
	fromFallbackQuery bool
}

// Candidates 返回按密钥路由顺序排好序的候选渠道。
//
// 候选有两个来源，合并后兜底段永远排在后面：
//  1. 精确命中 —— 渠道白名单里写了客户端请求的这个对外名；
//  2. 兜底 —— 渠道开了「默认模型映射」，于是用它的默认模型接单。
//
// 有了兜底，客户端换模型名就不必回来改配置；代价是**拼错模型名不再报错**
// （会被兜底悄悄接走），只能靠日志里的「兜底」标记让人发现。
func (r *Router) Candidates(ctx context.Context, q CandidateQuery) ([]Candidate, error) {
	exactRows, err := r.queryCandidates(ctx, q, false)
	if err != nil {
		return nil, err
	}
	// 没有模型名就没有「兜底」可言 —— 请求体缺 model 字段在上层已被拦下
	// （见 relay_handler），这里只是不让空名去匹配出一堆渠道。
	var fallbackRows []candidateRow
	if q.PublicModel != "" {
		fallbackRows, err = r.queryCandidates(ctx, q, true)
		if err != nil {
			return nil, err
		}
	}

	now := time.Now()
	exact := r.buildCandidates(exactRows, q, now, false)
	fallback := r.buildCandidates(fallbackRows, q, now, true)
	cands := mergeCandidates(exact, fallback)
	sortCandidates(cands, q.AllowedChannels)
	return cands, nil
}

// queryCandidates 取一轮候选行。fallback 为真时查「开了默认模型映射的渠道」，
// 否则查「白名单里写了这个模型名的渠道」。
//
// 两轮的渠道侧条件（启用、排序、密钥路由）完全一致，共用一份构造逻辑 ——
// 分开写迟早会漂移，而漂移的表现是兜底候选绕过了「渠道已停用」这类本该拦住的限制。
func (r *Router) queryCandidates(ctx context.Context, q CandidateQuery, fallback bool) ([]candidateRow, error) {
	var rows []candidateRow
	query := r.db.WithContext(ctx).
		Table("channels").
		Select("channels.*, channel_models.public_name AS public_name, channel_models.upstream_name AS upstream_name, channel_models.id AS binding_id, channel_models.proxy_id AS binding_proxy_id, COALESCE(channels.extra_config->>'default_model', '') AS default_model").
		Joins("JOIN channel_models ON channel_models.channel_id = channels.id AND channel_models.enabled = true").
		Where("channels.enabled = true").
		Order("channels.id ASC")

	if fallback {
		// 兜底的判定 = 「白名单里正好有渠道配置的那个默认模型名」。
		//
		// 用一个 JOIN 而不是 EXISTS 子查询：JOIN 顺手把默认模型那一行的
		// id 与 proxy_id 取回来，候选的 Binding 直接用它 —— 计费按这条
		// 白名单行定价、代理也跟随它自己的设置，与精确路径同一套语义。
		//
		// 这个 JOIN 同时完成了「指定的模型这条渠道真的能跑」的校验：
		// 默认模型不在白名单里（或那条被停用）的渠道不会成为兜底候选，
		// 请求不会带着上游不认识的名字发出去。**由此「开关开着但模型名
		// 没填」也自然不命中**（空串匹配不到任何白名单行）。
		//
		// SQL 只做粗筛（键存不存在），开关是否真的开着由 Go 侧的
		// ChannelDefaultModel 判定 —— 判据只有一处，避免「前端写 true、
		// 后端读 bool、SQL 读字符串」三处漂移。
		query = query.
			Where("jsonb_exists(channels.extra_config, 'default_model')").
			Where("channel_models.public_name = channels.extra_config->>'default_model'")
	} else {
		query = query.Where("channel_models.public_name = ?", q.PublicModel)
	}

	// 密钥路由：只在该密钥为这个模型指定的渠道中选。
	// 空切片与 nil 都表示不限制 —— 不带密钥上下文的调用方不该被影响。
	if len(q.AllowedChannels) > 0 {
		query = query.Where("channels.id IN ?", q.AllowedChannels)
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询候选渠道失败: %w", err)
	}
	// 来源在查询这一层就钉死，不留给下游从行内容猜
	for i := range rows {
		rows[i].fromFallbackQuery = fallback
	}
	return rows, nil
}

// buildCandidates 把查询结果行过一遍运行期条件，转成候选。
//
// fromFallbackQuery 由调用方从查询一并带来：它决定 Binding 指向哪一行
// （兜底时指向默认模型那一行，见下），不能从行内容推断。
func (r *Router) buildCandidates(rows []candidateRow, q CandidateQuery, now time.Time, fromFallbackQuery bool) []Candidate {
	excluded := make(map[uint]bool, len(q.Exclude))
	for _, id := range q.Exclude {
		excluded[id] = true
	}

	cands := make([]Candidate, 0, len(rows))
	for _, rw := range rows {
		if excluded[rw.ID] {
			continue
		}
		// 冷却中的渠道直接出局：上游明确要求退避，继续打只会让限流更久
		if _, cooling := r.state.InCooldown(rw.ID, now); cooling {
			continue
		}
		fallback := false
		if fromFallbackQuery {
			// 兜底轮：开关必须真的开着。SQL 只粗筛了「有 default_model 这个键」，
			// 开关状态在这里定 —— 判据复用 ChannelDefaultModel，与写入侧校验、
			// 渠道列表显示同一个出口。开关关着的行在这里丢掉，
			// 否则会把用户明确关掉的渠道重新拉进路由。
			_, on := ChannelDefaultModel(rw.ExtraConfig)
			if !on {
				continue
			}
			fallback = true
		}
		binding := model.ChannelModel{ID: rw.BindingID, ChannelID: rw.ID, PublicName: rw.PublicName, UpstreamName: rw.UpstreamName, Enabled: true, ProxyID: rw.BindingProxyID}
		if fallback {
			// 兜底候选的 Binding 指向默认模型那一行：PublicName 是计费键
			// （计价引擎按白名单行的对外名查价），UpstreamName 用那一行
			// **配置的上游名** —— 与精确命中同一语义。这一行可能配了
			// 「对外名 → 上游名」映射：若忽略映射、直接拿对外名发上游，
			// 上游会收到一个它不认识的名字，复现本功能要消灭的 2ms 502。
			// （UpstreamName 非空由 normalizeWhitelist 保证：留空即归一成对外名；
			// 行存在由查询的 JOIN 保证。）
			binding.PublicName = rw.DefaultModel
			binding.UpstreamName = rw.UpstreamName
		}
		maxConc := ChannelMaxConcurrency(rw.ExtraConfig)
		c := Candidate{
			Channel:   rw.Channel,
			Binding:   binding,
			Fallback:  fallback,
			Available: SlotAvailable(rw.Slots, now),
			Saturated: maxConc > 0 && r.state.Inflight(rw.ID) >= maxConc,
		}
		plain, err := r.cipher.Decrypt(rw.APIKeyEnc)
		if err != nil {
			// 密文解不开（换过主密钥、数据损坏）还放行的话，请求会带着空密钥
			// 发给上游、以 401 收场 —— 用户会去查上游，而问题其实在本地。
			// 剔出本轮候选并留下日志；APIKeyEnc 为空的免密渠道不受影响。
			r.logger.Warn("渠道密钥解密失败，已从候选中剔除",
				"channel_id", rw.ID, "channel", rw.Name, "err", err)
			continue
		}
		c.APIKeyPlain = plain
		cands = append(cands, c)
	}
	return cands
}

// mergeCandidates 把精确与兜底两段候选接起来，同一条渠道只保留精确那条。
//
// 一条渠道可以既精确命中、又开着兜底（用户既把模型名写进了白名单，
// 也给这条渠道配了默认映射）。两轮查询都会收它，不去重的话它会以两个身份
// 进候选链：第一次失败后 tried 记下渠道 ID，第二次被 filterTried 跳过 ——
// 白耗一个重试轮次，等于凭空少一次故障转移机会。
//
// 保留精确那条而不是兜底那条：精确那条是用户明确为这个模型配置的行，
// Binding 指向它自己的白名单行；兜底那条的 Binding 指向默认模型行，
// 用它计费会把这次调用算到别的模型头上。
func mergeCandidates(exact, fallback []Candidate) []Candidate {
	seen := make(map[uint]bool, len(exact))
	out := make([]Candidate, 0, len(exact)+len(fallback))
	for _, c := range exact {
		seen[c.Channel.ID] = true
		out = append(out, c)
	}
	for _, c := range fallback {
		if seen[c.Channel.ID] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// sortOrderOf 返回 id 在 order（密钥路由列表）里的下标；不在列表里时返回
// len(order) —— 排在所有已配置条目之后。抽成纯函数便于单测。
func sortOrderOf(order []uint, id uint) int {
	for i, v := range order {
		if v == id {
			return i
		}
	}
	return len(order)
}

// filterTried 在已排序的候选里跳过本次已试过的与刚进入冷却的渠道，保持相对顺序。
//
// 重试轮内用（候选查询已挪到循环外，见 Relay）：tried 随每次失败增长；
// 冷却状态可能被**其它并发请求**触发的熔断实时改变，所以每轮都要重新
// 查一遍 InCooldown —— 这也是轮内过滤仍能尊重新冷却的原因。
func (r *Router) filterTried(cands []Candidate, tried []uint) []Candidate {
	skip := make(map[uint]bool, len(tried))
	for _, id := range tried {
		skip[id] = true
	}
	now := time.Now()
	out := make([]Candidate, 0, len(cands))
	for _, c := range cands {
		if skip[c.Channel.ID] {
			continue
		}
		if _, cooling := r.state.InCooldown(c.Channel.ID, now); cooling {
			continue
		}
		out = append(out, c)
	}
	return out
}

// sortCandidates 依据密钥的路由顺序重排候选，第一个即首选渠道。
//
// 进到这里时候选已按渠道 id 升序（见 Candidates 的查询）—— 那只是确定性的
// 兜底序，真正的优先级来自密钥的 model_channels 列表。
//
// 排序分两层，顺序是刻意的：
//  1. 先按可用性分区（可用时段内且未达并发上限的优先）—— 一条到点才可用的
//     渠道，不该因为列表序号靠后就被一条当前不可用的渠道挡在前面；
//  2. 同一可用档内按密钥列表顺序稳定排列（sortOrderOf，未列入的垫底）。
//
// **兜底候选（Fallback）永远排在精确命中的候选之后，且不吃密钥顺序。**
// 必须在分区**之前**分成两段来排：可用性分区是稳定排序的前提，
// 混在一起排会让兜底渠道挤进精确段。
func sortCandidates(cands []Candidate, keyOrder []uint) {
	// 先把每条候选在密钥列表里的下标算好（不在列表里的垫底），再分层排序。
	for i := range cands {
		cands[i].keyOrder = sortOrderOf(keyOrder, cands[i].Channel.ID)
	}
	exact, fallback := splitFallback(cands)
	sortByAvailabilityAndKeyOrder(exact)
	sortByAvailabilityAndKeyOrder(fallback)
	copy(cands, exact)
	copy(cands[len(exact):], fallback)
}

// splitFallback 按是否兜底稳定分区：两段各自保持原相对顺序（即渠道 id 序）。
func splitFallback(cands []Candidate) (exact, fallback []Candidate) {
	exact = make([]Candidate, 0, len(cands))
	fallback = make([]Candidate, 0, len(cands))
	for _, c := range cands {
		if c.Fallback {
			fallback = append(fallback, c)
		} else {
			exact = append(exact, c)
		}
	}
	return exact, fallback
}

// sortByAvailabilityAndKeyOrder 可用时段内且未达并发上限的渠道优先；
// 同档之内按密钥路由顺序（keyOrder，已在 sortCandidates 里算好）升序。
//
// 判定抽成纯函数方便单测：它决定「用户配的顺序」与「当下可用性」谁先谁后 ——
// 答案是可用性先分区、顺序在同区内生效。
func sortByAvailabilityAndKeyOrder(cands []Candidate) {
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Available != cands[j].Available {
			return cands[i].Available
		}
		if cands[i].Saturated != cands[j].Saturated {
			return !cands[i].Saturated
		}
		return cands[i].keyOrder < cands[j].keyOrder
	})
}

// SlotAvailable 判断当前时刻是否落在渠道声明的可用时段内。
// slots 为空表示全天可用。时段支持跨午夜（结束时间早于开始时间）。
// 时刻按服务器本地时区判断（与定价时段规则同一口径）。
func SlotAvailable(slots model.JSONList, now time.Time) bool {
	if len(slots) == 0 {
		return true
	}
	weekday := int(now.Weekday()) // 0=周日
	if weekday == 0 {
		weekday = 7 // 归一化成 1..7，与 ISO 一致
	}
	cur := now.Format("15:04")

	for _, s := range slots {
		days := toIntSlice(s["days"])
		if len(days) > 0 && !containsInt(days, weekday) {
			continue
		}
		start, _ := s["start"].(string)
		end, _ := s["end"].(string)
		start = strings.TrimSpace(start)
		end = strings.TrimSpace(end)
		if start == "" || end == "" {
			continue
		}
		if start <= end {
			if cur >= start && cur <= end {
				return true
			}
		} else { // 跨午夜
			if cur >= start || cur <= end {
				return true
			}
		}
	}
	return false
}

func toIntSlice(v any) []int {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]int, 0, len(list))
	for _, item := range list {
		if f, ok := item.(float64); ok {
			result = append(result, int(f))
		}
	}
	return result
}

func containsInt(list []int, target int) bool {
	for _, v := range list {
		if v == target {
			return true
		}
	}
	return false
}
