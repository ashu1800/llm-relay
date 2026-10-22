package relay

import (
	"net/http"
	"strings"
)

// 入站客户端的识别：从请求头认出「这次调用是谁发来的」，随日志落一份快照
// （client_id / client_name / client_ua）。只观察、不改写 —— 与
// ExtractThinkingLevel / ExtractModel 是同一种风格，也在同一个地方被调用
// （api/relay_handler.go 构造 RelayRequest 时）。
//
// 为什么要识别：同一个密钥往往被多个客户端共用（Claude Code、Cursor、SDK、
// 脚本），只看「哪把密钥」回答不了「这批异常流量来自哪个客户端」。
//
// 三条设计原则：
//
//  1. **诚实展示优于猜测**（与思考档位透传同一条）：认不出就不假装认识 ——
//     id 取客户端自报的第一个 product token（不是猜一个「未知客户端」），
//     name 与 id 同源。认出但没来得及加映射的，id 也要落库，它比任何猜测都准确。
//
//     为什么未识别时 id 也必须有值（2026-10-22 生产事故）：id 曾经在未命中
//     规则时留空，而日志里只有 id 是机器可用的稳定键 —— 于是未识别的客户端
//     （如 deepseek-harness）每条日志的 client_id 都是空串，按客户端维度的
//     统计、筛选、限流全部失效。取 token 不违背「不猜」：这是客户端自己
//     报的名字，与 client_name 同一个来源、同一个值。
//     代价是它与「规则表里认出的 id」不再能靠空/非空区分，要看它是否出现在
//     规则表里；显示层（clientDisplay.ts）本来也是按 id || name 取键，两边一致。
//
//  2. **有据可查**：UA 原文随日志落库（client_ua）。规则表里每一条
//     「待实测」的匹配都是猜测，而排障时真正能定位问题的证据是原文 ——
//     上线后从库里捞「id 空但 ua 非空」的行，照着它们补规则，这是这个
//     功能能持续变准的闭环。
//
//  3. **规则表是一次性快照，不是自动学习的**：客户端会不断冒出来，
//     每次补规则都要连同 display name 一起写在这里 —— 显示名是给人看的，
//     宁可长一点（"OpenAI Python SDK"）也不要缩写（"openai-py"）。

// ExtractClient 从入站请求头识别客户端。
//
// 返回 (id, name, ua)：
//   - 命中规则：id/name 来自规则表，ua 是原文（TrimSpace + 截到列宽以内）
//   - 未命中：name 是 UA 的第一个 product token（去版本、去尾部标点），
//     id **与 name 相同** —— id 是机器可用的稳定键，留空会让该客户端在
//     日志里彻底失去身份（详见上面的设计原则第 1 条）
//   - 没有 UA：三元组全空串 —— 前端按「当时没记」显示 —，与 client_ip 同一种空值语义
//
// 入口收 http.Header 而不是 UA 字符串：辅助信号（x-app 这类客户端自报头）
// 必须与 UA 一起进判定 —— 有的客户端（如某些 VSCode 插件）的 UA 是运行环境
// 给的模板，反而是这些自报头更能说明它是谁。
func ExtractClient(h http.Header) (id, name, ua string) {
	ua = strings.TrimSpace(h.Get("User-Agent"))
	// 出口截断在这里统一做：调用方（BuildLog）不该关心长度，
	// 而 client_ua 是 varchar(1024) —— 超长 UA 会把整条日志写失败，
	// 那恰恰是排障最需要的那条（与 Error 列的 TruncateRunes 同一个坑）。
	// 限值取 1023 而不是 1024：TruncateRunes 的产出是「前 limit 个字符 +
	// 省略号」，Postgres 的 varchar 按字符数强校验，1024 + 1 = 1025 放不进
	// varchar(1024)（Error 列截 1000 进 1024 的列，留的就是这份余量）
	ua = TruncateRunes(ua, 1023)

	// 小写化只做一次：规则表里几十条都要按大小写不敏感比对
	lua := strings.ToLower(ua)
	for _, r := range clientRules {
		if r.match(h, lua) {
			return r.id, r.name, ua
		}
	}
	name = firstProductToken(ua)
	return name, name, ua
}

