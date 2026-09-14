package convert

import (
	"encoding/json"
	"testing"

	"llm-relay/internal/model"
)

// 入站 Anthropic -> 通用语：system 上的 cache_control 必须留住。
//
// 丢掉断点等于关掉 Anthropic 的提示缓存：多轮对话每一轮都按全量输入计费，
// 长 system prompt 场景成本差好几倍，而界面上只表现为「缓存命中率一直是 0」，
// 极难联想到是中转站吃掉了断点。
func TestAnthropicSystemCacheControlSurvivesInbound(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4",
		"max_tokens": 1024,
		"system": [
			{"type":"text","text":"你是一个助手。","cache_control":{"type":"ephemeral"}},
			{"type":"text","text":"补充说明。"}
		],
		"messages": [{"role":"user","content":"你好"}]
	}`)

	out, err := AnthropicRequestToOpenAIChat(body)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("没有生成 messages")
	}
	sys := asMap(msgs[0])
	if asString(sys["role"]) != "system" {
		t.Fatalf("第一条应是 system，实际 %v", sys["role"])
	}
	// 有断点时必须是块数组，不能拍平成字符串
	parts, ok := sys["content"].([]any)
	if !ok {
		t.Fatalf("带断点的 system 应是内容块数组，实际 %T（%v）", sys["content"], sys["content"])
	}
	foundCC := false
	for _, p := range parts {
		if takeCacheControl(asMap(p)) != nil {
			foundCC = true
		}
	}
	if !foundCC {
		t.Fatalf("system 的 cache_control 丢了：%v", parts)
	}
}

// 没有断点时保持原来的纯文本形态，避免为一组纯文本引入数组。
func TestAnthropicSystemWithoutCacheControlStaysPlainText(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4",
		"max_tokens": 1024,
		"system": [{"type":"text","text":"A"},{"type":"text","text":"B"}],
		"messages": [{"role":"user","content":"你好"}]
	}`)
	out, err := AnthropicRequestToOpenAIChat(body)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	msgs, _ := got["messages"].([]any)
	sys := asMap(msgs[0])
	if s, ok := sys["content"].(string); !ok {
		t.Fatalf("无断点的 system 应是纯文本，实际 %T", sys["content"])
	} else if s != "A\nB" {
		t.Fatalf("system 文本应为 A\\nB，实际 %q", s)
	}
}

// 完整往返：入站 Anthropic -> 通用语 -> 出站 Anthropic，断点必须还在。
// 这条链路是「Anthropic 客户端打 Anthropic 渠道」的真实路径。
func TestCacheControlRoundTripToAnthropicUpstream(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4",
		"max_tokens": 1024,
		"system": [
			{"type":"text","text":"长系统提示","cache_control":{"type":"ephemeral"}}
		],
		"messages": [
			{"role":"user","content":[{"type":"text","text":"第一轮","cache_control":{"type":"ephemeral"}}]},
			{"role":"assistant","content":[{"type":"text","text":"回答"}]},
			{"role":"user","content":[{"type":"text","text":"第二轮"}]}
		]
	}`)

	// 第一步：入站归一化成通用语
	common, err := AnthropicRequestToOpenAIChat(body)
	if err != nil {
		t.Fatalf("入站转换失败: %v", err)
	}
	// 第二步：出站改写回 Anthropic
	_, back, err := UpstreamRequest(model.ProtocolAnthropic, "/v1/chat/completions", common, "claude-sonnet-4")
	if err != nil {
		t.Fatalf("出站转换失败: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(back, &got); err != nil {
		t.Fatalf("出站结果不是合法 JSON: %v", err)
	}
	// system 上的断点
	sysParts, ok := got["system"].([]any)
	if !ok {
		t.Fatalf("出站 system 应保留块数组，实际 %T（%v）", got["system"], got["system"])
	}
	if takeCacheControl(asMap(sysParts[0])) == nil {
		t.Fatalf("system 的 cache_control 在出站时丢了：%v", sysParts)
	}
	// 用户消息上的断点
	msgs, _ := got["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("出站没有 messages")
	}
	first := asMap(msgs[0])
	blocks, _ := first["content"].([]any)
	if len(blocks) == 0 {
		t.Fatalf("第一条消息没有内容块：%v", first)
	}
	if takeCacheControl(asMap(blocks[0])) == nil {
		t.Fatalf("用户消息的 cache_control 在出站时丢了：%v", blocks)
	}
}

// 出站目标是 OpenAI 兼容端点时，cache_control 必须被清掉。
//
// 这个字段是站内通用语的暂存约定，不是 OpenAI 的字段；
// 严格校验的上游（如 Azure OpenAI）收到未知字段会直接 400。
func TestCacheControlStrippedForNonAnthropicUpstream(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4",
		"max_tokens": 1024,
		"system": [{"type":"text","text":"提示","cache_control":{"type":"ephemeral"}}],
		"messages": [{"role":"user","content":"你好"}]
	}`)
	common, err := AnthropicRequestToOpenAIChat(body)
	if err != nil {
		t.Fatalf("入站转换失败: %v", err)
	}
	// 确认通用语里确实带着这个字段（否则下面的断言没有意义）
	var peek any
	_ = json.Unmarshal(common, &peek)
	if !containsKey(peek, cacheControlKey) {
		t.Fatalf("前置条件不成立：通用语里没有 %s", cacheControlKey)
	}

	for _, proto := range []string{
		model.ProtocolOpenAIChat,
		model.ProtocolCustom,
		model.ProtocolOpenAIResponses,
		model.ProtocolGemini,
		model.ProtocolEmbeddings,
	} {
		_, out, err := UpstreamRequest(proto, "/v1/chat/completions", common, "gpt-4o")
		if err != nil {
			t.Fatalf("%s 转换失败: %v", proto, err)
		}
		var got any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%s 结果不是合法 JSON: %v", proto, err)
		}
		if containsKey(got, cacheControlKey) {
			t.Errorf("%s 上游不该收到 %s 字段", proto, cacheControlKey)
		}
	}
}

