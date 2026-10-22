package main

import (
	"os"
	"path/filepath"
	"testing"
)

// defaultConfigPath 的优先级是「CWD 优先，可执行文件同目录兜底」。
//
// 这两条分支各有真实场景：Linux 上 CWD 就是安装目录（systemd 配了
// WorkingDirectory），Windows 服务下 CWD 是 System32、必须靠 exe 同目录。
// 顺序反了会让本地开发时读到安装目录里的旧配置。
func TestDefaultConfigPathPrefersCwd(t *testing.T) {
	dir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("切换工作目录失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("server: {}\n"), 0o644); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	if got := defaultConfigPath(); got != "config.yaml" {
		t.Fatalf("CWD 有配置时应返回相对名，实际: %s", got)
	}
}

// 两处都没有时必须返回 "config.yaml" 而不是空串：
// 空串会让 config.Load 跳过文件，但调用方（以及错误信息）期望看到
// 一个「本来打算读哪个文件」的路径。
func TestDefaultConfigPathFallsBackToName(t *testing.T) {
	dir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("切换工作目录失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	if got := defaultConfigPath(); got != "config.yaml" {
		t.Fatalf("两处都没有时应返回 config.yaml，实际: %s", got)
	}
}
