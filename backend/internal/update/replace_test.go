package update

import (
	"os"
	"path/filepath"
	"testing"
)

func writeReplaceFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}
}

// 备份位删不掉时必须「把它挪开」而不是直接失败。
//
// Windows 上这是**必然会发生**的常态，不是边角情况：第一次更新把正在运行的
// 映像改名成了 .backup，那份映像在本进程退出前既不可删除、也不可被覆盖
// （实测：os.Remove → Access is denied；os.Rename 到一个已存在的同名文件同样被拒）。
// 于是「连续点两次更新、中间不重启」这一步就会死在 os.Remove 上，
// 报出来的还是「备份当前版本失败」——指向的原因完全不对。
//
// 关键约束：**不能**换一个名字当备份（比如 .backup.1）。RestoreBackup 只认
// exePath + ".backup"，回滚目标一旦漂移，界面上「回滚」就会报「没有备份」。
// 正确做法是把占位的旧备份改成时间戳名让出正位，正位重新指向上一版本。
//
// 测试用「非空目录」占住备份位来复现：Linux 与 Windows 上 os.Remove 都删不掉它，
// 而 os.Rename 都能把它挪走 —— 与运行中映像的行为一致。Windows 侧另有
// replace_windows_test.go 用真实映像映射验证。
func TestReplaceSelfMovesAsideUndeletableBackup(t *testing.T) {
	root := t.TempDir()
	exePath := filepath.Join(root, "llm-relay")
	writeReplaceFixture(t, exePath, "old-binary")

	backup := BackupPath(exePath)
	if err := os.Mkdir(backup, 0o755); err != nil {
		t.Fatalf("建占位备份目录失败: %v", err)
	}
	writeReplaceFixture(t, filepath.Join(backup, "keep"), "原备份，删不掉")

	newBinary := filepath.Join(root, "new-binary")
	writeReplaceFixture(t, newBinary, "new-binary")

	if err := ReplaceSelf(exePath, newBinary); err != nil {
		t.Fatalf("备份位删不掉时应挪开继续，而不是失败: %v", err)
	}

	if got, err := os.ReadFile(exePath); err != nil || string(got) != "new-binary" {
		t.Fatalf("目标文件内容不对: %q (err=%v)", got, err)
	}
	// 正位必须重新指向「上一版本」—— 这是回滚唯一认的位置
	if got, err := os.ReadFile(BackupPath(exePath)); err != nil || string(got) != "old-binary" {
		t.Fatalf("备份正位应是上一版本: %q (err=%v)", got, err)
	}
	// 被挪开的那份必须还在（可能还是运行中的映像，不能丢）
	moved, err := filepath.Glob(backup + ".*")
	if err != nil || len(moved) != 1 {
		t.Fatalf("占位备份应被挪到时间戳名下落一份，实际: %v (err=%v)", moved, err)
	}
	if got, err := os.ReadFile(filepath.Join(moved[0], "keep")); err != nil || string(got) != "原备份，删不掉" {
		t.Fatalf("挪开的备份内容被破坏了: %q (err=%v)", got, err)
	}
}

// 挪开后的名字不能与已有文件撞车：连续多次「更新但不重启」会在磁盘上
// 留下多份挪开的备份，每次挪开都必须挑一个还空着的名字，
// 否则第二次挪开会失败、更新再次卡住。
func TestMovedAsideBackupPicksFreeName(t *testing.T) {
	root := t.TempDir()
	exePath := filepath.Join(root, "llm-relay")
	writeReplaceFixture(t, BackupPath(exePath), "上一版")

	name1 := MoveAsideBackup(exePath)
	if name1 == "" {
		t.Fatal("第一次挪开应返回新路径")
	}
	// 再放一份备份让它挪，验证第二次不会撞上第一次的名字
	writeReplaceFixture(t, BackupPath(exePath), "再上一版")
	name2 := MoveAsideBackup(exePath)
	if name2 == "" {
		t.Fatal("第二次挪开应返回新路径")
	}
	if name1 == name2 {
		t.Fatalf("两次挪开拿到了同一个名字: %s", name1)
	}
}

