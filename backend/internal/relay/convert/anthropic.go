package convert

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AnthropicRequestToOpenAIChat 把 Anthropic Messages 请求翻译成 OpenAI Chat Completions 请求。
//
// 主要差异：
//   - Anthropic 的 system 是顶层字段，OpenAI 要求放进 messages 的第一条
//   - Anthropic 的 tool_result 是 user 消息里的内容块，OpenAI 要拆成独立的 role=tool 消息
//   - Anthropic 的 tools 用 input_schema，OpenAI 包一层 {type:function, function:{parameters}}
func AnthropicRequestToOpenAIChat(body []byte) ([]byte, error) {
	var src map[string]any
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("Anthropic 请求体解析失败: %w", err)
	}

	out := map[string]any{}
	if v, ok := src["model"]; ok {
		out["model"] = v
	}
	if v, ok := src["stream"]; ok {
		out["stream"] = v
	}
	// max_tokens 在 Anthropic 是必填，在 OpenAI 可选，直接透传
	if v, ok := src["max_tokens"]; ok {
		out["max_tokens"] = v
	}
	for _, k := range []string{"temperature", "top_p"} {
		if v, ok := src[k]; ok {
			out[k] = v
		}
	}
	if v, ok := src["stop_sequences"]; ok {
		out["stop"] = v
	}
	if v, ok := src["tools"]; ok {
		out["tools"] = anthropicToolsToOpenAI(v)
	}
	if v, ok := src["tool_choice"]; ok {
		out["tool_choice"] = anthropicToolChoiceToOpenAI(v)
	}
	// Anthropic 特有顶层参数（thinking/top_k/metadata/service_tier/output_config/
	// context_management）进私有字段暂存，出站目标还是 Anthropic 时能原样恢复
	// （见 takeAnthropicParams）
	carryAnthropicParams(out, src)
	// 思考强度另外写一份到通用语顶层：出站目标不是 Anthropic 时（Gemini /
	// Responses / OpenAI 兼容上游）thinking 本身没有对应物，只能靠这个字段传递 ——
	// 上面那份暂存只在「出站还是 Anthropic」时被读回。
	if effort := anthropicThinkingEffort(src); effort != "" {
		out[reasoningEffortField] = effort
	}

	var messages []any
	// 用 isEmptyContent 而不是 sys != nil：没有 system 时上面返回的是空字符串，
	// 接口值本身不是 nil，直接判 nil 会插进一条空的 system 消息
	if sys := anthropicSystemToOpenAI(src["system"]); !isEmptyContent(sys) {
		messages = append(messages, map[string]any{"role": "system", "content": sys})
	}

	rawMsgs, _ := src["messages"].([]any)
	for _, item := range rawMsgs {
		m := asMap(item)
		if m == nil {
			continue
		}
		role := asString(m["role"])
		content := m["content"]

		// content 为纯字符串时直接透传，避免不必要的结构改写
		if s, ok := content.(string); ok {
			messages = append(messages, map[string]any{"role": role, "content": s})
			continue
		}

		blocks, _ := content.([]any)
		var textParts []any
		var toolCalls []any
		var toolResults []any
		var privateBlocks []any

		for _, b := range blocks {
			blk := asMap(b)
			if blk == nil {
				continue
			}
			switch asString(blk["type"]) {
			case "text":
				part := map[string]any{
					"type": "text", "text": asString(blk["text"]),
				}
				carryCacheControl(part, blk)
				textParts = append(textParts, part)
			case "image":
				if part := anthropicImageToOpenAI(blk); part != nil {
					carryCacheControl(part, blk)
					textParts = append(textParts, part)
				}
			case "tool_use":
				args, _ := json.Marshal(blk["input"])
				toolCall := map[string]any{
					"id":   asString(blk["id"]),
					"type": "function",
					"function": map[string]any{
						"name":      asString(blk["name"]),
						"arguments": string(args),
					},
				}
				carryCacheControl(toolCall, blk)
				toolCalls = append(toolCalls, toolCall)
			case "tool_result":
				// Anthropic 把工具结果塞在 user 消息里，OpenAI 要求独立的 tool 消息
				result := map[string]any{
					"role":         "tool",
					"tool_call_id": asString(blk["tool_use_id"]),
					"content":      flattenToolResult(blk["content"]),
				}
				// 通用语表达不了的块（tool_reference 等）另存原文：
				// 上面的 content 是给非 Anthropic 上游的降级形态，出站还是
				// Anthropic 时用这份原文还原（见 anthropicToolResultKey）
				if toolResultNeedsCarry(blk["content"]) {
					result[anthropicToolResultKey] = blk["content"]
				}
				carryCacheControl(result, blk)
				toolResults = append(toolResults, result)
			default:
				// thinking / redacted_thinking / document 以及未来的新块类型：
				// OpenAI 通用语没有对应物，原样收进消息级私有容器（见
				// anthropicBlocksKey），出站还是 Anthropic 时按原顺序还原。
				//
				// thinking 不能丢：Anthropic 要求带 tool_use 的 assistant 轮
				// 回传思维链块（含 signature），丢了上游直接 400；
				// redacted_thinking 的 data、document 的 source 同理原样保真。
				privateBlocks = append(privateBlocks, blk)
			}
		}

		// Anthropic 把 tool_result 放在 user 回合的开头，而 OpenAI 要求 role=tool
		// 的消息紧跟在触发它的 assistant 消息之后、下一条 user 消息之前。
		// 顺序反了上游会直接报「tool 消息必须回应前面的 tool_calls」。
		messages = append(messages, toolResults...)

		// 纯 tool_result 的回合不应再产生一条空的 user 消息；
		// 带私有块（如整条 PDF document）的回合必须保留
		if len(textParts) == 0 && len(toolCalls) == 0 && len(privateBlocks) == 0 {
			continue
		}

		msg := map[string]any{"role": role}
		if len(privateBlocks) > 0 {
			msg[anthropicBlocksKey] = privateBlocks
		}
		// 只有一个 text 块时收成纯字符串，报文更简单、上游兼容性也更好。
		// 但它带着 cache_control 时必须保留数组形态 —— 收成字符串就把
		// 缓存断点丢了，而断点通常正好落在最后一条 user 消息上。
		var singleText map[string]any
		if len(textParts) == 1 {
			singleText = asMap(textParts[0])
		}
		if singleText != nil && asString(singleText["type"]) == "text" && takeCacheControl(singleText) == nil {
			msg["content"] = asString(singleText["text"])
		} else if len(textParts) > 0 {
			msg["content"] = textParts
		} else {
			msg["content"] = ""
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		messages = append(messages, msg)
	}

	out["messages"] = messages
	return json.Marshal(out)
}

