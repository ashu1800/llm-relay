package update

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrNoBackup 表示没有可回滚的备份。
var ErrNoBackup = errors.New("没有可回滚的备份文件")

// backupSuffix 是备份文件的后缀：原文件 `llm-relay` 的备份是 `llm-relay.backup`。
//
// 用固定的后缀而不是带时间戳的名字，是为了让「回滚」有一个确定的、
// 不需要读任何状态文件就能找到的目标 —— 更新失败时最不该发生的事，
// 就是「还得先搞清楚上一版被存到哪儿去了」。
const backupSuffix = ".backup"

// SelfPath 返回当前进程可执行文件的真实路径（解析掉符号链接）。
//
// 解析符号链接是必须的：install.sh / systemd 常见的部署形态是
// /usr/local/bin/llm-relay → /opt/llm-relay/llm-relay 这样的软链，
// 而 os.Executable() 可能返回软链路径本身。若按软链路径替换，
// 结果是「把软链换成了一个真实文件」，而 systemd 指向的那个
// （或用户自己敲的那个）路径行为会变得莫名其妙。
func SelfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("无法定位当前可执行文件: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		// 解析失败（极少见：路径被删了、权限不足）就用原路径，
		// 这不应该让整个更新功能不可用
		return exe, nil
	}
	return resolved, nil
}

// BackupPath 返回给定可执行文件对应的备份路径。
func BackupPath(exePath string) string { return exePath + backupSuffix }

// RollbackFromPath 是回滚时「当前版本」的临时落脚点。
// 它与 backupSuffix 分开命名：.backup 始终表示「上一版」，
// 而这个文件表示「回滚前正在跑的那一版」，两者用途不同，不能混用一个位置。
func RollbackFromPath(exePath string) string { return exePath + ".rollback-from" }

// moveAside 把 src 改名到「同目录、带时间戳、且当前空闲」的名字上。
//
// 用来处理一个 Windows 特有的僵局：某个文件既删不掉（是运行中的映像），
// 又必须从当前名字上让开（正位要放新内容）。改名是允许的 —— 实测
// 「运行中映像可改名、不可删/不可覆盖」—— 所以挪开是唯一出路。
//
// 返回挪到的新路径；任何一步失败都返回空串，由调用方报错。
// 名字带时间戳而不是固定后缀：连续多次「更新但不重启」会在磁盘上
// 留下多份挪开的备份，固定名字第二次就会撞车。撞车时追加 -1、-2…
// 数字后缀用连字符隔开，好让 CleanupStaleBackups 的「时间戳」判定
// 不会把那个后缀当成时间戳的一部分。
func moveAside(src string) string {
	stamp := time.Now().Format("20060102-150405")
	for i := 0; i < 100; i++ {
		dst := src + "." + stamp
		if i > 0 {
			dst = fmt.Sprintf("%s.%s-%d", src, stamp, i)
		}
		if _, err := os.Stat(dst); err == nil {
			continue // 已存在，换下一个
		}
		if err := os.Rename(src, dst); err != nil {
			return ""
		}
		return dst
	}
	return ""
}

// MoveAsideBackup 把当前备份挪到一个空闲名字上，返回新路径（失败返回空串）。
//
// 导出是因为「启动时清理」与「替换时让位」用的是同一套命名规则：
// 两者如果各写一份，清理就可能删不到替换挪出来的文件。
func MoveAsideBackup(exePath string) string {
	backup := BackupPath(filepath.Clean(exePath))
	if _, err := os.Stat(backup); err != nil {
		return ""
	}
	return moveAside(backup)
}

// isMovedAsideName 判断 name 是否是 moveAside 生成的形状：
// <base>.<8位日期>-<6位时间>[-<序号>]。
//
// 只按数字/连字符形状判断会误伤（.backup.1 这种手写文件名也会命中），
// 所以要求第一段必须是 8+1+6 的日期时间形状。用户的 .backup-notes.txt
// 之类因此不会被清掉。
func isMovedAsideName(name, base string) bool {
	rest, ok := strings.CutPrefix(name, base+".")
	if !ok {
		return false
	}
	const stampLen = len("20060102-150405") // 15
	if len(rest) < stampLen {
		return false
	}
	stamp := rest[:stampLen]
	if stamp[8] != '-' {
		return false
	}
	if strings.Trim(stamp[:8]+stamp[9:], "0123456789") != "" {
		return false
	}
	// 时间戳之后只允许「空」或「-<序号>」
	tail := rest[stampLen:]
	if tail == "" {
		return true
	}
	if !strings.HasPrefix(tail, "-") {
		return false
	}
	seq := tail[1:]
	return seq != "" && strings.Trim(seq, "0123456789") == ""
}

