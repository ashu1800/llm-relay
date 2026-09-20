package relay

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"llm-relay/internal/model"
)

// queuedLog 把日志与报文打包投递。
// 报文要等日志落库拿到自增 ID 才能关联，因此两者必须在同一条队列消息里。
type queuedLog struct {
	Log     model.RequestLog
	Payload *model.RequestPayload
}

// LogWriter 负责把调用明细异步落库，避免统计拖慢转发。
type LogWriter struct {
	db     *gorm.DB
	queue  chan queuedLog
	logger *slog.Logger
	done   chan struct{}

	// capacity 是队列容量（buffer 的原值）：看板要显示「日志队列 12/2048」
	// 这样的水位，只有 len(queue) 没有分母。构造后不变。
	capacity int

	// dropped 是进程启动以来被丢弃的日志条数（队列满、服务退出各算）。
	// 看板上的所有数字都出自这张表 —— 这条队列悄悄丢日志时，统计就在
	// 「看起来正常」地少算。这里把丢弃变成一个可观测的量，经 live 推送
	// （见 api.liveStatsLoop 的 health 消息）展示到看板，丢弃发生时第一时间喊出来。
	// 只在内存累计：重启归零是想要的语义，它回答的是「这一程丢了没有」。
	dropped atomic.Int64

	// mu 保护 queue 的「关闭」与「投递」不交错。
	//
	// 为什么需要它：往已关闭的通道发送**即使写在 select 里也会 panic**
	// （见 TestSendOnClosedChannelPanics），而优雅关闭确实会走到这一步 ——
	// 等的在途请求超时后代码继续往下关队列，那个请求结束时会回来 Enqueue。
	// 不加锁就只能靠 recover 兜住，那是「明知有竞态、等它崩了再救」；
	// 加锁之后这个窗口根本不存在。
	//
	// 代价是每次 Enqueue 一次 RLock。它在「请求结束、准备写库」时各调一次，
	// 与一次上游往返相比可以忽略，而它换来的是「关停期间绝不 panic」。
	mu     sync.RWMutex
	closed bool
}

// NewLogWriter 启动后台写入协程。
func NewLogWriter(db *gorm.DB, buffer int, logger *slog.Logger) *LogWriter {
	if buffer <= 0 {
		buffer = 1024
	}
	w := &LogWriter{db: db, queue: make(chan queuedLog, buffer), capacity: buffer, logger: logger, done: make(chan struct{})}
	go w.loop()
	return w
}

// Enqueue 投递一条日志。payload 为 nil 表示当前模式不留存报文。
// 队列满时丢弃并告警，绝不阻塞转发链路；已关闭时同样丢弃，绝不 panic。
func (w *LogWriter) Enqueue(entry model.RequestLog, payload *model.RequestPayload) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		// 走到这里说明进程正在退出（关闭发生在「等在途请求」超时之后）。
		// 这条统计只能丢，但必须留下痕迹 —— 静默丢数据是最难查的一类问题。
		w.warnDropped(entry, "服务正在退出")
		return
	}
	select {
	case w.queue <- queuedLog{Log: entry, Payload: payload}:
	default:
		w.warnDropped(entry, "日志队列已满")
	}
}

func (w *LogWriter) warnDropped(entry model.RequestLog, why string) {
	w.dropped.Add(1)
	if w.logger != nil {
		w.logger.Warn("丢弃一条统计", "why", why, "trace_id", entry.TraceID)
	}
}

// LogQueueStats 是日志队列的一个瞬时快照，供看板的「队列水位」展示与告警。
type LogQueueStats struct {
	// Queued 是此刻还排在队列里没落库的条数（0..Capacity）
	Queued int `json:"queued"`
	// Capacity 是队列容量（不随时间变化，前端拿它当分母）
	Capacity int `json:"capacity"`
	// Dropped 是进程启动以来累计丢弃的条数（队列满 + 服务退出）
	Dropped int64 `json:"dropped"`
}

// QueueStats 取队列快照。随时可调：len(chan) 与原子读都不需要锁，
// 关闭后的通道 len 同样安全（残余未取走的仍会计入）。
func (w *LogWriter) QueueStats() LogQueueStats {
	return LogQueueStats{
		Queued:   len(w.queue),
		Capacity: w.capacity,
		Dropped:  w.dropped.Load(),
	}
}

