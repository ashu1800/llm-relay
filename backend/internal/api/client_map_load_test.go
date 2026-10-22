package api

// 验收标准第 4、5 条的真库版（2026-10-22 事故修复）：
//
//	4. 5000 个不同 UA —— 不超限时全部登记；超限时按 LRU 淘汰而不是拒绝；
//	   内存有界；-race 干净；
//	5. 上限调到极小（2）时，告警不再刷屏。
//
// 这两条只有在「真库 + 真 LogWriter 协程」下才有意义：淘汰后重新出现的
// 键要真的再落一行（或命中 ON CONFLICT），告警要从 slog 输出里数出来。

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"llm-relay/internal/model"
	"llm-relay/internal/relay"
)

func openVerifyDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("LLMRELAY_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 LLMRELAY_TEST_DSN，跳过 PG 集成测试")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("连测试库失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, _ := db.DB(); sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&model.RequestLog{}, &model.ClientAlias{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}

// 5000 个不同 UA 经真实 handler 入链，全部登记；缓存条目封在上限内。
func TestClientMapManyDistinctUAs(t *testing.T) {
	db := openVerifyDB(t)
	const limit = 2048
	const n = 5000

	var buf strings.Builder
	// 队列要容得下全部记录：本用例测的是映射缓存，队列满了会丢日志
	// （丢弃是队列的既有语义，有 dropped 计数），那会把登记数搅浑。
	logs := relay.NewLogWriterWithClientMap(db, n+1024, slogForBuffer(&buf), relay.ClientMapOptions{
		Limit: limit,
		TTL:   time.Hour,
	})
	if err := db.Where("key LIKE ?", "ua-%").Delete(&model.ClientAlias{}).Error; err != nil {
		t.Fatalf("清理失败: %v", err)
	}

	// 并发灌入：模拟多 goroutine 的请求路径。Limit 是并发安全边界，
	// 这里不只要结果对，还要 -race 干净。
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := worker; i < n; i += 8 {
				logs.Enqueue(model.RequestLog{
					TraceID:    fmt.Sprintf("ua-%d", i),
					ClientID:   fmt.Sprintf("ua-%d", i),
					ClientName: fmt.Sprintf("ua-%d", i),
				}, nil)
			}
		}(worker)
	}
	wg.Wait()
	if !logs.CloseAndFlush(60*time.Second, nil) {
		t.Fatal("日志队列未在预算内写完")
	}

	qst := logs.QueueStats()
	if qst.Dropped != 0 {
		t.Fatalf("队列丢了 %d 条日志，用例前提不成立（队列容量 %d）", qst.Dropped, qst.Capacity)
	}

	st := logs.ClientCacheStats()
	if st.Limit != limit {
		t.Errorf("Limit = %d，期望 %d", st.Limit, limit)
	}
	if st.Entries > limit {
		t.Errorf("条目数 %d 超过上限 %d", st.Entries, limit)
	}
	// 每个键都是首次观测 → 每次插入都算一次「观测」；超出的部分全部
	// 通过淘汰腾位，而不是被拒绝（被拒绝时 Evicted 会是 0、登记数会停住）。
	if st.Evicted != n-limit {
		t.Errorf("淘汰数 = %d，期望 %d（超限应淘汰而不是拒绝）", st.Evicted, n-limit)
	}

	// 「不拒绝」的直接证据：每个键都至少走到过一次落库尝试。
	// 缓存里只有最近 limit 个，但 DB 行数应当覆盖全部 n 个键
	// （ON CONFLICT DO NOTHING 不会挡住插入）。
	var registered int64
	if err := db.Model(&model.ClientAlias{}).Where("key LIKE ?", "ua-%").Count(&registered).Error; err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if registered != n {
		t.Errorf("登记了 %d 个键，期望 %d", registered, n)
	}

	// 告警：容量告警最多一条（首次），且带诊断字段；不应有旧文案
	if got := strings.Count(buf.String(), "客户端映射缓存已满"); got > 1 {
		t.Errorf("容量告警 %d 条，限频失效：\n%s", got, buf.String())
	}
	if strings.Contains(buf.String(), "客户端映射键数量已达上限，不再自动登记新客户端") {
		t.Errorf("旧文案不该再出现：\n%s", buf.String())
	}
}

// 上限调到 2、灌 100 个不同客户端：告警只出现一次（限定频），
// 且**所有**键仍然被登记 —— 这正是「淘汰而不是拒绝」。
func TestClientMapSmallLimitWarnsOnce(t *testing.T) {
	db := openVerifyDB(t)

	var buf strings.Builder
	logs := relay.NewLogWriterWithClientMap(db, 512, slogForBuffer(&buf), relay.ClientMapOptions{
		Limit: 2,
		TTL:   time.Hour,
	})
	if err := db.Where("key LIKE ?", "tiny-%").Delete(&model.ClientAlias{}).Error; err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	for i := 0; i < 100; i++ {
		logs.Enqueue(model.RequestLog{
			TraceID:    fmt.Sprintf("tiny-%d", i),
			ClientID:   fmt.Sprintf("tiny-%d", i),
			ClientName: fmt.Sprintf("tiny-%d", i),
		}, nil)
	}
	if !logs.CloseAndFlush(30*time.Second, nil) {
		t.Fatal("日志队列未在预算内写完")
	}

	out := buf.String()
	if n := strings.Count(out, "客户端映射缓存已满"); n != 1 {
		t.Errorf("100 次淘汰应只告警 1 次（首次），实际 %d 次：\n%s", n, out)
	}
	for _, field := range []string{"limit=2", "entries=", "evicted="} {
		if !strings.Contains(out, field) {
			t.Errorf("告警缺少诊断字段 %s：\n%s", field, out)
		}
	}

	var registered int64
	if err := db.Model(&model.ClientAlias{}).Where("key LIKE ?", "tiny-%").Count(&registered).Error; err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if registered != 100 {
		t.Errorf("登记了 %d 个键，期望 100（容量满也必须登记新客户端）", registered)
	}
}

// 正常流量（少量真实客户端）跑很多轮：日志里不得出现任何容量告警。
// 这是事故现场的直接对照 —— 三个客户端 + 2048 上限却天天刷告警。
func TestClientMapRealisticTrafficSilent(t *testing.T) {
	db := openVerifyDB(t)
	var buf strings.Builder
	logs := relay.NewLogWriterWithClientMap(db, 1024, slogForBuffer(&buf), relay.ClientMapOptions{
		Limit: 2048,
		TTL:   time.Hour,
	})
	clients := []string{"claude-code", "deepseek-harness", "browser"}
	for round := 0; round < 500; round++ {
		for _, c := range clients {
			logs.Enqueue(model.RequestLog{
				TraceID:    fmt.Sprintf("%s-%d", c, round),
				ClientID:   c,
				ClientName: c,
			}, nil)
		}
	}
	if !logs.CloseAndFlush(30*time.Second, nil) {
		t.Fatal("日志队列未在预算内写完")
	}
	if out := buf.String(); strings.Contains(out, "客户端映射") {
		t.Fatalf("正常流量下不该出现任何映射告警：\n%s", out)
	}
}

// slogForBuffer 让用例能数「日志里到底出现了几条告警」。
func slogForBuffer(buf io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
