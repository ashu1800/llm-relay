package api

import (
	"context"
	"time"

	"llm-relay/internal/relay"
)

// finalizeLog 在入队前补齐定价信息。
// 定价快照会随日志一起落库，保证日后同步价格时历史账目不会漂移。
func (s *Server) finalizeLog(req *relay.RelayRequest, res *relay.RelayResult, usage relay.Usage,
	status int, errMsg string, firstByteMs, totalMs int,
) {
	entry := relay.BuildLog(req, res, usage, status, errMsg, firstByteMs, totalMs)
	if s.deps.Pricing != nil && entry.ModelRequested != "" {
		at := time.Now().UTC()
		if p, ok := s.deps.Pricing.Resolve(context.Background(), entry.ModelRequested, at); ok {
			entry.EstimatedCost = p.Cost(usage)
			entry.PricingSnapshot = p.Snapshot(at)
		}
	}
	s.deps.Logs.Enqueue(entry)
}