// shutdown 标记关闭并关掉队列。可重复调用：第二次起直接返回，
// 因为重复 close 通道同样会 panic。
// 调用方必须持有写锁。
func (w *LogWriter) shutdownLocked() {
	if w.closed {
		return
	}
	w.closed = true
	close(w.queue)
}

func (w *LogWriter) loop() {
	defer close(w.done)
	for item := range w.queue {
		if err := w.db.Create(&item.Log).Error; err != nil {
			if w.logger != nil {
				w.logger.Warn("写入请求日志失败", "trace_id", item.Log.TraceID, "err", err)
			}
			// 日志没落库就没有 ID，报文关联不上，直接跳过而不是留孤儿行
			continue
		}
		if item.Payload == nil {
			continue
		}
		item.Payload.LogID = item.Log.ID
		if err := w.db.Create(item.Payload).Error; err != nil && w.logger != nil {
			w.logger.Warn("写入报文留存失败", "trace_id", item.Log.TraceID, "err", err)
		}
	}
}

// Close 停止写入协程。它只关通道、不等队列写完 ——
// 进程紧接着就退出时，队列里剩下的记录会全部丢失。
//
// 想「关掉并且确保写完」请用 CloseAndFlush；保留这个是为了兼容
// 「关掉就走」的调用方（测试与命令行工具），语义与从前一致。
func (w *LogWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.shutdownLocked()
}

// CloseAndFlush 关闭队列并等待其中的记录全部落库，最多等 timeout。
//
// 存在的理由：请求日志是异步写的（转发链路绝不阻塞），进程退出时队列里
// 往往还有没落库的记录 —— 原来只有 Close()，它不等写入协程，于是每次
// 重启/部署都会静默丢掉最后那批日志，而这批恰好是「停机前正在发生的请求」，
// 排障时最需要看的就是它们。
//
// 返回 true 表示队列已清空。超时（false）不算错误：数据库慢或队列很深时，
// 宁可丢几条也不能把退出卡死 —— 容器有 stop_grace_period，超时会被 SIGKILL，
// 那样连这几行日志都打不出来。
//
// 关闭与等待分成两步是为了不长时间持锁：先持写锁把队列关掉（此后 Enqueue
// 会安全地丢弃），立刻放锁，再慢慢等写入协程把已入队的记录写完。
// 这样关闭期间还在结束的请求不会被这个等待阻塞 —— 它们只会被丢弃并告警。
func (w *LogWriter) CloseAndFlush(timeout time.Duration, logger *slog.Logger) bool {
	w.mu.Lock()
	w.shutdownLocked()
	w.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.done:
		return true
	case <-timer.C:
		if logger != nil {
			logger.Warn("日志队列未在预算内写完，剩余记录将丢失", "timeout", timeout)
		}
		return false
	}
}

// trailToJSONMap 把故障转移链路转成可落 jsonb 的结构。
// 用显式小写字段而不是直接序列化 AttemptTrail：结构体字段大写 JSON
// 里也就是大写，前端与既有日志的命名风格（全小写下划线）会分叉。
func trailToJSONMap(trail []AttemptTrail) model.JSONMap {
	steps := make([]map[string]any, 0, len(trail))
	for _, t := range trail {
		step := map[string]any{
			"channel_id":   t.ChannelID,
			"channel_name": t.ChannelName,
			"status_code":  t.StatusCode,
			"error":        t.Error,
		}
		if t.Usage != nil {
			step["usage"] = map[string]any{
				"prompt_tokens":     t.Usage.PromptTokens,
				"completion_tokens": t.Usage.CompletionTokens,
				"total_tokens":      t.Usage.TotalTokens,
			}
		}
		steps = append(steps, step)
	}
	return model.JSONMap{"steps": steps}
}

