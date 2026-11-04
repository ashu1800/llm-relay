// Package cache 实现响应缓存（2026-11 P3）：精确匹配的「请求 → 响应」内存缓存。
//
// 与 LiteLLM 的 cache 对齐的最小个人版：无 Redis、无语义缓存，只有
// 精确匹配 + TTL + 有界内存。几个刻意的取舍（详见 api 侧接线处的注释）：
//   - 缓存体是**归一化层的 OpenAI Chat 响应**：同一份缓存可以被任何入站协议
//     重放（Gemini 请求能命中 OpenAI 请求存下的缓存），因为站内所有对话
//     协议的响应在转发层都已被换回 OpenAI Chat 形状，而回给客户端前还要
//     过一遍入站 Translator —— 重放与实时路径走的是同一段代码；
//   - 缓存键含流式形态（见 CacheKey）：「要流式的客户端」与「要整段的
//     客户端」互相不命中本来就是对齐 LiteLLM 的行为，若共用键还会互相
//     覆盖、把命中率打成 0；
//   - 双界约束：条目数与字节总量都以 LRU 淘汰，超限响应整体不缓存。
package cache

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Entry 是一条缓存：响应体 + 存储形态。
type Entry struct {
	// Body 是 OpenAI Chat 形状的响应：Stream=false 时是完整 JSON，
	// Stream=true 时是上游（经协议转换后的）SSE 字节流原文。
	// 重放时交给入站 Translator —— 与实时路径逐字节同构。
	Body []byte
	// Stream 标记 Body 的形态。缓存键已含请求的流式形态（见 CacheKey），
	// 这个字段是给重放路径与排障用的冗余事实。
	Stream    bool
	CreatedAt time.Time
	ExpiresAt time.Time
}

// cachedValue 是 LRU 链表元素挂的东西：条目 + 键（淘汰时要从链上删 map）。
type cachedValue struct {
	key   string
	entry *Entry
}

// Cache 是字节总量受限的 LRU。所有方法 nil 安全：nil Cache 是「功能关闭」
// 的运行形态（装配层没开），调用方不必到处判空。
type Cache struct {
	mu         sync.Mutex
	order      *list.List // front = 最近使用
	entries    map[string]*list.Element
	bytes      int64
	maxBytes   int64
	maxEntries int
	ttl        time.Duration

	// 命中/未命中是进程生命周期内的累计值（Flush 不清零）：命中率是
	// 「这个功能有没有用」的长期证据，清空缓存的动作不该抹掉它。
	hits   int64
	misses int64
}

// New 构造缓存。ttl <= 0 视为永久；上限非法（<=0）时各自兜底到保守默认 ——
// 一个无界的内存缓存是事故，宁可配置错了变保守。
func New(ttl time.Duration, maxEntries int, maxTotalBytes int64) *Cache {
	if maxEntries <= 0 {
		maxEntries = 1024
	}
	if maxTotalBytes <= 0 {
		maxTotalBytes = 128 << 20
	}
	return &Cache{
		order:      list.New(),
		entries:    make(map[string]*list.Element, maxEntries),
		maxBytes:   maxTotalBytes,
		maxEntries: maxEntries,
		ttl:        ttl,
	}
}

// keyVersion 是缓存键的格式版本：键的构造规则变了就递增，避免重启后
// 新旧键混存（值不会错，但命中率会无声地劣化，排查方向还错）。
const keyVersion = "v1"

// CacheKey 计算请求的缓存键。
//
// 规范化三步：删 stream 字段（流式与否不影响内容，形态已单独入键）、
// 键序无关（map 序列化天然按键排序）、sha256。
// body 必须是**归一化后的 OpenAI Chat 请求体** —— 缓存建在归一化层，
// 这正是「Gemini 请求命中 OpenAI 请求的缓存」的来源。
//
// 数字用 json.Number 保真：float64 往返会把 2^53 以上的整数（超长 seed）
// 变形，两个原本相同的请求会因序列化差异错开成不同键。
func CacheKey(publicModel string, body []byte, stream bool) string {
	canonical := canonicalize(body)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%t|%s", keyVersion, publicModel, stream, canonical)))
	return hex.EncodeToString(sum[:])
}

