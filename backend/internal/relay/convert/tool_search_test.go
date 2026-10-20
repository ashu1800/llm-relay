package convert

import (
	"encoding/json"
	"strings"
	"testing"

	"llm-relay/internal/model"
)

// Claude Code 工具搜索（beta advanced-tool-use-2025-11-20）在小节链路上的兼容口径。
//
// 需求原文（实测 Claude Code 2.1.299）：
//   - 首轮 tools 里只有内置工具 + 一个 DeferredToolPlaceholder（defer_loading: true），
//     MCP 工具名只以纯文本列在 system prompt 里；
//   - 模型调 ToolSearch 后，客户端把完整 schema 以**仍带 defer_loading: true**
//     的形式塞回 tools，并在 tool_result.content 里放 tool_reference 块；
//   - 搜索/匹配/schema 返回全在客户端完成，中转站只负责「不破坏协议」。
//
// 四条硬要求：① beta 头不认识也不能 400；② defer_loading 对 Anthropic 上游保留、
// 对其它上游丢弃且绝不校验失败；③ 允许 ToolSearch 工具调用与嵌套的
// tool_reference，非 string 的 content 不得拒绝；④ 不对 tools 去重。
//
// 要求 ① 的链路在 relay/forward_headers_test.go（头透传），其余在这里。

const toolSearchBetaRequest = `{
	"model": "claude-x",
	"max_tokens": 1024,
	"tools": [
		{
			"name": "ToolSearch",
			"description": "Fetches full schema definitions for deferred tools",
			"input_schema": {
				"type": "object",
				"properties": {"query": {"type": "string"}, "max_results": {"type": "number"}},
				"required": ["query", "max_results"],
				"additionalProperties": false
			}
		},
		{
			"name": "DeferredToolPlaceholder",
			"description": "Reserved placeholder that keeps deferred tool loading active; never call this tool.",
			"input_schema": {"type": "object", "properties": {}},
			"defer_loading": true
		},
		{
			"name": "mcp__demo-mcp__demo_tool_0",
			"description": "demo tool",
			"input_schema": {"type": "object", "properties": {"q": {"type": "string"}}},
			"defer_loading": true
		}
	],
	"messages": [
		{"role": "user", "content": "帮我看下 demo 工具"},
		{"role": "assistant", "content": [
			{"type": "tool_use", "id": "toolu_1", "name": "ToolSearch",
			 "input": {"query": "select:mcp__demo-mcp__demo_tool_0", "max_results": 5}}
		]},
		{"role": "user", "content": [
			{"type": "tool_result", "tool_use_id": "toolu_1", "content": [
				{"type": "tool_reference", "tool_name": "mcp__demo-mcp__demo_tool_0"}
			]}
		]}
	]
}`

// toolsOf 取请求体里的 tools 数组。
func toolsOf(t *testing.T, body []byte) []any {
	t.Helper()
	got := decodeJSON(t, body)
	tools, _ := got["tools"].([]any)
	return tools
}

// TestAnthropicToolDeferLoadingRoundTrip：Anthropic→Anthropic 往返后
// defer_loading 原样保留，工具条数与顺序不变（要求 ② 保留 + ④ 不去重）。
func TestAnthropicToolDeferLoadingRoundTrip(t *testing.T) {
	mid, err := AnthropicRequestToOpenAIChat([]byte(toolSearchBetaRequest))
	if err != nil {
		t.Fatalf("入站转换失败: %v", err)
	}
	// 通用语里就应该已经在（否则出站无从还原）
	midTools := toolsOf(t, mid)
	if len(midTools) != 3 {
		t.Fatalf("通用语 tools 应保留 3 条，实际 %d", len(midTools))
	}
	for _, want := range []string{"DeferredToolPlaceholder", "mcp__demo-mcp__demo_tool_0"} {
		if !toolHasDeferLoading(midTools, want) {
			t.Fatalf("通用语里 %s 应带 defer_loading:\n%s", want, mid)
		}
	}

	out, err := OpenAIChatToAnthropicRequest(mid)
	if err != nil {
		t.Fatalf("出站转换失败: %v", err)
	}
	outTools := toolsOf(t, out)
	if len(outTools) != 3 {
		t.Fatalf("出站 tools 应仍是 3 条（不去重），实际 %d:\n%s", len(outTools), out)
	}
	for _, want := range []string{"DeferredToolPlaceholder", "mcp__demo-mcp__demo_tool_0"} {
		if !toolHasDeferLoading(outTools, want) {
			t.Fatalf("出站 Anthropic 请求里 %s 的 defer_loading 丢了:\n%s", want, out)
		}
	}
	// ToolSearch 本身没有 defer_loading，不能被凭空加上
	for _, item := range outTools {
		blk := asMap(item)
		if asString(blk["name"]) == "ToolSearch" {
			if _, ok := blk[deferLoadingKey]; ok {
				t.Fatalf("ToolSearch 不该带 defer_loading: %v", blk)
			}
		}
	}
}

