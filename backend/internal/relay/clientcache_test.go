package relay

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// slogForBuffer 让用例能断言「日志里到底出现了什么」——
// 本组用例断言的正是「不该出现的告警」，只能从输出文本里看。
func slogForBuffer(buf io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// 回归 2026-10-22 生产事故：只有 7–19 个真实客户端，却每 10–40 秒告警一次
// 「已达上限 limit=2048」。
//
// 根因是 rememberAliasKey 把「键已存在」与「容量已满」都返回 false，
// 调用方一律当作「已达上限」并打 WARN —— 每个已登记客户端每写一条日志
// 就告警一次。下面这组用例把「已存在 ≠ 满」这件事钉死。

func newTestCache(limit int, ttl time.Duration) *clientCache {
	return newClientCache(limit, ttl)
}

// 同一个键反复观测：只有第一次要落 DB，之后一律不落、不淘汰。
func TestClientCacheRepeatedKeyRegistersOnce(t *testing.T) {
	c := newTestCache(2048, time.Hour)

	first := c.Add("claude-code")
	if !first.Register {
		t.Fatal("首次观测应返回 Register=true")
	}
	for i := 0; i < 1000; i++ {
		res := c.Add("claude-code")
		if res.Register {
			t.Fatalf("第 %d 次重复观测不应再落 DB", i+2)
		}
		if res.Evicted {
			t.Fatalf("重复观测不应触发淘汰")
		}
	}
	if got := c.Stats().Entries; got != 1 {
		t.Fatalf("条目数 = %d，期望 1", got)
	}
	if got := c.Stats().Evicted; got != 0 {
		t.Fatalf("淘汰数 = %d，期望 0", got)
	}
}

// 这条是事故的直接复现：三个真实客户端各观测很多次，**一次淘汰都不该发生**，
// 更不该出现「已达上限」。
func TestClientCacheFewClientsNeverEvicts(t *testing.T) {
	c := newTestCache(2048, time.Hour)
	clients := []string{"claude-code", "deepseek-harness", "browser"}

	registered := map[string]int{}
	for round := 0; round < 2000; round++ {
		for _, k := range clients {
			if c.Add(k).Register {
				registered[k]++
			}
		}
	}
	for _, k := range clients {
		if registered[k] != 1 {
			t.Errorf("%s 登记了 %d 次，期望 1 次", k, registered[k])
		}
	}
	st := c.Stats()
	if st.Evicted != 0 || st.Entries != len(clients) {
		t.Fatalf("统计 = %+v，期望 entries=3 evicted=0", st)
	}
}

// 超过上限时按 LRU 淘汰而不是拒绝新键：新键永远能进来，被挤掉的是最久未用的。
func TestClientCacheEvictsLRUWhenFull(t *testing.T) {
	c := newTestCache(3, time.Hour)

	for _, k := range []string{"a", "b", "c"} {
		if !c.Add(k).Register {
			t.Fatalf("%s 应登记", k)
		}
	}
	// 碰一下 a，让 b 成为最久未用的
	if c.Add("a").Register {
		t.Fatal("a 已存在，不应重复登记")
	}

	res := c.Add("d")
	if !res.Register {
		t.Fatal("容量满时新键仍必须登记（淘汰旧条目，而不是拒绝）")
	}
	if !res.Evicted {
		t.Fatal("应报告发生了淘汰")
	}

	// b 被淘汰：再观测时应当重新登记
	if !c.Add("b").Register {
		t.Fatal("被淘汰的键再次出现时应重新登记")
	}
	// a 与 d 仍在：不应重复登记
	if c.Add("a").Register {
		t.Fatal("最近用过的 a 不该被淘汰")
	}
	// 淘汰 b 的插入把 c 挤掉了 → c 也不再在缓存里
	if !c.Add("c").Register {
		t.Fatal("c 应已被更近的使用淘汰")
	}
	st := c.Stats()
	if st.Entries > 3 {
		t.Fatalf("条目数 %d 超过上限 3", st.Entries)
	}
	if st.Evicted == 0 {
		t.Fatal("淘汰数应大于 0")
	}
}

// 5000 个不同 UA 键、上限 2048：全部都能「登记成功」（不拒绝），
// 内存占用被上限封住。这是验收标准第 4 条的形状。
func TestClientCacheManyKeysBounded(t *testing.T) {
	const limit = 2048
	c := newTestCache(limit, time.Hour)

	registered := 0
	for i := 0; i < 5000; i++ {
		key := fmt.Sprintf("ua-token-%d", i)
		if c.Add(key).Register {
			registered++
		}
	}
	if registered != 5000 {
		t.Fatalf("登记成功 %d 个，期望 5000（超限应淘汰而不是拒绝）", registered)
	}
	st := c.Stats()
	if st.Entries != limit {
		t.Fatalf("条目数 = %d，期望被上限封在 %d", st.Entries, limit)
	}
	if st.Evicted != 5000-limit {
		t.Fatalf("淘汰数 = %d，期望 %d", st.Evicted, 5000-limit)
	}
}

// TTL：超过 ttl 没被观测到的条目应被回收，再次出现时重新登记。
func TestClientCacheTTLExpiresIdleEntries(t *testing.T) {
	c := newTestCache(10, time.Hour)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }

	c.Add("stale")
	now = now.Add(30 * time.Minute)
	c.Add("fresh")

	// 推进到 stale 过期、fresh 还活着的位置
	now = now.Add(45 * time.Minute) // stale 已 75 分钟没出现，fresh 只有 45 分钟
	st := c.Stats()
	if st.Expired != 1 {
		t.Fatalf("过期回收 %d 条，期望 1 条", st.Expired)
	}
	if st.Entries != 1 {
		t.Fatalf("条目数 = %d，期望 1", st.Entries)
	}
	if !c.Add("stale").Register {
		t.Fatal("过期条目再次出现时应重新登记")
	}
	if c.Add("fresh").Register {
		t.Fatal("未过期的条目不应重新登记")
	}
}

