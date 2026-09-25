package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// tarGz 构造一个 .tar.gz 归档，entries 是「名字 → 内容」。
// typeflag 为 tar.TypeReg 之外的类型用来测符号链接防护。
type entry struct {
	name     string
	body     string
	typeflag byte
}

func makeTarGz(t *testing.T, entries []entry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, e := range entries {
		tf := e.typeflag
		if tf == 0 {
			tf = tar.TypeReg
		}
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     0o755,
			Size:     int64(len(e.body)),
			Typeflag: tf,
		}
		// 符号链接的 Size 必须是 0，内容放在 Linkname 里
		if tf == tar.TypeSymlink {
			hdr.Size = 0
			hdr.Linkname = e.body
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("写归档头失败: %v", err)
		}
		if tf == tar.TypeReg && len(e.body) > 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("写归档内容失败: %v", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("关闭 tar 失败: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("关闭 gzip 失败: %v", err)
	}

	path := filepath.Join(t.TempDir(), "test.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("写归档文件失败: %v", err)
	}
	return path
}

func TestExtractBinaryFromTarGz(t *testing.T) {
	want := "#!/bin/sh\necho hello\n"
	archive := makeTarGz(t, []entry{
		{name: "LICENSE", body: "license text"},
		{name: "llm-relay", body: want},
		{name: "README.md", body: "readme"},
	})

	dest := filepath.Join(t.TempDir(), "llm-relay")
	if err := ExtractBinary(archive, dest, maxBinarySize); err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("读取解压结果失败: %v", err)
	}
	if string(got) != want {
		t.Fatalf("解压内容不符: got %q, want %q", got, want)
	}
}

// TestExtractBinaryNestedPath 验证归档里可执行文件位于子目录时也能找到。
// goreleaser 的默认归档结构就是 <project>/<binary> 或多文件平铺，
// 两种都要能处理。
func TestExtractBinaryNestedPath(t *testing.T) {
	archive := makeTarGz(t, []entry{
		{name: "llm-relay_1.0.0_linux_amd64/llm-relay", body: "binary-content"},
	})
	dest := filepath.Join(t.TempDir(), "llm-relay")
	if err := ExtractBinary(archive, dest, maxBinarySize); err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "binary-content" {
		t.Fatalf("内容不符: %q", got)
	}
}