// anthropicSystemToOpenAI 把 Anthropic 的 system 字段转成通用语里的 content。
//
// system 可以是纯字符串，也可以是内容块数组。是数组、且其中带 cache_control 时，
// 转成 OpenAI 的多模态数组形式并**保留缓存断点**（见 carryCacheControl）；
// 否则拍平成纯文本，保持与原来一致的最简形态。
//
// 为什么必须保留：Anthropic 的提示缓存完全靠 cache_control 断点驱动。
// 丢掉它之后上游永远不会有缓存命中 —— 多轮对话每一轮都按全量输入计费，
// 长 system prompt 的场景成本会差好几倍，而用户看到的只是「缓存命中率一直是 0」，
// 很难联想到是中转站把断点吃掉了。
func anthropicSystemToOpenAI(v any) any {
	switch s := v.(type) {
	case string:
		return s
	case []any:
		parts := make([]any, 0, len(s))
		hasBreakpoint := false
		for _, item := range s {
			blk := asMap(item)
			if blk == nil {
				continue
			}
			text := asString(blk["text"])
			if text == "" {
				continue
			}
			part := map[string]any{"type": "text", "text": text}
			if cc := asMap(blk["cache_control"]); cc != nil {
				part["cache_control"] = cc
				hasBreakpoint = true
			}
			parts = append(parts, part)
		}
		if len(parts) == 0 {
			return ""
		}
		// 没有断点时保持原来的纯文本形态：上游 OpenAI 兼容端点对
		// 内容数组的支持参差不齐，没必要为一组纯文本引入数组
		if !hasBreakpoint {
			texts := make([]string, 0, len(parts))
			for _, p := range parts {
				texts = append(texts, asString(asMap(p)["text"]))
			}
			return strings.Join(texts, "\n")
		}
		return parts
	default:
		return ""
	}
}

