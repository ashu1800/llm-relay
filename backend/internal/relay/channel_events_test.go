package relay

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 渠道健康事件与运行期快照的测试。
//
// 熔断机制本身（冷却、连击、自动冷却）早已存在并有各自的测试；
// 这里钉住的是后来补上的「可观测」半边：
//  - Cooldown 的返回值（是否新进入冷却）决定事件去重，错一步就是告警轰炸；
//  - 三个触发点（上游冷却 / 连击熔断 / 恢复）必须只在状态**翻转**时发事件。
// 与 logwriter 的测试同一手法：库连不上，markChannelFailure 里的落库失败
// 只 warn 不阻断 —— 事件路径不依赖数据库写入成功。

// newBrokenDB 造一个「构造成功、但每次查询必失败」的 *gorm.DB。
// 与 newTestWriter 内部完全一致，但这里要的是 db 本身。
func newBrokenDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=127.0.0.1 port=1 user=nobody dbname=nothing sslmode=disable",
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("构造测试用 gorm.DB 失败: %v", err)
	}
	return db
}

// collectEvents 收集回调发出的事件（并发安全，转发路径是并发调它的）。
func collectEvents() (func(ChannelEvent), func() []ChannelEvent) {
	var mu sync.Mutex
	var got []ChannelEvent
	return func(e ChannelEvent) {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
		}, func() []ChannelEvent {
			mu.Lock()
			defer mu.Unlock()
			return append([]ChannelEvent(nil), got...)
		}
}

// Cooldown 的返回值语义：首次进入冷却 = true；冷却中再冷却 = false
// （延长不算新事件）；到期或手动清除后再冷却 = true。
// 事件去重完全压在这一个 bool 上，它错了看板就会被同一条告警刷屏。
func TestChannelStateCooldownTransition(t *testing.T) {
	cs := NewChannelState()

	if first := cs.Cooldown(1, 30*time.Second); !first {
		t.Fatal("首次冷却应报告「新进入」")
	}
	if again := cs.Cooldown(1, time.Minute); again {
		t.Fatal("冷却中的延长不应报告「新进入」")
	}
	if left, _ := cs.InCooldown(1, time.Now()); left < 50*time.Second {
		t.Fatalf("延长应取更长的那个，剩余 %v", left)
	}

	cs.ClearCooldown(1)
	if again := cs.Cooldown(1, time.Second); !again {
		t.Fatal("手动恢复后的再次冷却应报告「新进入」")
	}
}

// 上游信号触发的冷却（429/5xx/401/403）：新进入才发 cooldown 事件。
func TestServiceCooldownEventOnTransition(t *testing.T) {
	svc := NewService(newBrokenDB(t), nil, Options{}, nil)
	record, snapshot := collectEvents()
	svc.OnChannelEvent = record
	st := NewChannelState()
	svc.SetChannelState(st)

	attempt := &Attempt{StatusCode: http.StatusTooManyRequests, Headers: http.Header{}}
	svc.applyCooldown(1, attempt)
	svc.applyCooldown(1, attempt) // 已在冷却中：不允许再来一条

	svc.applyCooldown(2, attempt) // 另一条渠道：独立的冷却窗口

	events := snapshot()
	if len(events) != 2 {
		t.Fatalf("两次冷却 + 一条其他渠道应恰好 2 条事件，实际 %d：%+v", len(events), events)
	}
	if events[0].Kind != "cooldown" || events[0].ChannelID != 1 {
		t.Fatalf("第一条应为渠道 1 的 cooldown，实际 %+v", events[0])
	}
	if events[1].ChannelID != 2 {
		t.Fatalf("第二条应为渠道 2，实际 %+v", events[1])
	}
	if events[0].Until.IsZero() {
		t.Fatal("冷却事件必须带截止时间，前端要用它显示剩余秒数")
	}
}

