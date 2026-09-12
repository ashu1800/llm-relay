// 模型身份的视觉标识（请求日志里那个模型胶囊的颜色来源）。
//
// 色相只由模型名决定：同一个模型名永远得到同一种颜色，不同模型名互相独立。
//
// 这里**不做**「按厂商家族取色」。那个方案看着更聪明，实测更糟：
// 家族锚定会把同一家的型号全塞进家族色相左右 ±28° 的小区间里，
// 散列再均匀也只有几十个格子 —— 实测量 deepseek-chat / deepseek-reasoner
// 会直接落到同一个色相上，而这正是这个功能要避免的结果。
// 日志里要一眼分辨的是「哪个模型」，所以色相铺满整个色相环。
//
// 设计约束（改这里时请守住）：
//   1. 颜色只在这一处定义 —— 换算法只改这个文件，组件里不写死颜色
//   2. 胶囊文字对底色的对比度不低于 4.5:1（WCAG AA 正文标准）
//   3. 不靠颜色单独区分 —— 胶囊里始终写着模型名，色盲用户与黑白打印
//      同样能分辨；颜色只负责「扫一眼看出换没换模型」
//
// 色相用 oklch 的 H 分量：oklch 感知均匀，同一个 L 在不同色相下看起来一样亮，
// 各个模型的文字对比度因此天然一致；换成 hsl 的话黄色会比蓝色亮一大截，
// 就没法用同一个 L 保证所有模型都达标。

import { hash32 } from './hash'

export interface ModelStyle {
  /** 展示名：就是模型名本身，不做缩写 */
  label: string
  /** oklch 色相角（0-360） */
  hue: number
  /** oklch 彩度；0 表示中性灰（拿不到模型名时用） */
  chroma: number
}

/** 未知模型的彩度：看得出色相，又不至于溢出 sRGB 色域 */
const FALLBACK_CHROMA = 0.12

/** 取模型样式。任何模型名都有样式，不会没有颜色。 */
export function modelStyle(name?: string | null): ModelStyle {
  const raw = (name || '').trim()
  if (!raw) {
    // 拿不到模型名时给中性灰：不占用任何色相，也就不会被误读成某个模型
    return { label: '未知', hue: 0, chroma: 0 }
  }

  // 大小写与命名空间前缀（openai/gpt-4o、models/gemini-1.5-pro）不参与取色：
  // 同一个模型换个写法必须还是同一种颜色
  const bare = raw.toLowerCase().split('/').pop()?.trim() || raw.toLowerCase()
  return { label: raw, hue: hash32(bare) % 360, chroma: FALLBACK_CHROMA }
}

/** 把样式转成可内联的 CSS 变量，颜色本身仍由 CSS 决定（便于适配深色主题）。 */
export function modelVars(s: ModelStyle): Record<string, string> {
  return { '--mt-h': String(Math.round(s.hue)), '--mt-c': String(s.chroma) }
}