// cacheControlKey 是通用语里承载 Anthropic 提示缓存断点的附加字段。
//
// 为什么需要一个"私有"字段：站内通用语是 OpenAI Chat 形状，而 OpenAI 的请求体
// 里没有与 cache_control 对应的概念。中转站要支持「入站 Anthropic -> 出站 Anthropic」
// 时保住断点，就必须有个地方暂存它。
//
// 安全性：这个字段只在**出站目标也是 Anthropic** 时才被重新读出来
// （见 upstream_anthropic.go 的 takeCacheControl）。发给其它上游前会被
// stripCacheControl 清掉，因为各家对未知字段的态度不一，严格校验的会直接 400。
const cacheControlKey = "cache_control"

// anthropicParamsKey 是通用语里承载 Anthropic 特有**顶层参数**的附加字段，
// 与 cache_control 同一思路（见上），但装的是请求级配置而非块级断点：
// thinking（扩展思考及其 budget_tokens）、top_k、metadata、service_tier，
// 以及 4.6 代的 output_config（effort 记在这里）与 context_management。
//
// 这些参数在 OpenAI 通用语里没有对应物，白名单不抄就静默失效 ——
// Claude 客户端开了 extended thinking，中继 Anthropic→Anthropic 链路上
// 模型却不输出思维链，行为变化无任何信号，计费口径也跟着变。
// 只在出站目标也是 Anthropic 时被读回（takeAnthropicParams），
// 发给其它上游前随 stripCacheControl 一起清掉。
const anthropicParamsKey = "anthropic_params"

// anthropicBlocksKey 是通用语里承载 Anthropic 私有**内容块**的消息级附加字段，
// 与 anthropicParams 同一思路，但装的是消息里的块而非请求级配置：
// thinking / redacted_thinking（assistant 轮的思维链与加密思维链）、
// document（user 轮的 PDF 输入），以及 OpenAI 通用语没有对应物的其它新块。
//
// 为什么必须暂存而不是丢弃：
//   - Anthropic 要求带 tool_use 的 assistant 轮回传 thinking 块（signature
//     原样），丢了上游直接 400 —— Claude Code 在思考 + 工具调用场景每轮都在回传；
//   - document 丢了等于 PDF 输入被「模型无视」，客户端毫无感知。
//
// 块内所有字段（含 signature / source / cache_control）原样保真，
// 出站目标还是 Anthropic 时还原到该消息内容块的最前（thinking 必须在
// tool_use 之前，见 upstream_anthropic.go 的 prependAnthropicBlocks）；
// 发给其它上游前随 stripCacheControl 一起清掉。
const anthropicBlocksKey = "anthropic_blocks"

// deferLoadingKey 是工具条目上「延迟加载」标记的暂存字段。
//
// Claude Code 的工具搜索（beta advanced-tool-use-2025-11-20）把 MCP 工具的
// schema 从首轮请求里拿掉：tools 里只留一个 DeferredToolPlaceholder，带
// defer_loading: true；模型调 ToolSearch 后客户端在本地检索，再把完整 schema
// **仍带 defer_loading: true** 塞回 tools。语义完全在客户端，上游只需要
// 「不因为多了这个字段而拒绝」。
//
// 用原名字而不是再造一个 anthropic_ 前缀：出站目标是 Anthropic 时原样写回
// （官方认它），其它上游由 stripCacheControl 清掉。名字若改成私有前缀，
// 反而要多一次「还原成原名」的映射，没有收益。
const deferLoadingKey = "defer_loading"

