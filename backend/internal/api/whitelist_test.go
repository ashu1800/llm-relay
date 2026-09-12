package api

import (
	"testing"

	"llm-relay/internal/model"
)

// 白名单为空表示不限制 —— 这与「配了但配错」是两种状态，不能混为一谈。
// 分不清就会出现「没配白名单的密钥突然调不通」这种问题。
func TestModelAllowed(t *testing.T) {
	cases := []struct {
		name  string
		list  model.StringList
		model string
		want  bool
	}{
		{"没配白名单", nil, "any-model", true},
		{"配了空数组（视同没配）", model.StringList{}, "any-model", true},
		{"命中白名单", model.StringList{"a", "b"}, "b", true},
		{"不在白名单", model.StringList{"a", "b"}, "c", false},
		{"大小写敏感", model.StringList{"GPT-4o"}, "gpt-4o", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := modelAllowed(c.list, c.model); got != c.want {
				t.Errorf("modelAllowed(%v, %q) = %v，期望 %v", c.list, c.model, got, c.want)
			}
		})
	}
}
