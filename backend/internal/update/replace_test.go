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