// anthropicToolResultKey 是 tool 消息上暂存「Anthropic 原始 tool_result 内容」的
// 消息级附加字段，与 anthropicBlocksKey 同一思路。
//
// 需要它的块目前是 tool_reference（工具搜索把「某工具已解锁」表达为
// {"type":"tool_reference","tool_name":"..."}，不含 schema）。通用语里它被
// 降级成一行 [Tool loaded: ...] 文本 —— 对 OpenAI 兼容上游这是能读懂的提示，
// 但对 Anthropic 上游就是改写：原块表达的是结构化引用，且未来可能带更多字段。
// 入站时把原始数组整个暂存，出站还是 Anthropic 就原样放回。
const anthropicToolResultKey = "anthropic_tool_result"

// carryAnthropicParams 把 Anthropic 特有顶层参数抄进通用语的私有字段。
//
// output_config 与 context_management 是 4.6 代客户端的报文形状：
// 前者的 effort 是新的思考强度表达（Claude Code 就发它），后者是服务端
// 上下文管理配置。不进白名单的话它们在 Anthropic→Anthropic 链路上被丢掉，
// 表现为「思考开着但强度丢失」—— 与本项目此前踩过的同一类缺口。
func carryAnthropicParams(dst, src map[string]any) {
	if dst == nil || src == nil {
		return
	}
	extra := map[string]any{}
	for _, k := range []string{"thinking", "top_k", "metadata", "service_tier", "output_config", "context_management"} {
		if v, ok := src[k]; ok {
			extra[k] = v
		}
	}
	if len(extra) > 0 {
		dst[anthropicParamsKey] = extra
	}
}

// takeAnthropicParams 从通用语里取出暂存的 Anthropic 特有参数（供出站 Anthropic 使用）。
func takeAnthropicParams(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	return asMap(src[anthropicParamsKey])
}

// carryCacheControl 把源块上的 cache_control 抄到目标块上。
func carryCacheControl(dst, src map[string]any) {
	if dst == nil || src == nil {
		return
	}
	if cc := asMap(src[cacheControlKey]); cc != nil {
		dst[cacheControlKey] = cc
	}
}

// takeCacheControl 从块上取出 cache_control（供出站 Anthropic 使用）。
func takeCacheControl(blk map[string]any) map[string]any {
	if blk == nil {
		return nil
	}
	return asMap(blk[cacheControlKey])
}

// stripCacheControl 递归移除通用语里的私有附加字段（cache_control 与
// anthropic_params、anthropic_blocks、anthropic_tool_result、defer_loading）。
//
// 用于出站目标是「非 Anthropic」协议的场景：那些上游不认识这些字段，
// 严格校验的实现会直接 400。与其赌它被忽略，不如主动清掉。
//
// defer_loading 一并清掉：它对 OpenAI 兼容上游没有任何意义，而
// 「多了一个字段就让整条请求 400」正是工具搜索要避免的失败模式。
func stripCacheControl(v any) any {
	switch t := v.(type) {
	case map[string]any:
		delete(t, cacheControlKey)
		delete(t, anthropicParamsKey)
		delete(t, anthropicBlocksKey)
		delete(t, anthropicToolResultKey)
		delete(t, deferLoadingKey)
		for _, sub := range t {
			stripCacheControl(sub)
		}
		return t
	case []any:
		for _, sub := range t {
			stripCacheControl(sub)
		}
		return t
	default:
		return v
	}
}

// toolResultNeedsCarry 判断 tool_result 的内容里是否有通用语无法表达的块。
//
// text / image 都在通用语里有对应形态（字符串与 image_url），其余类型
// （目前是工具搜索的 tool_reference，将来可能还有新的）降级后会丢失结构，
// 只留下给非 Anthropic 上游看的一行文本。判据独立成函数而不是内联在
// 转换循环里：它决定 anthropic_tool_result 要不要写入，写错了上层链路
// 会在 Anthropic→Anthropic 往返里静默改写内容。
func toolResultNeedsCarry(content any) bool {
	arr, ok := content.([]any)
	if !ok {
		return false
	}
	for _, item := range arr {
		blk := asMap(item)
		if blk == nil {
			continue
		}
		switch asString(blk["type"]) {
		case "text", "image":
			// 通用语有对应形态，不需要原文
		default:
			return true
		}
	}
	return false
}

