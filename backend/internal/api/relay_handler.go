package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/model"
	"llm-relay/internal/relay"
	"llm-relay/internal/relay/convert"
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
	s.relayRequest(c, profileOpenAIChat, "", false)
}

// geminiGenerateContent 是 Gemini 入口。路径形如 /v1beta/models/{model}:generateContent，
// 模型名与流式标记都在 URL 里，请求体中没有。
func (s *Server) geminiGenerateContent(c *gin.Context) {
	action := strings.TrimPrefix(c.Param("action"), "/")
	idx := strings.LastIndex(action, ":")
	if idx <= 0 || idx == len(action)-1 {
		profileGemini.writeError(c, http.StatusBadRequest,
			"路径格式应为 /v1beta/models/{model}:generateContent 或 :streamGenerateContent", "invalid_request_error")
		return
	}
	model := action[:idx]
	method := action[idx+1:]

	stream := false
	switch method {
	case "generateContent":
	case "streamGenerateContent":
		stream = true
	default:
		profileGemini.writeError(c, http.StatusNotImplemented, "暂不支持的 Gemini 方法: "+method, "invalid_request_error")
		return
	}
	s.relayRequest(c, profileGemini, model, stream)
}

// responses 是 OpenAI Responses 入口。
func (s *Server) responses(c *gin.Context) {
	s.relayRequest(c, profileOpenAIResponses, "", false)
}

// anthropicMessages 是 Anthropic Messages 入口，供 Claude Code 等客户端使用。
func (s *Server) anthropicMessages(c *gin.Context) {
	s.relayRequest(c, profileAnthropic, "", false)
}

// embeddings 是向量化入口，目前与上游同协议透传。
func (s *Server) embeddings(c *gin.Context) {
	s.relayRequest(c, profileEmbeddings, "", false)
}

// gateWaitTimeout 是全局并发闸门的最长排队时间。
// 超过它说明本地确实过载了，此时快速失败比让客户端无限等待更有用。
const gateWaitTimeout = 60 * time.Second

