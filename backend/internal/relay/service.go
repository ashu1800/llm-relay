package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/relay/convert"
)

// Options 控制转发行为，避免让 relay 包依赖全局配置。
type Options struct {
	MaxRetries int
	// FirstByteTimeout 只约束「等待上游响应头」这一段；流式正文不受它限制。
	FirstByteTimeout time.Duration
	// UpstreamTimeout 是非流式调用的总时长上限（连接 + 响应头 + 响应体），
	// 防止上游返回响应头之后挂住正文、把请求无限期拖住。0 表示不设上限。
	UpstreamTimeout   time.Duration
	InjectStreamUsage bool
}

// 渠道名额等待上限。等不到也照发：并发是软约束，
// 不应该把「上游压力大」直接变成「用户的请求失败」。
const (
	channelSlotWait = 3 * time.Second
	// 上游给了 429 但没带 Retry-After 时的默认退避时长
	defaultRateLimitCooldown = 30 * time.Second
	// 上游 5xx 时的短退避，避免立刻把同一个故障实例再打一遍
	defaultServerErrorCooldown = 5 * time.Second
	// 上游 401/403 的退避：密钥失效或被封不会自愈，冷却期内让后续
	// 请求直接走别的渠道，别每个请求都先撞一遍死渠道。
	defaultAuthErrorCooldown = 30 * time.Second
	// 温和熔断：渠道连续失败达到阈值后自动冷却。
	// 「任何非 2xx 都转移」意味着全挂时每个请求都要把候选链完整撞一遍；
	// 有了这道熔断，第一个请求付学费，后续请求在冷却期内直接绕开。
	failStreakThreshold  = 3
	autoCooldownOnStreak = 60 * time.Second
)

// Service 编排一次中转：选渠道 -> 转发 -> 失败转移。
type Service struct {
	router *Router
	fwd    *Forwarder
	db     *gorm.DB
	opts   Options
	logger *slog.Logger
	state  *ChannelState
	// groupLimit 是分组级的每分钟额度（分组上配的 RPM / TPM）。
	// 为零值时 Check 直接放行，所以未注入也能正常工作。
	groupLimit *GroupLimiter
	// OnChannelEvent 是渠道健康事件的出口（冷却 / 自动熔断 / 恢复），
	// 由 HTTP 层注入并转发到 live 推送。熔断机制本身早已生效，
	// 但对外一直是黑盒 —— 这里的目的就是把它变成看得见的。
	// 回调在转发路径上同步执行，必须轻（查一次库 + 一次广播），不得阻塞。
	OnChannelEvent func(ChannelEvent)
}

// ChannelEvent 是一次渠道健康状态翻转。
type ChannelEvent struct {
	ChannelID uint
	// Kind：cooldown = 上游信号冷却（429/5xx/401/403，首次进入）；
	// auto_cooldown = 连续失败达阈值的自动熔断；recovered = 恢复健康
	Kind string
	// Reason 是人读得过的原因（上游状态码 / 最近一次错误摘要）
	Reason string
	// Until 是冷却截止时间（cooldown / auto_cooldown 有值）
	Until time.Time
}

// emit 在回调已注入时发一条事件。nil 检查集中在这里，
// 三个触发点都不必再判空。
func (s *Service) emit(e ChannelEvent) {
	if s.OnChannelEvent != nil {
		s.OnChannelEvent(e)
	}
}

// SetChannelState 注入渠道运行期状态，用于并发闸门与冷却。
func (s *Service) SetChannelState(st *ChannelState) { s.state = st }

// Probe 用一条最小请求探一次上游，供管理界面的「测试连通性」使用。
//
// 刻意**不**做重试、不写请求日志、不计费：它回答的是「这条渠道现在通不通」，
// 而不是「这次请求最终会不会成功」。跟着重试会把「上游在限流」
// 这种一眼能看出的状态藏起来，那正是用户点这个按钮时想知道的。
func (s *Service) Probe(ctx context.Context, cand Candidate, body []byte) (*Attempt, error) {
	// 传的是站内通用语（Chat）的路径，与真实转发完全一致：
	// 探测报文也是 Chat 形状，由 convert.UpstreamRequest 按渠道协议统一改写路径与报文
	// （Anthropic -> /v1/messages，Responses -> /v1/responses，Gemini 把模型名写进路径）。
	// 这里**不要**提前把路径换成目标协议的端点：那样会被 isChatPath 判成非对话请求，
	// 于是报文不再被转换，上游收到一个 Chat 形状的请求，报「input is required」。
	return s.fwd.Do(ctx, cand, "/v1/chat/completions", body, http.Header{}, false)
}

