package relay

import (
	"net/http"
	"strings"
	"testing"
)

// 识别规则的用例矩阵。分五组：头信号、实测 UA、顺序陷阱、fallback、
// 边界与截断 —— 每一组对应设计里的一条约定，坏掉的表现都是「列上显示错」，
// 不会报错，所以必须逐条钉住。
func TestExtractClient(t *testing.T) {
	type want struct {
		id   string
		name string
	}
	cases := []struct {
		name    string
		ua      string
		headers map[string]string
		want    want
	}{
		// ---- 1. 头信号（最高优先）----
		{
			name:    "x-app 头命中 claude-code（UA 是 curl 也不影响）",
			ua:      "curl/8.0",
			headers: map[string]string{"X-App": "claude-code"},
			want:    want{"claude-code", "Claude Code"},
		},
		{
			name:    "x-app 头大小写不敏感",
			ua:      "curl/8.0",
			headers: map[string]string{"X-App": "Claude-Code"},
			want:    want{"claude-code", "Claude Code"},
		},
		{
			name:    "x-app 头值为空不算命中",
			ua:      "curl/8.0",
			headers: map[string]string{"X-App": ""},
			want:    want{"curl", "curl"},
		},

		// ---- 2. 实测 UA（有源码/抓包依据）----
		{
			name: "Claude Code 官方 CLI（真实抓包形态）",
			ua:   "claude-cli/2.1.260 (external, cli)",
			want: want{"claude-code", "Claude Code"},
		},
		{
			name: "Codex 交互式 TUI",
			ua:   "codex-tui/1.0.0",
			want: want{"codex", "Codex CLI"},
		},
		{
			name: "Codex VSCode 扩展",
			ua:   "codex_vscode/0.1.2",
			want: want{"codex", "Codex CLI"},
		},
		{
			name: "OpenAI Python SDK（源码形态 {class}/Python {ver}）",
			ua:   "OpenAI/Python 1.109.1",
			want: want{"openai-python", "OpenAI Python SDK"},
		},
		{
			name: "OpenAI Node SDK（源码形态 {class}/JS {ver}）",
			ua:   "OpenAI/JS 7.33.0",
			want: want{"openai-node", "OpenAI Node SDK"},
		},
		{
			name: "Anthropic Python SDK",
			ua:   "Anthropic/Python 0.69.0",
			want: want{"anthropic-python", "Anthropic Python SDK"},
		},
		{
			name: "Gemini CLI（源码形态 GeminiCLI/{ver}/{model}）",
			ua:   "GeminiCLI/0.24.0/gemini-2.5-pro (linux; x64; cli)",
			want: want{"gemini-cli", "Gemini CLI"},
		},
		{
			name: "OpenCode（源码形态 opencode/{channel}/{ver}/{name}）",
			ua:   "opencode/stable/0.5.0/opencode",
			want: want{"opencode", "OpenCode"},
		},
		{
			name: "ZCode 官方通道（实测形态 ZCode/<版本>，版本随发版走）",
			ua:   "ZCode/3.15.3",
			want: want{"zcode", "ZCode"},
		},
		{
			name: "curl 大写",
			ua:   "CURL/8.4.0",
			want: want{"curl", "curl"},
		},
		{
			name: "python-requests",
			ua:   "python-requests/2.32.3",
			want: want{"python-requests", "Python requests"},
		},
		{
			name: "go-http-client",
			ua:   "Go-http-client/2.0",
			want: want{"go-http-client", "Go HTTP 客户端"},
		},
		{
			name: "Python urllib（标准库，脚本一行 urlopen 就用它）",
			ua:   "Python-urllib/3.13",
			want: want{"python-urllib", "Python urllib"},
		},

		// ---- 3. 顺序陷阱（本表最容易坏的地方）----
		{
			name: "okhttp（Android 上多家 App 的壳，但自身要认出来）",
			ua:   "okhttp/4.12.0",
			want: want{"okhttp", "OkHttp"},
		},
		{
			name: "node-fetch 不被 undici/别的规则抢",
			ua:   "node-fetch/1.0 (+https://github.com/bitinn/node-fetch)",
			want: want{"node-fetch", "node-fetch"},
		},
		{
			name: "Postman 默认 UA 带 Mozilla 前缀，但必须认成 Postman 而不是浏览器",
			ua:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) PostmanRuntime/7.32.3",
			want: want{"postman", "Postman"},
		},
		{
			name: "真浏览器落到兜底 browser 规则",
			ua:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0.0.0 Safari/537.36",
			want: want{"browser", "浏览器"},
		},
		{
			name: "Roo Code 的两代名字都认",
			ua:   "roo-cline/3.0",
			want: want{"roo-code", "Roo Code"},
		},
		{
			name: "Windsurf 旧名 Codeium 也认",
			ua:   "codeium/1.2.3",
			want: want{"windsurf", "Windsurf"},
		},
		{
			name: "Cline 前身名 claude-dev 归到 Cline",
			ua:   "claude-dev/3.2.0",
			want: want{"cline", "Cline"},
		},
		{
			// ZCode 自定义 provider 的真实形态：UA 是裸 Vercel AI SDK（ZCode
			// 不改写），产品身份只在自报头里 —— zcode 规则必须排在 SDK 规则
			// 之前，先被 ai-sdk/ 吞掉正是这一条要防的回归
			name:    "ZCode 自定义 provider：UA 是裸 ai-sdk，靠 x-app 自报头认出",
			ua:      "ai-sdk/openai-compatible/1.0.6 runtime/node.js/v22.14.0",
			headers: map[string]string{"X-App": "zcode"},
			want:    want{"zcode", "ZCode"},
		},
		{
			// 对照组：没有自报头的裸 SDK UA 就该显示 SDK —— 那是线上仅有
			// 的诚实信号，不许为了「多认一个」把它抢走
			name: "裸 ai-sdk UA 无自报头仍归 Vercel AI SDK",
			ua:   "ai-sdk/openai-compatible/1.0.6 runtime/node.js/v22.14.0",
			want: want{"vercel-ai-sdk", "Vercel AI SDK"},
		},

		// ---- 4. fallback：认不出就展示客户端自报的第一个 product token ----
		// id 与 name 同值（2026-10-22 事故修复）：id 曾经留空，而它是日志里
		// 唯一机器可用的稳定键 —— 留空等于让未识别客户端彻底失去身份。
		{
			name: "未知工具取第一个 product token（id 与 name 同值）",
			ua:   "my-tool/1.0 (Windows NT 10.0)",
			want: want{"my-tool", "my-tool"},
		},
		{
			name: "无版本号的首 token 原样保留",
			ua:   "SomeAgent",
			want: want{"SomeAgent", "SomeAgent"},
		},
		{
			name: "首 token 带尾逗号时去掉标点",
			ua:   "SomeAgent, foo/1.0",
			want: want{"SomeAgent", "SomeAgent"},
		},
		{
			name: "首段是注释时跳过它（RFC 9110 的 comment 段）",
			ua:   "(compatible; Foo/1.0)",
			want: want{"Foo", "Foo"},
		},
		{
			name: "裸 Mozilla 不做噪音过滤（过滤本身是另一种猜测）",
			ua:   "Mozilla/5.0",
			want: want{"browser", "浏览器"}, // 注意：会被 browser 规则命中，不是 fallback
		},

		// ---- 5. 边界 ----
		{
			name: "空 UA 三元组全空",
			ua:   "",
			want: want{"", ""},
		},
		{
			name: "只有空格的 UA 与空 UA 同",
			ua:   "   ",
			want: want{"", ""},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := http.Header{}
			if c.ua != "" {
				h.Set("User-Agent", c.ua)
			}
			for k, v := range c.headers {
				h.Set(k, v)
			}
			gotID, gotName, gotUA := ExtractClient(h)
			if gotID != c.want.id || gotName != c.want.name {
				t.Fatalf("ExtractClient() = (%q, %q), want (%q, %q)  [ua=%q]",
					gotID, gotName, c.want.id, c.want.name, c.ua)
			}
			// ua 参数应当是原文（空 UA 时为空串）
			if strings.TrimSpace(c.ua) != "" && gotUA != strings.TrimSpace(c.ua) {
				t.Fatalf("ExtractClient() 第三参 = %q, 期望 UA 原文 %q", gotUA, c.ua)
			}
		})
	}
}

