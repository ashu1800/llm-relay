package relay

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
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
	// GroupRPM / GroupTPM 是所属分组的每分钟额度（0 = 不限制）。
	// 路由时一并取出来，省得为每个候选再查一次分组表。
	GroupRPM int
	GroupTPM int
}

// EgressProxyID 返回这次请求该走哪个代理：模型条目上的设置优先于渠道级，
// 都是 0 表示直连。
func (c Candidate) EgressProxyID() uint {
	if c.Binding.ProxyID != 0 {
		return c.Binding.ProxyID
	}
	return c.Channel.ProxyID
}

// Router 负责按分组与模型挑选渠道，并给出故障转移顺序。
type Router struct {
	db     *gorm.DB
	cipher *secure.Cipher
	// logger 用于记录「渠道密钥解不开被剔除」这类需要人看见的异常
	logger *slog.Logger

	mu       sync.Mutex
	rrCursor map[uint]int // 分组 ID -> 轮询游标

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
	return &Router{db: db, cipher: cipher, logger: slog.Default(), rrCursor: make(map[uint]int)}
}

// CandidateQuery 是一次候选渠道查询的条件。
type CandidateQuery struct {
	PublicModel string
	// GroupID > 0 时只在该分组内选择，分组策略也取它的
	GroupID uint
	// AllowedGroups 非空时只在这些分组内选择（来自密钥白名单）
	AllowedGroups []uint
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
	GroupRPM       int
	GroupTPM       int
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

// Candidates 返回按策略排好序的候选渠道。
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

	// 策略归属：显式分组优先；密钥只限定了一个分组时用那个分组的策略；
	// 否则回落到默认分组。不加这一段的话，被白名单限定到某个分组的请求
	// 仍然按默认分组的策略排序，分组上配的策略形同虚设。
	strategyGroup := q.GroupID
	if strategyGroup == 0 && len(q.AllowedGroups) == 1 {
		strategyGroup = q.AllowedGroups[0]
	}
	sortCandidates(cands, r.strategyFor(ctx, strategyGroup), r)
	return cands, nil
}