// 没有 cache_control 的普通请求必须原样透传（字节级一致）。
//
// stripCacheControlFromJSON 会做一次「有没有这个键」的预检，
// 没有就直接返回原文 —— 这条断言锁住这个优化，也证明它不会顺手改动报文。
func TestPlainRequestPassesThroughUnchanged(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"你好"}],"stream":true}`)
	path, out, err := UpstreamRequest(model.ProtocolOpenAIChat, "/v1/chat/completions", body, "gpt-4o")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	if path != "/v1/chat/completions" {
		t.Errorf("路径不该变，实际 %s", path)
	}
	if string(out) != string(body) {
		t.Errorf("无 cache_control 的请求应原样透传\n期望: %s\n实际: %s", body, out)
	}
}

// embeddings 之类的非对话请求完全不改写，也不该被触碰。
func TestNonChatPathUntouched(t *testing.T) {
	body := []byte(`{"model":"text-embedding-3-small","input":"hi"}`)
	path, out, err := UpstreamRequest(model.ProtocolAnthropic, "/v1/embeddings", body, "")
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if path != "/v1/embeddings" {
		t.Errorf("路径被改了：%s", path)
	}
	if string(out) != string(body) {
		t.Errorf("非对话请求不该被改写\n期望: %s\n实际: %s", body, out)
	}
}

// tool_result 上的断点也要留住：多轮工具调用场景里断点常落在最后一条结果上。
func TestToolResultCacheControlRoundTrip(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4",
		"max_tokens": 1024,
		"messages": [
			{"role":"assistant","content":[
				{"type":"tool_use","id":"tu_1","name":"get_weather","input":{"city":"北京"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"tu_1","content":"晴",
				 "cache_control":{"type":"ephemeral"}}
			]}
		]
	}`)
	common, err := AnthropicRequestToOpenAIChat(body)
	if err != nil {
		t.Fatalf("入站转换失败: %v", err)
	}
	var mid any
	_ = json.Unmarshal(common, &mid)
	if !containsKey(mid, cacheControlKey) {
		t.Fatalf("tool_result 的 cache_control 在入站时就丢了")
	}

	_, back, err := UpstreamRequest(model.ProtocolAnthropic, "/v1/chat/completions", common, "claude-sonnet-4")
	if err != nil {
		t.Fatalf("出站转换失败: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(back, &got)
	msgs, _ := got["messages"].([]any)
	found := false
	for _, m := range msgs {
		blocks, _ := asMap(m)["content"].([]any)
		for _, b := range blocks {
			blk := asMap(b)
			if asString(blk["type"]) == "tool_result" && takeCacheControl(blk) != nil {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("tool_result 的 cache_control 在出站时丢了：%v", got["messages"])
	}
}