// SetProxyResolver 把「按 id 查代理配置」的能力交给转发器。
func (s *Service) SetProxyResolver(fn ProxyResolver) { s.fwd.SetProxyResolver(fn) }

// InvalidateProxy 让某个代理缓存的客户端失效。代理的地址/端口/密码一改、
// 或被删掉时都要调用，否则旧连接会继续按老配置拨下去。
func (s *Service) InvalidateProxy(id uint) { s.fwd.InvalidateProxy(id) }

// SetGroupLimiter 注入分组限流器。不注入时分组限额不生效（等价于全部不限）。
func (s *Service) SetGroupLimiter(l *GroupLimiter) { s.groupLimit = l }

// NewService 构造转发服务。
func NewService(db *gorm.DB, router *Router, opts Options, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		router: router,
		fwd:    NewForwarder(opts.FirstByteTimeout, opts.UpstreamTimeout),
		db:     db,
		opts:   opts,
		logger: logger,
	}
}

// RelayRequest 描述一次入站调用。
type RelayRequest struct {
	TraceID      string
	InboundProto string
	UpstreamPath string
	PublicModel  string
	// GroupID > 0 时只在该分组内选渠道，并用该分组的策略
	GroupID uint
	// AllowedGroups 是密钥上的分组白名单（已解析成分组 ID）。
	// 非空时只在这些分组内选渠道；与 GroupID 是「与」的关系。
	// 空表示不限制 —— 与没有配白名单是两种状态，不要混为一谈。
	AllowedGroups []uint
	// Body 是归一化后发给上游的载荷；InboundBody 是客户端原始报文，仅用于留存排障
	Body        []byte
	InboundBody []byte
	Headers     http.Header
	APIKeyID    uint
	APIKeyName  string
	ClientIP    string
	Stream      bool
	// ThinkingLevel 是入站请求体里的思考等级（归一档位，见 ExtractThinkingLevel）。
	// 随日志落一份快照：列表的「思考」列显示它；空 = 请求没带思考参数。
	ThinkingLevel string
}

// RelayResult 是编排结果。
type RelayResult struct {
	Attempt   *Attempt
	Candidate Candidate
	Retries   int
	TraceID   string
	StartedAt time.Time
	// Trail 记录每一次失败尝试，便于日志中还原故障转移链路。
	Trail []AttemptTrail

	// releaseSlot 释放本次成功尝试占用的渠道并发名额。
	//
	// 名额必须一直持有到响应体转发结束：流式请求在收到响应头时 fwd.Do 就返回了，
	// 若在那里释放，真正占用上游连接的整段时间里该渠道的 inflight 是 0 ——
	// max_concurrency 形同虚设，Router 的 Saturated 也永远为 false，
	// 「渠道饱和就排到后面」这条策略从不生效。
	releaseSlot func()
}

// ReleaseSlot 释放渠道并发名额。响应体转发结束后必须调用，重复调用无副作用。
func (r *RelayResult) ReleaseSlot() {
	if r == nil || r.releaseSlot == nil {
		return
	}
	// 先清空再调用：重复调用不能把计数减成负数
	fn := r.releaseSlot
	r.releaseSlot = nil
	fn()
}

// AttemptTrail 是单次失败尝试的摘要。
type AttemptTrail struct {
	ChannelID   uint
	ChannelName string
	StatusCode  int
	Error       string
}

// ErrNoChannel 表示该模型当前没有可用渠道。
var ErrNoChannel = errors.New("没有可用的渠道")

// ErrGroupLimited 表示候选渠道都在已达每分钟额度的分组里。
//
// 与 ErrNoChannel 分开是必须的：两者的处理方式完全不同 ——
// 「没有可用渠道」要用户去配渠道，「分组超限」是过一会儿重试就好，
// 混成一个错误会让用户跑去改一个根本没配错的配置。
var ErrGroupLimited = errors.New("分组已达每分钟额度")

// ErrRequestUnsupported 表示请求本身无法转成目标上游协议（例如发音频给 Anthropic）。
//
// 为什么必须单独一个类型：这是**客户端**的问题，不是渠道的问题。
// 若按普通上游错误处理，会连带走两条错路 ——
// ① 逐个渠道重试一遍（每个都必然同样失败，白白消耗上游配额）；
// ② markChannelFailure 记上一笔失败，健康的渠道被误判成故障，
//
//	重试次数够了还会被冷却摘掉。用户发一次音频，可能把好渠道打进冷却。
//
// 定义在 convert 包（错误由那边产生），这里做个别名让本包调用方少一个 import。
var ErrRequestUnsupported = convert.ErrUnsupportedContent