// clientRule 是一条识别规则：UA 子串命中，或（hdr 非空且）头信号命中。
// 绝大多数规则只有 ua。
type clientRule struct {
	id   string
	name string
	// ua 是 UA 里要出现的子串（大小写不敏感），任一命中即可
	ua []string
	// hdr 是 header 名 → 期望值子串（大小写不敏感）；空 = 不看头。
	// 头信号通常比 UA 可靠：客户端自己加的头不受运行环境（Electron 壳、
	// Node 的 undici、Go 的 http.Transport）改写，而 UA 常被这些环境顶掉。
	hdr map[string]string
}

// match 判本条规则是否命中。lua 是调用方预先小写化的 UA ——
// 由调用方算而不是在这里算，是因为规则表有几十条：
// 每条都重新 ToLower 一遍同一份 UA 是无谓的重复。
func (r clientRule) match(h http.Header, lua string) bool {
	for _, s := range r.ua {
		if strings.Contains(lua, s) {
			return true
		}
	}
	for k, want := range r.hdr {
		if strings.Contains(strings.ToLower(h.Get(k)), want) {
			return true
		}
	}
	return false
}

// clientRules 是按**声明顺序**匹配的规则表，第一个命中即返回。
//
// 顺序不是按长度排的，是手工维护的优先级 —— 每一条都对应一类真实来源，
// 顺序错了会把客户端认成它的载体（下面「顺序陷阱」一节的每一条都有单测）：
//
//   - 头信号规则排最前：x-app 这类自报头是客户端主动加的，比 UA 更接近真相；
//   - 具体客户端（CLI、桌面应用）排在通用 HTTP 库之前：okhttp 是 Android
//     上多家 App 的壳、undici/node-fetch 是 Node 生态的底层 fetch 实现，
//     先匹配通用库会把它们全都吞掉；
//   - 浏览器规则必须**最后**：Mozilla/5.0 前缀被太多库借用（Postman 默认 UA
//     就是它），先匹配它等于让后面所有规则失效。
//
// 不许出现的规则（踩过的坑，写下来免得有人再补）：「python」「go」「node」
// 「ai」这种两三个字母的子串单独成条会命中一大片无关 UA —— 库的标识必须
// 带着自己的 `-`/`/` 一起出现（`go-http-client/`、`node-fetch`）。
//
// 覆盖不了的（Electron 壳的 UA 常是应用名 + 版本，形态各异）：
// 上线后按 client_ua 原文补，见 ExtractClient 的注释第 2 条。
var clientRules = []clientRule{
	// ---------- 头信号（最高优先）----------
	// Claude Code 官方 CLI 除了 UA 还发 x-app 头；UA 被环境改写时它仍在
	// （客户端自己加的，不受 Electron/Node 影响）
	{id: "claude-code", name: "Claude Code", hdr: map[string]string{"x-app": "claude-code"}},

	// ---------- AI 编码 CLI / 代码代理 ----------
	// claude-cli/2.1.260 (external, cli) 是官方 CLI 的真实形态（抓包确认）；
	// 前缀带 / 避免命中别的含 claude 的串
	{id: "claude-code", name: "Claude Code", ua: []string{"claude-cli/"}},
	// Codex 官方客户端族的自报名（含下划线/连字符的确定字面量，来自对
	// codex-rs 源码与真实流量的整理）—— 不能用裸 "codex"：会吞掉
	// evil-codex 之类。尾部裸 "codex "（带空格）单独留一条兜底桌面版
	{id: "codex", name: "Codex CLI", ua: []string{
		"codex_cli_rs/", "codex-tui/", "codex_vscode/", "codex_vscode_copilot/",
		"codex_app/", "codex_chatgpt_desktop/", "codex_atlas/", "codex_exec/",
		"codex_sdk_ts/", "codex ",
	}},
	{id: "cursor", name: "Cursor", ua: []string{"cursor/"}},
	// Codeium 已改名 Windsurf，旧版本仍可能带旧名，两个一起认
	{id: "windsurf", name: "Windsurf", ua: []string{"windsurf/", "codeium/"}},
	// Roo 系两代名字（Roo Cline → Roo Code）都归到 Roo Code；
	// claude-dev 是 Cline 的前身名
	{id: "roo-code", name: "Roo Code", ua: []string{"roo-code/", "roocode/", "roo-cline/"}},
	{id: "kilo-code", name: "Kilo Code", ua: []string{"kilo-code/", "kilocode/"}},
	{id: "cline", name: "Cline", ua: []string{"cline/", "claude-dev/"}},
	{id: "continue", name: "Continue", ua: []string{"continue.dev", "continuedev/"}},
	{id: "aider", name: "Aider", ua: []string{"aider/"}},
	{id: "opencode", name: "OpenCode", ua: []string{"opencode/"}},
	{id: "gemini-cli", name: "Gemini CLI", ua: []string{"geminicli/", "gemini-cli/"}},
	{id: "qwen-code", name: "Qwen Code", ua: []string{"qwencode/", "qwen-code/", "qwencli/"}},
	{id: "trae", name: "Trae", ua: []string{"trae/"}},
	// ZCode（Z.ai 的桌面编码客户端）。两条信号，都有实测依据（3.15.3 的
	// model-io 请求日志 + 客户端 zcode.cjs 的源码形态）：
	//   · 官方通道：UA 形如 ZCode/3.15.3 —— 版本随发版走，只锚定前缀；
	//   · 自定义 provider（中转/自建端点走的就是这条）：UA 是裸 Vercel AI
	//     SDK 形态（ai-sdk/openai-compatible/… 由 SDK 的 fetch 注入，ZCode
	//     不改写、也不追加自己的名字），产品身份只能靠自报头 —— 在 ZCode
	//     的模型服务配置里加一个自定义请求头 x-app: zcode 即可命中。
	// 必须排在下面的 SDK 规则之前，否则先被 ai-sdk/ 吞掉（本条正是为
	// 「ZCode 的请求被显示成 Vercel AI SDK」这个 case 补的）。
	{id: "zcode", name: "ZCode", ua: []string{"zcode/"}, hdr: map[string]string{"x-app": "zcode"}},

	// ---------- 桌面 / Web 聊天客户端 ----------
	{id: "cherry-studio", name: "Cherry Studio", ua: []string{"cherrystudio/", "cherry-studio/"}},
	{id: "lobechat", name: "LobeChat", ua: []string{"lobe-chat", "lobechat/", "lobehub"}},
	{id: "chatbox", name: "ChatBox", ua: []string{"chatbox/"}},
	{id: "nextchat", name: "NextChat", ua: []string{"nextchat/", "chatgpt-next-web/"}},
	{id: "sillytavern", name: "SillyTavern", ua: []string{"sillytavern/"}},
	{id: "open-webui", name: "Open WebUI", ua: []string{"open-webui/"}},
	{id: "lm-studio", name: "LM Studio", ua: []string{"lmstudio/", "lm studio"}},

	// ---------- SDK（官方 UA 形态已核源码）----------
	// OpenAI Python: f"{class}/Python {version}"（类名 OpenAI、AsyncOpenAI）
	// OpenAI Node:   f"{constructor.name}/JS {version}"
	// Anthropic 两家 SDK 同一套 stainless 机制，故形态一致
	{id: "openai-python", name: "OpenAI Python SDK", ua: []string{"openai/python", "asyncopenai/python"}},
	{id: "openai-node", name: "OpenAI Node SDK", ua: []string{"openai/js", "asyncopenai/js"}},
	{id: "anthropic-python", name: "Anthropic Python SDK", ua: []string{"anthropic/python", "asyncanthropic/python"}},
	{id: "anthropic-ts", name: "Anthropic TypeScript SDK", ua: []string{"anthropic/js", "anthropicai/js"}},
	{id: "google-genai", name: "Google GenAI SDK", ua: []string{"google-genai/", "google-generativeai/"}},
	{id: "langchain", name: "LangChain", ua: []string{"langchain"}},
	{id: "vercel-ai-sdk", name: "Vercel AI SDK", ua: []string{"ai-sdk/"}},
	{id: "github-copilot", name: "GitHub Copilot", ua: []string{"githubcopilot/", "copilot/"}},

	// ---------- 通用 HTTP 库（放最后）----------
	// 这一族是最宽的一档：谁被更具体的规则认走，就不该落到这里。
	{id: "curl", name: "curl", ua: []string{"curl/"}},
	{id: "wget", name: "Wget", ua: []string{"wget/"}},
	{id: "go-http-client", name: "Go HTTP 客户端", ua: []string{"go-http-client/"}},
	// Python-urllib 是标准库自带的，比 requests 还常见（脚本一行 urlopen 就用它）——
	// 实测的 e2e 里它是数量最多的来源，不认出来会一直显示 fallback 名
	{id: "python-urllib", name: "Python urllib", ua: []string{"python-urllib/"}},
	{id: "python-requests", name: "Python requests", ua: []string{"python-requests/"}},
	{id: "httpx", name: "HTTPX", ua: []string{"python-httpx/", "httpx/"}},
	{id: "aiohttp", name: "AIOHTTP", ua: []string{"aiohttp/"}},
	{id: "axios", name: "Axios", ua: []string{"axios/"}},
	{id: "node-fetch", name: "node-fetch", ua: []string{"node-fetch"}},
	{id: "undici", name: "undici", ua: []string{"undici"}},
	{id: "okhttp", name: "OkHttp", ua: []string{"okhttp/"}},
	{id: "postman", name: "Postman", ua: []string{"postmanruntime/"}},
	{id: "insomnia", name: "Insomnia", ua: []string{"insomnia/"}},
	// 浏览器排最后：mozilla/5.0 会被工具伪装，先匹配它会让上面所有规则失效
	{id: "browser", name: "浏览器", ua: []string{"mozilla/"}},
}

