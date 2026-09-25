package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 源码同步的测试。
//
// 这段代码做的事情是「用下载到的新源码覆盖安装目录」。它最危险的失败
// 形态不是「没更新成功」，而是**把不该删的东西删了** —— deploy/.env 里
// 是数据库密码与加密主密钥，deploy/data 里是 postgres 的全部数据。
// 覆盖掉任何一个，服务就再也起不来了（而且数据不可恢复）。
//
// 所以这里的测试重点全部在「什么必须活下来」上，而不是「什么被更新了」。

// makeFakeInstall 造一个假的安装目录，包含运行期数据与旧源码。
func makeFakeInstall(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// 运行期数据：这些必须原封不动
	mustWrite(t, filepath.Join(dir, "deploy", ".env"), "DB_PASSWORD=super-secret\nRELAY_SECRET=master-key\n", 0o600)
	mustWrite(t, filepath.Join(dir, "deploy", "data", "postgres", "PG_VERSION"), "16\n", 0o644)
	mustWrite(t, filepath.Join(dir, "backups", "2026-01-01.sql"), "dump\n", 0o600)
	// 用户手改的 compose 覆盖文件
	mustWrite(t, filepath.Join(dir, "deploy", "docker-compose.override.yml"), "services: {}\n", 0o644)

	// 旧源码：会被更新掉
	mustWrite(t, filepath.Join(dir, "backend", "go.mod"), "module old\n", 0o644)
	mustWrite(t, filepath.Join(dir, "backend", "stale_file.go"), "package main\n", 0o644)
	mustWrite(t, filepath.Join(dir, "frontend", "package.json"), "{}\n", 0o644)
	return dir
}

// makeFakeSource 造一份「下载到的新源码」。
func makeFakeSource(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "backend", "go.mod"), "module new\n", 0o644)
	mustWrite(t, filepath.Join(dir, "backend", "main.go"), "package main // new\n", 0o644)
	mustWrite(t, filepath.Join(dir, "frontend", "package.json"), `{"v":2}`+"\n", 0o644)
	mustWrite(t, filepath.Join(dir, "Makefile"), "all:\n", 0o644)
	mustWrite(t, filepath.Join(dir, "deploy", "Dockerfile"), "FROM scratch\n", 0o644)
	mustWrite(t, filepath.Join(dir, ".goreleaser.yaml"), "version: 2\n", 0o644)
	// 源码归档里也可能带 .env.example（这是要更新的，它不含密钥）
	mustWrite(t, filepath.Join(dir, "deploy", ".env.example"), "DB_PASSWORD=CHANGE_ME\n", 0o644)
	return dir
}

func mustWrite(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return string(b)
}

// TestSyncSourcePreservesRuntimeData 是这组测试里最重要的一条。
//
// 它把「必须活下来」的东西逐个断言一遍。任何一条失败都意味着
// 一次一键更新会毁掉部署 —— 这种 bug 在真实使用中是不可挽回的，
// 所以值得用最直白的写法钉住。
func TestSyncSourcePreservesRuntimeData(t *testing.T) {
	install := makeFakeInstall(t)
	src := makeFakeSource(t)

	r := &runner{repoDir: install}
	if err := r.syncSource(src); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	// ---- 必须原封不动的东西 ----
	preserved := map[string]string{
		"deploy/.env":                        "DB_PASSWORD=super-secret\nRELAY_SECRET=master-key\n",
		"deploy/data/postgres/PG_VERSION":    "16\n",
		"backups/2026-01-01.sql":             "dump\n",
		"deploy/docker-compose.override.yml": "services: {}\n",
	}
	for rel, want := range preserved {
		path := filepath.Join(install, rel)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s 被删掉了（这是运行期数据，绝不能动）: %v", rel, err)
			continue
		}
		if got := readFile(t, path); got != want {
			t.Errorf("%s 内容被改动：got %q, want %q", rel, got, want)
		}
	}

	// ---- 必须被更新的东西 ----
	updated := map[string]string{
		"backend/go.mod":        "module new\n",
		"backend/main.go":       "package main // new\n",
		"frontend/package.json": `{"v":2}` + "\n",
		"deploy/Dockerfile":     "FROM scratch\n",
		"deploy/.env.example":   "DB_PASSWORD=CHANGE_ME\n",
	}
	for rel, want := range updated {
		path := filepath.Join(install, rel)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s 没有被同步过来: %v", rel, err)
			continue
		}
		if got := readFile(t, path); got != want {
			t.Errorf("%s 未被更新：got %q, want %q", rel, got, want)
		}
	}
}

