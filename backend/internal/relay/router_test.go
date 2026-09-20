package relay

import (
	"log/slog"
	"testing"
	"time"

	"llm-relay/internal/model"
	"llm-relay/internal/secure"
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

// fbCand 造一个候选，指定渠道 ID、优先级与是否为兜底（默认可用）。
func fbCand(id uint, weight int, fallback bool) Candidate {
	return Candidate{
		Channel:   model.Channel{ID: id, GroupID: 7, Weight: weight},
		Available: true,
		Fallback:  fallback,
	}
}

// 兜底候选永远排在精确命中之后。
//
// 这是「白名单精确命中优先」的实现点，而且必须压过 Available/Saturated ——
// 一条当前不可用（不在可用时段内、或已饱和）的精确渠道，也绝不能被可用的
// 兜底渠道插到前面去。否则请求会先撞兜底，把本该精确承接流量的渠道让过去，
// 「精确优先」就成了空话。
//
// 这条用例故意把最容易被插队的组合摆出来：精确的那条不可用，兜底的那条可用。
func TestSortCandidatesFallbackAlwaysLast(t *testing.T) {
	r := &Router{state: NewChannelState(), rrCursor: map[uint]int{}}
	precise := fbCand(10, 1, false)
	precise.Available = false // 精确但当前不可用
	cs := []Candidate{
		precise,
		fbCand(20, 2, true),
		fbCand(30, 3, true),
	}
	sortCandidates(cs, model.StrategyFailover, r)
	if got := ids(cs); !equalIDs(got, []uint{10, 20, 30}) {
		t.Fatalf("精确候选必须排在兜底之前（哪怕它当前不可用），实际 %v", got)
	}
}

// 轮询只轮转精确命中的渠道，不碰兜底段。
//
// 轮询回答的是「精确命中的几条渠道怎么轮流」。把兜底纳入轮转会让精确渠道
// 被无谓跳过，而兜底是容错通道，不该参与「公平分配」—— 它只在精确的都不行时顶上。
//
// 这条同时钉住游标的模数：必须是精确段的长度，而不是候选总数。
// 模数取错的话，加一条开了兜底的渠道就会改变精确渠道的轮转周期。
func TestSortCandidatesRoundRobinSkipsFallback(t *testing.T) {
	r := &Router{state: NewChannelState(), rrCursor: map[uint]int{}}
	// 每轮都重新造候选：sortCandidates 会就地重排，复用切片会互相污染
	build := func() []Candidate {
		return []Candidate{
			fbCand(10, 1, false),
			fbCand(20, 2, false),
			fbCand(30, 3, false),
			fbCand(99, 4, true), // 兜底，应始终在尾
		}
	}
	want := [][]uint{
		{10, 20, 30, 99},
		{20, 30, 10, 99},
		{30, 10, 20, 99},
		{10, 20, 30, 99}, // 走完一轮回到开头
	}
	for i, exp := range want {
		cs := build()
		sortCandidates(cs, model.StrategyRoundRobin, r)
		if got := ids(cs); !equalIDs(got, exp) {
			t.Fatalf("第 %d 次轮询应为 %v（兜底 99 始终在尾），实际 %v", i+1, exp, got)
		}
	}
}

// 最低延迟策略也不能把兜底提前。
//
// 兜底渠道如果恰好是全组观测延迟最低的那条（刚上线、只跑过几次探测），
// 纯按延迟排会把它顶到队首 —— 于是每次请求都先走兜底，精确命中的渠道
// 形同虚设。延迟只在段内比较。
func TestSortCandidatesLeastLatencyKeepsFallbackLast(t *testing.T) {
	st := NewChannelState()
	st.RecordLatency(10, 300)
	st.RecordLatency(20, 100)
	st.RecordLatency(99, 1) // 兜底渠道延迟最低
	r := &Router{state: st, rrCursor: map[uint]int{}}
	cs := []Candidate{
		fbCand(10, 1, false),
		fbCand(20, 2, false),
		fbCand(99, 3, true),
	}
	sortCandidates(cs, model.StrategyLeastLatency, r)
	// 精确段按延迟升序 [20 10]，兜底 99 尽管最快也只能在最后
	if got := ids(cs); !equalIDs(got, []uint{20, 10, 99}) {
		t.Fatalf("应 [20 10 99]（兜底 99 不参与段内比快），实际 %v", got)
	}
}

// 兜底段内部仍要按可用性排序。
//
// 拆段之后容易忘记给兜底段保留 Available/Saturated 的次级序，
// 于是「超出可用时段」的兜底渠道会排在「此刻可用」的兜底渠道前面 ——
// 请求先撞一条明知道用不了的渠道。
func TestSortCandidatesFallbackSegmentKeepsAvailabilityOrder(t *testing.T) {
	r := &Router{state: NewChannelState(), rrCursor: map[uint]int{}}
	unavailable := fbCand(98, 1, true)
	unavailable.Available = false
	cs := []Candidate{
		unavailable,     // 兜底但不可用，优先级最高
		fbCand(99, 2, true), // 兜底且可用
	}
	sortCandidates(cs, model.StrategyFailover, r)
	if got := ids(cs); !equalIDs(got, []uint{99, 98}) {
		t.Fatalf("兜底段内可用的应排前面，实际 %v", got)
	}
}

// 合并精确与兜底两段候选时，同一条渠道只能出现一次，且保留精确那条。
//
// 一条渠道完全可能既精确命中、又开着默认映射（用户既把 claude-opus-4-7
// 写进了白名单，也给这条渠道配了兜底）。两次查询都想收它，不去重的话
// 它会以「精确」和「兜底」两个身份同时进候选链：第一次尝试失败后
// tried 记下渠道 ID，第二次被 filterTried 跳过 —— 白耗一个重试轮次，
// 而 maxAttempts 是有限的，等于凭空少了一次故障转移的机会。
//
// 保留精确那条的理由不只是「先来后到」：精确那条带着它自己的 upst.name
// 与计费键，是用户明确为这个模型配置的行；兜底那条的 Binding 指向的是
// 默认模型行，用它计费会算到别的模型头上。
func TestMergeCandidatesDedupesExactWins(t *testing.T) {
	precise := fbCand(10, 1, false)
	precise.Binding = model.ChannelModel{PublicName: "claude-opus-4-7", UpstreamName: "claude-opus-4-7"}
	fallbackSameChannel := fbCand(10, 1, true)
	fallbackSameChannel.Binding = model.ChannelModel{PublicName: "deepseek-chat", UpstreamName: "deepseek-chat"}
	fallbackOther := fbCand(20, 2, true)
	fallbackOther.Binding = model.ChannelModel{PublicName: "deepseek-chat", UpstreamName: "deepseek-chat"}

	got := mergeCandidates([]Candidate{precise}, []Candidate{fallbackSameChannel, fallbackOther})

	if len(got) != 2 {
		t.Fatalf("渠道 10 重复出现应被去掉，期望 2 条候选，实际 %d 条: %v", len(got), ids(got))
	}
	if got[0].Channel.ID != 10 || got[0].Fallback {
		t.Fatalf("第一条应是渠道 10 的精确候选（Fallback=false），实际 %+v", got[0])
	}
	if got[0].Binding.PublicName != "claude-opus-4-7" {
		t.Fatalf("去重必须保留精确那条的 Binding（计费键），实际 %q", got[0].Binding.PublicName)
	}
	if got[1].Channel.ID != 20 || !got[1].Fallback {
		t.Fatalf("第二条应是渠道 20 的兜底候选，实际 %+v", got[1])
	}
}

// 兜底请求的日志：请求名记客户端的原名，兜底标记为真，上游名是默认模型名。
//
// 这三个字段的口径必须一起成立，缺一个就没法排查：
//   - 只记上游名（默认模型名）→ 看不出客户端原本要的是什么，也不知道
//     这条是兜底接的（所有兜底请求都会显示成同一个模型名，与真实调用混在一起）；
//   - 不记兜底标记 → 「我明明请求的是 claude，怎么日志里全是 deepseek」无从解释，
//     更无法回答「有多少请求是被兜底接走的」，而写错模型名从此不再报错，
//     这个标记是唯一的发现途径。
func TestBuildLogMarksFallback(t *testing.T) {
	req := &RelayRequest{TraceID: "t1", PublicModel: "claude-opus-4-7"}
	res := &RelayResult{
		TraceID: "t1",
		Candidate: Candidate{
			Channel:  model.Channel{ID: 7, Name: "DeepSeek", GroupID: 1},
			Binding:  model.ChannelModel{PublicName: "deepseek-chat", UpstreamName: "deepseek-chat"},
			Fallback: true,
		},
		Attempt: &Attempt{StatusCode: 200},
	}
	entry := BuildLog(req, res, Usage{}, 200, "", 1, 2)

	if entry.ModelRequested != "claude-opus-4-7" {
		t.Errorf("请求模型应记客户端原名，实际 %q", entry.ModelRequested)
	}
	if entry.ModelUpstream != "deepseek-chat" {
		t.Errorf("上游模型应是默认模型名，实际 %q", entry.ModelUpstream)
	}
	if !entry.FallbackMapped {
		t.Error("兜底接下的请求必须标记 FallbackMapped，否则日志里与真实调用分不开")
	}
}

// 精确命中的请求不能被标成兜底。
//
// 反过来的错误同样致命：把精确命中标成兜底，用户会以为自己的白名单配置没生效。
func TestBuildLogDoesNotMarkExactMatchAsFallback(t *testing.T) {
	req := &RelayRequest{TraceID: "t2", PublicModel: "deepseek-chat"}
	res := &RelayResult{
		TraceID: "t2",
		Candidate: Candidate{
			Channel: model.Channel{ID: 7, Name: "DeepSeek", GroupID: 1},
			Binding: model.ChannelModel{PublicName: "deepseek-chat", UpstreamName: "deepseek-chat"},
			// Fallback: false —— 精确命中
		},
		Attempt: &Attempt{StatusCode: 200},
	}
	entry := BuildLog(req, res, Usage{}, 200, "", 1, 2)

	if entry.FallbackMapped {
		t.Error("精确命中的请求不该标成兜底")
	}
}

// 精确命中的候选绝不能被标成兜底 —— 哪怕它请求的模型名恰好等于渠道的默认模型名。
//
// 这条钉住一个真实缺陷：候选行里带着 default_model 列（精确与兜底两轮查询
// 共用同一个 SELECT），如果按「public_name == default_model」去推断这一行是不是
// 兜底，那么「客户端请求的模型名正好就是渠道的默认模型名」时会被误判 ——
// 应该是精确命中的请求被降级成兜底：日志标上「兜底」（用户以为白名单没配好），
// 路由还把它排到末尾（优先级别白配）。来源必须显式传入，不能从行内容猜。
func TestBuildCandidatesExactMatchNotMisjudgedAsFallback(t *testing.T) {
	r := mustRouter(t)
	rows := []candidateRow{{
		Channel: model.Channel{
			ID: 7, Name: "DeepSeek", GroupID: 1,
			ExtraConfig: model.JSONMap{"default_model_enabled": true, "default_model": "deepseek-chat"},
		},
		PublicName:   "deepseek-chat",
		UpstreamName: "deepseek-chat",
		BindingID:    11,
		DefaultModel: "deepseek-chat", // 与请求名相同
	}}

	got := r.buildCandidates(rows, CandidateQuery{PublicModel: "deepseek-chat"}, time.Now(), false)

	if len(got) != 1 {
		t.Fatalf("应有 1 条候选，实际 %d", len(got))
	}
	if got[0].Fallback {
		t.Error("请求名与默认模型名相同时，精确命中被误判成了兜底")
	}
}

// 兜底轮里，Binding 要指向默认模型那一行（计费键与上游名都用它）。
//
// 这是「按指定模型单价计费」的落地点：计价引擎按 (渠道, 对外名) 查白名单行，
// 把 PublicName 设成默认模型名才能命中那条真实存在的价。
func TestBuildCandidatesFallbackUsesDefaultModelAsBinding(t *testing.T) {
	r := mustRouter(t)
	rows := []candidateRow{{
		Channel: model.Channel{
			ID: 7, Name: "DeepSeek", GroupID: 1,
			ExtraConfig: model.JSONMap{"default_model_enabled": true, "default_model": "deepseek-chat"},
		},
		PublicName:   "deepseek-chat",
		UpstreamName: "deepseek-chat",
		BindingID:    11,
		DefaultModel: "deepseek-chat",
	}}

	got := r.buildCandidates(rows, CandidateQuery{PublicModel: "claude-opus-4-7"}, time.Now(), true)

	if len(got) != 1 {
		t.Fatalf("应有 1 条候选，实际 %d", len(got))
	}
	if !got[0].Fallback {
		t.Fatal("兜底轮取回的行必须标成兜底")
	}
	if got[0].Binding.PublicName != "deepseek-chat" || got[0].Binding.UpstreamName != "deepseek-chat" {
		t.Errorf("Binding 应指向默认模型，实际 public=%q upstream=%q",
			got[0].Binding.PublicName, got[0].Binding.UpstreamName)
	}
	if got[0].Binding.ID != 11 {
		t.Errorf("Binding.ID 应是默认模型那条白名单行，实际 %d", got[0].Binding.ID)
	}
}

// 开关没开的渠道，即便被兜底查询取回来也不能成为兜底候选。
//
// SQL 只做粗筛（default_model 键存在），「开关是否真的开着」由
// ChannelDefaultModel 判定 —— 两者必须一致，否则会把一个用户明确关掉的
// 渠道重新拉进路由，而界面上它显示的是「关闭」。
func TestBuildCandidatesSkipsFallbackWhenSwitchOff(t *testing.T) {
	r := mustRouter(t)
	rows := []candidateRow{{
		Channel: model.Channel{
			ID: 7, Name: "DeepSeek", GroupID: 1,
			// 只有模型名、开关是 false
			ExtraConfig: model.JSONMap{"default_model_enabled": false, "default_model": "deepseek-chat"},
		},
		PublicName:   "deepseek-chat",
		UpstreamName: "deepseek-chat",
		BindingID:    11,
		DefaultModel: "deepseek-chat",
	}}

	got := r.buildCandidates(rows, CandidateQuery{PublicModel: "claude-opus-4-7"}, time.Now(), true)

	if len(got) != 0 {
		t.Fatalf("开关关着时不该成为兜底候选，实际取到 %d 条", len(got))
	}
}

// mustRouter 造一个够用的路由器（只有解密能力被用到）。
func mustRouter(t *testing.T) *Router {
	t.Helper()
	c, err := secure.NewCipher("test-secret")
	if err != nil {
		t.Fatalf("构造测试用 cipher 失败: %v", err)
	}
	return &Router{cipher: c, state: NewChannelState(), rrCursor: map[uint]int{}, logger: slog.Default()}
}
//
// 价格是「渠道 × 模型」维度的（见 pricing.Engine），所以计费必须用
// 真正走的那条白名单行的对外名：
//   - 精确命中时它恰好等于请求名（候选查询的条件就是 public_name = 请求名），
//     改用它是一次**行为无变化的等价替换**；
//   - 兜底时它是渠道配置的默认模型名 —— 客户端的 claude-opus-4-7 在渠道里
//     根本没配价，按请求名查只会得到「没配价、记 0 元」，而按默认模型名查
//     才能拿到那条真实存在的单价。
//
// 两种情况用同一个表达式覆盖，不需要判断分支，也不额外加列。
func TestBillingModelPrefersCandidateBinding(t *testing.T) {
	cases := []struct {
		name string
		req  *RelayRequest
		res  *RelayResult
		want string
	}{
		{
			name: "精确命中：等于请求名（等价替换）",
			req:  &RelayRequest{PublicModel: "deepseek-chat"},
			res: &RelayResult{Candidate: Candidate{
				Binding: model.ChannelModel{PublicName: "deepseek-chat"},
			}},
			want: "deepseek-chat",
		},
		{
			name: "精确命中且配了上游名：仍按对外名计费",
			req:  &RelayRequest{PublicModel: "gpt-4o"},
			res: &RelayResult{Candidate: Candidate{
				Binding: model.ChannelModel{PublicName: "gpt-4o", UpstreamName: "openai/gpt-4o"},
			}},
			want: "gpt-4o",
		},
		{
			name: "兜底：按默认模型名计费，不是客户端请求的名字",
			req:  &RelayRequest{PublicModel: "claude-opus-4-7"},
			res: &RelayResult{Candidate: Candidate{
				Binding:  model.ChannelModel{PublicName: "deepseek-chat", UpstreamName: "deepseek-chat"},
				Fallback: true,
			}},
			want: "deepseek-chat",
		},
		{
			name: "没有候选（失败路径）：回落到请求名",
			req:  &RelayRequest{PublicModel: "claude-opus-4-7"},
			res:  &RelayResult{},
			want: "claude-opus-4-7",
		},
		{
			name: "res 为 nil：回落到请求名",
			req:  &RelayRequest{PublicModel: "claude-opus-4-7"},
			res:  nil,
			want: "claude-opus-4-7",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := BillingModel(c.req, c.res); got != c.want {
				t.Errorf("BillingModel = %q，期望 %q", got, c.want)
			}
		})
	}
}