// TTL 为 0 表示不过期（容量仍是硬边界）。
func TestClientCacheZeroTTLNeverExpires(t *testing.T) {
	c := newTestCache(10, 0)
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	c.Add("x")
	now = now.Add(365 * 24 * time.Hour)
	if st := c.Stats(); st.Expired != 0 || st.Entries != 1 {
		t.Fatalf("TTL=0 时不该过期，实际 %+v", st)
	}
}

// 并发安全：多个 goroutine 同时观测同一批键，-race 必须干净，
// 且每个键**恰好**只登记一次（不能因为并发而重复落 DB）。
//
// 键空间（32）小于容量（64），所以不会有淘汰 —— 这条要钉的是
// 「并发下 Add 的判重不失效」，淘汰路径由后面的用例单独覆盖。
func TestClientCacheConcurrent(t *testing.T) {
	c := newTestCache(64, time.Hour)

	const workers = 16
	const perWorker = 500
	const keyspace = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	registered := map[string]int{}

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				key := fmt.Sprintf("k-%d", (w*perWorker+i)%keyspace)
				if c.Add(key).Register {
					mu.Lock()
					registered[key]++
					mu.Unlock()
				}
				_ = c.Stats()
			}
		}(w)
	}
	wg.Wait()

	for k, n := range registered {
		if n != 1 {
			t.Errorf("键 %s 被登记了 %d 次，期望恰好 1 次", k, n)
		}
	}
	if got := c.Stats().Entries; got != keyspace {
		t.Fatalf("条目数 = %d，期望 %d", got, keyspace)
	}
}

// 并发 + 持续淘汰（键空间远大于容量）：-race 必须干净、条目数始终有界，
// 且不能因为并发而让上限被短暂突破太多。
func TestClientCacheConcurrentWithEviction(t *testing.T) {
	const limit = 64
	c := newTestCache(limit, time.Hour)

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				c.Add(fmt.Sprintf("k-%d", w*2000+i))
				if i%50 == 0 {
					if st := c.Stats(); st.Entries > limit {
						t.Errorf("条目数 %d 超过上限 %d", st.Entries, limit)
						return
					}
				}
			}
		}(w)
	}
	wg.Wait()
	if st := c.Stats(); st.Entries > limit {
		t.Fatalf("条目数 %d 超过上限 %d", st.Entries, limit)
	}
}

// 删除后用同一把锁的 Remove 生效（管理接口删除映射的路径）。
func TestClientCacheRemove(t *testing.T) {
	c := newTestCache(10, time.Hour)
	c.Add("gone")
	c.Remove("gone")
	if c.Stats().Entries != 0 {
		t.Fatal("Remove 之后条目应消失")
	}
	if !c.Add("gone").Register {
		t.Fatal("Remove 之后再次观测应重新登记")
	}
	c.Remove("") // 空键不应 panic
}

// 告警限频：首次放行，间隔内重复被压掉并计数，间隔外放行时把压掉的条数带出来。
func TestWarnThrottle(t *testing.T) {
	th := newWarnThrottle(5 * time.Minute)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	th.now = func() time.Time { return now }

	if ok, _ := th.allow(); !ok {
		t.Fatal("首次应放行")
	}
	for i := 0; i < 100; i++ {
		if ok, _ := th.allow(); ok {
			t.Fatalf("第 %d 次重复不应放行", i+2)
		}
	}
	now = now.Add(5*time.Minute - time.Second)
	if ok, _ := th.allow(); ok {
		t.Fatal("间隔内不应放行")
	}
	now = now.Add(2 * time.Second) // 越过 5 分钟
	ok, suppressed := th.allow()
	if !ok {
		t.Fatal("间隔外应放行")
	}
	if suppressed != 101 {
		t.Fatalf("压掉的条数 = %d，期望 101", suppressed)
	}
	// 计数在放行后归零
	now = now.Add(6 * time.Minute)
	_, suppressed = th.allow()
	if suppressed != 0 {
		t.Fatalf("计数应在放行后归零，实际 %d", suppressed)
	}
}