// flattenToolResult 把工具结果的内容转换成 OpenAI 的 tool 消息 content。
//
// 纯文本（最常见）返回字符串，报文最简；含图片块时返回多模态数组
// （text / image_url 与入站 image 块同一通用形态）—— 工具返回截图时
// 拍平成空字符串会让模型按「空结果」继续推理，看起来正常、实际缺数据。
//
// tool_reference（Claude Code 工具搜索：客户端告诉模型「这个工具已解锁」）
// 降级成一行文本。丢掉它也不算错（需求方原话：丢掉也可以），但模型少一条
// 「工具已解锁」的提示，可能继续用 ToolSearch 而不是直接调用 —— 一行文本
// 的成本几乎为零，保留信息更稳妥。Anthropic→Anthropic 往返不经过这条
// 降级路径：原文由 anthropicToolResultKey 暂存后原样还原。
func flattenToolResult(v any) any {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var texts []string
		var rich []any
		hasImage := false
		for _, item := range c {
			blk := asMap(item)
			if blk == nil {
				continue
			}
			switch asString(blk["type"]) {
			case "text":
				texts = append(texts, asString(blk["text"]))
				rich = append(rich, map[string]any{"type": "text", "text": asString(blk["text"])})
			case "image":
				if part := anthropicImageToOpenAI(blk); part != nil {
					rich = append(rich, part)
					hasImage = true
				}
			case "tool_reference":
				// 工具搜索的「已解锁」标记，见函数头注释。
				// 工具名可能缺失（上游报文不规范时），此时仍留一行，
				// 免得整个结果变成空字符串、模型以为工具什么都没返回。
				line := "[Tool loaded: " + asString(blk["tool_name"]) + "]"
				texts = append(texts, line)
				rich = append(rich, map[string]any{"type": "text", "text": line})
			}
		}
		if !hasImage {
			// 无图片时保持字符串形态：与旧行为逐字节一致，不放大报文
			return strings.Join(texts, "\n")
		}
		return rich
	default:
		return ""
	}
}

// anthropicImageToOpenAI 转换图片块。Anthropic 用 base64/url 的 source，OpenAI 用 data URI。
func anthropicImageToOpenAI(blk map[string]any) map[string]any {
	src := asMap(blk["source"])
	if src == nil {
		return nil
	}
	var url string
	switch asString(src["type"]) {
	case "base64":
		url = "data:" + asString(src["media_type"]) + ";base64," + asString(src["data"])
	case "url":
		url = asString(src["url"])
	default:
		return nil
	}
	if url == "" {
		return nil
	}
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}
}

// anthropicToolsToOpenAI 把 Anthropic 的 tools 转成 OpenAI 的 function 工具。
//
// 只抄 name / description / input_schema：其余字段（含工具搜索的
// defer_loading）在 OpenAI 兼容上游没有意义，进通用语就会被出站剥离。
// 唯一例外是 defer_loading —— 它在出站目标还是 Anthropic 时要原样写回，
// 所以暂存在工具条目顶层（见 deferLoadingKey）。
func anthropicToolsToOpenAI(v any) []any {
	list, _ := v.([]any)
	outTools := make([]any, 0, len(list))
	for _, item := range list {
		t := asMap(item)
		if t == nil {
			continue
		}
		fn := map[string]any{"name": asString(t["name"])}
		if d := asString(t["description"]); d != "" {
			fn["description"] = d
		}
		if s, ok := t["input_schema"]; ok {
			fn["parameters"] = s
		}
		entry := map[string]any{"type": "function", "function": fn}
		if dl, ok := t[deferLoadingKey]; ok {
			entry[deferLoadingKey] = dl
		}
		outTools = append(outTools, entry)
	}
	return outTools
}

func anthropicToolChoiceToOpenAI(v any) any {
	m := asMap(v)
	if m == nil {
		if s, ok := v.(string); ok {
			return s
		}
		return nil
	}
	switch asString(m["type"]) {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": asString(m["name"])},
		}
	default:
		return nil
	}
}