// TestExtractBinaryRejectsSymlink 是最重要的一条安全测试。
//
// 符号链接是归档逃逸的经典手法：归档里放一个名为 llm-relay 的软链
// 指向 /etc/passwd，解压程序若按名字匹配并「写进去」，实际写的就是
// 链接目标。这里断言它被当作非常规文件跳过，最终报「找不到可执行文件」。
func TestExtractBinaryRejectsSymlink(t *testing.T) {
	archive := makeTarGz(t, []entry{
		{name: "llm-relay", body: "/etc/passwd", typeflag: tar.TypeSymlink},
	})
	dest := filepath.Join(t.TempDir(), "llm-relay")
	err := ExtractBinary(archive, dest, maxBinarySize)
	if err == nil {
		t.Fatal("符号链接条目应当被拒绝，但没有报错")
	}
	if !strings.Contains(err.Error(), "没有找到可执行文件") {
		t.Fatalf("错误信息不符合预期: %v", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("不应产生任何输出文件")
	}
}

// TestExtractBinaryRejectsPathTraversal 验证带 .. 的条目被拒绝。
func TestExtractBinaryRejectsPathTraversal(t *testing.T) {
	archive := makeTarGz(t, []entry{
		{name: "../../etc/llm-relay", body: "evil"},
		{name: "llm-relay", body: "good"},
	})
	dest := filepath.Join(t.TempDir(), "llm-relay")
	err := ExtractBinary(archive, dest, maxBinarySize)
	if err == nil {
		t.Fatal("路径穿越条目应当被拒绝，但没有报错")
	}
	if !strings.Contains(err.Error(), "路径穿越") {
		t.Fatalf("错误信息不符合预期: %v", err)
	}
}

// TestExtractBinaryEnforcesSizeLimit 验证解压炸弹防护。
func TestExtractBinaryEnforcesSizeLimit(t *testing.T) {
	// 内容 1000 字节，但把上限压到 100
	archive := makeTarGz(t, []entry{
		{name: "llm-relay", body: strings.Repeat("A", 1000)},
	})
	dest := filepath.Join(t.TempDir(), "llm-relay")
	err := ExtractBinary(archive, dest, 100)
	if err == nil {
		t.Fatal("超过大小上限应当报错")
	}
	if !strings.Contains(err.Error(), "过大") {
		t.Fatalf("错误信息不符合预期: %v", err)
	}
}

// TestExtractBinaryFromZip 验证 windows 产物路径。
func TestExtractBinaryFromZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("llm-relay.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("windows-binary")); err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Create("README.md"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "test.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "llm-relay.exe")
	if err := ExtractBinary(path, dest, maxBinarySize); err != nil {
		t.Fatalf("解压 zip 失败: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "windows-binary" {
		t.Fatalf("内容不符: %q", got)
	}
}

// ---------------------------------------------------------------------------
// 校验和
// ---------------------------------------------------------------------------

func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "llm-relay_1.0.0_linux_amd64.tar.gz")
	content := []byte("fake archive")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	good := hex.EncodeToString(sum[:])

	cases := []struct {
		name      string
		checksums string
		wantErr   string
	}{
		{
			name:      "标准两空格格式",
			checksums: good + "  llm-relay_1.0.0_linux_amd64.tar.gz\n",
		},
		{
			name:      "sha256sum 二进制模式星号",
			checksums: good + " *llm-relay_1.0.0_linux_amd64.tar.gz\n",
		},
		{
			name:      "CRLF 行尾不应影响匹配",
			checksums: good + "  llm-relay_1.0.0_linux_amd64.tar.gz\r\n",
		},
		{
			name:      "多文件时能挑出目标",
			checksums: "aaaa  other.tar.gz\n" + good + "  llm-relay_1.0.0_linux_amd64.tar.gz\nbbbb  third.zip\n",
		},
		{
			name:      "哈希不匹配",
			checksums: strings.Repeat("0", 64) + "  llm-relay_1.0.0_linux_amd64.tar.gz\n",
			wantErr:   "校验和不匹配",
		},
		{
			name:      "文件不在清单里",
			checksums: good + "  some-other-file.tar.gz\n",
			wantErr:   "没有",
		},
		{
			name:      "空清单",
			checksums: "",
			wantErr:   "空",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := verifyChecksum(path, []byte(c.checksums))
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过，实际报错: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("期望报错（含 %q），实际通过", c.wantErr)
			}
			// 大小写不敏感地找关键词，避免因措辞微调而误报
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("错误信息里没有 %q: %v", c.wantErr, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 产物命名契约
// ---------------------------------------------------------------------------

// TestArchiveNameContract 钉住归档命名契约。
//
// 这个契约横跨三处：goreleaser 配置、GitHub Actions 流水线、以及这里的代码。
// 任何一处改了而另两处没跟上，表现都是「检测到新版本但下载 404」，
// 而那要到真发版时才会暴露 —— 所以用一个测试把当前约定固定下来。
func TestArchiveNameContract(t *testing.T) {
	cases := []struct {
		version, goos, goarch, want string
	}{
		{"1.2.3", "linux", "amd64", "llm-relay_1.2.3_linux_amd64.tar.gz"},
		{"1.2.3", "linux", "arm64", "llm-relay_1.2.3_linux_arm64.tar.gz"},
		{"1.2.3", "darwin", "arm64", "llm-relay_1.2.3_darwin_arm64.tar.gz"},
		{"1.2.3", "windows", "amd64", "llm-relay_1.2.3_windows_amd64.zip"},
		// tag 带 v 前缀时必须被剥掉：GitHub 的 tag 是 v1.2.3，
		// 而归档名里没有 v —— 传进来时剥不干净就会 404
		{"v1.2.3", "linux", "amd64", "llm-relay_1.2.3_linux_amd64.tar.gz"},
	}
	for _, c := range cases {
		if got := ArchiveName(c.version, c.goos, c.goarch); got != c.want {
			t.Errorf("ArchiveName(%q,%q,%q) = %q，期望 %q", c.version, c.goos, c.goarch, got, c.want)
		}
	}

	if got := BinaryName("linux"); got != "llm-relay" {
		t.Errorf("BinaryName(linux) = %q", got)
	}
	if got := BinaryName("windows"); got != "llm-relay.exe" {
		t.Errorf("BinaryName(windows) = %q", got)
	}
	if ChecksumAssetName != "checksums.txt" {
		t.Errorf("校验和文件名必须是 checksums.txt，当前 %q", ChecksumAssetName)
	}
}

// TestFindAssetExactMatch 验证产物查找是精确匹配。
//
// 用 strings.Contains 会让 llm-relay_1.2.3_linux_amd64.tar.gz
// 也匹配上 llm-relay_1.2.3_linux_amd64_musl.tar.gz —— 下错文件，
// 而校验和会以「不匹配」的形式拦下它，把「选错文件」伪装成「文件被篡改」。
func TestFindAssetExactMatch(t *testing.T) {
	assets := []Asset{
		{Name: "llm-relay_1.2.3_linux_amd64_musl.tar.gz"},
		{Name: "checksums.txt"},
	}
	if _, ok := FindAsset(assets, "llm-relay_1.2.3_linux_amd64.tar.gz"); ok {
		t.Fatal("不应匹配到名字更长的 musl 变体")
	}
	if _, ok := FindAsset(assets, "checksums.txt"); !ok {
		t.Fatal("应当匹配到 checksums.txt")
	}
	if got := ChecksumURL(assets); got != "" {
		// 这里的 Asset 没有 URL，所以返回空串是正确的
		t.Logf("ChecksumURL 返回 %q（Asset 未设置 URL）", got)
	}
}

// ---------------------------------------------------------------------------
// 下载地址校验
// ---------------------------------------------------------------------------

func TestValidateDownloadURL(t *testing.T) {
	cases := []struct {
		url     string
		wantErr bool
	}{
		{"https://github.com/ashu1800/llm-relay/releases/download/v1.0.0/x.tar.gz", false},
		{"https://objects.githubusercontent.com/abc/x.tar.gz", false},
		{"https://release-assets.githubusercontent.com/abc/x.tar.gz", false},
		// 子域也放行（GitHub 会用到 codeload 等）
		{"https://codeload.github.com/x", false},
		// 必须是 https
		{"http://github.com/x", true},
		// 不能带凭据
		{"https://user:pass@github.com/x", true},
		// 不受信任的主机
		{"https://evil.com/x.tar.gz", true},
		// 后缀伪装：evil-github.com 不是 github.com 的子域
		{"https://evil-github.com/x", true},
		// 内网地址
		{"https://127.0.0.1/x", true},
		{"https://169.254.169.254/latest/meta-data/", true},
		{"", true},
		{"not a url", true},
	}
	for _, c := range cases {
		err := validateDownloadURL(c.url)
		if c.wantErr && err == nil {
			t.Errorf("%q 应当被拒绝，但通过了", c.url)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%q 应当通过，但被拒: %v", c.url, err)
		}
	}
}

// ---------------------------------------------------------------------------
// 自替换与回滚
// ---------------------------------------------------------------------------

// TestReplaceSelfAndRestore 覆盖最关键的写入路径：
// 替换留下备份、回滚能换回来、且内容是预期的。
func TestReplaceSelfAndRestore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows 上 rename 语义不同（目标存在时失败），本测试针对 unix")
	}

	dir := t.TempDir()
	exe := filepath.Join(dir, "llm-relay")
	if err := os.WriteFile(exe, []byte("old-version"), 0o755); err != nil {
		t.Fatal(err)
	}
	newBin := filepath.Join(dir, "new-binary")
	if err := os.WriteFile(newBin, []byte("new-version"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := ReplaceSelf(exe, newBin); err != nil {
		t.Fatalf("替换失败: %v", err)
	}

	got, _ := os.ReadFile(exe)
	if string(got) != "new-version" {
		t.Fatalf("替换后内容应为 new-version，实际 %q", got)
	}
	if !HasBackup(exe) {
		t.Fatal("替换后应当存在备份文件")
	}
	backup, _ := os.ReadFile(BackupPath(exe))
	if string(backup) != "old-version" {
		t.Fatalf("备份内容应为 old-version，实际 %q", backup)
	}
	// 可执行位必须保留，否则替换后的二进制跑不起来
	info, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("替换后丢失可执行位: %v", info.Mode())
	}

	// 回滚
	if err := RestoreBackup(exe); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	got, _ = os.ReadFile(exe)
	if string(got) != "old-version" {
		t.Fatalf("回滚后内容应为 old-version，实际 %q", got)
	}
	// 回滚也要留下「被换掉的那一版」，让回滚本身可撤销
	if _, err := os.Stat(exe + ".rollback-from"); err != nil {
		t.Fatalf("回滚应当留下 .rollback-from 以便撤销: %v", err)
	}
}

// TestReplaceSelfRejectsCrossDirectory 验证跨目录替换被拒绝。
//
// 跨目录可能跨文件系统，而跨文件系统的 rename 不是原子的 ——
// 那会在目标位置留下一个可能不完整的文件，服务正跑在上面。
func TestReplaceSelfRejectsCrossDirectory(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	exe := filepath.Join(dirA, "llm-relay")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dirB, "new-binary")
	if err := os.WriteFile(other, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := ReplaceSelf(exe, other)
	if err == nil {
		t.Fatal("跨目录替换应当被拒绝")
	}
	if !strings.Contains(err.Error(), "同目录") {
		t.Fatalf("错误信息不符合预期: %v", err)
	}
	// 被拒绝后原文件必须完好无损
	got, _ := os.ReadFile(exe)
	if string(got) != "old" {
		t.Fatalf("被拒绝的替换不应改动原文件，实际 %q", got)
	}
}

func TestRestoreBackupWithoutBackup(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "llm-relay")
	if err := os.WriteFile(exe, []byte("current"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := RestoreBackup(exe)
	if err == nil {
		t.Fatal("没有备份时应当报错")
	}
	if !strings.Contains(err.Error(), "没有可回滚") {
		t.Fatalf("错误信息不符合预期: %v", err)
	}
}

// TestTempDirIsSiblingOfExecutable 验证临时目录建在目标同目录。
// 这是 ReplaceSelf 的 rename 能保持原子的前提。
func TestTempDirIsSiblingOfExecutable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "llm-relay")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	tmp, err := TempDir(exe)
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	if filepath.Dir(tmp) != dir {
		t.Fatalf("临时目录应当与可执行文件同目录：tmp=%s, exe dir=%s", filepath.Dir(tmp), dir)
	}
}

