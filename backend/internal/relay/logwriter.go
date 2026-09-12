package relay

import (
	"log/slog"
	"time"

	"gorm.io/gorm"

	"llm-relay/internal/model"
)

// LogWriter 负责把调用明细异步落库，避免统计拖慢转发。
type LogWriter struct {
	db     *gorm.DB
	queue  chan model.RequestLog
	logger *slog.Logger
}

// NewLogWriter 启动后台写入协程。
func NewLogWriter(db *gorm.DB, buffer int, logger *slog.Logger) *LogWriter {
	if buffer <= 0 {
		buffer = 1024
	}
	w := &LogWriter{db: db, queue: make(chan model.RequestLog, buffer), logger: logger}
	go w.loop()
	return w
}

// Enqueue 投递一条日志。队列满时丢弃并告警，绝不阻塞转发链路。
func (w *LogWriter) Enqueue(entry model.RequestLog) {
	select {
	case w.queue <- entry:
	default:
		if w.logger != nil {
			w.logger.Warn("日志队列已满，丢弃一条统计", "trace_id", entry.TraceID)
		}
	}
}

func (w *LogWriter) loop() {
	for entry := range w.queue {
		if err := w.db.Create(&entry).Error; err != nil && w.logger != nil {
			w.logger.Warn("写入请求日志失败", "trace_id", entry.TraceID, "err", err)
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
			entry.ProviderID = res.Candidate.Channel.ProviderID
		}
	}
	return entry
}
