package relay

import (
	"log/slog"
	"sync"
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
	w := &LogWriter{db: db, queue: make(chan queuedLog, buffer), logger: logger, done: make(chan struct{})}
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
	if w.logger != nil {
		w.logger.Warn("丢弃一条统计", "why", why, "trace_id", entry.TraceID)
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

// BuildLog 由转发结果组装一条日志记录。
func BuildLog(req *RelayRequest, res *RelayResult, usage Usage, status int, errMsg string,
	firstByteMs, totalMs int,
) model.RequestLog {
	entry := model.RequestLog{
		TraceID:             req.TraceID,
		APIKeyID:            req.APIKeyID,
		APIKeyName:          req.APIKeyName,
		InboundProto:        req.InboundProto,
		ModelRequested:      req.PublicModel,
		IsStream:            req.Stream,
		StatusCode:          status,
		Error:               errMsg,
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
		if res.Attempt != nil {
			entry.UpstreamProto = res.Candidate.Channel.Protocol
			entry.ModelUpstream = res.Candidate.Binding.UpstreamName
			entry.UpstreamMs = res.Attempt.HeaderMs
		}
		if res.Candidate.Channel.ID != 0 {
			entry.ChannelID = res.Candidate.Channel.ID
			entry.ChannelName = res.Candidate.Channel.Name
			entry.GroupID = res.Candidate.Channel.GroupID
		}
	}
	return entry
}