// 启动时必须清掉上个进程留下的挪开备份：它们占着磁盘，
// 而且此刻已经不是运行中的映像，删得掉。
func TestCleanupStaleBackupsRemovesRotatedFiles(t *testing.T) {
	root := t.TempDir()
	exePath := filepath.Join(root, "llm-relay")
	writeReplaceFixture(t, exePath, "current")

	// 正位备份：必须保留，回滚要用
	writeReplaceFixture(t, BackupPath(exePath), "previous")

	// 挪开的残留（moveAside 的形状：.<8位日期>-<6位时间>[-序号]）
	stale := []string{
		exePath + ".backup.20261022-101500",
		exePath + ".backup.20261022-101500-1",
		exePath + ".rollback-from.20261022-101501",
	}
	for _, p := range stale {
		writeReplaceFixture(t, p, "stale")
	}
	// 名字相近但不是备份的文件：不能被误删
	unrelated := exePath + ".backup-old-notes.txt"
	writeReplaceFixture(t, unrelated, "别删我")

	removed := CleanupStaleBackups(exePath)
	if removed != len(stale) {
		t.Fatalf("应清理 %d 个残留，实际 %d", len(stale), removed)
	}
	if _, err := os.Stat(BackupPath(exePath)); err != nil {
		t.Fatalf("正位备份必须保留: %v", err)
	}
	for _, p := range stale {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("残留未清理: %s (err=%v)", p, err)
		}
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("无关文件被误删: %v", err)
	}
}

// isMovedAsideName 是「删哪些文件」的唯一判据，写错了会删掉用户的文件，
// 所以把形状边界逐条钉死。
//
// 特别是 .backup.1 这种**不带时间戳**的名字：它是人手写的，
// 不是 moveAside 生成的，清理时不能碰。
func TestIsMovedAsideName(t *testing.T) {
	const base = "llm-relay.backup"
	cases := []struct {
		name string
		want bool
	}{
		{base + ".20261022-101500", true},
		{base + ".20261022-101500-1", true},
		{base + ".20261022-101500-99", true},

		{base, false},                        // 正位本身
		{base + ".1", false},                 // 人手写的序号，没有时间戳
		{base + ".20261022", false},          // 只有日期
		{base + ".2026102-101500", false},    // 日期少一位
		{base + ".20261022_101500", false},   // 分隔符不对
		{base + ".20261022-10150x", false},   // 时间里有非数字
		{base + ".20261022-101500-x", false}, // 序号非数字
		{base + ".20261022-101500-", false},  // 空序号
		{base + "-old-notes.txt", false},     // 名字相近的用户文件
		{"other.bin.backup.20261022-101500", false},
	}
	for _, c := range cases {
		if got := isMovedAsideName(c.name, base); got != c.want {
			t.Errorf("isMovedAsideName(%q) = %v，期望 %v", c.name, got, c.want)
		}
	}
}

// TempDir 建的是 exe 目录下的**子目录**，ReplaceSelf 必须放行它。
//
// 这是 0.1.2/0.1.3 的真实回归：当时的实现比较「两边 Dir 相等」，而
// Dir(子目录/llm-relay) 是那个子目录、永远不等于 exe 目录 —— 于是
// binary 形态的自替换从来不可能成功，0.1.2 → 0.1.3 第一次真实触发即失败。
func TestReplaceSelfAcceptsTempSubdirectory(t *testing.T) {
	root := t.TempDir()
	exePath := filepath.Join(root, "llm-relay")
	writeReplaceFixture(t, exePath, "old-binary")

	tmpDir := filepath.Join(root, ".llm-relay-update-test")
	if err := os.Mkdir(tmpDir, 0o700); err != nil {
		t.Fatalf("建临时目录失败: %v", err)
	}
	newBinary := filepath.Join(tmpDir, "llm-relay")
	writeReplaceFixture(t, newBinary, "new-binary")

	if err := ReplaceSelf(exePath, newBinary); err != nil {
		t.Fatalf("ReplaceSelf 失败: %v", err)
	}

	if got, err := os.ReadFile(exePath); err != nil || string(got) != "new-binary" {
		t.Fatalf("目标文件内容不对: %q (err=%v)", got, err)
	}
	if got, err := os.ReadFile(BackupPath(exePath)); err != nil || string(got) != "old-binary" {
		t.Fatalf("备份内容不对: %q (err=%v)", got, err)
	}
	if _, err := os.Stat(newBinary); !os.IsNotExist(err) {
		t.Fatalf("新文件应已被 rename 走，仍存在: err=%v", err)
	}
}

// 不同目录树里的待安装文件必须被拒绝 —— 那意味着可能跨文件系统，
// rename 不再原子。兄弟目录（名字很像但不在树内）也要挡住。
func TestReplaceSelfRejectsForeignDirectory(t *testing.T) {
	root := t.TempDir()
	exePath := filepath.Join(root, "llm-relay")
	writeReplaceFixture(t, exePath, "old-binary")

	foreign := t.TempDir() // 与 root 平级的另一个目录
	newBinary := filepath.Join(foreign, "llm-relay")
	writeReplaceFixture(t, newBinary, "new-binary")

	if err := ReplaceSelf(exePath, newBinary); err == nil {
		t.Fatal("应当拒绝目录树之外的待安装文件")
	}
	if got, err := os.ReadFile(exePath); err != nil || string(got) != "old-binary" {
		t.Fatalf("拒绝后原文件必须原封未动: %q (err=%v)", got, err)
	}
}
