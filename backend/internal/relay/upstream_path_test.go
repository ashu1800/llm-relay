package relay

import "testing"

// 上游协议的路径拼接。
//
// 这里容易踩的坑：Anthropic 官方模板的 base_url 是 https://api.anthropic.com/v1，
// 而转换后给出的路径是 /v1/messages。BuildUpstreamURL 会去掉重复的 /v1，
// 但如果哪天有人把模板改成不带 /v1、或者让转换器输出不带前缀的路径，
// 就会出现 /v1/v1/messages 这种 404。两种写法都在这里钉住。
func TestUpstreamURLForAnthropicChannel(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{"https://api.anthropic.com/v1", "https://api.anthropic.com/v1/messages"},
		{"https://api.anthropic.com", "https://api.anthropic.com/v1/messages"},
		{"https://api.anthropic.com/", "https://api.anthropic.com/v1/messages"},
	}
	for _, c := range cases {
		if got := BuildUpstreamURL(c.base, "/v1/messages"); got != c.want {
			t.Errorf("base=%s 时应拼成 %s，实际 %s", c.base, c.want, got)
		}
	}
}