// canonicalize 把 JSON 请求体变成键序无关、无 stream 字段的规范形。
// 非 JSON（构造坏的报文）退回原文 —— 它们会在白名单/转换处被拦，
// 这里只为不 panic。
func canonicalize(body []byte) string {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var payload any
	if err := dec.Decode(&payload); err != nil {
		return string(body)
	}
	if m, ok := payload.(map[string]any); ok {
		delete(m, "stream")
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return string(body)
	}
	return string(out)
}

// Get 查缓存。命中时把条目挪到队首（LRU 的新鲜度）；过期按未命中处理
// 并顺手清掉。nil 接收者永远未命中。
func (c *Cache) Get(key string) (*Entry, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.entries[key]
	if !ok {
		c.misses++
		return nil, false
	}
	val := el.Value.(*cachedValue)
	if !val.entry.ExpiresAt.IsZero() && time.Now().After(val.entry.ExpiresAt) {
		c.removeElement(el)
		c.misses++
		return nil, false
	}
	c.order.MoveToFront(el)
	c.hits++
	return val.entry, true
}

// Put 写缓存。超过单条响应上限的 body 直接拒收（byteTotal 淘汰是给
// 「合法但很多」的场景准备的，「一条就撑爆」的场景不该挤掉别人）。
// 容量超限从队尾（最久未用）开始淘汰，直到装得下为止。
func (c *Cache) Put(key string, e *Entry) {
	if c == nil || e == nil || len(e.Body) == 0 {
		return
	}
	if int64(len(e.Body)) > c.maxBytes {
		return
	}
	if c.ttl > 0 {
		now := time.Now()
		if e.CreatedAt.IsZero() {
			e.CreatedAt = now
		}
		e.ExpiresAt = e.CreatedAt.Add(c.ttl)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// 同键覆盖：先摘旧条目再插入，字节账才不会双重计算
	if el, ok := c.entries[key]; ok {
		c.removeElement(el)
	}
	for c.bytes+int64(len(e.Body)) > c.maxBytes || len(c.entries) >= c.maxEntries {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.removeElement(oldest)
	}
	el := c.order.PushFront(&cachedValue{key: key, entry: e})
	c.entries[key] = el
	c.bytes += int64(len(e.Body))
}

// Flush 清空全部条目，返回清掉的条数。命中统计保留（见字段注释）。
func (c *Cache) Flush() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.entries)
	c.entries = make(map[string]*list.Element, c.maxEntries)
	c.order.Init()
	c.bytes = 0
	return n
}

// Stats 是给管理台的快照。
type Stats struct {
	Enabled    bool    `json:"enabled"`
	Entries    int     `json:"entries"`
	Bytes      int64   `json:"bytes"`
	MaxBytes   int64   `json:"max_bytes"`
	Hits       int64   `json:"hits"`
	Misses     int64   `json:"misses"`
	HitRate    float64 `json:"hit_rate"`
	TTLSeconds int     `json:"ttl_seconds"`
	MaxEntries int     `json:"max_entries"`
}

func (c *Cache) Stats() Stats {
	if c == nil {
		return Stats{Enabled: false}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	total := c.hits + c.misses
	rate := 0.0
	if total > 0 {
		rate = float64(c.hits) / float64(total)
	}
	ttlSecs := 0
	if c.ttl > 0 {
		ttlSecs = int(c.ttl.Seconds())
	}
	return Stats{
		Enabled:    true,
		Entries:    len(c.entries),
		Bytes:      c.bytes,
		MaxBytes:   c.maxBytes,
		Hits:       c.hits,
		Misses:     c.misses,
		HitRate:    rate,
		TTLSeconds: ttlSecs,
		MaxEntries: c.maxEntries,
	}
}

// removeElement 从链与 map 里摘除一个条目并扣字节账（须持锁）。
func (c *Cache) removeElement(el *list.Element) {
	val := el.Value.(*cachedValue)
	c.bytes -= int64(len(val.entry.Body))
	delete(c.entries, val.key)
	c.order.Remove(el)
}
