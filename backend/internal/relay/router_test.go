package relay

import (
	"testing"

	"llm-relay/internal/model"
)

// cands 按给定顺序造候选，weight 与 id 各自递增，用来验证「顺序由 weight 决定」
// 而不是由 id 决定。
func cands(ids ...uint) []Candidate {
	out := make([]Candidate, 0, len(ids))
	for i, id := range ids {
		out = append(out, Candidate{
			Channel:   model.Channel{ID: id, GroupID: 7, Weight: i + 1},
			Available: true,
		})
	}
	return out
}

func ids(cs []Candidate) []uint {
	out := make([]uint, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Channel.ID)
	}
	return out
}

func equalIDs(a, b []uint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// failover 必须严格保持候选顺序 —— 那已经是 weight 升序（见 Router.Candidates
// 的 Order），第一个就是首选。
func TestSortCandidatesFailoverKeepsWeightOrder(t *testing.T) {
	r := &Router{state: NewChannelState(), rrCursor: map[uint]int{}}
	// 故意让 id 顺序与 weight 顺序相反：旧实现按 id 排，会把它倒过来
	cs := cands(90, 50, 10)
	sortCandidates(cs, model.StrategyFailover, r)
	if got := ids(cs); !equalIDs(got, []uint{90, 50, 10}) {
		t.Fatalf("failover 应保持权重顺序 [90 50 10]，实际 %v", got)
	}
}

// 未知策略（含已下线的 weighted）走 default 分支：保持权重顺序，不抽签。
func TestSortCandidatesUnknownStrategyKeepsWeightOrder(t *testing.T) {
	r := &Router{state: NewChannelState(), rrCursor: map[uint]int{}}
	for _, strategy := range []string{"weighted", "", "no-such-strategy"} {
		cs := cands(90, 50, 10)
		sortCandidates(cs, strategy, r)
		if got := ids(cs); !equalIDs(got, []uint{90, 50, 10}) {
			t.Fatalf("策略 %q 应保持权重顺序，实际 %v", strategy, got)
		}
	}
}

// 轮询：起点逐次前进，一轮走完回到开头。
//
// 这条钉住的是一个真实缺陷：原来的 rotate() 不读游标，每次都把首个挪到末尾，
// 于是每个请求都从「第二优先」开始，而 rrCursor 声明了从未被使用。
func TestSortCandidatesRoundRobinAdvancesCursor(t *testing.T) {
	r := &Router{state: NewChannelState(), rrCursor: map[uint]int{}}
	want := [][]uint{{10, 20, 30}, {20, 30, 10}, {30, 10, 20}, {10, 20, 30}}
	for i, exp := range want {
		cs := cands(10, 20, 30)
		sortCandidates(cs, model.StrategyRoundRobin, r)
		if got := ids(cs); !equalIDs(got, exp) {
			t.Fatalf("第 %d 次轮询应为 %v，实际 %v", i+1, exp, got)
		}
	}
}

// 轮询游标按分组独立：一个分组的轮转不该推着另一个分组一起走。
func TestRoundRobinCursorIsPerGroup(t *testing.T) {
	r := &Router{state: NewChannelState(), rrCursor: map[uint]int{}}

	groupA := func() []Candidate {
		cs := cands(10, 20, 30)
		for i := range cs {
			cs[i].Channel.GroupID = 1
		}
		return cs
	}
	groupB := func() []Candidate {
		cs := cands(40, 50, 60)
		for i := range cs {
			cs[i].Channel.GroupID = 2
		}
		return cs
	}

	sortCandidates(groupA(), model.StrategyRoundRobin, r) // A 游标 -> 1
	csB := groupB()
	sortCandidates(csB, model.StrategyRoundRobin, r) // B 首次应从首位开始
	if got := ids(csB); !equalIDs(got, []uint{40, 50, 60}) {
		t.Fatalf("另一个分组的首次轮询应从首位开始，实际 %v", got)
	}
	csA := groupA()
	sortCandidates(csA, model.StrategyRoundRobin, r) // A 应继续走到第二个
	if got := ids(csA); !equalIDs(got, []uint{20, 30, 10}) {
		t.Fatalf("A 分组的游标应独立前进，实际 %v", got)
	}
}

// 不可用（不在时段内 / 已饱和）的渠道要排到后面，但不能因为优先级高就压过
// 「当前不可用」这个事实 —— 否则请求会先撞一条明知道用不了的渠道。
func TestSortCandidatesPutsUnavailableLast(t *testing.T) {
	r := &Router{state: NewChannelState(), rrCursor: map[uint]int{}}
	cs := []Candidate{
		{Channel: model.Channel{ID: 10, GroupID: 7, Weight: 1}, Available: false},
		{Channel: model.Channel{ID: 20, GroupID: 7, Weight: 2}, Available: true},
		{Channel: model.Channel{ID: 30, GroupID: 7, Weight: 3}, Available: true, Saturated: true},
	}
	sortCandidates(cs, model.StrategyFailover, r)
	if got := ids(cs); !equalIDs(got, []uint{20, 30, 10}) {
		t.Fatalf("可用的应排前面（饱和的次之，不可用的最后），实际 %v", got)
	}
}

// 最低延迟：按观测延迟升序，没有观测值的排最后。
func TestSortCandidatesLeastLatency(t *testing.T) {
	st := NewChannelState()
	st.RecordLatency(10, 300)
	st.RecordLatency(20, 100)
	r := &Router{state: st, rrCursor: map[uint]int{}}
	// 30 没有任何观测值
	cs := cands(10, 20, 30)
	sortCandidates(cs, model.StrategyLeastLatency, r)
	if got := ids(cs); !equalIDs(got, []uint{20, 10, 30}) {
		t.Fatalf("应最快的在前、无观测值的最后 [20 10 30]，实际 %v", got)
	}
}

func TestRotateOffset(t *testing.T) {
	cs := cands(1, 2, 3, 4)
	rotate(cs, 2)
	if got := ids(cs); !equalIDs(got, []uint{3, 4, 1, 2}) {
		t.Fatalf("左移 2 位应为 [3 4 1 2]，实际 %v", got)
	}
	// offset 为 0 或越界不该改动顺序
	cs2 := cands(1, 2, 3)
	rotate(cs2, 0)
	if got := ids(cs2); !equalIDs(got, []uint{1, 2, 3}) {
		t.Fatalf("offset=0 不应改动顺序，实际 %v", got)
	}
	rotate(cs2, 3)
	if got := ids(cs2); !equalIDs(got, []uint{1, 2, 3}) {
		t.Fatalf("offset 等于长度时不应改动顺序，实际 %v", got)
	}
}
