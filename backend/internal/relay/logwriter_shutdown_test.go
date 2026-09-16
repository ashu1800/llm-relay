package relay

import (
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"llm-relay/internal/model"
)

// newTestWriter 造一个「数据库连不上、但*gorm.DB 不是 nil」的写入器。
//
// 为什么不能直接传 nil：loop 真的会调用 db.Create，nil 会 panic
// （用 -race 连跑时暴露出来的）。这里用一个指向必然拒绝连接的端口的
// postgres 驱动 + DisableAutomaticPing：gorm.Open 本身不连库，
// 之后每次 Create 都立刻失败（连接被拒），loop 按设计记一条告警继续 ——
// 这正是本组测试要的：投递/关闭的并发安全与 db 无关，
// 而 loop 必须真的在跑。
func newTestWriter(buffer int) *LogWriter {
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=127.0.0.1 port=1 user=nobody dbname=nothing sslmode=disable",
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		// 没有真实连接时 gorm.Open 仍应成功；真失败了说明驱动行为变了
		panic("构造测试用 gorm.DB 失败: " + err.Error())
	}
	return NewLogWriter(db, buffer, nil)
}

func mustLog(id int) model.RequestLog {
	return model.RequestLog{TraceID: "t", ModelRequested: "m"}
}

// 往已关闭的通道发送会 panic —— 即使写在 select 里也一样：
// select 选中一个「已就绪」的发送分支时，那次发送照样 panic。
// 这个测试把结论钉下来，因为 LogWriter.Enqueue 正是这么写的。
func TestSendOnClosedChannelPanics(t *testing.T) {
	ch := make(chan int, 1)
	close(ch)
	panicked := func() (p bool) {
		defer func() {
			if recover() != nil {
				p = true
			}
		}()
		// 带 default 也没用：发送分支一旦就绪就会被选中
		select {
		case ch <- 1:
		default:
		}
		return false
	}()
	if !panicked {
		t.Fatal("向已关闭的通道发送竟然没有 panic —— 这条前提变了，" +
			"LogWriter 里那套「关闭后仍可能 Enqueue」的防护理由需要重新评估")
	}
}

// 关闭之后 Enqueue 必须安全：丢弃 + 告警，绝不 panic。
//
// 为什么这不是理论问题：优雅关闭的顺序是「先等在途请求结束，再关日志队列」，
// 但如果等超时（shutdownHTTPTimeout），代码会继续往下走去关队列 ——
// 那个还在跑的请求稍后结束时就会 Enqueue。这时若 panic，整个进程崩掉，
// 比丢一条日志严重得多。
func TestLogWriterEnqueueAfterCloseIsSafe(t *testing.T) {
	w := newTestWriter(4)
	ok := w.CloseAndFlush(time.Second, nil)
	if !ok {
		t.Fatal("空队列的 CloseAndFlush 应当立即成功")
	}

	// 关闭之后反复投递，一次都不能 panic
	for i := 0; i < 10; i++ {
		w.Enqueue(mustLog(i), nil)
	}
}

// Close 与 CloseAndFlush 可以任意顺序、任意次数调用，不会因重复关闭而 panic。
func TestLogWriterCloseIsIdempotent(t *testing.T) {
	w := newTestWriter(4)
	w.Close()
	w.Close()
	if !w.CloseAndFlush(time.Second, nil) {
		t.Fatal("重复关闭后 CloseAndFlush 仍应成功（幂等）")
	}
	w.Close()
	w.CloseAndFlush(time.Second, nil)
	// 关闭之后投递也要安全
	w.Enqueue(mustLog(1), nil)
}

// 关闭之后不再接收新记录（队列真的关掉了，而不是「关了但还在收」）。
func TestLogWriterStopsAcceptingAfterClose(t *testing.T) {
	w := newTestWriter(4)
	w.CloseAndFlush(time.Second, nil)
	w.Enqueue(mustLog(7), nil)

	// 通过「队列已关闭」来间接确认：再 Enqueue 会被丢弃而不是入队
	if got := len(w.queue); got != 0 {
		t.Fatalf("关闭后队列里不该再有记录，实际有 %d 条", got)
	}
}

// 并发关闭 + 并发投递：一个都不能 panic。
//
// 这是上面那个真实场景的放大版 —— 关闭发生在「等在途请求」超时之后，
// 此时可能仍有若干请求正在收尾。用 -race 跑更有意义：
// 它同时验证 mu 真的把 queue 的读写保护住了。
func TestLogWriterConcurrentCloseAndEnqueue(t *testing.T) {
	w := newTestWriter(8)

	var wg sync.WaitGroup
	// 若干个写入者不停地投递
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				w.Enqueue(mustLog(j), nil)
			}
		}(i)
	}
	// 同时有若干个关闭者（含 CloseAndFlush）
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if n%2 == 0 {
				w.Close()
			} else {
				w.CloseAndFlush(time.Second, nil)
			}
		}(i)
	}
	wg.Wait()

	// 收尾：仍要安全
	w.CloseAndFlush(time.Second, nil)
	w.Enqueue(mustLog(1), nil)
}

// 队列里已有的记录必须真的被处理完（CloseAndFlush 的语义）。
//
// 这里没法断言「记录落库了」—— 测试用的库是连不上的（见 newTestWriter），
// 但那条路径恰好也是要验的：写入失败时 loop 会记告警并继续，不该卡住。
// 所以判据是「CloseAndFlush 返回 true 且写入协程已结束」：
// 反过来说，如果 loop 会被某条坏记录卡死，这个测试就会超时失败。
func TestLogWriterFlushDrainsQueue(t *testing.T) {
	w := newTestWriter(8)
	// 先塞满一批（库连不上，每条都会失败 —— loop 必须照单全收地走完）
	for i := 0; i < 8; i++ {
		w.Enqueue(mustLog(i), nil)
	}
	if !w.CloseAndFlush(3*time.Second, nil) {
		t.Fatal("队列没能写完：写入失败不该让 loop 卡住")
	}
	select {
	case <-w.done:
	default:
		t.Fatal("CloseAndFlush 返回后写入协程应当已经结束（done 已关闭）")
	}
}