// ---------------------------------------------------------------------------
// 序列化契约
// ---------------------------------------------------------------------------

// TestTaskJSONFieldNames 钉住任务进度的 JSON 字段名。
//
// 这个测试来自一次真实的线上验证：Task 里 Ended/Failed/Done 三个字段
// 忘了写 json tag，Go 就把它们序列化成大写原名。前端读的是 `task.done`，
// 于是 `!task.done` 恒为 true —— 界面永远认为任务在跑，进度条不消失、
// 「重启」按钮永远不出现，而编译、类型检查、单元测试全都发现不了
// （TS 接口只在编译期存在，运行时少一个字段不会报错）。
//
// 所以这里直接断言**序列化后的键名**，而不是断言结构体字段。
func TestTaskJSONFieldNames(t *testing.T) {
	ended := time.Now().UTC()
	task := Task{
		ID: "t1", Kind: "update", Target: "1.2.3", Phase: "done",
		Percent: 100, Message: "ok", Started: ended, Ended: &ended,
		Done: true, Failed: false,
		Logs: []string{"line"},
	}

	raw, err := json.Marshal(task.Snapshot())
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}

	// 前端依赖的字段，一个都不能少、名字一个字都不能差
	for _, key := range []string{
		"id", "kind", "target", "phase", "percent", "message",
		"started_at", "ended", "done", "failed", "logs",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("序列化结果缺少字段 %q（当前键：%v）", key, keysOf(m))
		}
	}
	// 大写形式必须**不存在** —— 这正是那个 bug 的形状
	for _, bad := range []string{"Done", "Failed", "Ended", "Logs", "ID"} {
		if _, ok := m[bad]; ok {
			t.Errorf("出现了未加 json tag 的大写字段 %q，前端读不到它", bad)
		}
	}
	// cancel 是内部字段，绝不能出现在响应里
	if _, ok := m["cancel"]; ok {
		t.Error("内部字段 cancel 不应被序列化")
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestTaskNormalizedGivesArraysNotNull 钉住「数组字段永远是数组」。
//
// 线上验证时抓到过：更新失败的响应里 `logs` 是 null，而前端写的是
// `task.logs.length` —— 在 null 上取属性会抛错，表现成「面板打开就白屏」。
// 这个字段有六处构造点，逐个记得初始化是靠不住的，所以出口统一过
// Normalized()。这里断言的就是那个出口的契约。
func TestTaskNormalizedGivesArraysNotNull(t *testing.T) {
	cases := []struct {
		name string
		task *Task
	}{
		{"零值任务", &Task{}},
		{"nil 快照", (*Task)(nil).Snapshot()},
		{"刚启动的 docker 任务", &Task{ID: "a", Phase: "requested"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := c.task.Normalized()
			if n.Logs == nil {
				t.Fatal("Logs 不应为 nil（序列化后会变成 null）")
			}
			raw, err := json.Marshal(n)
			if err != nil {
				t.Fatalf("序列化失败: %v", err)
			}
			// 直接看 JSON 文本里有没有 null —— 这是前端实际收到的东西
			if strings.Contains(string(raw), `"logs":null`) {
				t.Fatalf("序列化结果里 logs 是 null: %s", raw)
			}
			if !strings.Contains(string(raw), `"logs":[]`) {
				t.Fatalf("序列化结果里 logs 应为空数组: %s", raw)
			}
		})
	}
}

// TestTaskNormalizedDoesNotLeakCancel 验证取消函数不会随响应出去。
//
// 它没有 json tag，所以本来就不会被序列化；但这个断言的意义在于
// 钉住「出口会把内部字段清掉」这个行为 —— 将来若有人给 cancel
// 加了个 tag（或改成导出字段），这个测试会立刻失败。
func TestTaskNormalizedDoesNotLeakCancel(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	task := &Task{ID: "x", cancel: cancel}
	raw, err := json.Marshal(task.Normalized())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "cancel") {
		t.Fatalf("内部字段 cancel 不应出现在响应里: %s", raw)
	}
	if task.Normalized().cancel != nil {
		t.Error("Normalized 应当清掉 cancel")
	}
}

// TestTaskSnapshotIsDetachedFromLive 验证快照与活动对象解耦。
//
// 快照会交给 HTTP 层去序列化，而活动对象在后台 goroutine 里持续被改写。
// 若返回的是同一个对象，就同时有两个 goroutine 在读写它 ——
// 数据竞争，表现为进度数字偶发错乱（`go test -race` 能抓到）。
func TestTaskSnapshotIsDetachedFromLive(t *testing.T) {
	live := &Task{ID: "a", Percent: 10, logs: []string{"x"}}
	snap := live.Snapshot()

	// 改写活动对象不应影响已取的快照
	live.Percent = 90
	live.logs = append(live.logs, "y")

	if snap.Percent != 10 {
		t.Errorf("快照的 Percent 被后续写入影响: %d", snap.Percent)
	}
	if len(snap.Logs) != 1 {
		t.Errorf("快照的 Logs 被后续写入影响: %v", snap.Logs)
	}

	// nil 接收者也要能安全工作：Progress 在没有任务时会走到这条路径
	var nilTask *Task
	if got := nilTask.Snapshot(); got == nil {
		t.Fatal("nil 任务的快照不应为 nil（否则序列化会 panic）")
	}
}