// relayRequest 是转发主流程：读取入参 -> 编排转发 -> 回写响应 -> 异步落库。
// pathModel/pathStream 供模型名写在 URL 里的协议（Gemini）使用，其余协议传空。
func (s *Server) relayRequest(c *gin.Context, p *inboundProfile, pathModel string, pathStream bool) {
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
	body, err := p.translateBody(rawBody, pathModel, pathStream)
	if err != nil {
		p.writeError(c, http.StatusBadRequest, "请求体转换失败: "+err.Error(), "invalid_request_error")
		return
	}

	publicModel := relay.ExtractModel(body)
	if publicModel == "" && pathModel != "" {
		publicModel = pathModel
	}
	if publicModel == "" {
		p.writeError(c, http.StatusBadRequest, "请求体缺少 model 字段", "invalid_request_error")
		return
	}

	// 密钥白名单在这里落地。
	//
	// allowed_models / allowed_groups 此前只有存储与回显：创建与更新会写库，
	// 列表接口会显示，但路由侧完全不看。也就是说界面上配了限制却毫无效果 ——
	// 属于「看起来有访问控制、实际没有」，比没有这个字段更危险。
	var allowedGroups []uint
	if key != nil {
		if !modelAllowed(key.AllowedModels, publicModel) {
			p.writeError(c, http.StatusForbidden,
				"该密钥不允许调用模型 "+publicModel, "permission_error")
			return
		}
		groups, err := s.resolveGroupWhitelist(key.AllowedGroups)
		if err != nil {
			// 白名单解析不出来就不放行：否则删掉那个分组就能绕过限制
			p.writeError(c, http.StatusForbidden, err.Error(), "permission_error")
			return
		}
		allowedGroups = groups
	}

	req := &relay.RelayRequest{
		TraceID:       traceID,
		InboundProto:  p.Name,
		UpstreamPath:  p.UpstreamPath,
		PublicModel:   publicModel,
		Body:          body,
		InboundBody:   rawBody,
		Headers:       c.Request.Header,
		ClientIP:      c.ClientIP(),
		Stream:        relay.ExtractStream(body),
		AllowedGroups: allowedGroups,
	}
	if key != nil {
		req.APIKeyID = key.ID
		req.APIKeyName = key.Name
	}

	// 全局并发闸门：排队等待，而不是把压力一股脑推给上游。
	// 客户端断开时排队立即结束，不会留下空转的等待者。
	gateCtx, cancelGate := context.WithTimeout(c.Request.Context(), gateWaitTimeout)
	defer cancelGate()
	if err := s.deps.Gate.Acquire(gateCtx); err != nil {
		if c.Request.Context().Err() != nil {
			return // 客户端已断开
		}
		p.writeError(c, http.StatusTooManyRequests,
			"本地并发已达上限（"+strconv.Itoa(s.deps.Gate.Max())+"），请稍后重试", "rate_limit_error")
		return
	}
	defer s.deps.Gate.Release()

	res, relayErr := s.deps.Service.Relay(c.Request.Context(), req)
	// 渠道并发名额持有到本次响应彻底转发结束（含流式读取），
	// 否则流式请求只覆盖到「拿到响应头」那一瞬间，并发上限形同虚设。
	// ReleaseSlot 内部判空且幂等，失败路径上的 res 不会重复归还。
	defer res.ReleaseSlot()
	if relayErr != nil {
		totalMs := int(time.Since(started).Milliseconds())
		p.writeError(c, http.StatusBadGateway, relayErr.Error(), "upstream_error")
		s.finalizeLog(req, res, relay.Usage{}, http.StatusBadGateway, relayErr.Error(), 0, totalMs, nil, nil)
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
			upMsg, att.HeaderMs, int(time.Since(started).Milliseconds()), att.Body, att.Headers)
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
		att.HeaderMs, int(time.Since(started).Milliseconds()), att.Body, att.Headers)
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

	// 上游中断、以及客户端主动断开，是两种不同的收尾，不能混为一谈
	var streamErr error
	clientGone := false

	// 报文留存用：只留前若干字节，长流不能无限攒在内存里
	captureLimit := s.deps.Config.Relay.PayloadMaxKB << 10
	if captureLimit <= 0 {
		captureLimit = relay.DefaultPayloadMaxBytes
	}
	var streamCapture []byte

	for {
		n, readErr := att.Stream.Read(buf)
		if n > 0 {
			if firstByteMs == 0 {
				firstByteMs = int(time.Since(att.StartedAt).Milliseconds())
			}
			_, _ = tee.Write(buf[:n])
			// 留存模式下才捕获，none 模式不做任何额外拷贝
			if relay.ShouldStorePayload(s.deps.Config.Relay.PayloadStorageMode, att.StatusCode) &&
				len(streamCapture) < captureLimit {
				room := captureLimit - len(streamCapture)
				if room > n {
					room = n
				}
				streamCapture = append(streamCapture, buf[:room]...)
			}
			if _, writeErr := tr.Write(buf[:n]); writeErr != nil {
				// 写不出去基本是客户端断开，不是上游的问题，不必再补错误事件
				clientGone = true
				break
			}
			c.Writer.Flush()
		}
		if readErr != nil {
			// io.EOF 是上游正常收尾（例如已发出 [DONE]）；
			// 其它错误说明连接在响应完成前断了，必须区分对待
			if readErr != io.EOF {
				streamErr = readErr
			}
			break
		}
	}

	// 上游中断时不能补发「正常结束」标记：客户端会把截断的回复当成完整回复，
	// 用户看到的是「模型答到一半停了」而系统显示成功。
	// 改为下发错误型终止事件，既结束等待又明确说明不完整。
	switch {
	case clientGone:
		// 客户端已断开，写什么都是白费
	case streamErr != nil:
		reason := convert.StreamAbortedMessage
		slog.Default().Warn("上游流中断，下发错误终止事件",
			"trace_id", req.TraceID, "err", streamErr)
		if ab, ok := tr.(convert.Aborter); ok {
			if err := ab.Abort(reason); err != nil {
				slog.Default().Warn("错误终止事件写出失败", "trace_id", req.TraceID, "err", err)
			}
		}
	default:
		if err := tr.Close(); err != nil {
			slog.Default().Warn("流式改写收尾失败", "trace_id", req.TraceID, "err", err)
		}
	}

	tee.Flush()
	usage, hasUsage := tee.Usage()
	if !hasUsage {
		// SSE 帧带 JSON 包装，按 1/4 折算正文字符数后再估算
		usage = relay.EstimateUsage(len(req.Body), tee.Bytes()/4)
	}

	// 截断记进日志：状态码按失败记，否则仪表盘上看不出这次请求有问题
	logStatus, logErr := att.StatusCode, ""
	if streamErr != nil && !clientGone {
		logStatus = http.StatusBadGateway
		logErr = "上游流中断: " + streamErr.Error()
	}
	s.finalizeLog(req, res, usage, logStatus, logErr,
		firstByteMs, int(time.Since(started).Milliseconds()), streamCapture, att.Headers)
}
