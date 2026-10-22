package relay

import (
	"container/list"
	"sync"
	"time"
)

// 客户端映射键缓存（承接 LogWriter.logClientAlias 的「本进程已登记过」判定）。
//
// 为什么要有上限：键来自请求方（未命中规则时取 UA 的第一个 product token，
// 见 ExtractClient），完全由调用方控制。逐请求换随机 UA 的爬虫/压测会让
// 这张缓存与 client_aliases 表无界增长，而后者不随日志保留期淘汰
// （2026-10-19 审查）。
//
// 为什么是 LRU + TTL 而不是「满了就拒绝」：后者会把「表满」变成永久状态 ——
// 一旦被随机 UA 灌满，真实客户端再也登记不进来，直到进程重启；而重启后
// 又会以同样的节奏被灌满。LRU 让容量始终服务于「最近在用的客户端」，
// TTL 让长期不出现的条目自动退场，两者合起来才有界且不误伤。
//
// 并发：Add / Remove / Stats 都只用一把短锁，锁内只有 map 与链表的 O(1)
// 操作，没有 IO、没有回调。调用方是日志写入协程（不在请求路径上），
// 即便拿不到锁也只是让这次登记晚一条日志，绝不会拖慢转发。
type clientCache struct {
	mu    sync.Mutex
	limit int
	ttl   time.Duration

	// now 可注入，供 TTL 用例推进时间；生产上是 time.Now。
	now func() time.Time

	// ll 的顺序是「最近使用在前」，尾部最旧。TTL 的过期判断只看 lastUsed，
	// 而条目的 ttl 相同、比较用的 now 相同，所以**过期条目必然连续落在尾部**
	// —— 回收只需从尾部弹，不必遍历整张表。
	ll    *list.List
	items map[string]*list.Element

	evicted int64
	expired int64
}

type aliasEntry struct {
	key      string
	lastUsed time.Time
}

// 默认值：容量与日志队列的缓冲（2048）是两回事，别混为一谈。
// 真实部署里不同的客户端以「几十」计，2048 足够宽松；TTL 取 7 天，
// 与默认日志保留期（30 天）同量级，远大于「一个客户端多久会再出现一次」。
const (
	DefaultClientMapLimit = 2048
	DefaultClientMapTTL   = 7 * 24 * time.Hour
)

func newClientCache(limit int, ttl time.Duration) *clientCache {
	if limit <= 0 {
		limit = DefaultClientMapLimit
	}
	if ttl < 0 {
		ttl = 0
	}
	return &clientCache{
		limit: limit,
		ttl:   ttl,
		now:   time.Now,
		ll:    list.New(),
		items: make(map[string]*list.Element),
	}
}

// aliasAddResult 是一次登记的判定结果。
type aliasAddResult struct {
	// Register 为 true 表示这个键需要落一行 DB（首次出现，或缓存条目已过期
	// 被回收后重新出现）。DB 侧有 uniqueIndex + ON CONFLICT DO NOTHING 兜底，
	// 「行已存在但仍需登记」只是多一次幂等写入，不会出错。
	Register bool
	// Evicted 表示这次插入挤掉了别的条目（容量已满），供告警诊断。
	Evicted bool
}

// Add 记一次观测，返回是否需要落 DB。
//
// 这是整个修复的核心：**「键已经在缓存里」与「容量不够」是两种完全不同的
// 结果**。旧实现把它们都表达成 false，调用方只能一律当成「已达上限」并
// 打 WARN —— 于是每个已登记客户端每写一条日志就告警一次，日志里看起来
// 「秒满」，实际上 2048 从未被触及（2026-10-22 生产事故）。
func (c *clientCache) Add(key string) aliasAddResult {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	// 先回收尾部过期条目：它们一定在尾部（见 ll 的注释）
	c.expireLocked(now)

	if el, ok := c.items[key]; ok {
		// 命中：只刷新使用位置，不落 DB、不告警
		el.Value.(*aliasEntry).lastUsed = now
		c.ll.MoveToFront(el)
		return aliasAddResult{}
	}

	el := c.ll.PushFront(&aliasEntry{key: key, lastUsed: now})
	c.items[key] = el
	res := aliasAddResult{Register: true}

	for len(c.items) > c.limit {
		back := c.ll.Back()
		if back == nil {
			break
		}
		old := back.Value.(*aliasEntry)
		c.ll.Remove(back)
		delete(c.items, old.key)
		c.evicted++
		res.Evicted = true
	}
	return res
}

// Remove 摘掉键（登记失败后重试、或管理接口删除映射后用）。
func (c *clientCache) Remove(key string) {
	if key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.ll.Remove(el)
		delete(c.items, key)
	}
}

func (c *clientCache) expireLocked(now time.Time) {
	if c.ttl <= 0 {
		return
	}
	for {
		back := c.ll.Back()
		if back == nil {
			return
		}
		e := back.Value.(*aliasEntry)
		if now.Sub(e.lastUsed) < c.ttl {
			return
		}
		c.ll.Remove(back)
		delete(c.items, e.key)
		c.expired++
	}
}

// ClientCacheStats 是映射缓存的瞬时快照，供告警与诊断使用。
type ClientCacheStats struct {
	// Limit 是容量上限（配置项 relay.client_map_limit）
	Limit int `json:"limit"`
	// Entries 是此刻缓存里的条目数（含尚未被触发的过期条目，顺带回收）
	Entries int `json:"entries"`
	// Evicted 是进程启动以来因容量满被淘汰的条数
	Evicted int64 `json:"evicted"`
	// Expired 是进程启动以来因 TTL 到期被回收的条数
	Expired int64 `json:"expired"`
}

// Stats 取快照。顺带做一次惰性回收，保证「静置很久之后」entries 也能如实
// 归零 —— 否则诊断日志会显示一堆早已失效的条目，误导容量判断。
func (c *clientCache) Stats() ClientCacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked(c.now())
	return ClientCacheStats{
		Limit:   c.limit,
		Entries: len(c.items),
		Evicted: c.evicted,
		Expired: c.expired,
	}
}

// warnThrottle 是告警限频器：「首次 + 每 interval 最多一次」，
// 并把被压掉的条数带在下一条里，避免「不刷屏」变成「看不见」。
//
// 为什么需要它：这类告警的触发条件与被观测的流量成正比，流量越大越刷 ——
// 而它要传达的信息（表满了/写失败了）在两次告警之间不会变，刷屏只会
// 把真正需要看的日志挤出视野（2026-10-22 那次每天 5000+ 条）。
type warnThrottle struct {
	interval time.Duration

	mu         sync.Mutex
	now        func() time.Time
	last       time.Time
	suppressed int64
}

func newWarnThrottle(interval time.Duration) *warnThrottle {
	return &warnThrottle{interval: interval, now: time.Now}
}

// allow 判断这次是否该真的打日志；第二返回值是上次以来被压掉的条数。
func (t *warnThrottle) allow() (bool, int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if !t.last.IsZero() && now.Sub(t.last) < t.interval {
		t.suppressed++
		return false, 0
	}
	n := t.suppressed
	t.suppressed = 0
	t.last = now
	return true, n
}
