package relay

import (
	"strings"
	"testing"
)

// 「没有可用渠道」时的补充说明。
//
// 这段文案的每一句都对应一种具体的配置失误，写错了等于把人往错的方向引。
// 最要紧的是「半残的默认模型映射」那一句 —— 开关开着但没填模型名（或填的
// 名字不在自己白名单里）时，请求失败的样子与「完全没配」一模一样，
// 用户对着渠道列表看半天也找不出差在哪。保存接口会拦这种配置，
// 但直接改库、导入旧备份都可能留下它。
func TestModelsHintText(t *testing.T) {
	cases := []struct {
		name            string
		names           []string
		brokenFallback  int
		cooling         int
		fallbackCooling int
		wantContains    []string
		wantAbsent      []string
	}{
		{
			name:  "有可用模型",
			names: []string{"deepseek-chat", "deepseek-reasoner"},
			wantContains: []string{
				"当前范围内可用的模型有 deepseek-chat、deepseek-reasoner",
			},
			wantAbsent: []string{"默认模型映射", "冷却"},
		},
		{
			name:         "一条白名单都没有",
			names:        nil,
			wantContains: []string{"没有任何渠道配置模型白名单"},
			wantAbsent:   []string{"默认模型映射", "冷却"},
		},
		{
			name:           "有半残的兜底配置要点名",
			names:          []string{"deepseek-chat"},
			brokenFallback: 2,
			wantContains: []string{
				"当前范围内可用的模型有 deepseek-chat",
				"另有 2 条渠道开了「默认模型映射」但不会生效",
			},
		},
		{
			// 半残配置与正常渠道可能同时存在：只报其中一种会让用户
			// 以为另一种也没问题
			name:           "一条白名单都没有但仍有半残兜底",
			names:          nil,
			brokenFallback: 1,
			wantContains: []string{
				"没有任何渠道配置模型白名单",
				"另有 1 条渠道开了「默认模型映射」但不会生效",
			},
		},
		{
			name:           "半残数为 0 时不该出现那句话",
			names:          []string{"deepseek-chat"},
			brokenFallback: 0,
			wantContains:   []string{"当前范围内可用的模型有"},
			wantAbsent:     []string{"但不会生效"},
		},
		{
			// 2026-09-20 实测踩到：提示说「没有可用渠道」，紧接着又列出
			// 「可用的模型有 deepseek-v4.1-flash」—— 请求的就是那个模型。
			// 两者都没说谎，但读者只会更困惑。真相是那条渠道正在冷却
			// （连续失败 6 次、退避 30 秒），而冷却状态在内存里，
			// 下面这条 SQL 查不到它。必须由调用方把冷却数传进来点名。
			name:    "渠道都在冷却时必须说明白",
			names:   []string{"deepseek-v4.1-flash", "glm-5.3-flash"},
			cooling: 2,
			wantContains: []string{
				"当前范围内可用的模型有 deepseek-v4.1-flash、glm-5.3-flash",
				"2 条渠道能接这个模型",
				"冷却",
			},
		},
		{
			name:         "冷却数为 0 时不该出现冷却那句",
			names:        []string{"deepseek-chat"},
			cooling:      0,
			wantContains: []string{"当前范围内可用的模型有"},
			wantAbsent:   []string{"冷却"},
		},
		{
			name:           "冷却与半残同时存在，两句都要有",
			names:          []string{"deepseek-chat"},
			cooling:        1,
			brokenFallback: 1,
			wantContains: []string{
				"1 条渠道能接这个模型",
				"另有 1 条渠道开了「默认模型映射」但不会生效",
			},
		},
		{
			// 2026-09-20 实测：用户配了兜底，仍报「没有可用渠道」，而提示
			// 只说「1 条渠道能接这个模型」—— 那句数的是**精确命中**的渠道，
			// 完全没提兜底那条也因为冷却没能顶上。用户据此无法判断
			// 「是兜底没生效，还是兜底也被挡住了」。
			// 开兜底的渠道一起冷却时必须单独说一句。
			name:             "兜底渠道也冷却时要单独说明",
			names:            []string{"claude-sonnet-4-6"},
			cooling:          2,
			fallbackCooling:  1,
			wantContains: []string{
				"2 条渠道能接这个模型",
				"其中 1 条是开了「默认模型映射」的兜底渠道",
			},
		},
		{
			name:            "没有兜底冷却时不提那句",
			names:           []string{"claude-sonnet-4-6"},
			cooling:         1,
			fallbackCooling: 0,
			wantContains:    []string{"1 条渠道能接这个模型"},
			wantAbsent:      []string{"兜底渠道"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := modelsHintText(c.names, c.brokenFallback, c.cooling, c.fallbackCooling)
			for _, want := range c.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("提示里应包含 %q，实际 %q", want, got)
				}
			}
			for _, absent := range c.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("提示里不该出现 %q，实际 %q", absent, got)
				}
			}
		})
	}
}