// toolHasDeferLoading 判断 tools 里名为 name 的条目是否带 defer_loading。
// 两种形状都要认：通用语是 {type:function, function:{name}}，
// 出站 Anthropic 是 {name, ...}。
func toolHasDeferLoading(tools []any, name string) bool {
	for _, item := range tools {
		blk := asMap(item)
		if blk == nil {
			continue
		}
		got := asString(blk["name"])
		if got == "" {
			got = asString(asMap(blk["function"])["name"])
		}
		if got != name {
			continue
		}
		if v, ok := blk[deferLoadingKey]; ok && v == true {
			return true
		}
	}
	return false
}

// TestToolDeferLoadingDroppedForNonAnthropicUpstream：对非 Anthropic 上游
// 必须静默丢弃 defer_loading 与私有暂存字段，且请求本身要转换成功（要求 ②）。
//
// 严格校验的上游（Azure OpenAI 等）收到未知字段会直接 400 —— 工具搜索
// 要避免的正是「多了个字段就整条失败」。
func TestToolDeferLoadingDroppedForNonAnthropicUpstream(t *testing.T) {
	mid, err := AnthropicRequestToOpenAIChat([]byte(toolSearchBetaRequest))
	if err != nil {
		t.Fatalf("入站转换失败: %v", err)
	}
	// 前置条件：通用语里确实带着这两个私有字段，否则下面的断言没有意义
	if !containsKey(decodeJSON(t, mid), deferLoadingKey) {
		t.Fatalf("前置条件不成立：通用语里没有 %s", deferLoadingKey)
	}
	if !containsKey(decodeJSON(t, mid), anthropicToolResultKey) {
		t.Fatalf("前置条件不成立：通用语里没有 %s", anthropicToolResultKey)
	}

	for _, proto := range []string{
		model.ProtocolOpenAIChat,
		model.ProtocolCustom,
		model.ProtocolOpenAIResponses,
		model.ProtocolGemini,
	} {
		_, out, err := UpstreamRequest(proto, "/v1/chat/completions", mid, "gpt-4o")
		if err != nil {
			t.Fatalf("%s 转换失败: %v", proto, err)
		}
		var parsed any
		if err := json.Unmarshal(out, &parsed); err != nil {
			t.Fatalf("%s 结果不是合法 JSON: %v", proto, err)
		}
		for _, key := range []string{deferLoadingKey, anthropicToolResultKey} {
			if containsKey(parsed, key) {
				t.Errorf("%s 上游不该收到 %s 字段:\n%s", proto, key, out)
			}
		}
	}
}

// TestToolReferenceRenderedForOpenAIUpstream：只有 tool_reference 的工具结果
// 在通用语里渲染成一行文本，而不是空字符串（要求 ③ 降级）。
func TestToolReferenceRenderedForOpenAIUpstream(t *testing.T) {
	mid, err := AnthropicRequestToOpenAIChat([]byte(toolSearchBetaRequest))
	if err != nil {
		t.Fatalf("入站转换失败: %v", err)
	}
	var parsed struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(mid, &parsed); err != nil {
		t.Fatal(err)
	}
	var toolMsg map[string]any
	for _, m := range parsed.Messages {
		if m["role"] == "tool" {
			toolMsg = m
		}
	}
	if toolMsg == nil {
		t.Fatalf("通用语里应有 role=tool 的消息:\n%s", mid)
	}
	want := "[Tool loaded: mcp__demo-mcp__demo_tool_0]"
	if got, ok := toolMsg["content"].(string); !ok || got != want {
		t.Fatalf("tool_reference 应渲染成 %q，实际 %T %v", want, toolMsg["content"], toolMsg["content"])
	}
}