// noChannelReason 说明为什么没有可用渠道。
//
// 特意把「被密钥白名单挡住」讲清楚：否则从「没有可用的渠道: 模型 xxx」
// 完全看不出是密钥限制导致的，而这种失败往往出现在改完密钥配置之后，
// 排查时最容易怀疑到别处去。
func (req *RelayRequest) noChannelReason() string {
	switch {
	case len(req.AllowedGroups) > 0 && req.GroupID > 0:
		return fmt.Sprintf("模型 %s 在分组 %d 与限定分组 %v 的交集内没有可用渠道",
			req.PublicModel, req.GroupID, req.AllowedGroups)
	case len(req.AllowedGroups) > 0:
		return fmt.Sprintf("模型 %s 在密钥限定的分组 %v 内没有可用渠道",
			req.PublicModel, req.AllowedGroups)
	case req.GroupID > 0:
		return fmt.Sprintf("模型 %s 在分组 %d 内没有可用渠道", req.PublicModel, req.GroupID)
	}
	return "模型 " + req.PublicModel
}

// availableModelsHint 在「没有可用渠道」时补一句「这个范围里现在能调什么」。
//
// 白名单化之后，请求失败的绝大多数原因就是「这个模型名没写进任何可用渠道的白名单」。
// 只回一句「没有可用的渠道」，用户对着渠道列表也看不出差在哪；
// 直接把可用模型名列出来，一眼就能发现是名字写错还是渠道没配。
// 只在请求已经失败时查库，正常路径不受影响。
func (s *Service) availableModelsHint(ctx context.Context, req *RelayRequest) string {
	db := s.router.db.WithContext(ctx)
	q := db.Table("channel_models").
		Distinct("channel_models.public_name").
		Joins("JOIN channels ON channels.id = channel_models.channel_id AND channels.enabled = true").
		Joins("JOIN channel_groups ON channel_groups.id = channels.group_id AND channel_groups.enabled = true").
		Where("channel_models.enabled = true")
	if req.GroupID > 0 {
		q = q.Where("channels.group_id = ?", req.GroupID)
	}
	if len(req.AllowedGroups) > 0 {
		q = q.Where("channels.group_id IN ?", req.AllowedGroups)
	}

	var names []string
	if err := q.Order("channel_models.public_name").Limit(12).Pluck("channel_models.public_name", &names).Error; err != nil {
		return ""
	}
	return modelsHintText(names, s.brokenFallbackCount(ctx, req), s.coolingCount(req))
}

// coolingCount 数出「能接这个模型、但此刻正在冷却」的渠道。
//
// 冷却状态在内存里（ChannelState），**数据库查不到** —— 而上面那段可用模型
// 提示是查库得来的。两者拼在一起就会出现自相矛盾的报错，2026-09-20 实测踩到：
//
//	没有可用的渠道: 模型 deepseek-v4.1-flash … 没有可用渠道；
//	当前范围内可用的模型有 deepseek-v4.1-flash、glm-5.3-flash
//
// 请求的模型名就在「可用」列表里，却说没有渠道。真相是那条渠道连续失败 6 次、
// 正在退避 30 秒 —— 提示里一个字都没提，用户只能对着渠道列表发呆。
//
// 判据必须与路由一致：同一个 InCooldown、同一份候选范围（启用渠道 + 启用分组 +
// 启用白名单行 + 密钥/分组白名单）。两边各查各的迟早漂移成
// 「提示说没冷却、路由说有」。
func (s *Service) coolingCount(req *RelayRequest) int {
	if s.state == nil || s.router == nil {
		return 0
	}
	now := time.Now()
	n := 0
	for _, id := range s.channelIDsForModel(req) {
		if _, cooling := s.state.InCooldown(id, now); cooling {
			n++
		}
	}
	return n
}