// 端到端：LogWriter 在「重复观测已登记客户端」时不得打出任何告警。
// 事故形态就是这条 —— 三个客户端常规流量，日志里却全是「已达上限」。
func TestLogWriterNoLimitWarnForRepeatedClients(t *testing.T) {
	var buf strings.Builder
	w := NewLogWriterWithClientMap(newBrokenDB(t), 64, slogForBuffer(&buf), ClientMapOptions{
		Limit: 8,
		TTL:   time.Hour,
	})
	defer w.Close()

	// 直接驱动缓存（DB 连不上，走 logClientAlias 会因写失败摘键，
	// 造不出「已登记」的稳态）
	for _, k := range []string{"claude-code", "deepseek-harness", "browser"} {
		w.alias.Add(k)
	}
	for i := 0; i < 50; i++ {
		for _, k := range []string{"claude-code", "deepseek-harness", "browser"} {
			if got := w.alias.Add(k); got.Register {
				t.Fatalf("已登记的 %s 不应再次登记", k)
			}
		}
	}
	if out := buf.String(); strings.Contains(out, "客户端映射缓存已满") {
		t.Fatalf("容量远未用满时不该出现容量告警：\n%s", out)
	}
}

// 容量告警本身也要限频：上限调到 2、灌 100 个新键，只该留下少量告警
// （验收标准第 5 条）。同时告警必须带上可诊断的字段。
//
// 直接驱动 alias 缓存并手工触发一次告警路径：测试库连不上，
// logClientAlias 会因写失败把键摘掉，容量永远填不满。
func TestClientCacheLimitWarnIsThrottled(t *testing.T) {
	var buf strings.Builder
	w := NewLogWriterWithClientMap(newBrokenDB(t), 64, slogForBuffer(&buf), ClientMapOptions{
		Limit: 2,
		TTL:   time.Hour,
	})
	defer w.Close()

	for i := 0; i < 100; i++ {
		res := w.alias.Add(fmt.Sprintf("ua-%d", i))
		if !res.Evicted {
			continue
		}
		if ok, suppressed := w.aliasLimitWarn.allow(); ok {
			st := w.alias.Stats()
			slogForBuffer(&buf).Warn("客户端映射缓存已满，按最近最少使用淘汰旧条目",
				"key", fmt.Sprintf("ua-%d", i), "limit", st.Limit, "entries", st.Entries,
				"evicted", st.Evicted, "expired", st.Expired, "suppressed", suppressed)
		}
	}
	out := buf.String()
	n := strings.Count(out, "客户端映射缓存已满")
	if n == 0 {
		t.Fatal("容量满时应至少告警一次")
	}
	if n > 2 {
		t.Fatalf("100 次淘汰触发了 %d 条容量告警，限频失效：\n%s", n, out)
	}
	for _, field := range []string{"limit=2", "entries=", "evicted="} {
		if !strings.Contains(out, field) {
			t.Errorf("告警缺少诊断字段 %s：\n%s", field, out)
		}
	}
}

// 边界：TTL 回收与容量淘汰在同一次 Add 里同时发生，计数不能互相吞掉。
// （回收先跑、淘汰后跑，两者都从尾部弹；顺序错了会出现 entries 超限
// 或者 evicted/expired 记到彼此账上。）
func TestClientCacheExpireAndEvictSameCall(t *testing.T) {
	c := newTestCache(1, time.Hour)
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }

	c.Add("old")
	now = now.Add(2 * time.Hour) // old 过期
	res := c.Add("new")
	if !res.Register {
		t.Fatal("new 应登记")
	}
	if res.Evicted {
		t.Error("old 是过期回收而不是容量淘汰，不该计入 Evicted")
	}
	st := c.Stats()
	if st.Entries != 1 || st.Expired != 1 || st.Evicted != 0 {
		t.Fatalf("统计 = %+v，期望 entries=1 expired=1 evicted=0", st)
	}

	// 容量为 1、两条都新鲜 → 第二次插入必须淘汰。
	// 此时缓存里是上一段的 "new"（未过期），它会被 "a" 挤掉。
	now = now.Add(time.Minute)
	if res := c.Add("a"); !res.Evicted {
		t.Fatal("容量 1 里插入第二条新鲜条目时应走容量淘汰")
	}
	// Evicted / Expired 是**进程累计值**，不是本次调用的增量。
	st = c.Stats()
	if st.Entries != 1 || st.Evicted != 1 || st.Expired != 1 {
		t.Fatalf("统计 = %+v，期望 entries=1 evicted=1 expired=1（累计）", st)
	}
}
