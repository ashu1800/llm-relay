package api

import (
	"strings"
	"testing"

	"llm-relay/internal/model"
)

// order 接口要求 ids 恰好是该分组的全部渠道。对不上的四种情形都要被拦下，
// 并且错误消息要点名差在哪 —— 只说一句「400」的话，用户看不出是列表过期了
// 还是自己传错了分组。
func TestOrderValidationError(t *testing.T) {
	group := []model.Channel{{ID: 1}, {ID: 2}, {ID: 3}}

	t.Run("完整集合通过", func(t *testing.T) {
		if err := orderValidationError(group, []uint{3, 1, 2}); err != nil {
			t.Fatalf("顺序不同应当通过（顺序就是要提交的内容），却报错: %v", err)
		}
	})

	t.Run("缺少成员被拒且点名", func(t *testing.T) {
		err := orderValidationError(group, []uint{1, 2})
		if err == nil {
			t.Fatal("缺少成员应当被拒")
		}
		if !strings.Contains(err.Error(), "3") {
			t.Fatalf("错误消息应点名缺少的渠道 3，实际: %v", err)
		}
	})

	t.Run("多余成员被拒且点名", func(t *testing.T) {
		err := orderValidationError(group, []uint{1, 2, 3, 99})
		if err == nil {
			t.Fatal("夹带了别的分组的渠道应当被拒")
		}
		if !strings.Contains(err.Error(), "99") {
			t.Fatalf("错误消息应点名 99，实际: %v", err)
		}
	})

	t.Run("重复成员被拒", func(t *testing.T) {
		if err := orderValidationError(group, []uint{1, 1, 2, 3}); err == nil {
			t.Fatal("重复的 id 应当被拒")
		}
	})

	t.Run("空分组配空 ids 通过", func(t *testing.T) {
		if err := orderValidationError(nil, nil); err != nil {
			t.Fatalf("空分组没有可排序的对象，应当通过: %v", err)
		}
	})
}

// 策略收敛：只留三个；空值、已下线的 weighted、以及拼错的值都落到 failover。
//
// 后端此前完全不校验策略，任意字符串都能落库 —— 拼错的值会静默走到路由的
// 默认分支，而界面上看不出异常。
func TestNormalizeStrategy(t *testing.T) {
	cases := map[string]string{
		"":                       model.StrategyFailover,
		model.StrategyFailover:   model.StrategyFailover,
		model.StrategyRoundRobin: model.StrategyRoundRobin,
		"least_latency":          model.StrategyLeastLatency,
		"weighted":               model.StrategyFailover, // 已下线，收敛到顺序故障转移
		"WEIGHTED":               model.StrategyFailover, // 大小写不匹配也算不认识
		"fialover":               model.StrategyFailover, // 拼错
	}
	for in, want := range cases {
		if got := normalizeStrategy(in); got != want {
			t.Errorf("normalizeStrategy(%q) = %q，期望 %q", in, got, want)
		}
	}
}
