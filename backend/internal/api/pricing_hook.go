package api

import (
	"context"
	"net/http"
	"time"

	"llm-relay/internal/relay"
)

// finalizeLog 在入队前补齐定价信息。
// 定价快照会随日志一起落库，保证日后同步价格时历史账目不会漂移。
// respBody/respHeader 为空表示本次没有可留存的响应（例如上游未返回就失败）。
func (s *Server) finalizeLog(req *relay.RelayRequest, res *relay.RelayResult, usage relay.Usage,
	status int, errMsg string, firstByteMs, totalMs int,
	respBody []byte, respHeader http.Header,
) {
	entry := relay.BuildLog(req, res, usage, status, errMsg, firstByteMs, totalMs)
	if s.deps.Pricing != nil && entry.ModelRequested != "" {
		// 必须用**服务器本地时间**：时段倍率（如工作日 9:00-12:00 双倍）
		// 是按用户看到的钟点填的，传 UTC 会让窗口整体偏 8 小时
		at := time.Now()
		if p, ok := s.deps.Pricing.Resolve(context.Background(), entry.ModelRequested, at); ok {
			entry.EstimatedCost = p.Cost(usage)
			entry.PricingSnapshot = p.Snapshot(at)
		}
	}
	// 报文留存按模式决定：all 全留、errors 只留出错、none 不留
	maxBytes := s.deps.Config.Relay.PayloadMaxKB << 10
	payload := relay.BuildPayload(s.deps.Config.Relay.PayloadStorageMode, status,
		req.InboundBody, respBody, req.Headers, respHeader, maxBytes)
	s.deps.Logs.Enqueue(entry, payload)
}