// channelIDsForModel 取「这个模型名在范围内、会被路由考虑到的渠道 ID」。
//
// 注意这里**不能**复用 Router.Candidates：它会把冷却中的渠道直接剔掉，
// 而本函数要回答的恰恰是「剔掉了几个」。
func (s *Service) channelIDsForModel(req *RelayRequest) []uint {
	q := s.router.db.WithContext(context.Background()).
		Table("channels").
		Joins("JOIN channel_models ON channel_models.channel_id = channels.id AND channel_models.enabled = true").
		Joins("JOIN channel_groups ON channel_groups.id = channels.group_id").
		Where("channel_models.public_name = ?", req.PublicModel).
		Where("channels.enabled = true").
		Where("channel_groups.enabled = true")
	if req.GroupID > 0 {
		q = q.Where("channels.group_id = ?", req.GroupID)
	}
	if len(req.AllowedGroups) > 0 {
		q = q.Where("channels.group_id IN ?", req.AllowedGroups)
	}
	var ids []uint
	if err := q.Pluck("channels.id", &ids).Error; err != nil {
		return nil
	}
	return ids
}

// brokenFallbackCount 数出范围内「开了默认模型映射但不会生效」的渠道。
//
// 这种半残状态（开关开着、模型名没填，或填的名字不在它自己的白名单里）在
// 请求失败时的表现与「完全没配」一模一样 —— 用户对着渠道列表看半天也找不出
// 差在哪。保存接口会拦它，但直接改库、导入旧备份都可能留下它，
// 所以在失败提示里点名。
//
// **只数「开关真的开着」的**：开关明确关掉是用户的决定，不是配置坏掉。
// 把「关着」也算进来会变成误报，而反复误报的提示会让人开始整体忽略它。
// 开关的判据用 lower(btrim(...)) 与 Go 侧的 truthyFlag 对齐
// （jsonb 的 true 与字符串 "true" 取出来都是 'true'，大小写不保证）。
func (s *Service) brokenFallbackCount(ctx context.Context, req *RelayRequest) int {
	q := s.router.db.WithContext(ctx).
		Table("channels").
		Joins("JOIN channel_groups ON channel_groups.id = channels.group_id AND channel_groups.enabled = true").
		Where("channels.enabled = true").
		Where("lower(btrim(channels.extra_config->>'default_model_enabled')) = 'true'").
		// 把「已配齐」的排除掉，剩下的就是半残的。
		// 用 NOT EXISTS 而不是 LEFT JOIN ... IS NULL：只关心条数，
		// EXISTS 的语义更直接，也不必担心后续加列时把 NULL 判断写漏
		Where(`NOT EXISTS (
			SELECT 1 FROM channel_models cm
			WHERE cm.channel_id = channels.id AND cm.enabled = true
			  AND cm.public_name = channels.extra_config->>'default_model'
		)`)
	if req.GroupID > 0 {
		q = q.Where("channels.group_id = ?", req.GroupID)
	}
	if len(req.AllowedGroups) > 0 {
		q = q.Where("channels.group_id IN ?", req.AllowedGroups)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return 0
	}
	return int(n)
}

// modelsHintText 拼装「没有可用渠道」时的补充说明。
//
// 抽成纯函数是为了能直接断言文案（这个包没有 DB 测试环境）。
//
// 三个数字各自对应一种「看着配好了、实际用不了」的状态，缺一个都会让
// 用户对着渠道列表找不出差在哪：
//   - names 为空      → 压根没配白名单
//   - cooling > 0     → 有渠道、但连续失败后正在退避（内存态，查库查不到）
//   - brokenFallback  → 开了默认模型映射但模型名没填 / 不在自己白名单里
func modelsHintText(names []string, brokenFallback, cooling int) string {
	var b strings.Builder
	if len(names) == 0 {
		b.WriteString("；当前范围内没有任何渠道配置模型白名单，请到「渠道管理」里给渠道加上模型")
	} else {
		b.WriteString("；当前范围内可用的模型有 " + strings.Join(names, "、"))
	}
	if cooling > 0 {
		// 这一句专门消解那个自相矛盾的提示：上面刚说「可用的模型有 X」，
		// 用户请求的正是 X，却被告知没有渠道 —— 原因是那些渠道在冷却中。
		// 点名它们，并顺带给出下一步（等退避结束，或去渠道页看失败原因）
		b.WriteString("。注意：此刻有 " + strconv.Itoa(cooling) +
			" 条渠道能接这个模型，但它们刚失败过、正在冷却中（退避几十秒后自动恢复；" +
			"若反复出现，去「渠道管理」看该渠道最近的错误）")
	}
	if brokenFallback > 0 {
		// 与上面那句并置而不替换：半残渠道可能与正常渠道同时存在，
		// 只报其中一种会让用户以为另一种也没问题
		b.WriteString("。另有 " + strconv.Itoa(brokenFallback) +
			" 条渠道开了「默认模型映射」但不会生效（没填模型名，或填的名字不在它自己的白名单里）")
	}
	return b.String()
}

