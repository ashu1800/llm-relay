package relay

import (
	"log/slog"
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
}

// NewLogWriter 启动后台写入协程。
func NewLogWriter(db *gorm.DB, buffer int, logger *slog.Logger) *LogWriter {
	if buffer <= 0 {
		buffer = 1024
	}
	w := &LogWriter{db: db, queue: make(chan queuedLog, buffer), logger: logger}
	go w.loop()
	return w
}

// Enqueue 投递一条日志。payload 为 nil 表示当前模式不留存报文。
// 队列满时丢弃并告警，绝不阻塞转发链路。
func (w *LogWriter) Enqueue(entry model.RequestLog, payload *model.RequestPayload) {
	select {
	case w.queue <- queuedLog{Log: entry, Payload: payload}:
	default:
		if w.logger != nil {
			w.logger.Warn("日志队列已满，丢弃一条统计", "trace_id", entry.TraceID)
		}
	}
}

func (w *LogWriter) loop() {
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

// Close 停止写入协程。
func (w *LogWriter) Close() { close(w.queue) }

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