// TestSyncSourceRemovesDeletedFiles 验证「先删后拷」。
//
// 新版本可能删掉了某个文件。只覆盖不删除会把它留在原地，
// 而 Go 的表现是编译报「重复声明」—— 一个与真正原因毫无关联的错误。
func TestSyncSourceRemovesDeletedFiles(t *testing.T) {
	install := makeFakeInstall(t)
	src := makeFakeSource(t)

	// 旧版本里有 stale_file.go，新源码里没有
	stale := filepath.Join(install, "backend", "stale_file.go")
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("测试前置条件不成立：%v", err)
	}

	r := &runner{repoDir: install}
	if err := r.syncSource(src); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	if _, err := os.Stat(stale); err == nil {
		t.Error("旧版本独有的文件应当被清理掉（否则会编译报重复声明）")
	}
}

// TestSyncSourceSkipsMissingItems 验证新源码里缺少某个条目时跳过而不报错。
//
// 这是必须容忍的：发布版可能合并或移除了某个部署文件，
// 为一个「新版本里不再有它」的条目让整个更新失败是说不过去的。
func TestSyncSourceSkipsMissingItems(t *testing.T) {
	install := makeFakeInstall(t)
	// 只给 backend，其余条目在源码里都不存在
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "backend", "go.mod"), "module new\n", 0o644)

	r := &runner{repoDir: install}
	if err := r.syncSource(src); err != nil {
		t.Fatalf("缺少条目时不应报错: %v", err)
	}
	if got := readFile(t, filepath.Join(install, "backend", "go.mod")); got != "module new\n" {
		t.Errorf("存在的条目应当被同步：%q", got)
	}
	// 未出现在源码里的条目不应被删掉（我们的清单只列了 backend）
	if _, err := os.Stat(filepath.Join(install, "frontend", "package.json")); err != nil {
		t.Error("清单外的文件不应被动到")
	}
}