// Relay 执行转发，按候选顺序做故障转移。
//
// 契约：err 非 nil 时 res 也**必须**非 nil（失败路径统一 return res, err）。
// 调用方拿到错误后第一件事就是读 res.Attempt（按真实状态码回客户端）、
// 最后还要 finalizeLog(res)——返回 (nil, err) 会让这些解引用直接 panic，
// 之前就发生过：handler 的新分支撞上「无可用渠道」的 nil 返回，
// 精心准备的可用模型提示全被一个裸 500 吞掉。
func (s *Service) Relay(ctx context.Context, req *RelayRequest) (*RelayResult, error) {
	started := time.Now()
	res := &RelayResult{TraceID: req.TraceID, StartedAt: started}

	var tried []uint
	var lastErr error
	// lastAttempt / lastCand 暂存「最后一次拿到上游应答的失败尝试」。
	// 候选链全部失败时把它放进结果：调用方能据此把上游真实的状态码与
	// 错误体回给客户端，而不是一律压成 502 —— 比如所有渠道都 401 时，
	// 客户端看到的应该是 401，那才是值得排查的方向。
	var lastAttempt *Attempt
	var lastCand Candidate

	// failures 收集本次转发中的渠道失败，函数返回时统一记账。
	// 不在循环里立即记，是为了等「多渠道共识」判定（见 recordFailures）：
	// 单次失败看不出是渠道的问题还是请求的问题，试完才知道。
	var failures []failRecord
	// 失败记账挪到后台 goroutine：这些 UPDATE（每条未豁免失败一次）原先
	// 同步挡在 Relay 返回之前 —— 「撞 3 个渠道后第 4 个成功」的流式请求，
	// 客户端首字节前要多等 3 次 DB 往返。记账不在关键路径上：UPDATE 是
	// 单条原子语句（无读-改-写竞态）、熔断计数自带锁；服务关停时的竞态
	// 最多让 goroutine 里的 UPDATE 报错进日志，无其它副作用。
	defer func() { go s.recordFailures(failures) }()
	maxAttempts := s.opts.MaxRetries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	// 候选查询放在重试循环**外**：重试轮内数据库的渠道数据不会变（失败
	// 记账已异步落库，几毫秒的窗口也影响不了本次转发），每次重试重新做
	// 三表 JOIN + 策略查询纯属浪费 —— 全渠道故障场景下一次请求要打
	// 2(N+1) 次查询，恰好在系统最脆弱时给 DB 加压。轮内改为内存过滤
	// （filterTried）：跳过已试过的与刚进入冷却的渠道。
	// 附带修正轮转语义：RR 游标每个请求只前进一格 —— 原先每次重试都
	// 推一格，带 3 次重试的请求让游标跳 4 格，轮转分布被故障转移流量扭曲。
	allCands, err := s.router.Candidates(ctx, CandidateQuery{
		PublicModel:   req.PublicModel,
		GroupID:       req.GroupID,
		AllowedGroups: req.AllowedGroups,
	})
	if err != nil {
		return res, err
	}

	// attemptNo 声明在循环外：失败收尾（循环后的兜底 return）要用它设
	// RetryCount —— break 提前退出时也保留着「实际进行到第几轮」
	attemptNo := 0
	for ; attemptNo < maxAttempts; attemptNo++ {
		cands := s.router.filterTried(allCands, tried)
		// 分组限额：把已达每分钟上限的分组这一轮剔掉。
		//
		// 必须在这里做，而不是在入口中间件里按密钥的分组白名单做：
		// 一个密钥可能允许好几个分组，只有选到具体候选才知道这次要走哪个分组。
		// 也正因为如此，被剔掉的候选不写进 tried —— 下一个重试轮次还要能重新考虑它
		// （限额是按分钟算的，跨过窗口边界就该恢复）。
		limitedReason := ""
		for len(cands) > 0 {
			ok, reason := s.groupLimit.Check(cands[0].Channel.GroupID, cands[0].GroupRPM, cands[0].GroupTPM, time.Now())
			if ok {
				break
			}
			limitedReason = reason
			cands = cands[1:]
		}
		if len(cands) == 0 {
			if limitedReason != "" {
				if attemptNo == 0 {
					return res, fmt.Errorf("%w: %s", ErrGroupLimited, limitedReason)
				}
				// 重试途中撞上分组额度：剩下的候选多半也在同一个分组里，
				// 继续重试只是重复撞墙。跳出循环，把真实的上游错误交给调用方
				// （直接返回 ErrGroupLimited 会把上游到底报了什么给吞掉）
				break
			}
			if attemptNo == 0 {
				return res, fmt.Errorf("%w: %s", ErrNoChannel, req.noChannelReason()+s.availableModelsHint(ctx, req))
			}
			break
		}

		cand := cands[0]
		tried = append(tried, cand.Channel.ID)
		// 计数放在「确定要发」这一刻：RPM 统计的是发往上游的请求数，重试也计入
		s.groupLimit.Record(cand.Channel.GroupID, time.Now())

		acquired := s.state.AcquireWait(ctx, cand.Channel.ID,
			ChannelMaxConcurrency(cand.Channel.ExtraConfig), channelSlotWait)
		if !acquired && ctx.Err() != nil {
			return res, ctx.Err() // 客户端已断开，没必要继续
		}
		if !acquired {
			s.logger.Warn("渠道并发已满，仍继续转发",
				"channel_id", cand.Channel.ID, "channel", cand.Channel.Name)
		}

		attempt, err := s.fwd.Do(ctx, cand, req.UpstreamPath, req.Body, req.Headers, s.opts.InjectStreamUsage)
		if err != nil {
			// 这一次没成，名额当场归还，让其它请求能用
			if acquired {
				s.state.Release(cand.Channel.ID)
			}
			// 客户端已断开（手动结束推理、关掉页面、请求超时取消）：
			// 换渠道重发毫无意义 —— 收件人已经不在了，下一个渠道只会
			// 对着空气再推理一遍。也**不**记渠道失败：渠道没有问题，
			// 问题在客户端侧；把好渠道打进 degraded 只会让后续请求错误地避开它。
			//
			// 判定看外层 ctx：fwd.Do 内部自设的 bodyTimeout 到期（上游太慢）
			// 不会取消外层 ctx，那种失败仍会正常走故障转移，不受这条豁免影响。
			if ctx.Err() != nil {
				res.Trail = append(res.Trail, AttemptTrail{
					ChannelID: cand.Channel.ID, ChannelName: cand.Channel.Name,
					Error: "客户端已断开: " + err.Error(),
				})
				return res, ctx.Err()
			}
			// 请求本身无法转换时立刻停手：换渠道结果一样，重试只是
			// 白耗上游配额，还会把健康渠道一笔笔记成失败（见
			// ErrRequestUnsupported 的说明）。直接返回，交给上层回 400。
			if errors.Is(err, ErrRequestUnsupported) {
				res.Trail = append(res.Trail, AttemptTrail{
					ChannelID: cand.Channel.ID, ChannelName: cand.Channel.Name, Error: err.Error(),
				})
				return res, err
			}
			res.Trail = append(res.Trail, AttemptTrail{
				ChannelID: cand.Channel.ID, ChannelName: cand.Channel.Name, Error: err.Error(),
			})
			lastErr = err
			failures = append(failures, failRecord{channelID: cand.Channel.ID, msg: err.Error()})
			continue
		}

		if attempt.Retryable() {
			// 同上：不可用的尝试不占用名额
			if acquired {
				s.state.Release(cand.Channel.ID)
			}
			// 物理性请求级错误（413/414/431）直接短路：请求的尺寸不因
			// 换渠道而变，后面的候选只会报同样的错、白耗配额。
			// 不转移、不冷却、不记账 —— 渠道只是如实拒绝了过大的请求。
			// 带着上游应答返回（error 为 nil），调用方按真实状态码回给客户端。
			if physicalRequestError(attempt.StatusCode) {
				res.Trail = append(res.Trail, AttemptTrail{
					ChannelID: cand.Channel.ID, ChannelName: cand.Channel.Name,
					StatusCode: attempt.StatusCode, Error: summarizeErrorBody(attempt.Body),
				})
				res.Attempt = attempt
				res.Candidate = cand
				return res, nil
			}
			s.applyCooldown(cand.Channel.ID, attempt)
			msg := summarizeErrorBody(attempt.Body)
			res.Trail = append(res.Trail, AttemptTrail{
				ChannelID: cand.Channel.ID, ChannelName: cand.Channel.Name,
				StatusCode: attempt.StatusCode, Error: msg,
			})
			lastErr = fmt.Errorf("上游返回 %d: %s", attempt.StatusCode, msg)
			// 是否「请求形状类」状态码先记下，等试完候选做共识判定；
			// 渠道级的（401/403/429/5xx）没有共识豁免一说
			failures = append(failures, failRecord{
				channelID: cand.Channel.ID, msg: msg,
				status: attempt.StatusCode, shape: requestShapeStatus[attempt.StatusCode],
			})
			if attempt.Stream != nil {
				_ = attempt.Stream.Close()
			}
			lastAttempt, lastCand = attempt, cand
			continue
		}

		// 走到这里说明上游返回了 2xx：成功，直接回给客户端。
		// （任何非 2xx 都在上面 Retryable 分支里转走了；413/414/431 在那里被短路。）
		//
		// 名额**不在这里释放**：流式响应此刻只拿到了响应头，正文还在从上游读，
		// 调用方转发结束后会调 res.ReleaseSlot()。
		s.markChannelSuccess(cand.Channel.ID)
		// 首包延迟喂给 least_latency 策略：只记成功响应，失败渠道该被冷却而不是比快
		if s.state != nil {
			s.state.RecordLatency(cand.Channel.ID, attempt.HeaderMs)
		}
		res.Attempt = attempt
		res.Candidate = cand
		res.Retries = attemptNo
		if acquired {
			channelID := cand.Channel.ID
			res.releaseSlot = func() { s.state.Release(channelID) }
		}
		return res, nil
	}

	if lastErr == nil {
		lastErr = ErrNoChannel
	}
	// 候选链全部失败（或撞上分组额度提前收场）：带上最后一次上游应答，
	// 让调用方回真实状态码。此时 Stream 已关闭、名额已归还，
	// ReleaseSlot 对空回调是幂等的，失败路径不会重复归还。
	if lastAttempt != nil {
		res.Attempt = lastAttempt
		res.Candidate = lastCand
	}
	// 失败链也带上转移规模（仅成功路径设过）：候选链全灭的请求无论
	// 实际撞了几个渠道，日志里的 RetryCount 原先都是 0 —— 故障发生时
	// 恰恰最需要知道转移了几层
	res.Retries = attemptNo
	return res, lastErr
}

