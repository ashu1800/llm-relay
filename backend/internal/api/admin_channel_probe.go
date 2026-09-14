package api

import (
	"context"
	"encoding/json"
	"errors"
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
// 不是模型的回答质量。max_tokens 也压到最小，测试不该产生可观的费用。
const channelTestPrompt = "hi"

// 64 而不是 1：推理型模型会先把预算花在思考上，给太少会拿到一个空的正文，
// 「通了但看不到任何内容」看起来像失败。64 个 token 的费用可以忽略。
const channelTestMaxTokens = 64

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
	if !ch.Enabled {
		// 停用的渠道测通了也不能用，先说清楚，免得白高兴
		writeUpstreamError(c, http.StatusBadRequest, "渠道已停用，测通了也不会参与路由", "invalid_request_error")
		return
	}

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

	s.recordChannelTest(db, id, true, "")
	c.JSON(http.StatusOK, gin.H{
		"ok": true, "status_code": att.StatusCode, "latency_ms": latency,
		"model": binding.PublicName, "upstream_model": binding.UpstreamName,
		"reply": extractReply(att.Body),
	})
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

// extractReply 取出助手回复的文本，只用于在界面上给一个「确实通了」的证据。
func extractReply(body []byte) string {
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				// 推理型模型（deepseek-reasoner 这一类）会把正文先写进
				// reasoning_content；只看 content 会拿到空字符串，
				// 界面上就变成「通了但什么都没返回」
				ReasoningContent string `json:"reasoning_content"`
				// 站内统一格式用的是 reasoning：Anthropic / Responses / Gemini
				// 这几个出站适配器都把思维链放在这个字段（见 convert 包）。
				// 少了它，测 GLM 这类「预算全花在思考上」的模型时正文为空，
				// 明明通了却显示不出任何内容。
				Reasoning string `json:"reasoning"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Choices) == 0 {
		return ""
	}
	msg := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if msg == "" {
		msg = strings.TrimSpace(parsed.Choices[0].Message.ReasoningContent)
	}
	if msg == "" {
		msg = strings.TrimSpace(parsed.Choices[0].Message.Reasoning)
	}
	return truncate(msg, 200)
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