// ClientKey 是显示映射表的查找键派生（三处同口径的权威版）：
// id 非空取 id，否则取观测到的 name token。这条口径错一个字符，
// 登记与解析就对不上键 —— 映射永远不生效且不报错，必须钉住。
func TestClientKey(t *testing.T) {
	cases := []struct {
		id, name, want string
	}{
		{"claude-code", "Claude Code", "claude-code"}, // 识别出的：id 优先
		{"", "deepseek-harness", "deepseek-harness"},  // 未识别的：token 是唯一身份
		{"curl", "", "curl"},                          // 有 id 无 name（防御形态）
		{"", "", ""},                                  // 全空：不登记（调用方判空）
	}
	for _, c := range cases {
		if got := ClientKey(c.id, c.name); got != c.want {
			t.Fatalf("ClientKey(%q, %q) = %q, want %q", c.id, c.name, got, c.want)
		}
	}
}

// 超长 UA 与超长 product token 都要被截断，而且截完必须**放得进库里的列**：
// client_ua 是 varchar(1024)、client_name 是 varchar(64)，Postgres 按字符数
// 强校验 —— 多出一个字符，整条日志的 INSERT 就失败，那恰恰是排障最需要
// 的那条（与 Error 列同一个坑）。断言口径因此是「≤ 列宽」而不是
// 「≤ 限值 + 1」：TruncateRunes 的产出是「前 limit 个字符 + 省略号」，
// 第一版把限值写成列宽本身（1024 + 省略号 = 1025），边界值恰好放不进
// 自己的列 —— 调用方必须给省略号留位。
func TestExtractClientTruncates(t *testing.T) {
	longUA := strings.Repeat("x", 5000)
	_, _, ua := ExtractClient(http.Header{"User-Agent": []string{longUA}})
	if n := len([]rune(ua)); n > 1024 { // client_ua 的列宽
		t.Fatalf("超长 UA 截断后仍放不进 varchar(1024)：%d 字符", n)
	}
	if !strings.HasSuffix(ua, "…") {
		t.Fatalf("超长 UA 截断后应带省略号，尾部是 %q", ua[len(ua)-10:])
	}

	longToken := strings.Repeat("y", 200)
	_, name, _ := ExtractClient(http.Header{"User-Agent": []string{longToken}})
	if n := len([]rune(name)); n > 64 { // client_name 的列宽
		t.Fatalf("超长 product token 截断后仍放不进 varchar(64)：%d 字符", n)
	}
	if !strings.HasSuffix(name, "…") {
		t.Fatalf("超长 token 截断后应带省略号，得到 %q", name)
	}
}

// 空 header 不 panic：历史上 request_payload 与 live 推送都出现过
// 没带 header 的调用形态，这个函数在转发链路上，panic 会带走整条请求。
func TestExtractClientEmptyHeader(t *testing.T) {
	id, name, ua := ExtractClient(http.Header{})
	if id != "" || name != "" || ua != "" {
		t.Fatalf("空 header 应返回三个空串，得到 (%q, %q, %q)", id, name, ua)
	}
	id, name, ua = ExtractClient(nil)
	if id != "" || name != "" || ua != "" {
		t.Fatalf("nil header 应返回三个空串，得到 (%q, %q, %q)", id, name, ua)
	}
}
