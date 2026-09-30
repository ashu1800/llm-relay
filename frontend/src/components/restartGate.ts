// 「重启之后什么时候可以刷新页面」的判据（2026-09-30 修 502 之后抽出来）。
//
// 事故：自动更新完成后点「立即重启」，页面立刻刷新，撞进重启窗口 ——
// 浏览器拿到的是一个 502 页面。根因是旧代码把「探活成功」直接当成可以刷新的信号：
//
//   · 后端是在**回完 /system/restart 响应之后**才退出进程的；
//   · 于是紧接着的那次 /healthz 探活探到的正是「还没退出的旧进程」，照样 200；
//   · 旧代码见 200 就 reload，而此刻 systemd 刚把新进程拉起来（RestartSec=3），
//     页面请求正好落在两代进程之间的空档里。
//
// 判据因此改成两条，缺一不可（倒计时由调用方的循环把守，见 VersionBadge.vue）：
//
//   1. 倒计时走完（8 秒）—— 旧进程早就退出、systemd 也把新进程拉起来了；
//   2. 探到的 uptime **小于**重启前那个值 —— 只认「新进程」，不认「还活着」。
//
// 第 2 条比第 1 条更硬：uptime 只增不减，旧进程无论活多久都比新进程大，
// 所以「更小」是「换过进程」的充分证据（同秒级的误差由 8 秒门槛兜住）。
export const RESTART_COUNTDOWN_MS = 8000

/** 探活读到的 uptime（毫秒）；null = 服务没探到，或后端没给这个字段 */
export type UptimeMs = number | null

/**
 * 现在能不能刷新页面。
 *
 * @param beforeMs 点「立即重启」之前那个进程的 uptime；null = 没读到
 *                 （那时只能靠倒计时兜底 —— 总比不刷新强）
 * @param uptimeMs 这次探活读到的 uptime；null = 服务还没起来
 */
export function canReloadAfterRestart(beforeMs: UptimeMs, uptimeMs: UptimeMs): boolean {
  if (uptimeMs === null) return false // 服务还没回来
  if (beforeMs === null) return true // 没有可比的基准，靠倒计时兜底
  return uptimeMs < beforeMs // 必须是新进程：旧进程还活着时 uptime 只会更大
}