// TruncateRunes 按字符数截断，超出部分以省略号收尾。
//
// 为什么按 rune 而不是字节：Postgres varchar(n) 的 n 数的是**字符**，
// 上游错误体里中英混排很常见，按字节截会把多字节字符切成乱码。
// 日志列（varchar(1024)）与客户端提示都用它，是超长错误体的统一出口 ——
// 网关 502 的 HTML 错误页可达几十 KB，不截的话轻则撑爆日志列导致
// 整条失败日志被丢弃（恰恰是排障最需要的），重则把超长 message 回给客户端。
func TruncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	if limit <= 0 {
		return ""
	}
	return string(r[:limit]) + "…"
}

// BillingModel 返回这次调用该按哪个模型名定价。
//
// 价格是「渠道 × 模型」维度的（见 pricing.Engine），所以必须用真正承接这次
// 调用的那条白名单行的**对外名**，而不是客户端请求的名字：
//
//   - 精确命中时两者本就相等 —— 候选查询的条件就是 public_name = 请求名，
//     所以这是一次行为无变化的等价替换；
//   - 兜底时它们不同：客户端的 claude-opus-4-7 在渠道里根本没配价，按请求名
//     查只会得到「没配价、记 0 元」，而按默认模型名查才能拿到那条真实单价
//     （候选的 Binding 已被指向默认模型那一行，见 Router.buildCandidates）。
//
// 两种情况用同一个表达式覆盖，调用方不需要判断分支。
// 候选不存在（请求根本没走到路由就失败）时回落到请求名，让失败日志
// 仍有一个能看懂的名字。
func BillingModel(req *RelayRequest, res *RelayResult) string {
	if res != nil && res.Candidate.Binding.PublicName != "" {
		return res.Candidate.Binding.PublicName
	}
	if req != nil {
		return req.PublicModel
	}
	return ""
}

// BuildLog 由转发结果组装一条日志记录。
func BuildLog(req *RelayRequest, res *RelayResult, usage Usage, status int, errMsg string,
	firstByteMs, totalMs int,
) model.RequestLog {
	entry := model.RequestLog{
		TraceID:        req.TraceID,
		APIKeyID:       req.APIKeyID,
		APIKeyName:     req.APIKeyName,
		InboundProto:   req.InboundProto,
		ModelRequested: req.PublicModel,
		ThinkingLevel:  req.ThinkingLevel,
		IsStream:       req.Stream,
		StatusCode:     status,
		// 兜底截断：errMsg 的来源不止一处（上游错误体、无渠道提示拼接的
		// 可用模型列表……），在这里统一压进 varchar(1024)，谁超长都丢不了日志
		Error:               TruncateRunes(errMsg, 1000),
		PromptTokens:        usage.PromptTokens,
		CompletionTokens:    usage.CompletionTokens,
		TotalTokens:         usage.TotalTokens,
		CachedTokens:        usage.CachedTokens,
		CacheCreationTokens: usage.CacheCreationTokens,
		ReasoningTokens:     usage.ReasoningTokens,
		UsageEstimated:      usage.Estimated,
		FirstByteMs:         firstByteMs,
		TotalMs:             totalMs,
		ClientIP:            req.ClientIP,
		CreatedAt:           time.Now().UTC(),
	}
	if res != nil {
		entry.RetryCount = res.Retries
		// 故障转移链路的逐次尝试随日志快照：失败尝试的 token 上游可能照收，
		// 详情里看得见它，账面对不上上游账单时才有线索
		if len(res.Trail) > 0 {
			entry.RetryTrail = trailToJSONMap(res.Trail)
		}
		if res.Attempt != nil {
			entry.UpstreamProto = res.Candidate.Channel.Protocol
			entry.ModelUpstream = res.Candidate.Binding.UpstreamName
			entry.UpstreamMs = res.Attempt.HeaderMs
			// 兜底标记只在真的发出去过（Attempt 非空）时才为真：
			// Attempt 为空说明连候选都没走到，那条路径上的 Fallback 无意义，
			// 标成兜底反而会让人以为「兜底生效了但失败了」。
			entry.FallbackMapped = res.Candidate.Fallback
		}
		if res.Candidate.Channel.ID != 0 {
			entry.ChannelID = res.Candidate.Channel.ID
			entry.ChannelName = res.Candidate.Channel.Name
			entry.GroupID = res.Candidate.Channel.GroupID
		}
	}
	return entry
}
