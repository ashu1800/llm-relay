package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"llm-relay/internal/model"
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
	if len(names) == 0 {
		return "；当前范围内没有任何渠道配置模型白名单，请到「渠道管理」里给渠道加上模型"
	}
	return "；当前范围内可用的模型有 " + strings.Join(names, "、")
}

// Relay 执行转发，按候选顺序做故障转移。
func (s *Service) Relay(ctx context.Context, req *RelayRequest) (*RelayResult, error) {
	started := time.Now()
	res := &RelayResult{TraceID: req.TraceID, StartedAt: started}

	var tried []uint
	var lastErr error
	maxAttempts := s.opts.MaxRetries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	for attemptNo := 0; attemptNo < maxAttempts; attemptNo++ {
		cands, err := s.router.Candidates(ctx, CandidateQuery{
			PublicModel:   req.PublicModel,
			GroupID:       req.GroupID,
			AllowedGroups: req.AllowedGroups,
			Exclude:       tried,
		})
		if err != nil {
			return nil, err
		}
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
					return nil, fmt.Errorf("%w: %s", ErrGroupLimited, limitedReason)
				}
				// 重试途中撞上分组额度：剩下的候选多半也在同一个分组里，
				// 继续重试只是重复撞墙。跳出循环，把真实的上游错误交给调用方
				// （直接返回 ErrGroupLimited 会把上游到底报了什么给吞掉）
				break
			}
			if attemptNo == 0 {
				return nil, fmt.Errorf("%w: %s", ErrNoChannel, req.noChannelReason()+s.availableModelsHint(ctx, req))
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
			res.Trail = append(res.Trail, AttemptTrail{
				ChannelID: cand.Channel.ID, ChannelName: cand.Channel.Name, Error: err.Error(),
			})
			lastErr = err
			s.markChannelFailure(cand.Channel.ID, err.Error())
			continue
		}

		if attempt.Retryable() {
			// 同上：不可用的尝试不占用名额
			if acquired {
				s.state.Release(cand.Channel.ID)
			}
			s.applyCooldown(cand.Channel.ID, attempt)
			msg := summarizeErrorBody(attempt.Body)
			res.Trail = append(res.Trail, AttemptTrail{
				ChannelID: cand.Channel.ID, ChannelName: cand.Channel.Name,
				StatusCode: attempt.StatusCode, Error: msg,
			})
			lastErr = fmt.Errorf("上游返回 %d: %s", attempt.StatusCode, msg)
			s.markChannelFailure(cand.Channel.ID, msg)
			if attempt.Stream != nil {
				_ = attempt.Stream.Close()
			}
			continue
		}

		// 成功或不可重试的业务错误，直接回给客户端。
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
	default:
		return
	}
	s.state.Cooldown(channelID, wait)
	s.logger.Warn("渠道进入冷却",
		"channel_id", channelID, "status", attempt.StatusCode, "cooldown", wait.String())
}

// markChannelFailure 记录渠道异常，供后台展示与后续调度参考。
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
}

// markChannelSuccess 恢复渠道健康状态。
func (s *Service) markChannelSuccess(channelID uint) {
	err := s.db.Model(&model.Channel{}).
		Where("id = ? AND health_status <> ?", channelID, "healthy").
		Updates(map[string]any{"health_status": "healthy", "last_error": ""}).Error
	if err != nil {
		s.logger.Warn("更新渠道健康状态失败", "channel_id", channelID, "err", err)
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
