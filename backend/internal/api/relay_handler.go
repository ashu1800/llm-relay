package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/model"
	"llm-relay/internal/relay"
	relaycache "llm-relay/internal/relay/cache"
	"llm-relay/internal/relay/convert"
)

// listModels 返回当前密钥能调用的对外模型，供客户端做模型探测。
//
// 模型目录不单独维护：它就是「启用渠道的启用白名单条目」的并集，
// 与路由用的是同一份数据 —— 列表里有、请求却调不通（或反过来）是不可能出现的。
// 密钥配了模型路由时只列「路由里至少有一条渠道仍能跑」的模型，否则客户端会看到
// 一堆自己调不了的模型（渠道被删/停用后路由条目失效的那部分要剔除）。
// 模型白名单同理：配了 allowed_models 就只列其中的 —— 客户端（Claude Code、
// Cursor 都靠这个接口探测）看到的仍是可调用全集，选中才不会 403。
//
// 每个模型额外带 supported_efforts：这个模型能接受的思考档位。同名模型可能挂在
// 多条渠道上，取并集（见 relay.MergeModelEfforts）—— 请求最终只落在一条渠道上。
func (s *Server) listModels(c *gin.Context) {
	key := apiKeyFromContext(c)

	// 先解析密钥路由：确定「哪些模型可服务」。路由为空的密钥（理论上创建
	// 路径已拦，老数据兜底）按不限处理，与转发侧 CandidateQuery 空列表同一语义。
	// 解析一次给两处用（servable 与下面的 scope 收窄）—— jsonb 解析虽轻，
	// 每个 /v1/models 请求做两遍纯属浪费。
	routes := KeyRoutesOf(key)
	servable := map[string]bool{}
	if key != nil {
		for name, channels := range routes {
			if len(channels) == 0 {
				continue
			}
			servable[name] = true
		}
	}

	// 查行而不是只查名字：supported_efforts 与渠道协议都要用来推导档位。
	// 同一个模型名可能有多行，所以不能 DISTINCT，去重放到内存里做。
	q := s.deps.Store.DB().Table("channel_models").
		Select("channel_models.public_name, channel_models.supported_efforts, channels.protocol").
		Joins("JOIN channels ON channels.id = channel_models.channel_id AND channels.enabled = true").
		Where("channel_models.enabled = true")
	if key != nil {
		ids := KeyChannelScope(routes)
		if len(ids) == 0 {
			// 路由全部失效：没有任何渠道可服务，直接回空列表。
			// 不返回错误 —— 客户端拿到空模型列表才知道配置有问题。
			c.JSON(http.StatusOK, gin.H{"object": "list", "data": []gin.H{}})
			return
		}
		q = q.Where("channels.id IN ?", ids)
	}

	type modelRow struct {
		PublicName       string           `gorm:"column:public_name"`
		SupportedEfforts model.StringList `gorm:"column:supported_efforts"`
		Protocol         string           `gorm:"column:protocol"`
	}
	var rows []modelRow
	if err := q.Order("channel_models.public_name").Scan(&rows).Error; err != nil {
		// 这个接口是 /v1/models，客户端是任意 OpenAI 兼容工具。
		// 同样不能把原始 DB 错误回显出去（表名、列名、列值都在里面）。
		writeInternalError(c, err)
		return
	}

	// 按对外名归并：顺序沿用查询的 public_name 升序（第一次出现的顺序），
	// 档位取并集。名称白名单与路由的过滤都在内存里做，判据必须与拦截
	// 请求的 modelAllowed / 路由解析完全同一份（两处各写一个查询条件迟早漂移成
	// 「列表里有、一调就被拒」）。
	order := make([]string, 0, len(rows))
	perName := make(map[string][][]string, len(rows))
	for _, r := range rows {
		if key != nil {
			if len(key.AllowedModels) > 0 && !modelAllowed(key.AllowedModels, r.PublicName) {
				continue
			}
			// 路由里没有这个模型（或它的候选渠道已全部失效）就不列：
			// 列出来只会让客户端选中后被 403 / 503。
			if !servable[r.PublicName] {
				continue
			}
		}
		if _, seen := perName[r.PublicName]; !seen {
			order = append(order, r.PublicName)
		}
		perName[r.PublicName] = append(perName[r.PublicName],
			relay.SupportedEfforts(r.SupportedEfforts, r.Protocol))
	}

	now := time.Now().Unix()
	data := make([]gin.H, 0, len(order))
	for _, name := range order {
		efforts := relay.MergeModelEfforts(perName[name])
		if efforts == nil {
			// JSON 里给空数组而不是 null：客户端（SDK）遍历它时不必先判空指针
			efforts = []string{}
		}
		data = append(data, gin.H{
			"id":                name,
			"object":            "model",
			"created":           now,
			"owned_by":          "llm-relay",
			"supported_efforts": efforts,
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

// ---- 直通端点（2026-11 P2）：图像 / 音频 ----
//
// 与对话端点走同一条 relayRequest 主流程（白名单、密钥路由、故障转移、
// 并发闸门、日志、计费全部复用），差异只有两点：请求体不做归一化（Direct），
// 模型名可能要从 multipart 表单字段里提取（ExtractModel）。

// imagesGenerations 是 OpenAI 图像生成入口（文生图）。
func (s *Server) imagesGenerations(c *gin.Context) {
	s.relayRequest(c, profileImageGenerations, "", false)
}

// audioSpeech 是 OpenAI 语音合成入口（TTS，响应是音频二进制）。
func (s *Server) audioSpeech(c *gin.Context) {
	s.relayRequest(c, profileAudioSpeech, "", false)
}

// audioTranscriptions 是 OpenAI 语音转写入口（multipart，含音频文件）。
func (s *Server) audioTranscriptions(c *gin.Context) {
	s.relayRequest(c, profileAudioTranscriptions, "", false)
}

// audioTranslations 是 OpenAI 语音翻译入口（multipart，同转写形状）。
func (s *Server) audioTranslations(c *gin.Context) {
	s.relayRequest(c, profileAudioTranslations, "", false)
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

	// 先把入站载荷归一化成 OpenAI Chat，再交给上游。
	// 直通端点（Direct）不做归一化：body 就是原始报文，原样转发。
	body, err := p.translateBody(rawBody, pathModel, pathStream)
	if err != nil {
		p.writeError(c, http.StatusBadRequest, "请求体转换失败: "+err.Error(), "invalid_request_error")
		return
	}

	// 模型名：默认按 JSON 的 "model" 字段读；multipart 端点走 profile 的
	// 提取器（表单字段）。二者都以原始报文为准 —— 归一化不会动模型名，
	// 但 multipart 报文根本不是 JSON，relay.ExtractModel 对它必然返回空。
	publicModel := ""
	if p.ExtractModel != nil {
		publicModel, err = p.ExtractModel(rawBody, c.Request.Header.Get("Content-Type"))
		if err != nil {
			p.writeError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
			return
		}
	} else {
		publicModel = relay.ExtractModel(body)
	}
	if publicModel == "" && pathModel != "" {
		publicModel = pathModel
	}
	if publicModel == "" {
		p.writeError(c, http.StatusBadRequest, "请求体缺少 model 字段", "invalid_request_error")
		return
	}

	// 密钥白名单在这里落地。
	//
	// allowed_models / model_channels 此前只有存储与回显：创建与更新会写库，
	// 列表接口会显示，但路由侧完全不看。也就是说界面上配了限制却毫无效果 ——
	// 属于「看起来有访问控制、实际没有」，比没有这个字段更危险。
	if key != nil && !modelAllowed(key.AllowedModels, publicModel) {
		p.writeError(c, http.StatusForbidden,
			"该密钥不允许调用模型 "+publicModel, "permission_error")
		return
	}
	// 该密钥为此模型配置的候选渠道（有序）。
	//
	// **空列表不能交给转发层**：router 对空 AllowedChannels 的语义是「不按
	// 密钥限制」（探测等无密钥路径复用），直接放行会把转发范围放宽到
	// 全库能跑该模型的渠道 —— 备份导入把映射不到的条目置空（admin_backup）
	// 或手工改库都能造出空列表，那是真实的静默权限放宽，不是理论问题。
	// 正常保存路径有 normalizeKeyRoutes 拦空列表，这里是最后一道闸。
	routes := KeyRoutesOf(key)
	if key != nil && len(routes[publicModel]) == 0 {
		p.writeError(c, http.StatusBadGateway,
			fmt.Sprintf("模型 %s 没有可用的候选渠道：这条密钥的路由里没有该模型（或条目已失效），请到「密钥管理」修改渠道路由", publicModel),
			"upstream_error")
		return
	}
	allowedChannels := routes[publicModel]

	// 客户端识别：从入站头认出「这次是谁在调」，随日志落一份快照。
	// 与 ThinkingLevel 同一处采集 —— relay 包不能反向 import api，
	// 而 handler 已经拿着 *gin.Context，是最便宜的采集点。
	clientID, clientName, clientUA := relay.ExtractClient(c.Request.Header)

	req := &relay.RelayRequest{
		TraceID:          traceID,
		InboundProto:     p.Name,
		UpstreamPath:     p.UpstreamPath,
		PublicModel:      publicModel,
		Body:             body,
		InboundBody:      rawBody,
		Headers:          c.Request.Header,
		ClientIP:         c.ClientIP(),
		ClientID:         clientID,
		ClientName:       clientName,
		ClientUA:         clientUA,
		Stream:           relay.ExtractStream(body),
		ThinkingLevel:    relay.ExtractThinkingLevel(p.Name, rawBody),
		AllowedChannels:  allowedChannels,
		RequireProtocols: nil,
		Direct:           p.Direct,
	}
	if p.Direct {
		// 直通端点只允许 OpenAI 兼容协议渠道：Anthropic/Gemini 协议的
		// 转换对图像/音频请求毫无意义，在路由层就排除（见 router.go）
		req.RequireProtocols = directChannelProtocols
	}
	if key != nil {
		req.APIKeyID = key.ID
		req.APIKeyName = key.Name
	}

	// ---- 响应缓存（2026-11 P3）----
	//
	// 查询点在并发闸门**之前**：命中不打上游，占着闸门名额纯属浪费。
	// 请求计入 RPM 的事实发生在更早的鉴权中间件里（requireAPIKey）——
	// 命中也计 RPM 是刻意的：限流保护的是客户端行为，不是上游。
	//
	// 生效条件是四层合取：全局开了（Cache 非 nil）、端点可缓存（对话族）、
	// 密钥勾了「参与缓存」、客户端没有 X-Relay-Cache: no-cache。
	// 任何一层不满足都当作缓存不存在，行为与本功能引入前完全一致。
	cacheKey := ""
	cacheStore := false
	if s.deps.Cache != nil && profileCacheable(p) && key != nil && key.CacheEnabled &&
		!strings.EqualFold(strings.TrimSpace(c.Request.Header.Get("X-Relay-Cache")), "no-cache") {
		cacheKey = cacheKeyOf(p, publicModel, body, req.Stream)
		if entry, ok := s.deps.Cache.Get(cacheKey); ok && entry.Stream == req.Stream {
			s.serveCacheHit(c, p, req, entry, started)
			return
		}
		// 只有「缓存可用且本次未命中」的请求才会在成功后回填缓存
		cacheStore = true
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
		// 候选链全部失败：最后一次上游应答还在 res.Attempt 里，
		// 按它的真实状态码与错误体回给客户端，而不是一律 502 ——
		// 所有渠道都 401 时客户端看到的应该是 401，那才是值得排查的方向。
		// 网络层失败（拿不到状态码）不命中这里，仍走下面的 502。
		if att := res.Attempt; att != nil && att.Stream == nil && att.StatusCode >= 400 {
			// 截断：网关 502 的 HTML 错误页可达几十 KB，整段回给客户端既难读
			// 也浪费流量；日志侧另由 BuildLog 兜底，丢不了记录
			upMsg := relay.TruncateRunes(convert.UpstreamErrorMessage(res.Candidate.Channel.Protocol, att.Body), 800)
			p.writeError(c, att.StatusCode, upMsg, "upstream_error")
			s.finalizeLog(req, res, attemptUsage(res), att.StatusCode, upMsg,
				relay.Timing{FirstByteMs: att.HeaderMs, TotalMs: totalMs}, att.Body, att.Headers)
			return
		}
		// 「没有可用渠道」也回 502：它不是上游故障，但客户端重试与否都一样，
		// 502 语义（网关侧无法代理）比 503（稍后重试会好）更接近真相 ——
		// 要修的是密钥或渠道配置，不是等。
		//
		// 只认 ErrRequestUnsupported 这种明确的语义错误来改状态码，
		// 其余（无可用渠道、上游不可达等）仍然是 502。
		status, errType := http.StatusBadGateway, "upstream_error"
		// 请求内容无法转换（例如给 Anthropic 渠道发音频）是**客户端**的错，
		// 不是上游的错：回 400 而不是 502。502 会让客户端以为上游坏了去重试，
		// 而重试多少次结果都一样 —— 用户真正要做的是改请求。
		if errors.Is(relayErr, relay.ErrRequestUnsupported) {
			status, errType = http.StatusBadRequest, "invalid_request_error"
		}
		p.writeError(c, status, relayErr.Error(), errType)
		// 没走到上游（或没有应答）：时序全为 0，只有总耗时是真的
		s.finalizeLog(req, res, relay.Usage{}, status, relayErr.Error(),
			relay.Timing{TotalMs: totalMs}, nil, nil)
		return
	}

	att := res.Attempt
	if att == nil {
		p.writeError(c, http.StatusBadGateway, "上游未返回响应", "upstream_error")
		return
	}

	// 上游返回不可重试的错误状态：按入站协议的错误结构回给客户端
	if att.Stream == nil && att.StatusCode >= 400 {
		// 上游按它自己的协议报错（Anthropic 是 {"type":"error","error":{...}}），
		// 抽成一行可读信息再回给客户端，别把整段 JSON 塞进 message 里套娃；
		// 超长错误体（HTML 错误页等）截到 800 字符，与上面失败链同一出口
		upMsg := relay.TruncateRunes(convert.UpstreamErrorMessage(res.Candidate.Channel.Protocol, att.Body), 800)
		p.writeError(c, att.StatusCode, upMsg, "upstream_error")
		s.finalizeLog(req, res, attemptUsage(res), att.StatusCode,
			upMsg, relay.Timing{
				// 非流式分支只量得到响应头时刻（那时整段正文与响应头一起到了），
				// BodyReads/LastByteMs 留 0 = 没观测，前端据此不显示速度
				FirstByteMs: att.HeaderMs,
				TotalMs:     int(time.Since(started).Milliseconds()),
			}, att.Body, att.Headers)
		return
	}

	if att.Stream != nil {
		s.streamToClient(c, p, req, res, att, started, cacheKey, cacheStore)
		return
	}

	// 非流式成功：经改写器输出，透传协议下即原样写出
	copyHeader(c.Writer.Header(), att.Headers)
	// 响应 Content-Type：对话端点按入站协议声明（响应已被改写成该协议的
	// 形状）；直通端点用上游的原值 —— 语音合成回的是 audio/mpeg，
	// 硬标成 application/json 会让客户端把音频当 JSON 解析失败
	contentType := p.ContentType
	if p.Direct {
		if v := att.Headers.Get("Content-Type"); v != "" {
			contentType = v
		}
	}
	c.Writer.Header().Set("Content-Type", contentType)
	c.Status(att.StatusCode)
	tr := p.translator(c.Writer, publicModel, false)
	if _, err := tr.Write(att.Body); err != nil {
		slog.Default().Warn("响应改写失败", "trace_id", traceID, "err", err)
	}
	if err := tr.Close(); err != nil {
		slog.Default().Warn("响应改写收尾失败", "trace_id", traceID, "err", err)
	}
	// 上游未回传 usage 时按报文长度兜底估算，避免统计全为零。
	// 直通端点不做这个估算：图像/音频根本没有 token 语义，把响应字节按
	// 1/3 折成「token」是纯粹的垃圾数（一段 25MB 的 mp3 会折出几百万），
	// 它们按维度计费（每张/每字符/每秒，见 finalizeLog 的 direct 分支），
	// token 全为 0 是如实计量而不是漏抓。
	usage := att.Usage
	if !att.HasUsage && !p.Direct {
		usage = relay.EstimateUsage(len(rawBody), len(att.Body))
	}
	s.finalizeLog(req, res, usage, att.StatusCode, "",
		relay.Timing{
			// 同上游错误分支：非流式只量得到响应头时刻
			FirstByteMs: att.HeaderMs,
			TotalMs:     int(time.Since(started).Milliseconds()),
		}, att.Body, att.Headers)
	// 成功回填缓存。body 尺寸的截断在 cache.Put 内部有总量判定，
	// 单条上限（MaxResponseKB）在这里判 —— 超限响应不进缓存，免得
	// 把「一条就撑爆」的大响应换进来挤掉一堆小响应（Put 的 LRU 淘汰
	// 是给「合法但多」的场景准备的）。
	if cacheStore && att.StatusCode == http.StatusOK &&
		len(att.Body) <= s.deps.Config.Relay.Cache.MaxResponseKB<<10 {
		s.deps.Cache.Put(cacheKey, &relaycache.Entry{
			Body:      att.Body,
			Stream:    false,
			CreatedAt: time.Now(),
		})
	}
}

// attemptUsage 取最后一次上游尝试回报的用量，供失败路径落账。
//
// 上游在错误响应体里也可能带 usage（如 context-length-exceeded 的 400），
// 这部分 token 上游是真收费的；一律记 0 会让成本统计与预算提醒系统性偏低，
// 而且看不出来偏低了。没有应答（网络层失败）时仍是零值。
func attemptUsage(res *relay.RelayResult) relay.Usage {
	if res != nil && res.Attempt != nil && res.Attempt.HasUsage {
		return res.Attempt.Usage
	}
	return relay.Usage{}
}

// serveCacheHit 把缓存命中的响应回给客户端。
//
// 关键性质：重放走的是与实时路径**同一个** Translator —— 缓存体是归一化层
// 的 OpenAI Chat 形状（转发层在响应进 Attempt 前已完成协议转换），Translator
// 把它改写成入站协议。这保证了「Gemini 请求命中 OpenAI 请求的缓存」时客户端
// 看到的响应形状与实时链路逐字段一致。
//
// 计量：命中不打上游，token 与金额如实为 0（req.CacheHit 让日志标出这是
// 缓存给的 0，与「上游没计量」区分）；时序只有总耗时是真的（没有上游环节）。
func (s *Server) serveCacheHit(c *gin.Context, p *inboundProfile, req *relay.RelayRequest, entry *relaycache.Entry, started time.Time) {
	req.CacheHit = true
	elapsed := int(time.Since(started).Milliseconds())

	if req.Stream {
		// 流式重放：缓存的 SSE 字节过同一个流式 Translator，
		// 客户端看到的事件序列与实时链路完全一致
		h := c.Writer.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		h.Set("X-Relay-Cache", "hit")
		c.Status(http.StatusOK)
		c.Writer.Flush()
		tr := p.translator(c.Writer, req.PublicModel, true)
		if _, err := tr.Write(entry.Body); err != nil {
			slog.Default().Warn("缓存重放写出失败", "trace_id", req.TraceID, "err", err)
		}
		if err := tr.Close(); err != nil {
			slog.Default().Warn("缓存重放收尾失败", "trace_id", req.TraceID, "err", err)
		}
	} else {
		c.Writer.Header().Set("Content-Type", p.ContentType)
		c.Writer.Header().Set("X-Relay-Cache", "hit")
		c.Status(http.StatusOK)
		tr := p.translator(c.Writer, req.PublicModel, false)
		if _, err := tr.Write(entry.Body); err != nil {
			slog.Default().Warn("缓存重放写出失败", "trace_id", req.TraceID, "err", err)
		}
		if err := tr.Close(); err != nil {
			slog.Default().Warn("缓存重放收尾失败", "trace_id", req.TraceID, "err", err)
		}
	}

	s.finalizeLog(req, nil, relay.Usage{}, http.StatusOK, "",
		relay.Timing{FirstByteMs: elapsed, TotalMs: elapsed}, entry.Body, nil)
}

// streamToClient 边转发边旁路抓取用量。
// 首包时间以真正写出第一个字节为准，而非上游响应头到达时间。
// cacheKey/cacheStore 供响应缓存的流捕获：本次请求成功收尾时把完整
// SSE 字节流（经协议转换、OpenAI Chat 形状）存进缓存，供流式重放。
func (s *Server) streamToClient(c *gin.Context, p *inboundProfile, req *relay.RelayRequest, res *relay.RelayResult, att *relay.Attempt, started time.Time, cacheKey string, cacheStore bool) {
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
	// 交付观测（2026-09-30 立）：正文分几次到达、最后一次是什么时候。
	// 只记这两个数就能回答「首字之后那段窗口是不是生成时段」——
	// 上游整段一次发出时 Reads = 1、窗口里只有尾包传输；
	// 逐帧返回时 Reads 是几十上百，LastMs − FirstMs 就是真实生成时段。
	// 判据与消费见 frontend/src/components/speed.ts。
	var body relay.BodyTiming

	// 上游中断、以及客户端主动断开，是两种不同的收尾，不能混为一谈
	var streamErr error
	clientGone := false

	// 报文留存用：只留前若干字节，长流不能无限攒在内存里
	captureLimit := s.deps.Config.Relay.PayloadMaxKB << 10
	if captureLimit <= 0 {
		captureLimit = relay.DefaultPayloadMaxBytes
	}
	var streamCapture []byte

	// 缓存捕获用：与报文留存相互独立（留存是排障采样、可能截断；
	// 缓存必须是完整可重放的应答）。cacheLimit 用缓存自己的单条上限，
	// 超限的流不缓存 —— 省内存的同时也保住「缓存的是完整应答」这条性质。
	cacheLimit := 0
	var cacheCapture []byte
	if cacheStore {
		cacheLimit = s.deps.Config.Relay.Cache.MaxResponseKB << 10
		if cacheLimit <= 0 {
			cacheLimit = 256 << 10
		}
	}

	for {
		n, readErr := att.Stream.Read(buf)
		if n > 0 {
			// 打点基准是本次上游尝试的 StartedAt（与首字同一套基准），
			// 一次 time.Since 覆盖首块与末块两个观测值，不为每字节付代价
			body.Observe(n, int(time.Since(att.StartedAt).Milliseconds()))
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
			// 缓存捕获：只在本请求「缓存可用且未命中」时进行。
			// 超出上限后停止追加（cacheCapture 长度 < tee.Bytes() 即知截断，
			// 收尾时据此放弃缓存），不中断转发。
			if cacheStore && len(cacheCapture) < cacheLimit {
				room := cacheLimit - len(cacheCapture)
				if room > n {
					room = n
				}
				cacheCapture = append(cacheCapture, buf[:room]...)
			}
			if _, writeErr := tr.Write(buf[:n]); writeErr != nil {
				if errors.Is(writeErr, convert.ErrUpstreamReported) {
					// 上游在流内报了错，改写器已把该协议的错误事件发给客户端：
					// 按上游故障收尾（记 502），不当作客户端断开。
					// 哨兵里包着上游的原始 message，落日志时能看见
					streamErr = writeErr
				} else {
					// 写不出去基本是客户端断开，不是上游的问题，不必再补错误事件
					clientGone = true
				}
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
	//
	// 先补判客户端断开：读阻塞阶段断开的客户端没机会触发写失败 ——
	// 客户端取消让 ctx 取消、上游 body 读报 context canceled，
	// streamErr 置位但 clientGone 仍是 false。不补判的话，Claude Code
	// 每次手动打断推理都会被记成一条 502（既写 Air，也污染失败统计），
	// 而这里对已断开的客户端补发错误事件本来就是白费。
	if streamErr != nil && !clientGone && c.Request.Context().Err() != nil {
		clientGone = true
	}
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
	// （clientGone 的补判已在上面收尾前做过，这里只剩真正的上游中断）
	logStatus, logErr := att.StatusCode, ""
	if streamErr != nil && !clientGone {
		logStatus = http.StatusBadGateway
		logErr = "上游流中断: " + streamErr.Error()
	}
	s.finalizeLog(req, res, usage, logStatus, logErr,
		relay.Timing{
			FirstByteMs: body.FirstMs,
			LastByteMs:  body.LastMs,
			BodyReads:   body.Reads,
			TotalMs:     int(time.Since(started).Milliseconds()),
		}, streamCapture, att.Headers)

	// 只有完整收尾的流才配进缓存：上游中断（截断的应答）与客户端断开
	//（根本没有完整应答）重放出来都是残次品。截断判定用捕获长度与
	// 总字节数对比 —— 超限截断时两者必然不等。
	if cacheStore && streamErr == nil && !clientGone &&
		len(cacheCapture) == int(tee.Bytes()) {
		s.deps.Cache.Put(cacheKey, &relaycache.Entry{
			Body:      cacheCapture,
			Stream:    true,
			CreatedAt: time.Now(),
		})
	}
}
