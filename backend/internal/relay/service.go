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
	MaxRetries        int
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
}

// SetChannelState 注入渠道运行期状态，用于并发闸门与冷却。
func (s *Service) SetChannelState(st *ChannelState) { s.state = st }

// NewService 构造转发服务。
func NewService(db *gorm.DB, router *Router, opts Options, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		router: router,
		fwd:    NewForwarder(opts.UpstreamTimeout),
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
		if len(cands) == 0 {
			if attemptNo == 0 {
				return nil, fmt.Errorf("%w: %s", ErrNoChannel, req.noChannelReason())
			}
			break
		}

		cand := cands[0]
		tried = append(tried, cand.Channel.ID)

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
