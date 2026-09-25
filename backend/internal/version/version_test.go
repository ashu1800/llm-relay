package version

import (
	"runtime"
	"strings"
	"testing"
)

// TestResolveNeverEmpty 钉住「版本号永远不为空」这条底线。
//
// 空版本号不是「少显示一点信息」那么轻：前端徽标会整块消失、
// 更新比较会把当前版本当成 0.0.0 从而永远提示有更新。
func TestResolveNeverEmpty(t *testing.T) {
	if strings.TrimSpace(Version) == "" {
		t.Fatal("Version 为空：init 的兜底逻辑没有生效")
	}
}

func TestBuildTypeIsKnown(t *testing.T) {
	// 没有注入时探测结果只能是这两种之一；
	// 若出现了别的值，说明有地方写错了字符串
	switch BuildType {
	case BuildSource, BuildDocker, BuildBinary:
	default:
		t.Fatalf("BuildType 取值非法: %q", BuildType)
	}
	// 测试进程不在容器里（CI 的 Go 步骤也是裸机），应当是 source
	if inContainer() {
		t.Skip("当前进程在容器内，跳过形态断言")
	}
	if BuildType != BuildSource {
		t.Fatalf("容器外未注入时应为 source，实际 %q", BuildType)
	}
}

func TestDisplayStripsGitDescribeTail(t *testing.T) {
	saved := Version
	defer func() { Version = saved }()

	cases := []struct{ in, want string }{
		{"v1.2.3", "v1.2.3"},
		{"v1.2.3-7-g3f9a1c", "v1.2.3-7"},
		{"v1.2.3-7-g3f9a1c-dirty", "v1.2.3-7"},
		{"v1.2.3-dirty", "v1.2.3"},
		// 前缀不是 v 的（源码导出目录构建）也要能剥
		{"g3f9a1c", "g3f9a1c"},
		{"dev", "dev"},
		// 三段版本号后面跟 -rc1 这类预发布标识不能被误伤：
		// 它不是 git describe 的哈希段（g 后面要跟 6 位以上十六进制）
		{"v1.2.3-rc1", "v1.2.3-rc1"},
	}
	for _, c := range cases {
		Version = c.in
		if got := Display(); got != c.want {
			t.Errorf("Display(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestCurrentCarriesRuntimeFacts(t *testing.T) {
	info := Current()
	if info.Version == "" || info.Display == "" {
		t.Fatal("Current 返回了空版本号")
	}
	// 这三个字段是排障用的（「你那个二进制是 arm64 的」），
	// 必须来自运行时而不是写死
	if info.OS != runtime.GOOS || info.Arch != runtime.GOARCH {
		t.Fatalf("OS/Arch 与运行时不一致: %s/%s", info.OS, info.Arch)
	}
	if info.GoVersion == "" {
		t.Fatal("GoVersion 为空")
	}
}

func TestIsRelease(t *testing.T) {
	saved := BuildType
	defer func() { BuildType = saved }()

	for _, c := range []struct {
		bt   string
		want bool
	}{
		{BuildDocker, true},
		{BuildBinary, true},
		{BuildSource, false},
		{"", false},
	} {
		BuildType = c.bt
		if got := IsRelease(); got != c.want {
			t.Errorf("IsRelease() 在 BuildType=%q 时为 %v，期望 %v", c.bt, got, c.want)
		}
	}
}