// applyCooldown 按上游的限流/故障信号把渠道暂时摘掉。
//
// 这段是「上游已经在限流，我们却还在往上打」的解法：
// 收到 429 后带 Retry-After 就按它退避，没带就按默认值，
// 让后续流量自动去别的渠道，而不是把同一个上游打得更惨。
func (s *Service) applyCooldown(channelID uint, attempt *Attempt) {
	if s.state == nil || attempt == nil {
		return
	}
	var wait time.Duration
	switch {
	case attempt.StatusCode == http.StatusTooManyRequests:
		wait = ParseRetryAfter(attempt.Headers.Get("Retry-After"), time.Now())
		if wait <= 0 {
			wait = defaultRateLimitCooldown
		}
	case attempt.StatusCode == http.StatusServiceUnavailable:
		wait = ParseRetryAfter(attempt.Headers.Get("Retry-After"), time.Now())
		if wait <= 0 {
			wait = defaultServerErrorCooldown
		}
	case attempt.StatusCode == http.StatusBadGateway || attempt.StatusCode == http.StatusGatewayTimeout:
		wait = defaultServerErrorCooldown // 5xx 短暂退避，换渠道重试
	case attempt.StatusCode == http.StatusUnauthorized || attempt.StatusCode == http.StatusForbidden:
		// 密钥失效/被封属于渠道级故障：请求重发多少次结果都一样，
		// 冷却一段时间让流量自动绕开，等运维换密钥后再自然恢复
		wait = defaultAuthErrorCooldown
	default:
		return
	}
	// 只在「新进入冷却」时广播：冷却中的渠道反复 429 是常态，
	// 每次都通知就是告警轰炸。Cooldown 的返回值正是为此而设
	if s.state.Cooldown(channelID, wait) {
		s.emit(ChannelEvent{ChannelID: channelID, Kind: "cooldown",
			Reason: fmt.Sprintf("上游返回 %d", attempt.StatusCode), Until: time.Now().Add(wait)})
	}
	s.logger.Warn("渠道进入冷却",
		"channel_id", channelID, "status", attempt.StatusCode, "cooldown", wait.String())
}

