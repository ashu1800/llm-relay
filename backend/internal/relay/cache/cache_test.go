package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// 缓存是「请求 → 响应」的账外捷径：键算错等于把 A 的答案回给 B，
// 淘汰算错等于内存无界。这里的测试都是这两个事故的防线。

func TestCacheKeyCanonicalization(t *testing.T) {
	// stream 字段不参与：同一个请求要流式/不要流式，内容语义相同
	a := CacheKey("gpt-4o", []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`), true)
	b := CacheKey("gpt-4o", []byte(`{"stream":false,"messages":[{"role":"user","content":"hi"}],"model":"gpt-4o"}`), false)
	// 键序无关（上面两条的键序故意不同）但流式形态在键里（true vs false）
	if a == b {
		t.Error("流式形态必须进键：否则流式与非流式客户端互相覆盖缓存")
	}

	// 同语义不同键序（同形态）：必须命中同一个键
	c1 := CacheKey("m", []byte(`{"a":1,"b":{"x":2,"y":3}}`), false)
	c2 := CacheKey("m", []byte(`{"b":{"y":3,"x":2},"a":1}`), false)
	if c1 != c2 {
		t.Error("键序无关的规范化失效：语义相同的请求会错开成两个键")
	}

	// 模型名在键里：不同模型不共享缓存（哪怕请求体其它部分完全一致）
	if CacheKey("m1", []byte(`{"model":"m1"}`), false) == CacheKey("m2", []byte(`{"model":"m2"}`), false) {
		t.Error("不同模型不得共用缓存键")
	}

	// 非 JSON 退回原文，不 panic
	if CacheKey("m", []byte(`not-json`), false) == "" {
		t.Error("非 JSON 也要产出确定性的键")
	}
}

func TestCachePutGetAndTTL(t *testing.T) {
	c := New(50*time.Millisecond, 16, 1<<20)
	c.Put("k", &Entry{Body: []byte(`{"ok":true}`), CreatedAt: time.Now()})
	if e, ok := c.Get("k"); !ok || string(e.Body) != `{"ok":true}` {
		t.Fatalf("命中失败: %v %v", e, ok)
	}
	time.Sleep(60 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Error("过期条目必须按未命中处理")
	}
	if c.Stats().Entries != 0 {
		t.Error("过期条目应被顺手清掉")
	}
}

func TestCacheByteBoundEviction(t *testing.T) {
	// 总量 100 字节、单条 40 字节：塞 5 条必然淘汰最早的两条
	c := New(0, 16, 100)
	for i := 0; i < 5; i++ {
		c.Put(fmt.Sprintf("k%d", i), &Entry{Body: make([]byte, 40)})
	}
	st := c.Stats()
	if st.Bytes > 100 {
		t.Errorf("字节总量超界: %d", st.Bytes)
	}
	if _, ok := c.Get("k0"); ok {
		t.Error("最早的条目应已被 LRU 淘汰")
	}
	if _, ok := c.Get("k4"); !ok {
		t.Error("最新的条目应存活")
	}
}

func TestCacheEntryCountBound(t *testing.T) {
	c := New(0, 3, 1<<20)
	for i := 0; i < 10; i++ {
		c.Put(fmt.Sprintf("k%d", i), &Entry{Body: []byte("x")})
	}
	if st := c.Stats(); st.Entries > 3 {
		t.Errorf("条目数超界: %d", st.Entries)
	}
}

func TestCacheOversizedBodyRejected(t *testing.T) {
	c := New(0, 16, 100)
	c.Put("small", &Entry{Body: []byte("s")})
	c.Put("huge", &Entry{Body: make([]byte, 200)})
	if _, ok := c.Get("huge"); ok {
		t.Error("超过总量上限的单条响应必须整体拒收")
	}
	if _, ok := c.Get("small"); !ok {
		t.Error("拒收超大条目不应惊动已有条目")
	}
}

func TestCacheSameKeyOverwrite(t *testing.T) {
	c := New(0, 16, 1<<20)
	c.Put("k", &Entry{Body: []byte("first-16bytes")})
	c.Put("k", &Entry{Body: []byte("second")})
	if st := c.Stats(); st.Entries != 1 || st.Bytes != int64(len("second")) {
		t.Errorf("同键覆盖后应只剩一条: entries=%d bytes=%d", st.Entries, st.Bytes)
	}
}

func TestCacheFlushKeepsHitStats(t *testing.T) {
	c := New(0, 16, 1<<20)
	c.Put("k", &Entry{Body: []byte("v")})
	_, _ = c.Get("k")
	_, _ = c.Get("missing")
	n := c.Flush()
	if n != 1 {
		t.Fatalf("Flush 应清掉 1 条，实际 %d", n)
	}
	st := c.Stats()
	if st.Hits != 1 || st.Misses != 1 {
		t.Errorf("Flush 不得清零命中统计: %+v", st)
	}
}

// -race 下验证并发安全；同时用带 deadline 的循环证明不会死锁。
func TestCacheConcurrent(t *testing.T) {
	c := New(time.Minute, 64, 1<<20)
	done := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				k := fmt.Sprintf("k%d", i%32)
				if i%3 == 0 {
					c.Put(k, &Entry{Body: make([]byte, 64), CreatedAt: time.Now()})
				} else {
					_, _ = c.Get(k)
				}
			}
		}(w)
	}
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("并发操作未在预算内完成（疑似死锁）")
	}
	if st := c.Stats(); st.Entries > 64 {
		t.Errorf("并发下条目数超界: %d", st.Entries)
	}
}

// nil Cache 是「功能关闭」的运行形态：全部操作安全空转。
func TestNilCacheSafe(t *testing.T) {
	var c *Cache
	if _, ok := c.Get("k"); ok {
		t.Error("nil 缓存永远未命中")
	}
	c.Put("k", &Entry{Body: []byte("v")})
	if n := c.Flush(); n != 0 {
		t.Error("nil 缓存 Flush 应为 0")
	}
	_ = c.Stats()
}
