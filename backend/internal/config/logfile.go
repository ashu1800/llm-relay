package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// OpenLogWriter 打开日志输出。
//
// path 为空时返回 os.Stdout —— 这是 Linux 上的默认形态（日志交给
// systemd 的 journald 收），也是「本地直接在终端里跑」的形态。
//
// path 非空时返回一个会按 maxSizeMB 轮转的文件 writer。Windows 服务
// **必须**用这条路：SCM 启动的进程没有 stdout 接收方，写 stdout 等于
// 把日志丢进虚空，出任何问题都没有线索。
//
// maxSizeMB <= 0 表示不轮转（只管写，磁盘由运维自己盯）。
func OpenLogWriter(path string, maxSizeMB int) (io.WriteCloser, error) {
	if path == "" {
		return nopWriteCloser{os.Stdout}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建日志目录失败: %w", err)
	}
	return newRotatingWriter(path, int64(maxSizeMB)<<20)
}

// nopWriteCloser 让 os.Stdout（只有 Write）满足 io.WriteCloser。
// Close 不能真的关 stdout —— 那会让后续 fmt.Print 之类的写入直接失败。
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// rotatingWriter 在每次写入后检查文件大小，超过阈值就把文件改名成 .1
// 并重建。检查放在写入之后而不是之前：启动时文件可能已经超了阈值，
// 但那时还没写东西，等第一条日志落地再轮转对使用者的观感更自然。
type rotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
	size     int64
}

func newRotatingWriter(path string, maxBytes int64) (*rotatingWriter, error) {
	w := &rotatingWriter{path: path, maxBytes: maxBytes}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("打开日志文件失败: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("读取日志文件状态失败: %w", err)
	}
	w.f = f
	w.size = st.Size()
	return nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n, err := w.f.Write(p)
	w.size += int64(n)
	if err != nil {
		return n, err
	}
	// 轮转失败不该让这次写入变成错误：日志已经写进去了，
	// 报错会让调用方（slog）把它当写失败处理。下次写入还会再试。
	if w.maxBytes > 0 && w.size >= w.maxBytes {
		_ = w.rotateLocked()
	}
	return n, nil
}

func (w *rotatingWriter) rotateLocked() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	if _, err := rotateLog(w.path, w.maxBytes); err != nil {
		// 轮转失败（例如 .1 被别的进程占着）：重新打开原文件继续追加，
		// 至少不丢日志
		return w.open()
	}
	return w.open()
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

// rotateLog 把 path 改名为 path+".1"（覆盖旧历史），返回是否真的轮转了。
//
// maxBytes <= 0 表示不轮转（返回 false, nil）。
// 文件不存在不是错误：首次启动、或上一次轮转留下的空档都是常态。
func rotateLog(path string, maxBytes int64) (bool, error) {
	if maxBytes <= 0 {
		return false, nil
	}
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if st.Size() < maxBytes {
		return false, nil
	}
	// os.Rename 在 Unix 上覆盖已存在的目标；Windows 的 os.Rename
	// 走 MoveFileEx(MOVEFILE_REPLACE_EXISTING)，同样覆盖。
	if err := os.Rename(path, path+".1"); err != nil {
		return false, err
	}
	return true, nil
}
