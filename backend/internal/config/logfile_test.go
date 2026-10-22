package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rotateLog 把超过阈值的日志文件改名为 .1，然后由调用方重建新文件。
//
// 只保留一份历史（.1）：这是「别让磁盘被写满」的最低需求，
// 不是日志归档系统。更早的内容允许丢 —— 排障要看的是最近一次启动之后的事。
func TestRotateLogMovesOversizedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "llm-relay.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 200)), 0o644); err != nil {
		t.Fatalf("写日志失败: %v", err)
	}

	ok, err := rotateLog(path, 100)
	if err != nil {
		t.Fatalf("轮转失败: %v", err)
	}
	if !ok {
		t.Fatal("超过阈值时应执行轮转")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("原文件应已被改名: err=%v", err)
	}
	if got, err := os.ReadFile(path + ".1"); err != nil || len(got) != 200 {
		t.Fatalf("历史文件内容不对: len=%d err=%v", len(got), err)
	}
}

// 没到阈值时不能动它 —— 否则每写一行就轮转一次，
// 日志文件永远只有一行。
func TestRotateLogKeepsSmallFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "llm-relay.log")
	if err := os.WriteFile(path, []byte("small"), 0o644); err != nil {
		t.Fatalf("写日志失败: %v", err)
	}

	ok, err := rotateLog(path, 100)
	if err != nil {
		t.Fatalf("轮转失败: %v", err)
	}
	if ok {
		t.Fatal("未超过阈值时不该轮转")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "small" {
		t.Fatalf("文件应原封不动: %q err=%v", got, err)
	}
}

// 文件不存在时不算错误（首次启动）。
func TestRotateLogMissingFileIsNotError(t *testing.T) {
	dir := t.TempDir()
	ok, err := rotateLog(filepath.Join(dir, "nope.log"), 100)
	if err != nil {
		t.Fatalf("文件不存在不该报错: %v", err)
	}
	if ok {
		t.Fatal("文件不存在时不该报告已轮转")
	}
}

// 上一份 .1 已存在时必须覆盖它，而不是让轮转失败。
// 否则服务会在第二次轮转之后就写不进日志。
func TestRotateLogOverwritesPreviousHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "llm-relay.log")
	if err := os.WriteFile(path+".1", []byte("old-history"), 0o644); err != nil {
		t.Fatalf("写历史失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("y", 200)), 0o644); err != nil {
		t.Fatalf("写日志失败: %v", err)
	}

	ok, err := rotateLog(path, 100)
	if err != nil || !ok {
		t.Fatalf("应覆盖旧历史并成功轮转: ok=%v err=%v", ok, err)
	}
	if got, err := os.ReadFile(path + ".1"); err != nil || string(got) != strings.Repeat("y", 200) {
		t.Fatalf("历史文件应被新内容覆盖: len=%d err=%v", len(got), err)
	}
}

// 阈值 <= 0 表示不轮转。这是「用户不想管磁盘」时的合法选择，
// 不能因为阈值是 0 就每次都轮转。
func TestRotateLogDisabledWhenThresholdZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "llm-relay.log")
	if err := os.WriteFile(path, []byte("anything"), 0o644); err != nil {
		t.Fatalf("写日志失败: %v", err)
	}

	ok, err := rotateLog(path, 0)
	if err != nil || ok {
		t.Fatalf("阈值为 0 时不该轮转: ok=%v err=%v", ok, err)
	}
}
