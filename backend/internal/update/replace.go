package update

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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
	// 同目录校验：不同目录意味着可能跨文件系统，rename 不再原子
	if filepath.Dir(exePath) != filepath.Dir(newBinary) {
		return fmt.Errorf("待安装文件必须位于目标同目录（保证重命名是原子操作）：%s 不在 %s 下",
			newBinary, filepath.Dir(exePath))
	}
	if exePath == newBinary {
		return fmt.Errorf("源与目标相同，无需替换")
	}

	backupPath := BackupPath(exePath)

	// 清掉上一轮的备份。失败不致命：可能是上一个进程留下的、
	// 权限不对，rename 会覆盖它（rename 到已存在文件是允许的）
	_ = os.Remove(backupPath)

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
	// 后者始终表示「上一版」，是回滚的目标
	cur := exePath + ".rollback-from"
	_ = os.Remove(cur)
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