// CleanupStaleBackups 删除上一个进程遗留的、被挪开的备份，返回删除个数。
//
// 必须在**启动早期**调用，理由有两层：
//   - 那些文件是上一次更新/回滚时「删不掉只好挪开」的运行中影像，
//     此刻新进程已经起来，旧影像早就不在了，删得掉；
//   - 不删的话它们会一直堆在安装目录里，每点一次更新多一份。
//
// 正位备份（exePath+".backup"）必须保留 —— 那是回滚的目标。
// 只匹配 moveAside 生成的形状，避免误删用户自己的文件。
func CleanupStaleBackups(exePath string) int {
	exePath = filepath.Clean(exePath)
	dir := filepath.Dir(exePath)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}

	n := 0
	bases := []string{filepath.Base(BackupPath(exePath)), filepath.Base(RollbackFromPath(exePath))}
	for _, e := range entries {
		name := e.Name()
		matched := false
		for _, base := range bases {
			if isMovedAsideName(name, base) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err == nil {
			n++
		}
	}
	return n
}

// ReplaceSelf 用 newBinary 原子替换 exePath，并把原文件留作备份。
//
// # 为什么临时文件必须与目标同目录
//
// os.Rename 只有在同一文件系统内才是原子的。跨设备（例如 /tmp 与
// /opt 常常分属不同挂载点）时 Go 会退化成「复制 + 删除」，
// 那期间的窗口里目标文件可能是不完整甚至不存在的 —— 服务正跑在上面，
// 断电或 SIGKILL 落在窗口里就留下一个半截的二进制，服务再也起不来。
// 调用方把临时目录建在可执行文件同目录（见 TempDir），这里再校验一次。
//
// # 为什么是「先备份再换」而不是「直接覆盖」
//
// 直接覆盖（rename 到已存在的目标）会丢掉旧文件，而新文件是否真的
// 能跑起来在替换的那一刻并不确定。保留备份让「回滚」成为一条
// 不需要网络的本地操作 —— 这正是它在出事时唯一还能用的原因。
//
// # 三步走，每步都可在中断后恢复
//
//  1. exePath → exePath.backup    （旧版本安全落袋）
//  2. newBinary → exePath          （新版本就位；失败则把备份换回来）
//  3. chmod 0755                   （确保可执行位）
//
// 第 2 步失败时会尝试把备份恢复回原位，并且**把恢复失败也报出来** ——
// 那种情况下服务目录里既没有可用二进制、备份还在，用户需要知道
// 具体该把哪个文件改回什么名字，而不是看到一句笼统的「替换失败」。
func ReplaceSelf(exePath, newBinary string) error {
	exePath = filepath.Clean(exePath)
	newBinary = filepath.Clean(newBinary)

	if _, err := os.Stat(newBinary); err != nil {
		return fmt.Errorf("待安装的文件不存在: %w", err)
	}
	// 同目录校验：不同目录意味着可能跨文件系统，rename 不再原子。
	// 允许目标是 exe 所在目录的**子目录** —— TempDir 就建在那里，同一棵
	// 目录树的成员在同一文件系统上，rename 依然是原子的。用 filepath.Rel
	// 而不是前缀比较：后者会把兄弟目录（/opt/llm-relay-evil）误判为在树内。
	exeDir := filepath.Dir(exePath)
	newDir := filepath.Dir(newBinary)
	if newDir != exeDir {
		if rel, err := filepath.Rel(exeDir, newDir); err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("待安装文件必须位于目标同目录（保证重命名是原子操作）：%s 不在 %s 下",
				newBinary, exeDir)
		}
	}
	if exePath == newBinary {
		return fmt.Errorf("源与目标相同，无需替换")
	}

	backupPath := BackupPath(exePath)

	// 清掉上一轮的备份。失败**不能**忽略。
	//
	// 原实现是 `_ = os.Remove(backupPath)`，注释里假设「rename 会覆盖它」。
	// 那个假设在 Linux 上成立，在 Windows 上不成立：上一轮的备份很可能
	// 正是**正在运行的那个映像**（第一次更新把运行中的 exe 改名成了备份，
	// 进程在重启之前一直从它运行）。映像节锁删除 —— os.Remove 被拒、
	// os.Rename 到同一个已存在的名字同样被拒。于是第二次更新会在这里
	// 失败，报出来却是「备份当前版本失败」，把排查引向「目录不可写」。
	//
	// 正确做法：删不掉就把它**挪到时间戳名下**让出正位。不能让备份换名字，
	// 因为 RestoreBackup 只认 exePath+".backup"，回滚目标必须留在正位上。
	if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
		if moved := moveAside(backupPath); moved == "" {
			return fmt.Errorf("清理上一轮的备份失败（%s）：%w。"+
				"它可能仍是正在运行的旧版本，重启服务后再试", filepath.Base(backupPath), err)
		}
	}

	// 第 1 步：旧版本转入备份位
	if err := os.Rename(exePath, backupPath); err != nil {
		return fmt.Errorf("备份当前版本失败（%s）：%w。可能原因：运行目录不可写", filepath.Base(exePath), err)
	}

	// 第 2 步：新版本就位
	if err := os.Rename(newBinary, exePath); err != nil {
		// 尽力把备份换回去，让服务至少能按旧版本继续跑
		if restoreErr := os.Rename(backupPath, exePath); restoreErr != nil {
			return fmt.Errorf("替换失败且恢复备份也失败：%w（恢复错误：%v）。"+
				"手动恢复方式：把 %s 重命名为 %s",
				err, restoreErr, backupPath, exePath)
		}
		return fmt.Errorf("替换失败，已恢复为原版本：%w", err)
	}

	// 第 3 步：确保可执行位。windows 上 Chmod 基本是空操作，忽略错误即可
	if runtime.GOOS != "windows" {
		if err := os.Chmod(exePath, 0o755); err != nil {
			// 这里不返回错误：文件已经就位了，权限问题可以人工修，
			// 而报错会让调用方以为整个替换失败，从而去做多余的补救
			return fmt.Errorf("替换成功但设置可执行权限失败: %w", err)
		}
	}
	return nil
}

