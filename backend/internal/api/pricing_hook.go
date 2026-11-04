package api

import (
	"context"
	"net/http"
	"time"

	"llm-relay/internal/model"
	"llm-relay/internal/pricing"
	"llm-relay/internal/relay"
)

// finalizeLog 在入队前补齐定价信息。
// 定价快照会随日志一起落库，保证日后同步价格时历史账目不会漂移。
// respBody/respHeader 为空表示本次没有可留存的响应（例如上游未返回就失败）。
func (s *Server) finalizeLog(req *relay.RelayRequest, res *relay.RelayResult, usage relay.Usage,
	status int, errMsg string, tm relay.Timing,
	respBody []byte, respHeader http.Header,
) {
	entry := relay.BuildLog(req, res, usage, status, errMsg, tm)
	// 缓存命中：不打上游，token 与金额如实为 0，快照只标「cached」。
	// 下面的定价块要求 ChannelID > 0，命中请求天然跳过 —— 这里补上
	// 标记快照，让详情页能说出「这笔 0 元是缓存给的」。
	if req.CacheHit {
		entry.PricingSnapshot = model.JSONMap{"cached": true}
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
			// 直通端点（图像/音频）按非 token 维度计费：金额来自
			// 张数/字符数/秒数 × 对应单价，口径写进日志的 usage_kind
			// 与快照的 billing_kind。判定与取数在 direct_billing.go
			//（纯函数，见那里的注释）。
			kind := ""
			if req.Direct {
				kind = directBillingKind(req.InboundProto)
			}
			if kind == "" {
				entry.EstimatedCost = p.Cost(usage)
				entry.PricingSnapshot = p.Snapshot(at)
			} else {
				cost, snapKind := directCost(kind, req.Body, directRespBody(res), usage, p)
				entry.EstimatedCost = cost
				entry.UsageKind = snapKind
				// 非 token 口径时把口径带进快照（token 口径保持缺省，
				// 与历史日志的快照形状一致）
				if snapKind != pricing.BillingKindToken {
					p.Kind = snapKind
				}
				entry.PricingSnapshot = p.Snapshot(at)
			}
		}
	}
	// 报文留存按模式决定：all 全留、errors 只留出错、none 不留
	maxBytes := s.deps.Config.Relay.PayloadMaxKB << 10
	payload := relay.BuildPayload(s.deps.Config.Relay.PayloadStorageMode, status,
		req.InboundBody, respBody, req.Headers, respHeader, maxBytes)
	s.deps.Logs.Enqueue(entry, payload)
}
