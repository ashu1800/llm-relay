package convert

import (
	"io"
	"strings"
)

// ioNopCloser 把 Reader 包成 ReadCloser，便于构造假的流式上游。
func ioNopCloser(r io.Reader) io.ReadCloser { return io.NopCloser(r) }

// readAll 读尽转换后的流。
func readAll(r io.Reader) ([]byte, error) {
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			if err == io.EOF {
				return []byte(sb.String()), nil
			}
			return []byte(sb.String()), err
		}
	}
}
