// Package version 回答一个问题：**这份二进制到底是哪一版、跑在什么形态下**。
//
// 为什么值得单独一个包：版本号有两个截然不同的用途，混在一起就必然出错。
//
//  1. 给人看 —— 界面侧栏那枚小徽标。它要的是「我改的代码部署上去了没有」，
//     所以必须是**编译进二进制的**值，不能来自配置文件（改配置不用重新编译，
//     那样显示的版本和实际运行的代码可以不一致，徽标就失去了全部意义）。
//  2. 给更新器用 —— 与 GitHub 上的 release tag 比较大小。它要的是严格的
//     语义化版本解析，而不是字符串。
//
// 版本号有三个来源，优先级从高到低：
//
//	-ldflags -X llm-relay/internal/version.Version=...   （install.sh / CI 注入）
//	→ //go:embed VERSION                                  （仓库里的文件，兜底）
//	→ "dev"                                               （什么都没有时的诚实答案）
//
// 中间那一层是刻意的：CI 在打 tag 时会把 VERSION 文件写成 tag 对应的版本号并
// 提交回主分支，而 goreleaser 只注入 Commit/Date/BuildType、**不**注入 Version ——
// 于是「tag 上的代码」与「VERSION 文件」互为校验，任何一方漏更新都会在界面上
// 露出马脚，而不是悄悄显示一个错的版本号。
package version

import (
	_ "embed"
	"os"
	"regexp"
	"runtime"
	"strings"
)

// VERSION 由发布流程写入（见 scripts/resolve-version.sh）。
// embed 只能引用同包目录下的文件，所以它必须待在这里，不能放到仓库根。
//
//go:embed VERSION
var embedded string

// 构建期变量。全部经 -ldflags 注入；默认值刻意留空或中性，
// 因为「没注入」本身就是一种需要被识别的状态（见 resolve）。
var (
	// Version 形如 v1.2.3 / v1.2.3-7-g3f9a1c / v1.2.3-7-g3f9a1c-dirty。
	// 留空表示构建方没有注入，此时回落到 embed 的 VERSION 文件。
	Version = ""
	// Commit 是构建时的提交短哈希。
	Commit = "unknown"
	// Date 是构建时刻（RFC3339）。
	Date = "unknown"
	// BuildType 决定「能不能自动更新、以及怎么更新」，取值见下面的常量。
	// 留空表示没有注入，由 detectBuildType 自行探测。
	BuildType = ""
)

// 构建形态。这个字段不只是描述性的 —— 它是**更新方式的唯一判据**：
// 三种形态的更新动作完全不同，选错了轻则无效重则把服务搞坏。
const (
	// BuildSource 本地随手 go build / go run 出来的。没有发布产物与之对应，
	// 界面上只提示「有新版本」并给一个跳转链接，绝不提供一键更新 ——
	// 否则会用官方二进制覆盖掉开发者本地的构建物。
	BuildSource = "source"
	// BuildDocker 官方镜像。进程在容器里**不能替换自己**（替换了也没用，
	// 容器一重建就没了），必须由宿主侧的 updater 拉取新镜像并重建容器。
	BuildDocker = "docker"
	// BuildBinary 官方预编译二进制 + systemd。进程可以直接原子替换自己的
	// 可执行文件，然后靠 systemd 的 Restart=always 把自己拉起来。
	BuildBinary = "binary"
)

// resolve 在包初始化时把三项信息补齐。放在 init 而不是构造函数里：
// 版本信息在进程整个生命周期内不变，任何地方 import 它拿到的都该是同一份事实。
func init() {
	if strings.TrimSpace(Version) == "" {
		Version = strings.TrimSpace(embedded)
	}
	if Version == "" {
		// 连 VERSION 文件都是空的（例如有人删了它）——如实说「dev」，
		// 不编造一个看起来正常的版本号
		Version = "dev"
	}
	Version = strings.TrimSpace(Version)

	if strings.TrimSpace(BuildType) == "" {
		BuildType = detectBuildType()
	}
}

// detectBuildType 在没有显式注入时猜一次构建形态。
//
// 只区分「在容器里」与「不在容器里」这一件事：容器内一律按 docker 处理，
// 容器外一律按 source 处理。宁可把裸机二进制误判成 source（少一个一键更新按钮，
// 用户仍能看到新版本并手动部署），也不要把源码构建误判成 binary
// （那会给开发者一个「一键把自己编译的二进制换成官方版」的按钮）。
//
// 因此 install-bare.sh 必须显式注入 BuildType=binary，不能依赖这里的探测。
func detectBuildType() string {
	if inContainer() {
		return BuildDocker
	}
	return BuildSource
}

// inContainer 判断当前进程是否跑在容器里。
//
// 用 /.dockerenv 这个约定俗成的标记文件，而不是读 /proc/1/cgroup：
// 后者在 cgroup v2 + 私有命名空间下经常认不出 docker，而前者由 Docker
// 自己创建，稳得多。podman 用 /run/.containerenv，一并认上。
func inContainer() bool {
	for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// Info 是一次性打包好的版本信息，供接口直接序列化。
type Info struct {
	// Version 是完整版本号，含 git describe 的提交数与哈希尾巴
	Version string `json:"version"`
	// Display 是给人看的短版本号，剥掉了 -g<hash> 与 -dirty
	Display string `json:"display"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	// BuildType 取值见 BuildSource / BuildDocker / BuildBinary
	BuildType string `json:"build_type"`
	// GoVersion/OS/Arch 只在排障时有用（「你那个二进制是 arm64 的」）
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Current 返回当前进程的完整版本信息。
func Current() Info {
	return Info{
		Version:   Version,
		Display:   Display(),
		Commit:    Commit,
		Date:      Date,
		BuildType: BuildType,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
}

// IsRelease 表示这份二进制来自官方发布产物，因而具备自动更新的前提条件。
// 「前提条件」四个字是认真的：能不能真的更新还取决于部署形态
// （docker 形态还需要宿主侧 updater 在场，见 update 包）。
func IsRelease() bool {
	return BuildType == BuildDocker || BuildType == BuildBinary
}

// reGitHash 匹配 git describe 追加的提交段，如 -7-g3f9a1c。
var (
	reGitHash = regexp.MustCompile(`-g[0-9a-f]{6,}$`)
	reDirty   = regexp.MustCompile(`-dirty$`)
)

// Display 返回侧栏徽标上显示的那一串。
//
// 剥掉哈希段与 -dirty 是因为它们对「界面上这版是不是正在跑的那份代码」
// 这个问题没有贡献，却会把徽标撑长到被省略号截断 —— 侧栏只有 192px，
// 版本号又必须逐字可读（比例字体下 0/O、1/l 分不清，所以用等宽字体）。
// 完整值仍在悬停提示与 /system/info 里，对照部署时够用。
func Display() string {
	v := reDirty.ReplaceAllString(Version, "")
	v = reGitHash.ReplaceAllString(v, "")
	return v
}
