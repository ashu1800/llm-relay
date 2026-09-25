package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// verifyChecksum 校验文件的 sha256 是否与 checksums.txt 里的记录一致。
//
// 格式是 goreleaser 生成的 `<hex>  <filename>`（两个空格），但这里用
// strings.Fields 拆 —— 它对空格数不敏感，也能容忍行尾的 \r
// （checksums.txt 若被以 CRLF 检出，严格按两空格拆会把文件名带上 \r，
// 于是永远匹配不到，报出一句让人摸不着头脑的「校验和里没有这个文件」）。
func verifyChecksum(filePath string, checksums []byte) error {
	sum, err := fileSHA256(filePath)
	if err != nil {
		return err
	}

	want := filepath.Base(filePath)
	scanner := bufio.NewScanner(bytes.NewReader(checksums))
	// checksums.txt 行很短，但扫描器的默认上限是 64KB，
	// 这里给 1MB 以免将来格式变复杂时静默截断
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	lines := 0
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		lines++
		// 兼容 goreleaser 的两种写法：`<hash>  <name>` 与 `<hash> *<name>`
		// （后者是 sha256sum 的二进制模式标记）
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if name != want {
			continue
		}
		got := strings.ToLower(fields[0])
		if got == sum {
			return nil
		}
		return fmt.Errorf("校验和不匹配（%s）：期望 %s，实际 %s —— 下载可能被篡改或中断，已拒绝安装", want, fields[0], sum)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取校验和文件失败: %w", err)
	}
	if lines == 0 {
		return fmt.Errorf("校验和文件是空的或格式不对")
	}
	return fmt.Errorf("校验和文件里没有 %s 的记录 —— 该平台的产物可能没有随本次发布上传", want)
}

// fileSHA256 计算文件的 sha256（小写十六进制）。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	// 分块拷贝：归档可能上百 MB，不整个读进内存
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("计算校验和失败: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---------------------------------------------------------------------------
// 解压
// ---------------------------------------------------------------------------

// ExtractBinary 从归档里取出可执行文件，写到 destPath。
//
// 这是整个更新链路里唯一「按外部数据决定写什么文件」的地方，因此防护最密。
// 归档来自网络，即便校验和过了也不能假设它结构正常 —— 校验和只证明
// 「这是发布方上传的那份」，而发布方的构建环境本身也可能被污染。
//
// 四道防护：
//
//  1. **只认普通文件**（tar.TypeReg / zip 非目录）。符号链接是最经典的
//     逃逸手法：归档里放一个指向 /etc/passwd 的软链，解压后往「链接」
//     里写内容就等于写真实文件。设备文件、FIFO 同理，一律跳过。
//  2. **拒绝路径穿越**。条目名里出现 `..` 直接报错，而不是「清理掉继续」——
//     一个正常的发布归档里根本不该有 `..`，出现即说明有问题。
//  3. **只用 filepath.Base 做匹配**，且落盘路径是调用方给的固定 destPath。
//     也就是说，即使归档里的名字通过了前面的检查，写到哪里也**不由归档决定**。
//  4. **单文件大小上限**，防解压炸弹：几 KB 的 gzip 能解出几十 GB，
//     把磁盘写满之后服务连日志都落不下去。
//
// 注意 zip 与 tar 的入口分开：Go 标准库没有统一的归档读取接口，
// 硬凑一个只会让两边的边界处理都变模糊（zip 的 mode 判定与 tar 的
// Typeflag 语义并不对应）。
func ExtractBinary(archivePath, destPath string, maxSize int64) error {
	if maxSize <= 0 {
		maxSize = maxBinarySize
	}
	lower := strings.ToLower(archivePath)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractFromZip(archivePath, destPath, maxSize)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractFromTarGz(archivePath, destPath, maxSize)
	case strings.HasSuffix(lower, ".tar"):
		return extractFromTar(archivePath, destPath, maxSize)
	default:
		return fmt.Errorf("不认识的归档格式: %s", filepath.Base(archivePath))
	}
}

// extractFromTarGz 处理 .tar.gz / .tgz。
func extractFromTarGz(archivePath, destPath string, maxSize int64) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip 解压失败（文件可能损坏）: %w", err)
	}
	defer func() { _ = gz.Close() }()

	return extractFromTarReader(tar.NewReader(gz), destPath, maxSize)
}

