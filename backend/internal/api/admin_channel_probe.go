package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/relay"
)

// 连通性测试的超时。比首字节超时宽松一些：这个请求会等完整响应，
// 而不是只等响应头。
const channelTestTimeout = 25 * time.Second

// 测试用的提示词。一句 "hi" 就够：要验证的是链路（鉴权、协议、出口、模型名），
// 不是模型的回答质量。
const channelTestPrompt = "hi"

// 1024 而不是 64：推理型模型（GLM thinking、deepseek-reasoner 这类）会先把
// 输出预算花在思考上，64 个 token 连思维链都装不下，正文必然为空 ——
// 测试明明通了，界面上却显示「没有任何内容」，看起来像失败。
// 输入只有一句 "hi"，1024 的输出费用依然可以忽略。
const channelTestMaxTokens = 1024

type channelTestPayload struct {
	// Model 是**对外模型名**，留空时用该渠道白名单里的第一条
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// testChannel 往渠道发一句 "hi"，回答「这条渠道现在能不能用」。
//
// 与真实的转发走同一套代码（relay.Forwarder）：协议转换、鉴权头、
// 出站代理、响应转换全都一致。自己拼一个 http 请求去测的话，
// 测通的是「我拼的请求」，不是「中继实际发出去的请求」。
func (s *Server) testChannel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var p channelTestPayload
	_ = c.ShouldBindJSON(&p)

	db := s.deps.Store.DB()
	var ch model.Channel
	if err := db.First(&ch, id).Error; err != nil {
		writeUpstreamError(c, http.StatusNotFound, "渠道不存在", "not_found_error")
		return
	}
	if s.deps.Service == nil {
		writeUpstreamError(c, http.StatusInternalServerError, "转发内核未就绪", "internal_error")
		return
	}

	binding, err := pickTestBinding(db, id, strings.TrimSpace(p.Model))
	if err != nil {
		writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	// 停用的渠道**照样可以测**。
	//
	// 原来的写法在这里直接拒绝（「渠道已停用，测通了也不会参与路由」），
	// 但这恰好挡住了最需要它的场景：排查一条渠道为什么不可用、或者先把配置
	// 调好再启用，都得在停用状态下先验证。停用只是「不参与路由」，
	// 与「能不能与上游通上话」是两件事。
	//
	// 状态仍然如实记录（见 recordChannelTest）：探测结果会写进 health_status，
	// 列表里那条渠道依旧显示「已禁用」——停用不会被探测结果掩盖。

	plain := ""
	if ch.APIKeyEnc != "" {
		dec, err := s.deps.Cipher.Decrypt(ch.APIKeyEnc)
		if err != nil {
			writeUpstreamError(c, http.StatusInternalServerError, "渠道密钥解密失败，请重新填写 API Key", "internal_error")
			return
		}
		plain = dec
	}

	prompt := strings.TrimSpace(p.Prompt)
	if prompt == "" {
		prompt = channelTestPrompt
	}
	body, _ := json.Marshal(map[string]any{
		"model":      binding.UpstreamName,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
		"max_tokens": channelTestMaxTokens,
		"stream":     false,
	})

	cand := relay.Candidate{Channel: ch, Binding: binding, APIKeyPlain: plain, Available: true}
	ctx, cancel := context.WithTimeout(c.Request.Context(), channelTestTimeout)
	defer cancel()

	start := time.Now()
	att, err := s.deps.Service.Probe(ctx, cand, body)
	latency := int(time.Since(start).Milliseconds())

	if err != nil {
		s.recordChannelTest(db, id, false, err.Error())
		c.JSON(http.StatusOK, gin.H{
			"ok": false, "latency_ms": latency, "model": binding.PublicName,
			"error": err.Error(),
		})
		return
	}
	// 上游可能无视 stream:false 直接回 SSE（转发器无条件带 Accept: text/event-stream）：
	// 那种情况下正文在 Stream 里，Body 是空的。不关会让连接无法复用、FD 悬挂
	if att.Stream != nil {
		defer att.Stream.Close()
	}
	if att.StatusCode >= 400 {
		// 上游的错误体是排障的关键信息（密钥错、模型名不存在、余额不足
		// 都靠它区分），截断后原样回给用户
		msg := summarizeUpstreamError(att.Body)
		s.recordChannelTest(db, id, false, msg)
		c.JSON(http.StatusOK, gin.H{
			"ok": false, "status_code": att.StatusCode, "latency_ms": latency,
			"model": binding.PublicName, "error": msg,
		})
		return
	}

	// 上游可能无视 stream:false 直接回 SSE（转发器无条件带 Accept: text/event-stream）：
	// 那种情况下正文在 Stream 里，Body 是空的 —— 不解析的话「明明通了却什么内容
	// 都显示不出来」。读取（读完即弃，不关连接的话 FD 会悬挂）。
	var reply, finishReason string
	if att.Stream != nil {
		defer att.Stream.Close()
		reply, finishReason = readReplyFromStream(att.Stream)
	} else {
		reply, finishReason = extractReply(att.Body)
	}

	s.recordChannelTest(db, id, true, "")
	c.JSON(http.StatusOK, gin.H{
		"ok": true, "status_code": att.StatusCode, "latency_ms": latency,
		"model": binding.PublicName, "upstream_model": binding.UpstreamName,
		"reply": reply, "finish_reason": finishReason,
	})
}

// readReplyFromStream 从「上游以流式传输回的响应体」里拼出正文。
//
// 实测上游有两种做法（D1 中转两种都会出现，同一渠道不同时刻还不一样）：
//   - 标准 SSE：一行一个 "data: {chunk}"，chunk 用 choices[].delta；
//   - 更不讲理的：直接把完整的 chat.completion JSON（choices[].message）
//     按 chunked 传输——没有 "data:" 前缀，整行就是一个大 JSON。
//
// 所以这里对每一行：有 "data:" 前缀就剥掉；然后只要是 "{"
// 开头就尝试解析，delta（流式块）与 message（整块响应）两种形状都认，
// 正文优先、思维链兜底。读完即弃。8KB 截到足够界面展示。
func readReplyFromStream(r io.Reader) (string, string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 512*1024)
	var content, reasoning, extra strings.Builder
	finish := ""
	type segShape struct {
		Content          string `json:"content"`
		ReasoningContent string `json:"reasoning_content"`
		Reasoning        string `json:"reasoning"`
	}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		payload := line
		if strings.HasPrefix(line, "data:") {
			payload = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if payload == "" || payload == "[DONE]" || !strings.HasPrefix(payload, "{") {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta   segShape `json:"delta"`
				Message segShape `json:"message"`
				// 整块响应的正文也可能直接放在 choice 顶层（个别中转的怪形状）
				Text         string `json:"text"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		for _, c := range chunk.Choices {
			seg := c.Delta
			if seg.Content == "" && seg.ReasoningContent == "" && seg.Reasoning == "" {
				seg = c.Message
			}
			if content.Len() < 8192 {
				content.WriteString(seg.Content)
			}
			if reasoning.Len() < 8192 {
				reasoning.WriteString(seg.ReasoningContent)
				extra.WriteString(seg.Reasoning)
			}
			if content.Len() < 8192 && c.Text != "" {
				content.WriteString(c.Text)
			}
			if c.FinishReason != "" {
				finish = c.FinishReason
			}
		}
		if content.Len() >= 8192 {
			break
		}
	}
	text := strings.TrimSpace(content.String())
	if text == "" {
		text = strings.TrimSpace(reasoning.String())
	}
	if text == "" {
		text = strings.TrimSpace(extra.String())
	}
	return truncate(text, 200), finish
}

// pickTestBinding 选一个用来测试的模型条目。
func pickTestBinding(db *gorm.DB, channelID uint, publicName string) (model.ChannelModel, error) {
	var b model.ChannelModel
	q := db.Where("channel_id = ? AND enabled = true", channelID)
	if publicName != "" {
		if err := q.Where("public_name = ?", publicName).First(&b).Error; err != nil {
			return b, errors.New("这个渠道的白名单里没有启用中的模型 " + publicName)
		}
		return b, nil
	}
	if err := q.Order("id").First(&b).Error; err != nil {
		return b, errors.New("这个渠道还没有启用的模型，先在白名单里加一个再测")
	}
	return b, nil
}

// recordChannelTest 把结果写回渠道的健康状态，列表里的「状态」列随之更新。
func (s *Server) recordChannelTest(db *gorm.DB, id uint, ok bool, msg string) {
	status := "healthy"
	if !ok {
		status = "degraded"
	}
	now := time.Now()
	// 写失败只影响列表上那个标签，不该让测试本身返回错误
	_ = db.Model(&model.Channel{}).Where("id = ?", id).Updates(map[string]any{
		"health_status": status, "last_error": truncate(msg, 1000), "last_checked_at": &now,
	}).Error
}

// summarizeUpstreamError 从上游错误体里取出人话。
//
// OpenAI 风格是 {"error":{"message":"..."}}，但各家的形状并不统一
// （有的直接给字符串，有的给 {"message":...}），取不到就退回原文。
func summarizeUpstreamError(body []byte) string {
	raw := strings.TrimSpace(string(body))
	if raw == "" {
		return "上游返回了空响应体"
	}
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if parsed.Error.Message != "" {
			if parsed.Error.Type != "" {
				return truncate(parsed.Error.Message+" ("+parsed.Error.Type+")", 600)
			}
			return truncate(parsed.Error.Message, 600)
		}
		if parsed.Message != "" {
			return truncate(parsed.Message, 600)
		}
	}
	return truncate(raw, 600)
}

// extractReply 取出助手回复的文本与结束原因，用于在界面上给一个
// 「确实通了」的证据。
//
// 探测与真实转发不同：真实转发的响应会经出站适配器归一，而探测拿到的是
// **上游原生形状**。渠道协议不止一种，正文/结束原因的字段名各说各话，
// 这里按形状逐一认领：
//
//	OpenAI Chat      choices[0].message.{content,reasoning_content,reasoning} + finish_reason
//	Anthropic        content[].text（type=text）+ stop_reason
//	OpenAI Responses output[].content[].text（type=output_text）+ status
//	Gemini           candidates[0].content.parts[].text + finishReason
//
// 正文仍为空时 finish_reason 能说明是为什么（length = 输出预算耗尽）。
// 站内统一格式的 reasoning 字段也在认领范围内（部分上游把思维链放这里）。
func extractReply(body []byte) (string, string) {
	// OpenAI Chat 形状
	var chat struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &chat); err == nil && len(chat.Choices) > 0 {
		c := chat.Choices[0]
		msg := strings.TrimSpace(c.Message.Content)
		if msg == "" {
			msg = strings.TrimSpace(c.Message.ReasoningContent)
		}
		if msg == "" {
			msg = strings.TrimSpace(c.Message.Reasoning)
		}
		return msg, c.FinishReason
	}

	// Anthropic Messages 形状：content 是块数组，只拼 type=text 的正文
	var anthropic struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(body, &anthropic); err == nil && len(anthropic.Content) > 0 {
		var b strings.Builder
		for _, blk := range anthropic.Content {
			if blk.Type == "text" {
				b.WriteString(blk.Text)
			}
		}
		return strings.TrimSpace(b.String()), anthropic.StopReason
	}

	// OpenAI Responses 形状：output[].content[].text（type=output_text）
	var responses struct {
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &responses); err == nil && len(responses.Output) > 0 {
		var b strings.Builder
		for _, item := range responses.Output {
			if item.Type != "message" {
				continue
			}
			for _, seg := range item.Content {
				if seg.Type == "output_text" {
					b.WriteString(seg.Text)
				}
			}
		}
		return strings.TrimSpace(b.String()), responses.Status
	}

	// Gemini generateContent 形状：candidates[0].content.parts[].text
	var gemini struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(body, &gemini); err == nil && len(gemini.Candidates) > 0 {
		var b strings.Builder
		for _, p := range gemini.Candidates[0].Content.Parts {
			b.WriteString(p.Text)
		}
		return strings.TrimSpace(b.String()), gemini.Candidates[0].FinishReason
	}

	return "", ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// 按字节截断可能切坏多字节字符，退回上一个完整字符边界
	cut := s[:n]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
}
