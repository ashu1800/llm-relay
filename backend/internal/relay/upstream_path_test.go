package relay

import "testing"

// 上游协议的路径拼接。
//
// 这里容易踩的坑：Anthropic 官方模板的 base_url 是 https://api.anthropic.com/v1，
// 而转换后给出的路径是 /v1/messages。BuildUpstreamURL 会去掉重复的 /v1，
// 但如果哪天有人把模板改成不带 /v1、或者让转换器输出不带前缀的路径，
// 就会出现 /v1/v1/messages 这种 404。两种写法都在这里钉住。
//
// 后来补上的第三个坑（真实故障）：智谱 GLM Coding Plan 的 OpenAI 协议
// base 是 https://open.bigmodel.cn/api/coding/paas/v4 —— 版本段不是 /v1。
// 老实现只认 /v1 与 /v1beta，于是拼成 /v4/v1/chat/completions，
// 管理界面的「测试连通性」报 404。这里把各家真实用过的 base 形态都钉住。
func TestUpstreamURLForAnthropicChannel(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{"https://api.anthropic.com/v1", "https://api.anthropic.com/v1/messages"},
		{"https://api.anthropic.com", "https://api.anthropic.com/v1/messages"},
		{"https://api.anthropic.com/", "https://api.anthropic.com/v1/messages"},
		// 智谱 Anthropic 协议：base 不带版本段，由 path 提供
		{"https://open.bigmodel.cn/api/anthropic", "https://open.bigmodel.cn/api/anthropic/v1/messages"},
	}
	for _, c := range cases {
		if got := BuildUpstreamURL(c.base, "/v1/messages"); got != c.want {
			t.Errorf("base=%s 时应拼成 %s，实际 %s", c.base, c.want, got)
		}
	}
}

// 版本段不是 /v1 的 base：不能再插入多余的版本段。
func TestUpstreamURLKeepsForeignVersionSegment(t *testing.T) {
	cases := []struct {
		name string
		base string
		path string
		want string
	}{
		// 智谱 GLM Coding Plan 的 OpenAI 协议端点（本次故障现场）
		{"智谱 Coding v4", "https://open.bigmodel.cn/api/coding/paas/v4", "/v1/chat/completions",
			"https://open.bigmodel.cn/api/coding/paas/v4/chat/completions"},
		// 智谱 OpenAI Responses 协议的 base（本次故障现场之二）
		{"智谱 api/v1", "https://open.bigmodel.cn/api/v1", "/v1/chat/completions",
			"https://open.bigmodel.cn/api/v1/chat/completions"},
		// 智谱通用端点 /api/paas/v4
		{"智谱 paas v4", "https://open.bigmodel.cn/api/paas/v4", "/v1/chat/completions",
			"https://open.bigmodel.cn/api/paas/v4/chat/completions"},
		// base 不带版本段时，版本段仍由 path 提供（OpenAI 官方、OHub、
		// 以及本仓库默认模板都属于这一类）
		{"无版本段 base", "https://api.openai.com", "/v1/chat/completions",
			"https://api.openai.com/v1/chat/completions"},
		{"无版本段且带尾斜杠", "https://llm.ohub.vip/", "/v1/chat/completions",
			"https://llm.ohub.vip/v1/chat/completions"},
		// Gemini：版本段是 v1beta，且路径里还有 models/<模型名>，
		// 去掉版本段时绝不能连 models 一起削掉
		{"Gemini 非流式", "https://generativelanguage.googleapis.com", "/v1beta/models/gemini-2.0-flash:generateContent",
			"https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent"},
		{"Gemini base 带 v1beta", "https://generativelanguage.googleapis.com/v1beta", "/v1beta/models/gemini-2.0-flash:generateContent",
			"https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent"},
		// 带查询串的路径：版本段后面的 ?alt=sse 必须保留
		{"Gemini 流式带查询串", "https://generativelanguage.googleapis.com/v1beta", "/v1beta/models/gemini-2.0-flash:streamGenerateContent?alt=sse",
			"https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:streamGenerateContent?alt=sse"},
		// base 为空（相对路径）时原样返回 path
		{"空 base", "", "/v1/chat/completions", "/v1/chat/completions"},
		// 普通路径段不能被当成版本段：/api 不是版本
		{"普通段不算版本", "https://example.com/api", "/v1/chat/completions",
			"https://example.com/api/v1/chat/completions"},
		// 只有字母没有数字的段不算版本：version / v 都不算
		{"version 不算版本", "https://example.com/version", "/v1/chat/completions",
			"https://example.com/version/v1/chat/completions"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := BuildUpstreamURL(c.base, c.path); got != c.want {
				t.Errorf("base=%s path=%s 时应拼成 %s，实际 %s", c.base, c.path, c.want, got)
			}
		})
	}
}

func TestIsVersionSegment(t *testing.T) {
	yes := []string{"v1", "v2", "v4", "v1beta", "V1", "v10", "v1alpha"}
	no := []string{"", "v", "version", "api", "openai", "v1x2", "1", "vv1", "v1.0"}
	for _, s := range yes {
		if !isVersionSegment(s) {
			t.Errorf("%q 应被判定为版本段", s)
		}
	}
	for _, s := range no {
		if isVersionSegment(s) {
			t.Errorf("%q 不应被判定为版本段", s)
		}
	}
}