// RestoreBackup 把备份文件换回原位（本地回滚，不需要联网）。
func RestoreBackup(exePath string) error {
	exePath = filepath.Clean(exePath)
	backupPath := BackupPath(exePath)

	if _, err := os.Stat(backupPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNoBackup
		}
		return fmt.Errorf("检查备份文件失败: %w", err)
	}

	// 把当前版本挪到一个临时名字而不是直接删掉：
	// 回滚本身也可能是个错误决定（比如回滚到了一个更老的、有已知问题的版本），
	// 留着它就能再换回来。命名为 .rollback-from 以便与 .backup 区分 ——
	// 后者始终表示「上一版」，是回滚的目标。
	//
	// 这里同样不能假设删得掉：.rollback-from 可能是**上一轮回滚留下的、
	// 仍在运行中的映像**（回滚后没重启，用户又点了一次回滚）。删不掉就
	// 挪到时间戳名下 —— 它已经是个待清理的残骸，不需要留在正位。
	cur := RollbackFromPath(exePath)
	if err := os.Remove(cur); err != nil && !os.IsNotExist(err) {
		if moved := moveAside(cur); moved == "" {
			return fmt.Errorf("暂存当前版本失败（%s 已存在且删不掉）: %w", filepath.Base(cur), err)
		}
	}
	if err := os.Rename(exePath, cur); err != nil {
		return fmt.Errorf("暂存当前版本失败: %w", err)
	}
	if err := os.Rename(backupPath, exePath); err != nil {
		// 换不回去就把当前版本放回原位，保持原状
		if backErr := os.Rename(cur, exePath); backErr != nil {
			return fmt.Errorf("回滚失败且无法复原：%w（复原错误：%v）", err, backErr)
		}
		return fmt.Errorf("回滚失败，已保持原状：%w", err)
	}
	return nil
}

// HasBackup 判断是否存在可回滚的备份。
func HasBackup(exePath string) bool {
	_, err := os.Stat(BackupPath(filepath.Clean(exePath)))
	return err == nil
}

// TempDir 在可执行文件**同目录**下建一个临时目录。
//
// 存在的唯一理由是保证 ReplaceSelf 里的 rename 不跨文件系统。
// 系统默认的 os.TempDir() 在多数发行版上是 tmpfs（/tmp），
// 与安装目录分属不同挂载点 —— 用它就是主动踩这个坑。
func TempDir(exePath string) (string, error) {
	dir := filepath.Dir(filepath.Clean(exePath))
	tmp, err := os.MkdirTemp(dir, ".llm-relay-update-*")
	if err != nil {
		return "", fmt.Errorf("在 %s 下创建临时目录失败（该目录需要可写）: %w", dir, err)
	}
	return tmp, nil
}
