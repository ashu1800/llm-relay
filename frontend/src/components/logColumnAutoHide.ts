// 窄窗口下按优先级自动隐藏的列（2026-10-27）。
//
// 需求：状态 / 密钥 / 客户端这三列随窗口宽度变化 —— 装不下时按顺序依次隐藏，
// 宽回来再依次恢复。
//
// 为什么做成纯函数：判据（哪个宽度藏哪几列）与「怎么与用户手动勾选共存」
// 都是容易出错的逻辑，写在组件里就只能靠肉眼验证。本仓库的固定做法是
// 判据抽成 components/*.ts + 单测（见 CLAUDE.md 硬约定第 10 条）。
//
// ---- 顺序：藏从右往左，恢复从左往右 ----
//
// 需求给的是「状态，密钥，客户端」这个列举顺序，但**隐藏顺序必须反过来**：
// 列表里越靠后的列越先让位，否则收掉的是表格中段的列，右边那几列会突然
// 贴到左边来，读起来像行错位。而这三列在表里的实际位置是
// 状态 → 密钥 → 客户端（见 logColumns.COL_KEYS），所以：
//   藏：先 client，再 key，最后 status（从右往左收）
//   恢复：先 status，再 key，最后 client（从左往右还）
//
// ---- 阈值必须是「算出来的」，不是拍出来的 ----
//
// 第一版把阈值写成固定数字 1400/1600/1800，实测立刻暴露两个问题
// （站主 2026-10-27 反馈「隐藏没问题，显示有点问题」）：
//   1. 11 列声明宽合计只有约 1190px，而阈值说「低于 1400 藏三列」——
//      容器 1300px 时明明全装得下，却白白藏掉三列，浪费 300 多 px；
//   2. 更糟的是顺序反了：窗口变宽时列宽已经开始被分摊拉伸，而该恢复的列
//      还没回来 —— 用户看到的是「列没回来，但每列变宽了」，正是他说的
//      「应该先恢复显示隐藏的列，最后再动态调整列宽」。
//
// 现在的判据是**真实几何**：从「全显示」出发，若声明宽合计装不下容器，
// 就按顺序一列一列收，直到装得下。这样自然满足「先恢复列、后拉伸」——
// 分摊（distribute）只在「合计已经装得下」时才有多余宽度可分，
// 而那时所有该显示的列都已经回来了。
//
// 于是阈值不再是一组魔数，而是由列宽表算出来的：列宽改了（加列、改下限、
// 改内容测量）判据自动跟着变，不会再出现「数字与现实脱节」。
import type { ColKey } from '@/components/logColumns'

/** 响应式自动隐藏的三列，按「隐藏顺序」排列。
 *
 * 顺序（2026-10-27 站主指定）：密钥 → 状态 → 客户端。
 * 恢复顺序自动相反：客户端 → 状态 → 密钥（见 autoHiddenColumns 的
 * 「从全显示往下收」判据 —— 最后被收掉的就是最先回来的）。
 *
 * 为什么顺序要显式定义而不是按列位置（从右往左收）：
 * 本文件第一版是按「越靠右越先收」写的（客户端最先），站主实测后指出
 * 他要的是另一种优先级 —— 密钥（谁在调）最先让位，
 * 客户端（哪家客户端）反而留到最后。列位置与被收顺序没有必然关系，
 * 所以这里写死一份清单，不再从 COL_KEYS 的位置推导。
 */
export const AUTO_HIDE_COLUMNS: readonly ColKey[] = [
  // 先收：密钥。它是最长的三列之一（100~148px），腾出的宽度最多
  'key',
  // 再收：状态。一枚短徽标，但它回答「这次成不成」，比客户端更需要留住
  'status',
  // 最后收：客户端。留到最后才让位
  'client'
]

/**
 * 按容器宽度算出「应该被自动隐藏」的列。
 *
 * @param width 表格容器的可用宽度（px）。0 / 负数 / NaN 视为未知 ——
 *              返回空集合（不隐藏任何列）：首帧还没量到容器宽时应当
 *              **全显示**，而不是先闪一下「缺三列」再补回来。
 * @param cols  各列**当前声明宽**（内容紧宽，不含余量分摊），只需包含
 *              候选列与需要参与合计的列。传当前值而不是初始常量：
 *              列宽是量出来的（见 CONTENT_MEASURE），用旧值会让判据与实际
 *              渲染脱节。
 * @returns 被自动隐藏的列集合
 *
 * 判据：从「全显示」出发，若合计装不下，就按 AUTO_HIDE_COLUMNS 的顺序
 * 一列一列收，直到装得下（或收无可收）。
 *
 * 为什么从全显示**往下收**而不是从空往上加：需求是「装不下才藏」——
 * 只要装得下就该全显示，哪怕只差 1px。
 */
export function autoHiddenColumns(
  width: number,
  cols: Partial<Record<ColKey, number>> = {}
): Set<ColKey> {
  const out = new Set<ColKey>()
  if (!Number.isFinite(width) || width <= 0) return out

  const keys = Object.keys(cols) as ColKey[]
  if (!keys.length) return out
  let total = keys.reduce((s, k) => s + (cols[k] ?? 0), 0)
  if (total <= width) return out

  for (const key of AUTO_HIDE_COLUMNS) {
    if (total <= width) break
    // 只收「参与合计」的列：没传进来的列（用户已手动关掉的）不在 total 里，
    // 收它不会腾出宽度，跳过继续看下一个
    if (!(key in cols)) continue
    const w = cols[key] ?? 0
    if (w <= 0) continue
    out.add(key)
    total -= w
  }
  return out
}

/**
 * 把「用户手动隐藏」与「宽度自动隐藏」合成最终的列显隐。
 *
 * @param hiddenByUser 用户在列设置里关掉的列（持久化）
 * @param width        容器可用宽度
 * @param keys         全部列（顺序即表格顺序）
 * @param cols         各列声明宽（用于算装不装得下）
 * @returns 每列是否可见
 */
export function mergeColumnVisibility<K extends string>(
  hiddenByUser: readonly string[],
  width: number,
  keys: readonly K[],
  cols: Partial<Record<ColKey, number>> = {}
): Record<K, boolean> {
  // 只把「用户还显示着」的列交给宽度判据：他已关掉的列本来就不占宽，
  // 不该因为它们的存在而多收一列
  const userVisibleW: Partial<Record<ColKey, number>> = {}
  for (const k of keys) {
    if (!hiddenByUser.includes(k)) userVisibleW[k as ColKey] = cols[k as ColKey] ?? 0
  }
  const auto = autoHiddenColumns(width, userVisibleW)
  const out = {} as Record<K, boolean>
  for (const k of keys) {
    out[k] = !hiddenByUser.includes(k) && !auto.has(k as ColKey)
  }
  return out
}

/** 可见列（顺序与 keys 一致）—— 列宽合计与余量分摊都按它遍历。 */
export function visibleColumnKeys<K extends string>(
  visible: Record<K, boolean>,
  keys: readonly K[]
): K[] {
  return keys.filter((k) => visible[k])
}