// queryCandidates 取一轮候选行。fallback 为真时查「开了默认模型映射的渠道」，
// 否则查「白名单里写了这个模型名的渠道」。
//
// 两轮的渠道侧条件（启用、分组、分组启用、排序、密钥/分组白名单）完全一致，
// 共用一份构造逻辑 —— 分开写迟早会漂移，而漂移的表现是兜底候选绕过了
// 「渠道已停用」这类本该拦住的限制。
func (r *Router) queryCandidates(ctx context.Context, q CandidateQuery, fallback bool) ([]candidateRow, error) {
	var rows []candidateRow
	query := r.db.WithContext(ctx).
		Table("channels").
		Select("channels.*, channel_models.public_name AS public_name, channel_models.upstream_name AS upstream_name, channel_models.id AS binding_id, channel_models.proxy_id AS binding_proxy_id, channel_groups.rpm AS group_rpm, channel_groups.tpm AS group_tpm, COALESCE(channels.extra_config->>'default_model', '') AS default_model").
		Joins("JOIN channel_models ON channel_models.channel_id = channels.id AND channel_models.enabled = true").
		Joins("JOIN channel_groups ON channel_groups.id = channels.group_id").
		Where("channels.enabled = true").
		// 停用的分组连同它的渠道一起退出候选，否则「停用分组」这个开关毫无作用
		Where("channel_groups.enabled = true").
		// weight 是组内优先级序号（越小越优先），按它升序取出来，
		// 排在最前的就是首选渠道，后面的依次是故障转移链。
		// id 只是同序号时的兜底（正常情况下组内序号唯一，用不上）
		Order("channels.weight ASC, channels.id ASC")

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

	if q.GroupID > 0 {
		query = query.Where("channels.group_id = ?", q.GroupID)
	}
	// 密钥白名单：分组之外的渠道一律不参选。
	// 空切片与 nil 都表示不限制 —— 白名单没配就不该影响路由。
	if len(q.AllowedGroups) > 0 {
		query = query.Where("channels.group_id IN ?", q.AllowedGroups)
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
			// 兜底候选的对外名就是默认模型名（计费键），上游名也用它。
			// 上游名留空是绝不允许的：那会让请求带着客户端的原名发出去，
			// 又变成那个 2ms 的 502 —— 正是本功能要消灭的东西。
			// 查询的 JOIN 保证了它非空且在该渠道白名单里。
			binding.PublicName = rw.DefaultModel
			binding.UpstreamName = rw.DefaultModel
		}
		maxConc := ChannelMaxConcurrency(rw.ExtraConfig)
		c := Candidate{
			Channel:   rw.Channel,
			Binding:   binding,
			Fallback:  fallback,
			Available: SlotAvailable(rw.Slots, now),
			Saturated: maxConc > 0 && r.state.Inflight(rw.ID) >= maxConc,
			GroupRPM:  rw.GroupRPM,
			GroupTPM:  rw.GroupTPM,
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

// strategyFor 取分组的路由策略，取不到则回退到顺序故障转移。
func (r *Router) strategyFor(ctx context.Context, groupID uint) string {
	if groupID == 0 {
		var g model.ChannelGroup
		if err := r.db.WithContext(ctx).Where("is_default = true").First(&g).Error; err == nil {
			return g.Strategy
		}
		return model.StrategyFailover
	}
	var g model.ChannelGroup
	if err := r.db.WithContext(ctx).First(&g, groupID).Error; err != nil {
		return model.StrategyFailover
	}
	return g.Strategy
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

// sortCandidates 依据策略重排候选，第一个即首选渠道。
//
// 进到这里时候选已按 weight 升序（见 Candidates 的 Order）—— 那就是优先级序。
// 各策略的差别只在于「谁排到最前」：
//   - failover：不动，严格照优先级来
//   - round_robin：从游标处起轮转
//   - least_latency：按观测到的首包延迟重排
//
// **兜底候选（Fallback）永远排在精确命中的候选之后，且不参与策略。**
// 这是「白名单精确命中优先」的落地点，必须在分区**之前**分成两段来排：
// 三种策略都会主动把任意候选挪到最前（RR 是整段左移、least_latency 按延迟重排），
// 排在最后再分区是来不及的。所以这里先稳定分区，只对精确段套用策略，
// 兜底段只按可用性排序后原样接在尾部。
func sortCandidates(cands []Candidate, strategy string, r *Router) {
	exact, fallback := splitFallback(cands)
	sortByAvailability(exact)
	sortByAvailability(fallback)
	applyStrategy(exact, strategy, r)
	copy(cands, exact)
	copy(cands[len(exact):], fallback)
}

// splitFallback 按是否兜底稳定分区：两段各自保持原相对顺序（即 weight 序）。
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

// sortByAvailability 可用时段内且未达并发上限的渠道优先。
//
// 注意这一步会把「优先级」暂时压到次要位置：一条到点才可用的渠道，
// 不该因为序号靠后就被一条当前不可用的渠道挡在前面。
func sortByAvailability(cands []Candidate) {
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Available != cands[j].Available {
			return cands[i].Available
		}
		return !cands[i].Saturated && cands[j].Saturated
	})
}

// applyStrategy 对**同一段内**的候选套用分组策略，把该段的首选挪到最前。
func applyStrategy(cands []Candidate, strategy string, r *Router) {
	if len(cands) == 0 {
		return
	}
	switch strategy {
	case model.StrategyRoundRobin:
		rotate(cands, r.nextCursor(cands))
	case model.StrategyLeastLatency:
		// 按最近成功响应的首包延迟（EWMA，见 ChannelState.RecordLatency）升序。
		// 没有观测值的渠道排最后但不出局 —— 否则新渠道永远得不到第一次机会。
		lat := func(id uint) float64 {
			if v := r.state.Latency(id); v > 0 {
				return v
			}
			return math.Inf(1)
		}
		sort.SliceStable(cands, func(i, j int) bool {
			return lat(cands[i].Channel.ID) < lat(cands[j].Channel.ID)
		})
	default:
		// failover（以及任何未知取值）：候选已按 weight 升序，
		// 什么都不用做 —— 顺序本身就是故障转移顺序
	}
}

// nextCursor 取轮询起点并推进游标。
//
// 游标按分组记：不同分组的轮转互不干扰，各按键自己的节奏走。
//
// 这里曾经是坏的：rotate() 不读游标，每个请求都「把首个挪到末尾」，
// 于是每次挑中的都是第二优先的渠道，而 rrCursor 声明了却从未被使用。
// 轮询的语义是「依次轮着来」，起点必须逐次前进。
func (r *Router) nextCursor(cands []Candidate) int {
	if len(cands) == 0 {
		return 0
	}
	groupID := cands[0].Channel.GroupID
	r.mu.Lock()
	defer r.mu.Unlock()
	start := r.rrCursor[groupID] % len(cands)
	r.rrCursor[groupID] = (start + 1) % len(cands)
	return start
}

// rotate 把候选整体左移 offset：offset 处的那条成为首选，其余保持相对顺序。
func rotate(cands []Candidate, offset int) {
	if len(cands) <= 1 || offset <= 0 {
		return
	}
	offset %= len(cands)
	if offset == 0 {
		return
	}
	rotated := make([]Candidate, 0, len(cands))
	rotated = append(rotated, cands[offset:]...)
	rotated = append(rotated, cands[:offset]...)
	copy(cands, rotated)
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
