package version

import (
	"regexp"
	"strconv"
	"strings"
)

// Semver 是解析后的版本号。
//
// 为什么要自己写而不是引第三方库：本项目的版本号**不是标准 semver** ——
// install.sh 用 `git describe --tags --always --dirty` 生成，于是会出现
//
//	v1.2.0-7-g3f9a1c          基于 tag v1.2.0，之后还有 7 个提交
//	v1.2.0-7-g3f9a1c-dirty    工作区还有未提交改动
//
// 这种形状。标准 semver 库会把 `-7-g3f9a1c` 当成预发布标识符，于是
// 判定 v1.2.0-7-g3f9a1c **小于** v1.2.0 —— 那会让界面把一个降级
// 当成「发现新版本」推给用户，点下去就等于把服务退回旧代码。
//
// 所以这里显式区分两种后缀：
//   - git describe 尾巴（-<提交数>-g<hash>）→ 表示「tag 之后又走了这么多提交」，
//     语义上是**比那个 tag 更新**，记在 Post 字段里。
//   - 真正的预发布标识（-rc1 / -beta.2）→ 按 semver 规则，**比正式版旧**。
type Semver struct {
	Major int
	Minor int
	Patch int
	// Pre 是预发布标识（不含开头的连字符），空表示正式版
	Pre string
	// Post 是 git describe 的提交计数：相对最近 tag 多出来的提交数。
	// 0 表示这就是 tag 指向的那次提交（或版本号里根本没这个信息）。
	Post int
	// Dirty 表示工作区有未提交改动（git describe 的 -dirty）
	Dirty bool
}

// reDescribeTail 匹配 git describe 追加的 `-<提交数>-g<hash>` 段。
// 提交数允许 1 位以上，哈希要求 4 位以上十六进制（git 的 abbrev 下限是 4）。
var reDescribeTail = regexp.MustCompile(`-(\d+)-g([0-9a-f]{4,})$`)

// reDirtyTail 匹配结尾的 -dirty。
var reDirtyTail = regexp.MustCompile(`-dirty$`)

// Parse 解析版本号。无法识别的部分一律当 0 处理，绝不 panic ——
// 版本号来自人手与 git，格式出错时的正确反应是「当成很旧的版本，
// 于是不会触发更新」，而不是让整个管理接口 500。
func Parse(s string) Semver {
	var v Semver

	rest := strings.TrimSpace(s)
	rest = strings.TrimPrefix(rest, "v")
	rest = strings.TrimPrefix(rest, "V")

	// 先剥 -dirty，再剥 git describe 尾巴：两者的先后不能反，
	// 因为 -dirty 永远在最末尾（v1.2.0-7-g3f9a1c-dirty）
	if reDirtyTail.MatchString(rest) {
		v.Dirty = true
		rest = reDirtyTail.ReplaceAllString(rest, "")
	}
	if m := reDescribeTail.FindStringSubmatch(rest); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			v.Post = n
		}
		rest = rest[:len(rest)-len(m[0])]
	}

	// 剩下的部分按 semver 拆：major.minor.patch[-pre][+build]
	// 构建元数据（+ 之后）不参与比较，直接丢掉
	if i := strings.IndexByte(rest, '+'); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		v.Pre = rest[i+1:]
		rest = rest[:i]
	}

	parts := strings.Split(rest, ".")
	if len(parts) > 0 {
		v.Major = atoiOr0(parts[0])
	}
	if len(parts) > 1 {
		v.Minor = atoiOr0(parts[1])
	}
	if len(parts) > 2 {
		v.Patch = atoiOr0(parts[2])
	}
	return v
}

// atoiOr0 只接受纯数字，遇到 "1a" 这类脏值返回 0。
// 用 strconv.Atoi 而不是 fmt.Sscanf：后者对 "1abc" 会成功解析出 1，
// 把一个本该被视为无效的版本号伪装成正常值。
func atoiOr0(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// Compare 按「谁更新」的语义比较两个版本号字符串。
//
// 返回 -1 表示 a 比 b 旧，1 表示 a 比 b 新，0 表示相等。
func Compare(a, b string) int {
	return Parse(a).Compare(Parse(b))
}

// Compare 是比较的实体实现。
func (a Semver) Compare(b Semver) int {
	// 1) 主体三段版本号
	if c := cmpInt(a.Major, b.Major); c != 0 {
		return c
	}
	if c := cmpInt(a.Minor, b.Minor); c != 0 {
		return c
	}
	if c := cmpInt(a.Patch, b.Patch); c != 0 {
		return c
	}
	// 2) 预发布：正式版比任何预发布版都新（semver 规则）
	if c := cmpPre(a.Pre, b.Pre); c != 0 {
		return c
	}
	// 3) git describe 的提交数：同一个 tag 之后走得越远越新
	if c := cmpInt(a.Post, b.Post); c != 0 {
		return c
	}
	// 4) 有未提交改动的排在后面（更新）
	//    这一条只在前面全部相等时才起作用，实际影响是：
	//    本地改过的构建不会被同版本的发布产物「更新」掉
	if a.Dirty != b.Dirty {
		if a.Dirty {
			return 1
		}
		return -1
	}
	return 0
}

// Newer 判断 candidate 是否比 current 新 —— 也就是「该不该提示更新」。
func Newer(candidate, current string) bool {
	return Compare(candidate, current) > 0
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// cmpPre 按 semver 规则比较预发布标识：空 > 非空；两个都非空时逐段比较，
// 数字段按数值比、其余按字典序；数字段小于非数字段。
func cmpPre(a, b string) int {
	if a == b {
		return 0
	}
	// 正式版（无预发布标识）更新
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aErr := strconv.Atoi(as[i])
		bn, bErr := strconv.Atoi(bs[i])
		aIsNum, bIsNum := aErr == nil, bErr == nil
		switch {
		case aIsNum && bIsNum:
			if c := cmpInt(an, bn); c != 0 {
				return c
			}
		case aIsNum:
			// 数字标识符比字母标识符优先级低（semver 第 11 条）
			return -1
		case bIsNum:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	// 前缀全部相同：段数多的更新（1.0.0-alpha < 1.0.0-alpha.1）
	return cmpInt(len(as), len(bs))
}
