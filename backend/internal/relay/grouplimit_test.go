package relay

import (
	"strings"
	"testing"
	"time"
)

// 分组 RPM：到点拦住、跨窗口恢复、不同分组互不影响。
func TestGroupLimiterRPM(t *testing.T) {
	l := NewGroupLimiter()
	now := time.Now()

	// 0 表示不限制：这是默认值，配错一个 0 就把分组卡死是不能接受的
	if ok, _ := l.Check(1, 0, 0, now); !ok {
		t.Error("rpm/tpm 都为 0 时必须放行")
	}

	if ok, _ := l.Check(1, 2, 0, now); !ok {
		t.Fatal("第一次应当放行")
	}
	l.Record(1, now)
	l.Record(1, now)
	if ok, reason := l.Check(1, 2, 0, now); ok {
		t.Error("已用满 2 次后必须拦住")
	} else if !strings.Contains(reason, "请求数") {
		t.Errorf("原因要说明是哪一项超了，实际 %q", reason)
	}

	// 跨过分钟边界就恢复：固定窗口的语义
	if ok, _ := l.Check(1, 2, 0, now.Add(time.Minute)); !ok {
		t.Error("进入下一分钟应当恢复额度")
	}

	// 另一个分组不受影响
	if ok, _ := l.Check(2, 2, 0, now); !ok {
		t.Error("其它分组不该被牵连")
	}
}

// 分组 TPM：按上游回报的 token 累计，累计到的窗口里拦住后续请求。
func TestGroupLimiterTPM(t *testing.T) {
	l := NewGroupLimiter()
	now := time.Now()

	l.AddTokens(7, 900, now)
	if ok, _ := l.Check(7, 0, 1000, now); !ok {
		t.Error("900 < 1000，应当仍放行")
	}
	l.AddTokens(7, 200, now)
	ok, reason := l.Check(7, 0, 1000, now)
	if ok {
		t.Error("累计 1100 > 1000，必须拦住")
	}
	if !strings.Contains(reason, "token") {
		t.Errorf("原因要说明是哪一项超了，实际 %q", reason)
	}

	// 负数与 0 不该被记进去（畸形用量不该把分组卡住）
	l.AddTokens(7, 0, now)
	l.AddTokens(7, -5, now)
	if l2 := NewGroupLimiter(); l2 != nil {
		l2.AddTokens(7, -100, now)
		if reqs, toks := l2.Usage(7, now); reqs != 0 || toks != 0 {
			t.Errorf("非正数不该计入，实际 请求 %d token %d", reqs, toks)
		}
	}
	if _, toks := l.Usage(7, now); toks != 1100 {
		t.Errorf("token 累计应为 1100，实际 %d", toks)
	}
}

// 分组 ID 为 0（未指定分组）时不记账：否则会把不同来源的流量
// 混进同一个「0 号分组」，让某个分组莫名其妙被限流。
func TestGroupLimiterIgnoresZeroGroup(t *testing.T) {
	l := NewGroupLimiter()
	now := time.Now()
	l.Record(0, now)
	l.AddTokens(0, 500, now)
	if reqs, toks := l.Usage(0, now); reqs != 0 || toks != 0 {
		t.Errorf("分组 0 不该记账，实际 请求 %d token %d", reqs, toks)
	}
}

// 记账口径：上游给了 total_tokens 就用它，否则按分量累加。
func TestUsageBillableTokens(t *testing.T) {
	cases := []struct {
		name string
		u    Usage
		want int
	}{
		{"上游给了总数", Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, 15},
		{"没给总数就累加", Usage{PromptTokens: 10, CompletionTokens: 5}, 15},
		{"缓存也要算进去", Usage{PromptTokens: 10, CachedTokens: 3, CacheCreationTokens: 2, CompletionTokens: 5}, 20},
		{"只有 reasoning 时不漏", Usage{ReasoningTokens: 7}, 7},
		{"全空", Usage{}, 0},
	}
	for _, c := range cases {
		if got := c.u.BillableTokens(); got != c.want {
			t.Errorf("%s: 期望 %d 实际 %d", c.name, c.want, got)
		}
	}
}
