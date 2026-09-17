// 请求日志「新日志入场动效」的档位表与持久化。
//
// 与 utils/persistedChoice.ts（筛选、每页条数）和 stores/theme.ts（主题）同一口径：
// 这是一个**浏览器本地偏好** —— 存 localStorage、不落库、不进备份文件、不改后端。
// 理由很直接：设置页上的运行参数全部只读、来自容器环境变量（页面自己写着
// 「与其做出改了不生效的假开关，不如直接告诉用户改哪里」），而入场动效是纯前端
// 观感，与主题、每页条数同性质，没有理由为它开一个后端写接口。
//
// 名字与说明**只在这里写一遍**：设置页的卡片、面板里的提示文案、契约脚本的
// 档位清单全部取自 LOG_FX_OPTIONS。分散在各处迟早会漂移成「设置页叫光晕脉动、
// 面板里叫呼吸光」这种对不上，而且不会报错。

export type LogFxId = 'sweep' | 'slide' | 'glow'

export type LogFxOption = {
  id: LogFxId
  /** 界面上显示的名字（设置页卡片标题、面板提示文案） */
  name: string
  /** 一句话说明：动的是什么、什么时候动 */
  desc: string
}

/**
 * 三档动效，顺序即设置页展示顺序。
 *
 * sweep 是 2026-09-16 就有的那道彩虹扫光，也是**默认档**：站主没有显式选过时
 * 一切照旧，谁也不该因为多了一个功能而发现自己的列表变了样。
 *
 * 2026-09-17 站主原本还想加一档「卡通猫趴在表头上扒拉、新日志从它爪子里抽出来」，
 * 做到一半决定不做了（形象与工作量都不划算），已经实现的猫相关代码整体拆掉，
 * 只留下面两档纯 CSS 的。要恢复的话 git 历史里有。
 *
 * 这里刻意没有「完全关闭」档：站主定清单时没有选它，而系统层面已经有统一的
 * 静音路径 —— theme.css 末尾那条 prefers-reduced-motion 会把全站动画压到
 * 0.01ms（理由与做法见那里的注释）。需要绝对安静的用户走那条，
 * 不必再多一个只对一块区域生效的开关。
 */
export const LOG_FX_OPTIONS: LogFxOption[] = [
  {
    id: 'sweep',
    name: '彩虹扫光',
    desc: '沿新行下沿从左扫过一道彩虹亮带，约 2 秒后从右端消失',
  },
  {
    id: 'slide',
    name: '自上滑入',
    desc: '新行从上方轻轻滑到位并淡入，像刚被摆上桌面',
  },
  {
    id: 'glow',
    name: '光晕脉动',
    desc: '新行泛起一层光晕，快速呼吸两下后褪去',
  },
]

/** 契约脚本与设置页都要用的 id 清单：从上面那张表派生，不另写一份 */
export const LOG_FX_IDS: LogFxId[] = LOG_FX_OPTIONS.map((o) => o.id)

export const DEFAULT_LOG_FX: LogFxId = 'sweep'

/** localStorage 键名（前缀由下面的 withPrefix 补，与全站 llm-relay- 前缀一致） */
const LOG_FX_KEY = 'log-fx'
const PREFIX = 'llm-relay-'

function withPrefix(key: string) {
  return PREFIX + key
}

/**
 * 读取档位。拿到不认识的值（旧版本留下的、用户手改的脏数据）一律回落默认档。
 *
 * 必须容错：面板是按这个值选样式块的，取到一个没有对应样式的 id 时不会报错，
 * 表现只是「这一步什么都不动」—— 而「新日志没有入场提示」正是这个功能要解决的
 * 问题，坏了还看不出来。
 */
export function readLogFx(): LogFxId {
  try {
    const raw = localStorage.getItem(withPrefix(LOG_FX_KEY))
    if (raw && (LOG_FX_IDS as string[]).includes(raw)) return raw as LogFxId
  } catch {
    // 隐私模式下 localStorage 会抛异常：退化成「不记住」，不该让看板打不开
  }
  return DEFAULT_LOG_FX
}

/** 写入档位。写失败只影响「下次还记不记得」，不影响本次切换 */
export function writeLogFx(id: LogFxId): void {
  try {
    localStorage.setItem(withPrefix(LOG_FX_KEY), id)
  } catch {
    /* 忽略：见 readLogFx 的说明 */
  }
}
