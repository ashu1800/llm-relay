// 请求日志「新日志入场动效」的档位表与持久化。
//
// 与 utils/persistedChoice.ts（筛选、每页条数）和 stores/theme.ts（主题）同一口径：
// 这是一个**浏览器本地偏好** —— 存 localStorage、不落库、不进备份文件、不改后端。
// 理由很直接：设置页上的运行参数全部只读、来自部署环境变量（页面自己写着
// 「与其做出改了不生效的假开关，不如直接告诉用户改哪里」），而入场动效是纯前端
// 观感，与主题、每页条数同性质，没有理由为它开一个后端写接口。
//
// 名字与说明**只在这里写一遍**：设置页的卡片、面板里的提示文案、契约脚本的
// 档位清单全部取自 LOG_FX_OPTIONS。分散在各处迟早会漂移成「设置页叫光晕脉动、
// 面板里叫呼吸光」这种对不上，而且不会报错。

export type LogFxId = 'sweep' | 'pulse' | 'stardust'

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
 * pulse（双星对撞）与 stardust（星尘上浮）是 2026-09-18 站主嫌原来的
 * 「自上滑入」「光晕脉动」太一般之后换上的两档，全部画在效果层里
 * （不碰任何单元格的样式，布局契约因此天然满足）。
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
    id: 'pulse',
    name: '双星对撞',
    desc: '行中央迸出十粒火星，两道火红光带沿行底边奔向两端，端点炸开光斑，约 1.3 秒收束',
  },
  {
    id: 'stardust',
    name: '星尘上浮',
    desc: '十四粒星尘从新行错落升起、上浮飘散，白心亮核、四色流转，轻柔不吵',
  },
]

/** 契约脚本与设置页都要用的 id 清单：从上面那张表派生，不另写一份 */
export const LOG_FX_IDS: LogFxId[] = LOG_FX_OPTIONS.map((o) => o.id)

export const DEFAULT_LOG_FX: LogFxId = 'sweep'

/**
 * 旧档位到新档位的映射：2026-09-18 换血后 slide/glow 两个 id 从档位表里
 * 消失了，但存了旧值的浏览器不应该被静默打回默认档 —— 选过「自上滑入」的
 * 显然偏好「有点动作」的效果，把他迁到气质最接近的新档上，选择被尊重。
 */
const LEGACY_ALIAS: Record<string, LogFxId> = {
  slide: 'pulse',
  glow: 'stardust',
}

/** localStorage 键名（前缀由下面的 withPrefix 补，与全站 llm-relay- 前缀一致） */
const LOG_FX_KEY = 'log-fx'
const PREFIX = 'llm-relay-'

function withPrefix(key: string) {
  return PREFIX + key
}

/**
 * 读取档位。旧档位按上面的映射迁移；真正不认识的值（手改的脏数据）回落默认档。
 *
 * 必须容错：面板是按这个值选样式块的，取到一个没有对应样式的 id 时不会报错，
 * 表现只是「这一步什么都不动」—— 而「新日志没有入场提示」正是这个功能要解决的
 * 问题，坏了还看不出来。
 */
export function readLogFx(): LogFxId {
  try {
    const raw = localStorage.getItem(withPrefix(LOG_FX_KEY))
    if (raw && (LOG_FX_IDS as string[]).includes(raw)) return raw as LogFxId
    if (raw && LEGACY_ALIAS[raw]) return LEGACY_ALIAS[raw]
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