// ClientKey 返回客户端显示映射（model.ClientAlias）的查找键：识别出的
// id 非空取 id，否则取观测到的 name token。
//
// 未识别的客户端 id 与 name 同为 product token（见 ExtractClient），所以
// 这个键对它们而言与历史值一致 —— 已有的 client_aliases 行不会因为
// 「未识别时 id 不再留空」而失配。
//
// 同一口径有三份实现、注释互指：这里（权威）、store.backfillClientAliases
// 的 SQL 版、前端 clientDisplay.ts 的显示层版。改口径必须三处一起动，
// 否则登记与解析会对不上键。
func ClientKey(id, name string) string {
	if id != "" {
		return id
	}
	return name
}

// firstProductToken 取 UA 的第一个 product token（RFC 9110 的
// product = token ["/" product-version]），例如：
//
//	my-tool/1.0 (Windows NT 10.0)  → my-tool
//	SomeAgent                      → SomeAgent
//	(compatible; Foo/1.0)          → Foo（跳过开头的注释段）
//
// 保留原大小写（用户写的是什么就显示什么 —— 转小写是另一种改写）；
// 不做噪音过滤（把 Mozilla 当噪音丢掉是一种猜测，而这里的原则是不猜）。
// 结果按 client_name 的长度上限截断：display name 是我们可控的短串，
// 64 字符足够；超长 token 只能来自畸形请求，截断即可、不必特殊处理。
// 限值 63 而不是 64：TruncateRunes 会追加省略号，63 + 1 = 64 恰好列宽，
// 64 + 1 放不进 varchar(64)（与 ExtractClient 里 client_ua 同一条算术）。
func firstProductToken(ua string) string {
	for _, seg := range strings.Fields(ua) {
		// 跳过注释段（(compatible; Foo/1.0) 里真正有信息的是 Foo）
		if strings.HasPrefix(seg, "(") {
			continue
		}
		token := seg
		if i := strings.IndexByte(token, '/'); i >= 0 {
			token = token[:i]
		}
		// 去掉尾部的分号/逗号（SomeAgent, 这种）
		token = strings.TrimRight(token, ";,")
		if token == "" {
			continue
		}
		return TruncateRunes(token, 63)
	}
	return ""
}