// TestToolReferenceMixedWithTextAndImage：text + tool_reference + image 混合时
// 走多模态数组，文本两段、image_url 一段（不因新块类型改变原有形态）。
func TestToolReferenceMixedWithTextAndImage(t *testing.T) {
	req := `{
		"model": "claude-x",
		"max_tokens": 1024,
		"messages": [
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "tu_1", "name": "ToolSearch", "input": {"query": "select:x"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "tu_1", "content": [
					{"type": "text", "text": "已找到工具"},
					{"type": "tool_reference", "tool_name": "mcp__x__y"},
					{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "aW1n"}}
				]}
			]}
		]
	}`
	mid, err := AnthropicRequestToOpenAIChat([]byte(req))
	if err != nil {
		t.Fatalf("入站转换失败: %v", err)
	}
	var parsed struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(mid, &parsed); err != nil {
		t.Fatal(err)
	}
	var parts []any
	for _, m := range parsed.Messages {
		if m["role"] == "tool" {
			parts, _ = m["content"].([]any)
		}
	}
	if len(parts) != 3 {
		t.Fatalf("应还原成 3 段（text + 降级文本 + image_url），实际 %d: %v", len(parts), parts)
	}
	var texts []string
	var images int
	for _, p := range parts {
		blk := asMap(p)
		switch asString(blk["type"]) {
		case "text":
			texts = append(texts, asString(blk["text"]))
		case "image_url":
			images++
		}
	}
	if len(texts) != 2 || texts[1] != "[Tool loaded: mcp__x__y]" {
		t.Fatalf("两段文本应是原文 + 解锁提示，实际 %v", texts)
	}
	if images != 1 {
		t.Fatalf("图片应保留，实际 %d 段 image_url", images)
	}
}

// TestToolReferenceRoundTripToAnthropic：Anthropic→Anthropic 往返时
// tool_result.content 还原成原始 tool_reference 块，而不是降级文本（要求 ③ 保真）。
func TestToolReferenceRoundTripToAnthropic(t *testing.T) {
	mid, err := AnthropicRequestToOpenAIChat([]byte(toolSearchBetaRequest))
	if err != nil {
		t.Fatalf("入站转换失败: %v", err)
	}
	out, err := OpenAIChatToAnthropicRequest(mid)
	if err != nil {
		t.Fatalf("出站转换失败: %v", err)
	}
	msgs := messagesOf(t, out)
	// messages[0]=user, [1]=assistant(tool_use), [2]=user(tool_result)
	last := asMap(msgs[len(msgs)-1])
	blocks, _ := last["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("最后一条 user 消息应只有 1 个 tool_result 块，实际 %d: %v", len(blocks), blocks)
	}
	tr := asMap(blocks[0])
	inner, _ := tr["content"].([]any)
	if len(inner) != 1 {
		t.Fatalf("tool_result.content 应还原为 1 个原始块，实际 %v", tr["content"])
	}
	ref := asMap(inner[0])
	if asString(ref["type"]) != "tool_reference" || asString(ref["tool_name"]) != "mcp__demo-mcp__demo_tool_0" {
		t.Fatalf("tool_reference 块应原样保真（不是降级文本），实际 %v", ref)
	}
}

// TestToolReferenceMixedRoundTripKeepsAllBlocks：混合内容往返时三块都在，
// 顺序与入站一致 —— 暂存的是整个原始数组，不是只有 tool_reference。
func TestToolReferenceMixedRoundTripKeepsAllBlocks(t *testing.T) {
	req := `{
		"model": "claude-x",
		"max_tokens": 1024,
		"messages": [
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "tu_1", "name": "ToolSearch", "input": {"query": "select:x"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "tu_1", "content": [
					{"type": "text", "text": "已找到工具"},
					{"type": "tool_reference", "tool_name": "mcp__x__y"}
				]}
			]}
		]
	}`
	mid, err := AnthropicRequestToOpenAIChat([]byte(req))
	if err != nil {
		t.Fatalf("入站转换失败: %v", err)
	}
	out, err := OpenAIChatToAnthropicRequest(mid)
	if err != nil {
		t.Fatalf("出站转换失败: %v", err)
	}
	msgs := messagesOf(t, out)
	last := asMap(msgs[len(msgs)-1])
	blocks, _ := last["content"].([]any)
	tr := asMap(blocks[0])
	inner, _ := tr["content"].([]any)
	if len(inner) != 2 {
		t.Fatalf("tool_result 应还原 2 个原始块，实际 %d: %v", len(inner), tr["content"])
	}
	if asString(asMap(inner[0])["type"]) != "text" || asString(asMap(inner[0])["text"]) != "已找到工具" {
		t.Fatalf("第一块应是原文 text，实际 %v", inner[0])
	}
	if asString(asMap(inner[1])["type"]) != "tool_reference" {
		t.Fatalf("第二块应是 tool_reference，实际 %v", inner[1])
	}
}

