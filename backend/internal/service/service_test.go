package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

// serve 是「运行主函数 + 等待停止请求」的唯一实现：控制台信号与
// Windows SCM 的 stop/shutdown 都走它。两条路径各写一份的话，
// 迟早出现「Linux 能优雅关闭、Windows 直接被杀」这类只在一侧发生的偏差。
//
// 这里测的是它的三条硬契约。

// mainFn 的错误必须原样上抛 —— 启动失败（配置错、数据库连不上）
// 要能让调用方走 os.Exit(1)。
func TestServePropagatesError(t *testing.T) {
	sentinel := errors.New("数据库连不上")

	err := serve(make(chan struct{}), func(context.Context) error {
		return sentinel
	})

	if !errors.Is(err, sentinel) {
		t.Fatalf("应原样返回 mainFn 的错误，实际: %v", err)
	}
}

// 停止请求到达时，ctx 必须被取消，且 serve 要等 mainFn 收尾完才返回。
// 「等它收尾完」是关键：优雅关闭（日志 flush）就在 mainFn 里，
// 先返回再由 os.Exit 收场会把没落库的日志全丢掉。
func TestServeCancelsContextOnStop(t *testing.T) {
	stop := make(chan struct{})
	ctxCanceled := make(chan struct{})
	mainFnReturned := make(chan struct{})

	go func() {
		err := serve(stop, func(ctx context.Context) error {
			<-ctx.Done()
			close(ctxCanceled)
			time.Sleep(50 * time.Millisecond) // 模拟优雅关闭收尾
			close(mainFnReturned)
			return nil
		})
		if err != nil {
			t.Errorf("正常收尾不该报错: %v", err)
		}
	}()

	close(stop)

	select {
	case <-ctxCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("停止请求后 ctx 未被取消")
	}
	// ctx 取消只代表 mainFn 开始收尾；serve 必须等它真的返回。
	// 收尾要在 2 秒内完成（测试里是 50ms），超时说明 serve 提前返回了。
	select {
	case <-mainFnReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("serve 在 mainFn 收尾完成前就返回了")
	}
}

// 没有任何停止请求时不能自行结束 —— 服务是常驻的。
func TestServeWaitsWithoutStop(t *testing.T) {
	stop := make(chan struct{})
	returned := make(chan struct{})

	go func() {
		_ = serve(stop, func(ctx context.Context) error {
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
			return nil
		})
		close(returned)
	}()

	select {
	case <-returned:
		t.Fatal("没有停止请求时不应结束")
	case <-time.After(150 * time.Millisecond):
	}
	close(stop) // 收尾，避免测试结束后留下 goroutine
}

// 非服务形态下 Run 必须把 ctx 交给 mainFn，并把返回值原样交还。
func TestRunPassesContextThrough(t *testing.T) {
	var gotCtx context.Context

	err := Run("llm-relay-test", func(ctx context.Context) error {
		gotCtx = ctx
		return nil
	})

	if err != nil {
		t.Fatalf("Run 不该凭空造出错误: %v", err)
	}
	if gotCtx == nil {
		t.Fatal("mainFn 必须拿到 ctx")
	}
	if gotCtx.Done() == nil {
		t.Fatal("ctx 必须是可取消的（否则停止信号传不进来）")
	}
}
