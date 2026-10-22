//go:build windows

package update

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// lockAsRunningImage 把 path 以 SEC_IMAGE 方式映射进当前进程，
// 复现「这个文件是正在运行的映像」的语义。
//
// 为什么不能简单地 open 一个句柄占住文件：那样 os.Rename 也会被拒
// （共享模式里没有 FILE_SHARE_DELETE），与真实情况不符，测试就变成了
// 「验证一个不存在的场景」。真实语义是**映像节锁删除、不锁改名**：
//
//	删除   → Access is denied    （本次实测）
//	改名   → 成功
//
// SEC_IMAGE 映射精确复现了这两条，所以是唯一可信的复现方式。
// 调用方须在测试结束前 Close（Cleanup 里关），否则临时目录删不掉。
func lockAsRunningImage(t *testing.T, path string) {
	t.Helper()

	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("转换路径失败: %v", err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatalf("打开 %s 失败: %v", path, err)
	}
	m, err := windows.CreateFileMapping(h, nil, windows.PAGE_READONLY|0x01000000 /* SEC_IMAGE */, 0, 0, nil)
	windows.CloseHandle(h)
	if err != nil {
		t.Fatalf("以 SEC_IMAGE 映射 %s 失败: %v", path, err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(m) })
}

// 这是 Windows 上「连续更新两次、中间不重启」的真实事故形态：
// 第一次更新把运行中的映像改名成 .backup，第二次更新时那份 .backup
// 既删不掉也覆盖不了，旧实现直接失败并报「备份当前版本失败」。
func TestReplaceSelfWithRunningImageBackup(t *testing.T) {
	root := t.TempDir()
	exePath := filepath.Join(root, "llm-relay.exe")

	// 第 1 次更新：当前 exe 是「正在运行的映像」
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("定位测试进程失败: %v", err)
	}
	src, err := os.Open(self)
	if err != nil {
		t.Fatalf("打开测试进程失败: %v", err)
	}
	dst, err := os.Create(exePath)
	if err != nil {
		src.Close()
		t.Fatalf("复制测试进程失败: %v", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		src.Close()
		dst.Close()
		t.Fatalf("复制测试进程失败: %v", err)
	}
	src.Close()
	dst.Close()

	// 让 exePath 成为「运行中映像」，再执行第一次替换
	lockAsRunningImage(t, exePath)

	newBinary := filepath.Join(root, "new-1.exe")
	writeReplaceFixture(t, newBinary, "v2")
	if err := ReplaceSelf(exePath, newBinary); err != nil {
		t.Fatalf("第一次更新失败: %v", err)
	}
	// 此刻 BackupPath 就是那份运行中的映像
	if _, err := os.Stat(BackupPath(exePath)); err != nil {
		t.Fatalf("备份应存在: %v", err)
	}

	// 第 2 次更新：进程仍在跑，备份仍是运行中映像
	newBinary2 := filepath.Join(root, "new-2.exe")
	writeReplaceFixture(t, newBinary2, "v3")
	if err := ReplaceSelf(exePath, newBinary2); err != nil {
		t.Fatalf("第二次更新（备份是运行中映像）必须成功，实际失败: %v", err)
	}

	if got, err := os.ReadFile(exePath); err != nil || string(got) != "v3" {
		t.Fatalf("目标文件应是 v3: %q (err=%v)", got, err)
	}
	// 回滚位必须指向 v2（上一版本），不能被挪开的名字占走
	if got, err := os.ReadFile(BackupPath(exePath)); err != nil || string(got) != "v2" {
		t.Fatalf("备份正位应是 v2: %q (err=%v)", got, err)
	}
}

// 回滚同样会撞上「当前 exe 是运行中映像」：RestoreBackup 先把当前版本
// 挪到 .rollback-from，那一步是改名（允许），接着把备份换回原位。
// 但 .rollback-from 也可能是上一轮留下的运行中映像 —— 删不掉时同样要挪开。
func TestRestoreBackupWithUndeletableRollbackFrom(t *testing.T) {
	root := t.TempDir()
	exePath := filepath.Join(root, "llm-relay.exe")
	writeReplaceFixture(t, exePath, "broken-new")
	writeReplaceFixture(t, BackupPath(exePath), "good-old")

	// .rollback-from 位置被一个删不掉的目录占住
	stale := exePath + ".rollback-from"
	if err := os.Mkdir(stale, 0o755); err != nil {
		t.Fatalf("占位失败: %v", err)
	}
	writeReplaceFixture(t, filepath.Join(stale, "x"), "占位")

	if err := RestoreBackup(exePath); err != nil {
		t.Fatalf("回滚不应因 .rollback-from 占位而失败: %v", err)
	}
	if got, err := os.ReadFile(exePath); err != nil || string(got) != "good-old" {
		t.Fatalf("回滚后应是旧版本: %q (err=%v)", got, err)
	}
}
