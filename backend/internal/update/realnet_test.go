package update

// 真访问 GitHub 的冒烟测试（2026-09-30 立）。
//
// 默认全部跳过：日常 go test 与 CI 不该依赖外网（未配 token 时限额只有 60 次/小时，
// 国内机器更是常年连不上，要靠代理）。需要确认「代理 / token 配好之后真能通」
// 或改动 netguard / transport 之后，手动跑：
//
//	LLMRELAY_TEST_NET=1 go test ./internal/update -run TestReal -v
//	LLMRELAY_TEST_NET=1 LLMRELAY_TEST_PROXY=http://127.0.0.1:7890 go test ./internal/update -run TestReal -v
//
// 不设 LLMRELAY_TEST_PROXY 时走的是「环境变量代理」那条路（HTTP(S)_PROXY），
// 这正是 2026-09-30 修掉的那条：修复前它会被 netguard 当成内网目标拒掉
// （proxyconnect tcp: 目标地址指向内网或本机，已拒绝: 127.0.0.1）。
//
// 这里验的是 stub 用例覆盖不到的半条链路：netguard 的建连校验与代理豁免、
// api.github.com 的响应解析、下载 URL 的白名单校验、checksums 的真实内容。

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// realClient 构造一个真的连 GitHub 的客户端（代理按环境变量或 LLMRELAY_TEST_PROXY）。
func realClient(t *testing.T, repo string) *Client {
	t.Helper()
	c, err := NewClient(ClientOptions{Repo: repo, ProxyURL: os.Getenv("LLMRELAY_TEST_PROXY")})
	if err != nil {
		t.Fatalf("构造客户端失败: %v", err)
	}
	return c
}

// 跳过开关：所有真实网络用例共用一个环境变量。
func requireNet(t *testing.T) {
	t.Helper()
	if os.Getenv("LLMRELAY_TEST_NET") != "1" {
		t.Skip("未设置 LLMRELAY_TEST_NET=1，跳过真实 GitHub 冒烟测试")
	}
}

// 检测更新这条链路：客户端 → api.github.com → 版本比较。
func TestRealCheckAgainstDefaultRepo(t *testing.T) {
	requireNet(t)
	s := &Service{logger: slog.Default(), cfg: DefaultConfig()}
	s.client = realClient(t, s.cfg.Repo)

	info := s.Check(context.Background(), true)
	if info.Warning != "" {
		t.Fatalf("真实检测失败: %s", info.Warning)
	}
	if info.Latest == "" {
		t.Fatal("应当拿到一个最新版本号")
	}
	t.Logf("仓库 %s：当前 %s，最新 %s，有新版本=%v", s.cfg.Repo, info.Current, info.Latest, info.HasUpdate)
}

// 下载这条链路：用的是另一个 transport（下载客户端），同样要能穿过代理，
// 而且每一跳都要过 checkDownloadTarget —— 只跑检测是盖不住它的。
func TestRealDownloadChecksumsThroughProxy(t *testing.T) {
	requireNet(t)
	client := realClient(t, DefaultConfig().Repo)
	ctx := context.Background()

	rel, err := client.FetchLatestRelease(ctx)
	if err != nil {
		t.Fatalf("取最新发布失败: %v", err)
	}
	url := ChecksumURL(rel.Assets)
	if url == "" {
		t.Fatalf("最新发布 %s 里没有 %s", rel.TagName, ChecksumAssetName)
	}
	body, err := client.FetchChecksums(ctx, url)
	if err != nil {
		t.Fatalf("下载 checksums 失败: %v", err)
	}
	// 归档名是「安装脚本与在线更新拼下载 URL」的硬契约，顺手核一下它真的在
	// 这份 checksums 里（这正是更新时校验用的那份文件）
	want := ArchiveName(strings.TrimPrefix(rel.TagName, "v"), "linux", "amd64")
	if !strings.Contains(string(body), want) {
		t.Fatalf("checksums 里应当有 %s，实际内容：\n%s", want, string(body))
	}
	t.Logf("已通过代理下载 %s（%d 字节），含 %s", url, len(body), want)
}