// failRecord 是一次失败尝试的记账材料。
type failRecord struct {
	channelID uint
	msg       string
	status    int // 0 表示网络层错误
	// shape 标记这次失败是否「请求形状类」状态码（见 requestShapeStatus），
	// 供共识判定决定要不要豁免记账。
	shape bool
}

// exemptedByConsensus 判定每条失败记录是否被「多渠道共识」豁免记账：
// 一次转发里有 >=2 个渠道报了**同一个**请求形状类状态码（比如都 400），
// 说明问题出在请求本身 —— 渠道是无辜的，不该被记成 degraded。
//
// 这是中继特有的信号：单个渠道报 400 分不清是渠道的问题还是请求的问题，
// 但跨渠道的相同 4xx 是强证据。渠道级错误（401/403/429/5xx/网络失败，
// 即 shape=false 的记录）永远不豁免 —— 两个渠道密钥都坏不代表第三个也坏。
func exemptedByConsensus(failures []failRecord) []bool {
	shapeCount := map[int]int{}
	for _, f := range failures {
		if f.shape {
			shapeCount[f.status]++
		}
	}
	out := make([]bool, len(failures))
	for i, f := range failures {
		out[i] = f.shape && shapeCount[f.status] >= 2
	}
	return out
}

// recordFailures 在一次转发结束后统一记账（Relay 用 defer 调用）。
// 共识豁免之外的失败逐条落库并喂给熔断计数。
func (s *Service) recordFailures(failures []failRecord) {
	if len(failures) == 0 {
		return
	}
	exempt := exemptedByConsensus(failures)
	for i, f := range failures {
		if exempt[i] {
			s.logger.Info("共识判定为请求级错误，不记渠道失败",
				"channel_id", f.channelID, "status", f.status)
			continue
		}
		s.markChannelFailure(f.channelID, f.msg)
	}
}

