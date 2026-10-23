package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"llm-relay/internal/config"
	"llm-relay/internal/store"
)

// 启动阶段的数据库等待预算。
//
// 为什么需要等：Windows 上服务由 SCM 启动，而 SCM 对 auto-start 服务是
// **并行**启动的 —— llm-relay 与 PostgreSQL 之间没有依赖关系时，本进程可能
// 比数据库先就绪。实测（2026-10-23）：开机 7 秒后 llm-relay 以退出码 1
// 终止（SCM 7023 "Incorrect function"），而此时 postgres.exe 才刚起来。
// 日志里一行都没有，因为失败发生在 logger 建立之前。
//
// 30 秒足够覆盖一台慢机器上 PostgreSQL 的启动时间，又不会让「配置写错、
// 数据库根本不存在」的部署在启动阶段白等几分钟。
const dbStartupWait = 30 * time.Second

// startupFailure 记录启动期的致命错误并原样返回。
//
// 存在的唯一理由：**服务形态下 stderr 不属于任何人**。
//
// SCM 启动的进程没有控制台，main 里那句
// fmt.Fprintf(os.Stderr, "启动失败: %v\n", err) 写到虚空里；日志文件里
// 也一行不会有，因为原来的实现要到 store.Open 之后再建立 logger。
// 实测（2026-10-23）：开机时服务以退出码 1 终止（事件日志 7023
// "Incorrect function"），而 logs\llm-relay.log 里从开机到手动重试之间
// 完全空白 —— 排查方向被彻底带偏，最后只能靠改配置端口逐个二分。
//
// 所以凡是「logger 已建立、服务还没对外提供」之间的失败，都要经过这里：
// 先写进文件，再交给上层以退出码 1 结束。logger 之外的失败（读配置、
// 建日志文件）仍然只能靠 stderr，那部分由安装器在启动失败时回读
// 日志文件与 `sc query` 兜底。
func startupFailure(logger *slog.Logger, err error) error {
	if logger != nil && err != nil {
		logger.Error("启动失败", "err", err)
	}
	return err
}

// openStoreWithRetry 连接数据库并跑完迁移，对**连接级**失败做有界重试。
//
// 只重试「数据库还没准备好」这一类错误（端口没监听、正在启动、连接被
// 重置）；认错密码、库不存在、SQL 语法错误这类一次性错误立即返回 ——
// 重试它们只会把一次明确的失败拖成半分钟的无响应。判定见
// isRetryableStartupError。
func openStoreWithRetry(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*store.Store, error) {
	deadline := time.Now().Add(dbStartupWait)
	delay := 500 * time.Millisecond

	for attempt := 1; ; attempt++ {
		st, err := openAndPrepareStore(cfg)
		if err == nil {
			if attempt > 1 {
				logger.Info("数据库已就绪", "attempts", attempt)
			}
			return st, nil
		}
		if !isRetryableStartupError(err) || time.Now().After(deadline) {
			return nil, err
		}

		logger.Warn("数据库暂不可用，稍后重试",
			"attempt", attempt,
			"retry_in", delay.String(),
			"err", err)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, err
		case <-timer.C:
		}
		if delay < 4*time.Second {
			delay *= 2
		}
	}
}

// openAndPrepareStore 是「连库 + 建表 + 种子数据 + 外键」的完整序列。
// 抽成函数是为了让重试覆盖全部四步：PostgreSQL 在启动过程中已经能接受
// TCP 连接、但还没准备好执行 DDL，此时失败发生在 Migrate 而不是 Open。
func openAndPrepareStore(cfg *config.Config) (*store.Store, error) {
	st, err := store.Open(cfg)
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(); err != nil {
		return nil, err
	}
	if err := st.Seed(); err != nil {
		return nil, err
	}
	// 必须排在 Seed 之后：回填模板分组需要默认分组已经存在，
	// 否则全新安装时这里找不到分组就直接跳过了，外键一直不会被建出来
	if err := st.EnsureForeignKeys(); err != nil {
		return nil, err
	}
	return st, nil
}

// isRetryableStartupError 判断启动期的数据库错误是否值得再等一会儿。
//
// 先看错误链（errors.Is）：pgx 把底层 syscall 错误原样包在 *net.OpError 里，
// 所以 ECONNREFUSED / ECONNRESET 能直接命中，且跨平台（Windows 上
// WSAECONNREFUSED 被 Go 映射到同一个 errno）。
//
// 再退回字符串匹配，因为真正要处理的两种「等一会儿就好了」的情况没有
// 对应的 errno：
//   - "the database system is starting up" / "is in recovery mode"：
//     PostgreSQL 能接受连接，但在完成恢复前拒绝一切语句（SQLSTATE 57P03）。
//     这是开机竞态最常见的形态 —— 端口已经监听，连得上，但 DDL 失败。
//   - "server closed the connection unexpectedly"：PostgreSQL 在启动过程中
//     主动断开（同样发生在恢复阶段）。
//
// 这里刻意**不**匹配 "password authentication failed" / "does not exist"：
// 那两个是配置错误，重试毫无意义，只会让安装失败晚 30 秒才报出来。
func isRetryableStartupError(err error) bool {
	if err == nil {
		return false
	}
	// 上下文被取消（SCM 要求停止）时不该再等
	if errors.Is(err, context.Canceled) {
		return false
	}
	for _, target := range []error{
		syscall.ECONNREFUSED,
		syscall.ECONNRESET,
		syscall.ETIMEDOUT,
		os.ErrDeadlineExceeded,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"connection refused",
		"connection reset",
		"actively refused", // Windows 措辞："actively refused it"
		"forcibly closed",  // Windows 措辞：WSAECONNRESET = "forcibly closed by the remote host"
		"starting up",      // PostgreSQL 57P03
		"in recovery mode", // PostgreSQL 57P02/57P03
		"server closed the connection unexpectedly",
		"i/o timeout",
		"no such host", // DNS 还没就绪（容器/虚拟网卡场景）
		"network is unreachable",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
