# 「立即重启」后刷新时机：为什么不能一探活成功就刷新（2026-09-30）

> 站主反馈：「自动更新完成后点『立即重启』，不应该倒计时 8 秒后自动刷新网页吗？
> 现在是点完立即刷新，页面 502。」

## 一、根因

旧代码把「`/healthz` 探活成功」直接当成可以刷新页面的信号：

```js
// 旧实现（已改）
for (let i = 0; i < 15; i++) {
  const res = await fetch('/healthz')   // ← 第一次探活几乎必然成功
  if (res.ok) { window.location.reload(); return }
  await sleep(2000)
}
```

而 `/system/restart` 是**回完响应之后**进程才退出（`backend/internal/api`：先写响应，
再由调用方触发退出），所以紧接着的第一次探活探到的是**还没退出的旧进程** ——
`/healthz` 照样 200。页面于是在两代进程之间的空档里刷新：systemd 还在
`RestartSec=3` 的等待里（`deploy/install.sh`），新进程没起来 → 浏览器拿到 502 页面。
倒计时那 8 秒当时只是「最长还要等多久」的展示，没有把守任何东西。

## 二、判据改成两条（缺一不可）

1. **倒计时走完（8 秒）**：旧进程退出 + `RestartSec=3` + 新进程监听，都在这个窗口内；
   倒计时结束之前**一次都不探**，更不刷新。
2. **探到的 uptime 小于重启前那个值**：`uptime` 只增不减，旧进程无论活多久都比新进程大，
   所以「更小」是「换过进程」的充分证据。

判据抽成 `frontend/src/components/restartGate.ts` 的 `canReloadAfterRestart(beforeMs, uptimeMs)`
（纯函数，用例见 `restartGate.spec.ts`，其中「旧进程还活着 → 不能刷新」那一格就是这个事故的回归）。
为此 `/healthz` 增加了一个机器可读字段：

```json
{ "status": "ok", "uptime": "3d0h0m0s", "uptime_ms": 259200000 }
```

`uptime` 保持原样（给人看），`uptime_ms` 给程序用 —— 让前端去解析 `1h2m3s` 既啰嗦又容易错。

## 三、行为

| 时刻 | 界面 | 页面 |
|---|---|---|
| 点击后 0~8s | 「正在重启…（7s→1s）」，按钮禁用 | 不探活、不刷新 |
| 倒计时结束、新进程已起 | 「正在重启…」 | 立即刷新 |
| 倒计时结束、旧进程仍在（重启没生效） | 「正在重启…」 | 不刷新；30 秒后提示「可能重启没有生效，请检查服务状态」 |

超时文案也一并改了：旧文案是「服务在 30 秒内没有恢复响应」，而这一支里服务可能
**一直**在响应（只是没换进程），照旧文案会把人往「服务挂了」的方向带。

## 四、验证

- 单测：`frontend/src/components/restartGate.spec.ts`（6 例）、
  `backend/internal/api/admin_system_test.go` 的 `TestHealthzExposesUptimeMs`。
- 浏览器（桩数据模拟两个阶段）：
  - 阶段 A 旧进程还在（`uptime_ms` 持续增长）：倒计时 8→1 秒走完，11 秒内导航次数 **0**，
    页面标记仍在 —— 旧代码在这里就会刷新；
  - 阶段 B 把 `uptime_ms` 换成 1200：3 秒内完成刷新（导航 1 次）。