// extractFromTar 处理未压缩的 .tar。
func extractFromTar(archivePath, destPath string, maxSize int64) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	return extractFromTarReader(tar.NewReader(f), destPath, maxSize)
}

// extractFromTarReader 是 tar 系列的共同实现。
func extractFromTarReader(tr *tar.Reader, destPath string, maxSize int64) error {
	wanted := filepath.Base(destPath)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("读取归档失败: %w", err)
		}

		// 防护 2：路径穿越。放在最前面 —— 一个带 `..` 的条目
		// 无论是不是我们要找的文件，都说明这份归档不该被信任
		if strings.Contains(hdr.Name, "..") {
			return fmt.Errorf("归档中存在路径穿越条目，已中止: %s", hdr.Name)
		}
		// 防护 1：只处理普通文件。目录、符号链接、硬链接、设备文件全部跳过
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// 防护 3：按基名精确匹配
		if filepath.Base(hdr.Name) != wanted {
			continue
		}
		// 防护 4：先看头部声明的大小，再看实流
		if hdr.Size > maxSize {
			return fmt.Errorf("归档内文件过大: %d 字节（上限 %d）", hdr.Size, maxSize)
		}
		return copyToFile(destPath, tr, maxSize)
	}
	return errBinaryNotFound(wanted)
}

// extractFromZip 处理 .zip（windows 产物）。
func extractFromZip(archivePath, destPath string, maxSize int64) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("zip 解压失败（文件可能损坏）: %w", err)
	}
	defer func() { _ = zr.Close() }()

	wanted := filepath.Base(destPath)
	for _, zf := range zr.File {
		if strings.Contains(zf.Name, "..") {
			return fmt.Errorf("归档中存在路径穿越条目，已中止: %s", zf.Name)
		}
		// zip 没有 tar 那样的 Typeflag：目录以名字结尾的 / 表示，
		// 其余非常规条目靠 FileInfo().Mode() 判定
		if zf.FileInfo().IsDir() || !zf.FileInfo().Mode().IsRegular() {
			continue
		}
		if filepath.Base(zf.Name) != wanted {
			continue
		}
		// zip 用未压缩大小，同样先卡一道。
		// 注意 UncompressedSize64 是 uint64，直接与 int64 比会编译不过，
		// 也会在超大值时溢出成负数 —— 所以先判上界再比较
		if zf.UncompressedSize64 > uint64(maxSize) {
			return fmt.Errorf("归档内文件过大: %d 字节（上限 %d）", zf.UncompressedSize64, maxSize)
		}
		rc, err := zf.Open()
		if err != nil {
			return fmt.Errorf("打开归档条目失败: %w", err)
		}
		err = copyToFile(destPath, rc, maxSize)
		_ = rc.Close()
		return err
	}
	return errBinaryNotFound(wanted)
}

// errBinaryNotFound 在归档里找不到可执行文件时给出可操作的错误。
//
// 这条错误几乎总是意味着「发布流水线的归档命名/内容与本程序的预期不一致」，
// 所以直接把要找的名字写出来 —— 否则用户只能看到一句「解压失败」，
// 完全不知道是网络问题还是产物问题。
func errBinaryNotFound(wanted string) error {
	return fmt.Errorf("归档里没有找到可执行文件 %q —— 发布产物结构可能与当前版本的程序不一致，请检查发布流水线", wanted)
}

// copyToFile 把 reader 写到 destPath，超过 maxSize 立即中止。
//
// 用 LimitReader(maxSize+1) + 写入量复查，而不是只在头部查大小：
// 头部字段来自归档本身（可以被伪造），实流才是事实。
func copyToFile(destPath string, r io.Reader, maxSize int64) error {
	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("创建输出文件失败: %w", err)
	}

	written, copyErr := io.Copy(out, io.LimitReader(r, maxSize+1))
	if copyErr != nil {
		_ = out.Close()
		_ = os.Remove(destPath)
		return fmt.Errorf("写入可执行文件失败: %w", copyErr)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(destPath)
		return fmt.Errorf("关闭输出文件失败: %w", err)
	}
	if written > maxSize {
		_ = os.Remove(destPath)
		return fmt.Errorf("解压出的文件超过上限 %d 字节，已中止（疑似解压炸弹）", maxSize)
	}
	if written == 0 {
		_ = os.Remove(destPath)
		return fmt.Errorf("解压出的可执行文件是空的")
	}
	return nil
}
