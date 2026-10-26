package relay

import (
	"log/slog"
	"testing"
	"time"

	"llm-relay/internal/model"
	"llm-relay/internal/secure"
)

// cands 按给定顺序造候选（id 即渠道 ID），用来验证「顺序由密钥路由决定」
// 而不是由 id 决定。
func cands(ids ...uint) []Candidate {
	out := make([]Candidate, 0, len(ids))
	for _, id := range ids {
		out = append(out, Candidate{
			Channel:   model.Channel{ID: id},
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

// sortOrderOf 是密钥顺序的取值函数：列表内取下标，列表外垫底。
func TestSortOrderOf(t *testing.T) {
	order := []uint{90, 50, 10}
	for id, want := range map[uint]int{90: 0, 50: 1, 10: 2, 99: 3, 7: 3} {
		if got := sortOrderOf(order, id); got != want {
			t.Errorf("sortOrderOf(%d) = %d，期望 %d", id, got, want)
		}
	}
	// 空列表：所有渠道都垫底，值一致（顺序退化为稳定排序的原序）
	if sortOrderOf(nil, 10) != 0 || sortOrderOf(nil, 99) != 0 {
		t.Error("空列表应统一返回 0")
	}
}

// 路由顺序完全由密钥的 model_channels 列表决定，与渠道 id 无关。
func TestSortCandidatesFollowsKeyOrder(t *testing.T) {
	// 故意让 id 顺序与密钥顺序相反：按 id 排会把链路倒过来
	cs := cands(10, 50, 90)
	sortCandidates(cs, []uint{90, 50, 10})
	if got := ids(cs); !equalIDs(got, []uint{90, 50, 10}) {
		t.Fatalf("应按密钥顺序 [90 50 10]，实际 %v", got)
	}

	// 密钥没列出的渠道垫底（理论上不该出现，SQL 已按 IN 过滤；
	// 这里钉的是 sortOrderOf 的兜底行为）
	cs2 := cands(10, 99)
	sortCandidates(cs2, []uint{10})
	if got := ids(cs2); !equalIDs(got, []uint{10, 99}) {
		t.Fatalf("列表外的渠道应垫底，实际 %v", got)
	}
}

// 不可用（不在时段内 / 已饱和）的渠道要排到后面，但不能因为密钥顺序靠前就压过
// 「当前不可用」这个事实 —— 否则请求会先撞一条明知道用不了的渠道。
//
// 顺序与可用性冲突时可用性赢：到点才可用的渠道被提前，是既有语义
// （原先它与 weight 的关系也是这样）。
func TestSortCandidatesPutsUnavailableLast(t *testing.T) {
	cs := []Candidate{
		{Channel: model.Channel{ID: 10}, Available: false},
		{Channel: model.Channel{ID: 20}, Available: true},
		{Channel: model.Channel{ID: 30}, Available: true, Saturated: true},
	}
	// 密钥顺序是 10 → 20 → 30
	sortCandidates(cs, []uint{10, 20, 30})
	if got := ids(cs); !equalIDs(got, []uint{20, 30, 10}) {
		t.Fatalf("可用的应排前面（饱和的次之，不可用的最后），实际 %v", got)
	}
}

// 同一可用档内才按密钥顺序：可用性优先分区，顺序在区内生效。
func TestSortCandidatesKeyOrderWithinAvailabilityTier(t *testing.T) {
	cs := []Candidate{
		{Channel: model.Channel{ID: 10}, Available: true},
		{Channel: model.Channel{ID: 20}, Available: false},
		{Channel: model.Channel{ID: 30}, Available: true},
	}
	sortCandidates(cs, []uint{30, 20, 10})
	if got := ids(cs); !equalIDs(got, []uint{30, 10, 20}) {
		t.Fatalf("可用档内按密钥顺序 [30 10]，不可用的 20 垫底，实际 %v", got)
	}
}

// fbCand 造一个候选，指定渠道 ID 与是否为兜底（默认可用）。
func fbCand(id uint, fallback bool) Candidate {
	return Candidate{
		Channel:   model.Channel{ID: id},
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
	precise := fbCand(10, false)
	precise.Available = false // 精确但当前不可用
	cs := []Candidate{
		precise,
		fbCand(20, true),
		fbCand(30, true),
	}
	sortCandidates(cs, []uint{10, 20, 30})
	if got := ids(cs); !equalIDs(got, []uint{10, 20, 30}) {
		t.Fatalf("精确候选必须排在兜底之前（哪怕它当前不可用），实际 %v", got)
	}
}

// 兜底段内部仍要按可用性排序。
//
// 拆段之后容易忘记给兜底段保留 Available/Saturated 的次级序，
// 于是「超出可用时段」的兜底渠道会排在「此刻可用」的兜底渠道前面 ——
// 请求先撞一条明知道用不了的渠道。
func TestSortCandidatesFallbackSegmentKeepsAvailabilityOrder(t *testing.T) {
	unavailable := fbCand(98, true)
	unavailable.Available = false
	cs := []Candidate{
		unavailable,      // 兜底但不可用
		fbCand(99, true), // 兜底且可用
	}
	sortCandidates(cs, []uint{98, 99})
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
// 保留精确那条的理由不只是「先来后到」：精确那条带着它自己的 upstream_name
// 与计费键，是用户明确为这个模型配置的行；兜底那条的 Binding 指向的是
// 默认模型行，用它计费会算到别的模型头上。
func TestMergeCandidatesDedupesExactWins(t *testing.T) {
	precise := fbCand(10, false)
	precise.Binding = model.ChannelModel{PublicName: "claude-opus-4-7", UpstreamName: "claude-opus-4-7"}
	fallbackSameChannel := fbCand(10, true)
	fallbackSameChannel.Binding = model.ChannelModel{PublicName: "deepseek-chat", UpstreamName: "deepseek-chat"}
	fallbackOther := fbCand(20, true)
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
			Channel:  model.Channel{ID: 7, Name: "DeepSeek"},
			Binding:  model.ChannelModel{PublicName: "deepseek-chat", UpstreamName: "deepseek-chat"},
			Fallback: true,
		},
		Attempt: &Attempt{StatusCode: 200},
	}
	entry := BuildLog(req, res, Usage{}, 200, "", Timing{FirstByteMs: 1, TotalMs: 2})

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
			Channel: model.Channel{ID: 7, Name: "DeepSeek"},
			Binding: model.ChannelModel{PublicName: "deepseek-chat", UpstreamName: "deepseek-chat"},
			// Fallback: false —— 精确命中
		},
		Attempt: &Attempt{StatusCode: 200},
	}
	entry := BuildLog(req, res, Usage{}, 200, "", Timing{FirstByteMs: 1, TotalMs: 2})

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
			ID: 7, Name: "DeepSeek",
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

// 兜底轮里，Binding 要指向默认模型那一行：PublicName 是计费键（对外名），
// UpstreamName 是那一行**配置的上游名** —— 映射照常生效，与精确命中同一语义。
//
// 这是「按指定模型单价计费」的落地点：计价引擎按 (渠道, 对外名) 查白名单行，
// 把 PublicName 设成默认模型名才能命中那条真实存在的价。
//
// 测试数据特意让上游名与对外名不同：两者相同时，断言区分不了
// 「用行的上游名」与「拿对外名硬发」两种实现 —— 后者会让配了映射的行
// 收到上游不认识的名字（2026-09-21 评审实证的缺陷）。
func TestBuildCandidatesFallbackUsesDefaultModelAsBinding(t *testing.T) {
	r := mustRouter(t)
	rows := []candidateRow{{
		Channel: model.Channel{
			ID: 7, Name: "DeepSeek",
			ExtraConfig: model.JSONMap{"default_model_enabled": true, "default_model": "deepseek-chat"},
		},
		PublicName:   "deepseek-chat",
		UpstreamName: "deepseek-v4.1-flash", // 该行配了对外名 → 上游名映射
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
	if got[0].Binding.PublicName != "deepseek-chat" {
		t.Errorf("计费键应是对外名 deepseek-chat，实际 %q", got[0].Binding.PublicName)
	}
	if got[0].Binding.UpstreamName != "deepseek-v4.1-flash" {
		t.Errorf("上游名应是该行映射的 deepseek-v4.1-flash，实际 %q（映射被忽略，上游会收到它不认识的名字）",
			got[0].Binding.UpstreamName)
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
			ID: 7, Name: "DeepSeek",
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
	return &Router{cipher: c, state: NewChannelState(), logger: slog.Default()}
}

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