// markChannelFailure 记录渠道异常，供后台展示与后续调度参考。
// 同时驱动温和熔断：连续失败达到阈值的渠道自动冷却 ——
// 「任何非 2xx 都转移」意味着全挂时每个请求都要把候选链完整撞一遍，
// 有了这道熔断，第一个请求付学费，后续请求在冷却期内直接绕开。
func (s *Service) markChannelFailure(channelID uint, msg string) {
	if len(msg) > 500 {
		msg = msg[:500]
	}
	now := time.Now().UTC()
	err := s.db.Model(&model.Channel{}).Where("id = ?", channelID).Updates(map[string]any{
		"health_status":   "degraded",
		"last_error":      msg,
		"last_checked_at": now,
	}).Error
	if err != nil {
		s.logger.Warn("更新渠道健康状态失败", "channel_id", channelID, "err", err)
	}
	// 连击数达到阈值就冷却。冷却到期后渠道若还在失败，一次就能再次
	// 触发；只有成功过一次（NoteSuccess 清零）才算真正恢复 ——
	// 所以不会出现「冷却刚好过期，失败一次又被摘掉」导致永远没机会
	// 自愈的死循环：到期后的第一次尝试就是机会。
	if s.state != nil && s.state.NoteFailure(channelID) >= failStreakThreshold {
		if s.state.Cooldown(channelID, autoCooldownOnStreak) {
			// 同样只在新进入冷却时广播（与 applyCooldown 同理）
			s.emit(ChannelEvent{ChannelID: channelID, Kind: "auto_cooldown",
				Reason: msg, Until: time.Now().Add(autoCooldownOnStreak)})
		}
		s.logger.Warn("渠道连续失败，自动冷却",
			"channel_id", channelID, "streak", failStreakThreshold, "cooldown", autoCooldownOnStreak.String())
	}
}

// markChannelSuccess 恢复渠道健康状态。
func (s *Service) markChannelSuccess(channelID uint) {
	// 连续失败计数清零：成功一次即完全康复（熔断解除的前提）
	if s.state != nil {
		s.state.NoteSuccess(channelID)
	}
	res := s.db.Model(&model.Channel{}).
		Where("id = ? AND health_status <> ?", channelID, "healthy").
		Updates(map[string]any{"health_status": "healthy", "last_error": ""})
	if res.Error != nil {
		s.logger.Warn("更新渠道健康状态失败", "channel_id", channelID, "err", res.Error)
		return
	}
	// 只有 degraded -> healthy 真的翻转时才广播「已恢复」：
	// 渠道健康时每条成功请求都会走到这里，无差别广播就是每秒一条噪音
	if res.RowsAffected > 0 {
		s.emit(ChannelEvent{ChannelID: channelID, Kind: "recovered",
			Reason: "最近一次转发成功"})
	}
}

// summarizeErrorBody 从上游错误响应里抽取可读信息。
func summarizeErrorBody(raw []byte) string {
	if len(raw) == 0 {
		return "上游未返回错误详情"
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// NewTraceID 生成请求追踪 ID。
func NewTraceID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
