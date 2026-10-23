package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
)

// 这组测试锁定的是「开机竞态」的判定边界：哪些错误值得等，哪些一出现
// 就该立刻失败。判错的代价不对称 ——
//   - 该重试却立即失败：服务开机起不来（本次修复的原始症状）；
//   - 不该重试却等待：配置写错的部署要白等 30 秒才看到真正的原因。
func TestIsRetryableStartupError(t *testing.T) {
	// 这两个错误字符串是从真实部署里抄来的，不是臆造的：
	// 第一个来自本机复现（2026-10-23，把 DB 端口改成 59999），
	// 后面的 PostgreSQL 措辞来自 57P03 / 57P02 的官方文案。
	wrapped := func(s string) error {
		return fmt.Errorf("连接 PostgreSQL 失败: failed to connect to `user=postgres database=llm_relay`: 127.0.0.1:5432 (127.0.0.1): %s", s)
	}

	retryable := []struct {
		name string
		err  error
	}{
		{
			name: "端口尚未监听（本机实测原文）",
			err:  wrapped("dial error: dial tcp 127.0.0.1:5432: connectex: No connection could be made because the target machine actively refused it."),
		},
		{
			name: "PostgreSQL 正在启动",
			err:  errors.New(`failed to connect: server error: FATAL: the database system is starting up (SQLSTATE 57P03)`),
		},
		{
			name: "PostgreSQL 恢复中",
			err:  errors.New("server error: FATAL: the database system is in recovery mode (SQLSTATE 57P02)"),
		},
		{
			name: "启动过程中被断开",
			err:  errors.New("failed to receive message: server closed the connection unexpectedly"),
		},
		{
			name: "syscall ECONNREFUSED（跨平台，Windows 的 WSAECONNREFUSED 也映射到这里）",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)},
		},
		{
			name: "连接被重置",
			err:  wrapped("read tcp 127.0.0.1:5000->127.0.0.1:5432: wsarecv: An existing connection was forcibly closed by the remote host."),
		},
		{
			name: "超时",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: &timeoutError{}},
		},
	}
	for _, tc := range retryable {
		t.Run("可重试/"+tc.name, func(t *testing.T) {
			if !isRetryableStartupError(tc.err) {
				t.Fatalf("应判定为可重试：%v", tc.err)
			}
		})
	}

	fatal := []struct {
		name string
		err  error
	}{
		{
			name: "密码错误",
			err:  errors.New(`failed to connect to ` + "`user=postgres database=llm_relay`" + `: server error: FATAL: password authentication failed for user "postgres" (SQLSTATE 28P01)`),
		},
		{
			name: "数据库不存在",
			err:  errors.New(`server error: FATAL: database "llm_relay" does not exist (SQLSTATE 3D000)`),
		},
		{
			name: "SQL 语法错误",
			err:  errors.New(`ERROR: syntax error at or near "ALTER" (SQLSTATE 42601)`),
		},
		{
			name: "上下文已取消（SCM 要求停止）",
			err:  context.Canceled,
		},
		{
			name: "nil",
			err:  nil,
		},
	}
	for _, tc := range fatal {
		t.Run("不重试/"+tc.name, func(t *testing.T) {
			if isRetryableStartupError(tc.err) {
				t.Fatalf("不应重试：%v", tc.err)
			}
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