// TestToolSearchToolUseIsNormalToolCall：ToolSearch 就是普通工具调用，
// 不需要任何特判（要求 ③ 前半）。name 与 input 都要原样往返。
func TestToolSearchToolUseIsNormalToolCall(t *testing.T) {
	mid, err := AnthropicRequestToOpenAIChat([]byte(toolSearchBetaRequest))
	if err != nil {
		t.Fatalf("入站转换失败: %v", err)
	}
	var parsed struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(mid, &parsed); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range parsed.Messages {
		calls, _ := m["tool_calls"].([]any)
		for _, c := range calls {
			fn := asMap(asMap(c)["function"])
			if asString(fn["name"]) != "ToolSearch" {
				continue
			}
			found = true
			if !strings.Contains(asString(fn["arguments"]), "select:mcp__demo-mcp__demo_tool_0") {
				t.Fatalf("ToolSearch 的参数应原样序列化，实际 %s", fn["arguments"])
			}
		}
	}
	if !found {
		t.Fatalf("ToolSearch 应被当作普通工具调用转换:\n%s", mid)
	}

	out, err := OpenAIChatToAnthropicRequest(mid)
	if err != nil {
		t.Fatalf("出站转换失败: %v", err)
	}
	var back bool
	for _, item := range messagesOf(t, out) {
		blocks, _ := asMap(item)["content"].([]any)
		for _, b := range blocks {
			if asString(asMap(b)["type"]) == "tool_use" && asString(asMap(b)["name"]) == "ToolSearch" {
				back = true
			}
		}
	}
	if !back {
		t.Fatalf("ToolSearch 的 tool_use 应原样回到 Anthropic 请求:\n%s", out)
	}
}

// TestAnthropicParamsStrippedWithoutCacheControl：只开 thinking、不带
// cache_control 的请求，anthropic_params 也不能漏给 OpenAI 兼容上游。
//
// 这是 2026-10-20 加 Tool Search 支持时顺手发现的既有缺口：
// privateFieldKeys 里原本没有 anthropic_params，而 stripCacheControlFromJSON
// 的粗筛按那个列表做子串匹配 —— 只有恰好也带 cache_control 的请求才会被
// 解析并清掉，单独开 thinking 的请求会把 {"anthropic_params":{...}} 原样
// 发给上游（严格校验的实现直接 400）。semantics_test 里那条 Gemini 用例
// 走的是直转、不经过 UpstreamRequest，所以一直没覆盖到。
func TestAnthropicParamsStrippedWithoutCacheControl(t *testing.T) {
	mid, err := AnthropicRequestToOpenAIChat([]byte(`{
		"model": "claude-3",
		"max_tokens": 100,
		"thinking": {"type": "enabled", "budget_tokens": 512},
		"messages": [{"role": "user", "content": "hi"}]
	}`))
	if err != nil {
		t.Fatalf("入站转换失败: %v", err)
	}
	if !containsKey(decodeJSON(t, mid), anthropicParamsKey) {
		t.Fatalf("前置条件不成立：通用语里没有 %s", anthropicParamsKey)
	}
	for _, proto := range []string{model.ProtocolOpenAIChat, model.ProtocolCustom, model.ProtocolOpenAIResponses} {
		_, out, err := UpstreamRequest(proto, "/v1/chat/completions", mid, "gpt-4o")
		if err != nil {
			t.Fatalf("%s 转换失败: %v", proto, err)
		}
		if containsKey(decodeJSON(t, out), anthropicParamsKey) {
			t.Errorf("%s 上游不该收到 %s 字段:\n%s", proto, anthropicParamsKey, out)
		}
	}
}

// TestDuplicateToolNamesNotDeduped：同名工具两条（一条带 defer_loading）时
// 不去重 —— 需求 ④ 明确要求保留，去重会改变客户端意图。
func TestDuplicateToolNamesNotDeduped(t *testing.T) {
	req := `{
		"model": "claude-x",
		"max_tokens": 100,
		"tools": [
			{"name": "mcp__x__y", "description": "plain", "input_schema": {"type": "object"}},
			{"name": "mcp__x__y", "description": "deferred", "input_schema": {"type": "object"}, "defer_loading": true}
		],
		"messages": [{"role": "user", "content": "hi"}]
	}`
	mid, err := AnthropicRequestToOpenAIChat([]byte(req))
	if err != nil {
		t.Fatalf("入站转换失败: %v", err)
	}
	if n := len(toolsOf(t, mid)); n != 2 {
		t.Fatalf("通用语 tools 应保留 2 条同名条目，实际 %d:\n%s", n, mid)
	}
	out, err := OpenAIChatToAnthropicRequest(mid)
	if err != nil {
		t.Fatalf("出站转换失败: %v", err)
	}
	tools := toolsOf(t, out)
	if len(tools) != 2 {
		t.Fatalf("出站 tools 应保留 2 条同名条目，实际 %d:\n%s", len(tools), out)
	}
	if !toolHasDeferLoading(tools, "mcp__x__y") {
		t.Fatalf("带 defer_loading 的那条必须保留:\n%s", out)
	}
}