// 连击熔断（markChannelFailure）：第 3 次失败触发 auto_cooldown；
// 冷却期内继续失败（第 4 次）不得重复发事件 —— 「首个请求付学费，
// 后续静默绕开」同样适用于通知。
func TestServiceAutoCooldownEventOnStreak(t *testing.T) {
	svc := NewService(newBrokenDB(t), nil, Options{}, nil)
	record, snapshot := collectEvents()
	svc.OnChannelEvent = record
	st := NewChannelState()
	svc.SetChannelState(st)

	for i := 0; i < 3; i++ {
		svc.markChannelFailure(7, "上游返回 502")
	}
	for i := 0; i < 3; i++ {
		svc.markChannelFailure(7, "上游返回 502") // 冷却期内继续失败
	}
	if _, cooling := st.InCooldown(7, time.Now()); !cooling {
		t.Fatal("连击达阈值后渠道应在冷却中")
	}

	var autos int
	for _, e := range snapshot() {
		if e.Kind == "auto_cooldown" {
			autos++
			if e.Reason == "" {
				t.Fatal("自动熔断事件应带上最近一次失败的原因")
			}
		}
	}
	if autos != 1 {
		t.Fatalf("6 次失败只应在首次达阈值时发 1 条 auto_cooldown，实际 %d 条", autos)
	}
}

// 成功恢复（markChannelSuccess）：NoteSuccess 清零连击。
// recovered 事件的「degraded→healthy 翻转」判定靠 RowsAffected，
// 坏库上恒为 0 —— 恰好也验证了「库失败时不得误报恢复」。
func TestServiceRecoverEventRequiresDBFlip(t *testing.T) {
	svc := NewService(newBrokenDB(t), nil, Options{}, nil)
	record, snapshot := collectEvents()
	svc.OnChannelEvent = record
	st := NewChannelState()
	svc.SetChannelState(st)

	st.NoteFailure(9)
	svc.markChannelSuccess(9)
	if got := st.NoteFailure(9); got != 1 {
		t.Fatalf("成功后连击应清零（下次从 1 计），实际 %d", got)
	}
	for _, e := range snapshot() {
		if e.Kind == "recovered" {
			t.Fatal("落库失败（RowsAffected=0）时不得广播恢复事件 —— 广播了「恢复」而渠道其实还在 degraded，比不广播更糟")
		}
	}
}

// 运行期快照：一次锁内取全部，剩余冷却从给定时刻推算，空渠道不出现。
func TestChannelStateSnapshot(t *testing.T) {
	cs := NewChannelState()
	cs.Cooldown(1, 30*time.Second)
	cs.NoteFailure(1)
	cs.NoteFailure(1)
	cs.RecordLatency(2, 300)
	cs.Acquire(2, 10)

	now := time.Now()
	snap := cs.Snapshot(now)

	rt, ok := snap[1]
	if !ok {
		t.Fatal("渠道 1 有冷却与连击，必须出现在快照里")
	}
	if rt.CooldownMS <= 0 || rt.CooldownMS > 30_000 {
		t.Fatalf("冷却剩余应落在 (0, 30000] 毫秒，实际 %d", rt.CooldownMS)
	}
	if rt.FailStreak != 2 {
		t.Fatalf("连击应为 2，实际 %d", rt.FailStreak)
	}

	rt2, ok := snap[2]
	if !ok {
		t.Fatal("渠道 2 有延迟与在途，必须出现在快照里")
	}
	if rt2.CooldownMS != 0 {
		t.Fatalf("渠道 2 不在冷却，剩余应为 0，实际 %d", rt2.CooldownMS)
	}
	if rt2.LatencyMS != 300 || rt2.Inflight != 1 {
		t.Fatalf("延迟/在途不符：%+v", rt2)
	}

	// 快照必须是副本：改它不许影响内部状态
	delete(snap, 1)
	if _, ok := cs.Snapshot(time.Now())[1]; !ok {
		t.Fatal("快照是副本，外部删除不应影响后续快照")
	}
}
