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
	// 分组 TPM 记账：只认上游回报的实际用量（usage.Estimated 为真时是按报文长度
	// 估的，也一并计入 —— 估出来的值同样代表消耗，不计反而会让限制失效）
	if s.deps.GroupLimit != nil && entry.GroupID > 0 {
		s.deps.GroupLimit.AddTokens(entry.GroupID, usage.BillableTokens(), time.Now())
	}
	// 价格是「渠道 × 模型」维度的：同一个模型名在不同渠道成本不同，
	// 所以查价必须带上这次请求真正走的那条渠道（entry.ChannelID）。
	//
	// 模型名用 BillingModel 而不是 entry.ModelRequested：兜底请求的
	// 请求名（客户端发的 claude-opus-4-7）在渠道里根本没配价，按它查会
	// 得到「没配价、记 0 元」；要按候选实际指向的那条白名单行定价。
	// 精确命中时两者相等，是等价替换。
	priceModel := relay.BillingModel(req, res)
	if s.deps.Pricing != nil && priceModel != "" && entry.ChannelID > 0 {
		// 必须用**服务器本地时间**：时段倍率（如工作日 9:00-12:00 双倍）
		// 是按用户看到的钟点填的，传 UTC 会让窗口整体偏 8 小时
		at := time.Now()
		// 币种与「配没配价」无关，先按渠道记下来：没配价的调用金额是 0，
		// 但它仍然属于某条渠道的账，币种留空只能靠人猜
		entry.CostCurrency = s.deps.Pricing.CurrencyOf(context.Background(), entry.ChannelID)
		if p, ok := s.deps.Pricing.Resolve(context.Background(), entry.ChannelID, priceModel, at); ok {
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
