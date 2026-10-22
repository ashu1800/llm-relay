package api

import (
	"runtime"
	"testing"
)

// restartExitCode 是「进程退出、让托管方把自己拉起来」时的退出码。
//
// 两个平台的判据完全不同，而写错的表现都是「点了重启，服务再也没回来」：
//
//   - systemd 的 Restart=always 只看「进程退出」这件事，不看退出码；
//     但 OnFailure= 之类是按非零码触发的，正常重启不该把自己的
//     退出记录成一次失败。所以 Linux 用 0。
//   - Windows SCM 的恢复动作（sc failure 配的 restart）**只对非零退出码生效**；
//     exit(0) 被 SCM 视为「服务正常停止」，恢复策略不会触发，
//     服务就永远停在那儿。所以 Windows 必须用非零码。
func TestRestartExitCode(t *testing.T) {
	got := restartExitCode()

	if runtime.GOOS == "windows" {
		if got == 0 {
			t.Fatal("Windows 上必须是非零退出码，否则 SCM 的恢复动作不会触发，重启后服务不再起来")
		}
		return
	}
	if got != 0 {
		t.Fatalf("非 Windows 平台应由 systemd 的 Restart=always 拉起，退出码应为 0，实际: %d", got)
	}
}