// TestCopyTreePreservesModesAndSymlinks 验证目录复制保留权限与符号链接。
//
// 权限位很重要：deploy 下的脚本没了可执行位就跑不起来，
// 而那种失败发生在很久之后的某次调用里，很难关联到「同步源码」这一步。
func TestCopyTreePreservesModesAndSymlinks(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "script.sh"), "#!/bin/sh\necho hi\n", 0o755)
	mustWrite(t, filepath.Join(src, "sub", "data.txt"), "data\n", 0o644)

	// 一个指向同目录文件的符号链接
	if err := os.Symlink("script.sh", filepath.Join(src, "link.sh")); err != nil {
		t.Skipf("当前环境不支持符号链接: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "out")
	if err := copyTree(src, dst); err != nil {
		t.Fatalf("复制失败: %v", err)
	}

	info, err := os.Stat(filepath.Join(dst, "script.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if info.Mode().Perm()&0o100 == 0 {
			t.Errorf("可执行位丢失: %v", info.Mode())
		}
	}
	if got := readFile(t, filepath.Join(dst, "sub", "data.txt")); got != "data\n" {
		t.Errorf("子目录内容不符: %q", got)
	}

	// 符号链接必须仍是链接（而不是被展开成一份内容副本）
	li, err := os.Lstat(filepath.Join(dst, "link.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if li.Mode()&os.ModeSymlink == 0 {
		t.Error("符号链接应当被保留为链接，而不是复制成普通文件")
	}
}

// TestRepoParts 验证仓库名解析。
func TestRepoParts(t *testing.T) {
	cases := []struct {
		in, owner, name string
		wantErr         bool
	}{
		{"ashu1800/llm-relay", "ashu1800", "llm-relay", false},
		// 带 .git 后缀的写法也要能处理（git remote 的输出就是这个形状）
		{"ashu1800/llm-relay.git", "ashu1800", "llm-relay", false},
		{"  ashu1800/llm-relay  ", "ashu1800", "llm-relay", false},
		{"no-slash", "", "", true},
		{"", "", "", true},
		{"/llm-relay", "", "", true},
		{"ashu1800/", "", "", true},
	}
	for _, c := range cases {
		r := &runner{repo: c.in}
		owner, name, err := r.repoParts()
		if c.wantErr {
			if err == nil {
				t.Errorf("repoParts(%q) 应当报错", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("repoParts(%q) 意外报错: %v", c.in, err)
			continue
		}
		if owner != c.owner || name != c.name {
			t.Errorf("repoParts(%q) = (%q,%q)，期望 (%q,%q)", c.in, owner, name, c.owner, c.name)
		}
	}
}

// TestUpgradeJSONFieldNames 钉住更新器任务的 JSON 字段名。
//
// 与管理台侧同样的理由（见 internal/update 的同名测试）：漏了 json tag
// 就会被序列化成大写原名，而管理台读的是小写，界面就会永远认为
// 任务在运行。这个进程是独立的二进制，测试也必须独立守住。
func TestUpgradeJSONFieldNames(t *testing.T) {
	u := &upgrade{
		ID: "u1", Kind: "upgrade", Phase: "done", Percent: 100,
		Message: "ok", Done: true, Failed: false, Logs: []string{"x"},
	}
	raw, err := json.Marshal(snapshotUpgrade(u))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"id"`, `"kind"`, `"phase"`, `"percent"`, `"message"`, `"done"`, `"failed"`, `"logs"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("序列化结果缺少 %s: %s", key, raw)
		}
	}
	for _, bad := range []string{`"Done"`, `"Failed"`, `"Logs"`, `"ID"`, "previousImage"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("出现了不该有的字段 %s: %s", bad, raw)
		}
	}
}

// TestSnapshotUpgradeGivesArraysNotNull 验证空日志归一成 [] 而不是 null。
func TestSnapshotUpgradeGivesArraysNotNull(t *testing.T) {
	raw, err := json.Marshal(snapshotUpgrade(&upgrade{ID: "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"logs":null`) {
		t.Fatalf("logs 不应为 null: %s", raw)
	}
	if !strings.Contains(string(raw), `"logs":[]`) {
		t.Fatalf("logs 应为空数组: %s", raw)
	}
}

// TestVersionMatches 钉住版本比较的口径。
//
// 这段比较决定两件事，方向相反，所以每一条边界都要说清楚：
//
//	「已是最新，无需重建」—— 误判为 false 的代价是白重建几分钟
//	「更新后确认版本生效」—— 误判为 true 的代价是**把一次失败的更新
//	                        报告成成功**
//
// 后者严重得多，所以这里对「问不出来」（空串）一律判不匹配。
func TestVersionMatches(t *testing.T) {
	cases := []struct {
		current, target string
		want            bool
		why             string
	}{
		{"v1.2.3", "1.2.3", true, "注入时带 v、tag 不带，指的是同一版"},
		{"v1.2.3-7-g3f9a1c-dirty", "1.2.3", true, "git describe 尾巴不该影响判定"},
		{"v1.2.3", "1.2.3", true, "完全相同"},
		{"v1.2.2", "1.2.3", false, "旧版本必须判为不匹配"},
		{"v1.2.30", "1.2.3", false, "1.2.3 只是 1.2.30 的前缀，不是同一版（子串匹配会把它误判成相同）"},
		{"v0.1.1-rc1", "0.1.1", false, "rc 与正式版是两版：正式版发布后 rc 应触发重建"},
		{"", "1.2.3", false, "读不出当前版本 → 不匹配（宁可多构建一次）"},
		{"v1.2.3", "", false, "没有目标版本 → 不匹配（不能凭空说成功）"},
		{"", "", false, "两边都空"},
	}
	for _, c := range cases {
		if got := versionMatches(c.current, c.target); got != c.want {
			t.Errorf("versionMatches(%q,%q) = %v，期望 %v（%s）",
				c.current, c.target, got, c.want, c.why)
		}
	}
}

// TestVersionMatchesNeverClaimsSuccessOnEmpty 单独强调最要命的那条。
//
// 「读不出当前版本」必须判为**不匹配**：如果这里返回 true，
// 一个根本没换成新镜像的更新会被报告成「已确认版本生效」——
// 那是这个功能最不该犯的错（把失败说成成功）。
func TestVersionMatchesNeverClaimsSuccessOnEmpty(t *testing.T) {
	for _, target := range []string{"1.0.0", "v2.3.4", "dev", "latest"} {
		if versionMatches("", target) {
			t.Errorf("当前版本读不出来时，绝不能声称匹配（target=%q）", target)
		}
	}
}

// TestOrUnknown 是个小工具，但它出现在用户能看到的日志里。
func TestOrUnknown(t *testing.T) {
	if got := orUnknown(""); got != "未知" {
		t.Errorf("空串应显示为「未知」，实际 %q", got)
	}
	if got := orUnknown("v1.0.0"); got != "v1.0.0" {
		t.Errorf("非空应原样返回，实际 %q", got)
	}
}

// TestDeriveRollbackTag 钉住回滚标签名的推导规则。
//
// 这个推导直接决定「回滚能不能成功」：标签名一旦与创建时不一致，
// findRollbackTarget 就找不到目标，于是所有回滚都会以
// 「没有可回滚的目标」告终 —— 而那正是这个功能存在的意义。
//
// 特别要守住的是**带端口的镜像仓库**：`registry:5000/foo` 里那个冒号
// 不是 tag 分隔符。简单按最后一个冒号切会把名字切成
// `registry` + `rollback`，得到一个完全错误的标签。
func TestDeriveRollbackTag(t *testing.T) {
	cases := []struct {
		image, want string
	}{
		{"llm-relay:local", "llm-relay:rollback"},
		{"llm-relay", "llm-relay:rollback"},
		{"ghcr.io/ashu1800/llm-relay:latest", "ghcr.io/ashu1800/llm-relay:rollback"},
		{"ghcr.io/ashu1800/llm-relay", "ghcr.io/ashu1800/llm-relay:rollback"},
		// 带端口的仓库：冒号属于主机名，不是 tag
		{"registry:5000/llm-relay", "registry:5000/llm-relay:rollback"},
		{"registry:5000/llm-relay:v1", "registry:5000/llm-relay:rollback"},
		{"localhost:5000/a/b:v2", "localhost:5000/a/b:rollback"},
	}
	for _, c := range cases {
		if got := deriveRollbackTag("", c.image); got != c.want {
			t.Errorf("deriveRollbackTag(%q) = %q，期望 %q", c.image, got, c.want)
		}
	}

	// 显式指定时以它为准（多实例部署要靠它区分）
	if got := deriveRollbackTag("custom:rb", "llm-relay:local"); got != "custom:rb" {
		t.Errorf("显式标签应优先，实际 %q", got)
	}
	// 空镜像名不能 panic，也不能产出以冒号开头的怪标签
	if got := deriveRollbackTag("", ""); got != rollbackTag {
		t.Errorf("空镜像名应回落到默认标签，实际 %q", got)
	}
}

// rollbackTagName 的「实例字段优先于推导」测试已随该方法一起删除：
// main 构造 runner 时必然经 deriveRollbackTag 赋值且永不返回空，
// 推导逻辑本身由 TestDeriveRollbackTag 覆盖。

// TestBuildEnvOverridesVersion 钉住「构建时必须显式传版本号」这条契约。
//
// 这是一个只有真正发布过之后才会暴露的缺陷：更新器下载 v1.2.3 的源码、
// 覆盖、然后 docker compose build —— 而 VERSION 是从 install 目录的 .env
// 插值来的，那个文件只有 install.sh 会写（更新器刻意不动它，因为里面
// 还有数据库密码）。默认路径下于是会这样：
//
//	新镜像里编的还是**旧**版本号
//	→ 界面上的版本号纹丝不动
//	→ imageVersion 读到的仍是旧版本，判定为「没更新成功」
//	→ 用户每次点更新都真的重建一遍（几分钟），却看不出任何变化
//
// 仓库里一个 Release 都没有时，这条路径在第一步（取最新版本）就失败了，
// 所以单元测试与端到端验证都覆盖不到它。这里用最直接的方式守住：
// 只要版本号非空，环境里就必须有对应的一项。
func TestBuildEnvOverridesVersion(t *testing.T) {
	r := &runner{}

	env := r.buildEnv("v1.2.3")
	if len(env) == 0 {
		t.Fatal("版本号非空时必须给出覆盖项，否则构建出的镜像带的是旧版本号")
	}
	found := false
	for _, kv := range env {
		if kv == "VERSION=v1.2.3" {
			found = true
		}
		// 每个元素都必须是独立的 KEY=VALUE（exec 直接吃这个切片），
		// 不能出现空格拼多个变量的写法 —— 那种写法要求经过 shell 展开，
		// 而这里刻意不用 shell
		if strings.ContainsAny(kv, " \t\n") {
			t.Errorf("环境变量项不应含空白字符（会被误当成一个值）: %q", kv)
		}
		if !strings.Contains(kv, "=") {
			t.Errorf("环境变量项必须形如 KEY=VALUE: %q", kv)
		}
	}
	if !found {
		t.Errorf("缺少 VERSION=v1.2.3，实际: %v", env)
	}

	// 空版本号时不覆盖：那意味着「没有具体目标」（走 .env 的既有值即可），
	// 硬塞一个空 VERSION 会让 Dockerfile 走进兜底分支，反而丢掉 .env 里的值
	if got := r.buildEnv(""); got != nil {
		t.Errorf("版本号为空时不应覆盖任何变量，实际: %v", got)
	}
}
