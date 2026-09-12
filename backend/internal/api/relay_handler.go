package api

import (
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/model"
	"llm-relay/internal/relay"
)

// listModels 返回已启用的对外模型，供客户端做模型探测。
func (s *Server) listModels(c *gin.Context) {
	var models []model.Model
	if err := s.deps.Store.DB().Where("enabled = ?", true).
		Order("public_name").Find(&models).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	data := make([]gin.H, 0, len(models))
	for _, m := range models {
		data = append(data, gin.H{
			"id":       m.PublicName,
			"object":   "model",
			"created":  m.CreatedAt.Unix(),
			"owned_by": "llm-relay",
		})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

// chatCompletions 是 OpenAI 兼容的对话入口。
func (s *Server) chatCompletions(c *gin.Context) {
	s.relayRequest(c, profileOpenAIChat)
}

// anthropicMessages 是 Anthropic Messages 入口，供 Claude Code 等客户端使用。
func (s *Server) anthropicMessages(c *gin.Context) {
	s.relayRequest(c, profileAnthropic)
}

// embeddings 是向量化入口，目前与上游同协议透传。
func (s *Server) embeddings(c *gin.Context) {
	s.relayRequest(c, profileEmbeddings)
}

// relayRequest 是转发主流程：读取入参 -> 编排转发 -> 回写响应 -> 异步落库。
func (s *Server) relayRequest(c *gin.Context, p *inboundProfile) {
	started := time.Now()
	traceID := relay.NewTraceID()
	key := apiKeyFromContext(c)

	limitBytes := int64(s.deps.Config.Relay.MaxRequestBodyMB) << 20
	if limitBytes <= 0 {
		limitBytes = 64 << 20
	}
	rawBody, err := io.ReadAll(io.LimitReader(c.Request.Body, limitBytes+1))
	if err != nil {
		p.writeError(c, http.StatusBadRequest, "读取请求体失败: "+err.Error(), "invalid_request_error")
		return
	}
	if int64(len(rawBody)) > limitBytes {
		p.writeError(c, http.StatusRequestEntityTooLarge, "请求体超出限制", "invalid_request_error")
		return
	}

	// 先把入站载荷归一化成 OpenAI Chat，再交给上游
	body, err := p.translateBody(rawBody)
	if err != nil {
		p.writeError(c, http.StatusBadRequest, "请求体转换失败: "+err.Error(), "invalid_request_error")
		return
	}

	publicModel := relay.ExtractModel(body)
	if publicModel == "" {
		p.writeError(c, http.StatusBadRequest, "请求体缺少 model 字段", "invalid_request_error")
		return
	}

	req := &relay.RelayRequest{
		TraceID:      traceID,
		InboundProto: p.Name,
		UpstreamPath: p.UpstreamPath,
		PublicModel:  publicModel,
		Body:         body,
		Headers:      c.Request.Header,
		ClientIP:     c.ClientIP(),
		Stream:       relay.ExtractStream(body),
	}
	if key != nil {
		req.APIKeyID = key.ID
		req.APIKeyName = key.Name
	}

	res, relayErr := s.deps.Service.Relay(c.Request.Context(), req)
	if relayErr != nil {
		totalMs := int(time.Since(started).Milliseconds())
		p.writeError(c, http.StatusBadGateway, relayErr.Error(), "upstream_error")
		s.finalizeLog(req, res, relay.Usage{}, http.StatusBadGateway, relayErr.Error(), 0, totalMs)
		return
	}

	att := res.Attempt
	if att == nil {
		p.writeError(c, http.StatusBadGateway, "上游未返回响应", "upstream_error")
		return
	}

	// 上游返回不可重试的错误状态：按入站协议的错误结构回给客户端
	if att.Stream == nil && att.StatusCode >= 400 {
		upMsg := string(att.Body)
		p.writeError(c, att.StatusCode, upMsg, "upstream_error")
		s.finalizeLog(req, res, relay.Usage{}, att.StatusCode,
			upMsg, att.HeaderMs, int(time.Since(started).Milliseconds()))
		return
	}

	if att.Stream != nil {
		s.streamToClient(c, p, req, res, att, started)
		return
	}

	// 非流式成功：经改写器输出，透传协议下即原样写出
	copyHeader(c.Writer.Header(), att.Headers)
	c.Writer.Header().Set("Content-Type", p.ContentType)
	c.Status(att.StatusCode)
	tr := p.translator(c.Writer, publicModel, false)
	if _, err := tr.Write(att.Body); err != nil {
		slog.Default().Warn("响应改写失败", "trace_id", traceID, "err", err)
	}
	if err := tr.Close(); err != nil {
		slog.Default().Warn("响应改写收尾失败", "trace_id", traceID, "err", err)
	}
	// 上游未回传 usage 时按报文长度兜底估算，避免统计全为零
	usage := att.Usage
	if !att.HasUsage {
		usage = relay.EstimateUsage(len(rawBody), len(att.Body))
	}
	s.finalizeLog(req, res, usage, att.StatusCode, "",
		att.HeaderMs, int(time.Since(started).Milliseconds()))
}

// streamToClient 边转发边旁路抓取用量。
// 首包时间以真正写出第一个字节为准，而非上游响应头到达时间。
func (s *Server) streamToClient(c *gin.Context, p *inboundProfile, req *relay.RelayRequest, res *relay.RelayResult, att *relay.Attempt, started time.Time) {
	defer att.Stream.Close()

	h := c.Writer.Header()
	copyHeader(h, att.Headers)
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")

	c.Status(att.StatusCode)
	c.Writer.Flush()

	// 协议不同时逐事件改写；相同时是零开销透传
	tr := p.translator(c.Writer, req.PublicModel, true)

	tee := relay.NewUsageTee(nil)
	buf := make([]byte, 32*1024)
	firstByteMs := 0

	for {
		n, readErr := att.Stream.Read(buf)
		if n > 0 {
			if firstByteMs == 0 {
				firstByteMs = int(time.Since(att.StartedAt).Milliseconds())
			}
			_, _ = tee.Write(buf[:n])
			if _, writeErr := tr.Write(buf[:n]); writeErr != nil {
				break
			}
			c.Writer.Flush()
		}
		if readErr != nil {
			break
		}
	}

	if err := tr.Close(); err != nil {
		slog.Default().Warn("流式改写收尾失败", "trace_id", req.TraceID, "err", err)
	}
	tee.Flush()
	usage, hasUsage := tee.Usage()
	if !hasUsage {
		// SSE 帧带 JSON 包装，按 1/4 折算正文字符数后再估算
		usage = relay.EstimateUsage(len(req.Body), tee.Bytes()/4)
	}
	s.finalizeLog(req, res, usage, att.StatusCode, "",
		firstByteMs, int(time.Since(started).Milliseconds()))
}
