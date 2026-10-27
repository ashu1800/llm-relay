import { describe, expect, it } from 'vitest'
import {
  AUTO_HIDE_COLUMNS,
  autoHiddenColumns,
  mergeColumnVisibility,
  visibleColumnKeys
} from './logColumnAutoHide'
import { COL_KEYS, COL_LOCKED, type ColKey } from './logColumns'

// 窄窗口按优先级自动隐藏三列（2026-10-27 需求）。
//
// 这条判据的第一个版本把阈值写成固定数字 1400/1600/1800，实测暴露了两个
// 真实问题（站主反馈「隐藏没问题，显示有点问题」）：
//   1. 11 列声明宽合计只有约 1190px，容器 1300px 时明明全装得下，
//      却白白藏掉三列；
//   2. 窗口变宽时列宽先被拉伸、该恢复的列却没回来。
// 下面这些用例就是钉住这两件事不再发生。

/** 默认列宽（与 RequestLogPanel 的 baseW 初值一致） */
const W: Record<ColKey, number> = {
  time: 96,
  model: 190,
  thinking: 62,
  channel: 88,
  tokens: 150,
  elapsed: 120,
  cost: 90,
  speed: 118,
  status: 64,
  key: 100,
  client: 112
}
const TOTAL = Object.values(W).reduce((a, b) => a + b, 0)

describe('autoHiddenColumns 用真实几何而不是固定阈值', () => {
  it('全列装得下时一列都不藏（哪怕只多 1px）', () => {
    // 这条正是第一版的主要 bug：容器 1300px 装得下 1190px 的合计，
    // 但固定阈值 1400 让它藏掉三列
    expect(autoHiddenColumns(TOTAL, W).size).toBe(0)
    expect(autoHiddenColumns(TOTAL + 1, W).size).toBe(0)
    expect(autoHiddenColumns(1300, W).size).toBe(0)
    expect(autoHiddenColumns(2400, W).size).toBe(0)
  })

  it('少 1px 装不下：只收一列就够（先收密钥）', () => {
    const hidden = autoHiddenColumns(TOTAL - 1, W)
    expect([...hidden]).toEqual(['key'])
  })

  it('逐步变窄时逐列收，而不是一次收光', () => {
    // 用「收掉 k 列后刚好装得下」的宽度来驱动，避免依赖具体的合计数字
    // （思考列宽度改过一次，写死宽度减固定值的写法当场失效）
    const widthFor = (hiddenCount: number) => {
      const hidden = AUTO_HIDE_COLUMNS.slice(0, hiddenCount)
      const total = (Object.keys(W) as ColKey[])
        .filter((k) => !hidden.includes(k))
        .reduce((s, k) => s + W[k], 0)
      return total // 恰好装下
    }

    // 收 1 列就能装下的宽度 → 只收 key
    expect([...autoHiddenColumns(widthFor(1), W)]).toEqual(['key'])
    // 收 2 列 → key + status
    expect([...autoHiddenColumns(widthFor(2), W)]).toEqual(['key', 'status'])
    // 收 3 列 → 三列全收
    expect([...autoHiddenColumns(widthFor(3), W)]).toEqual(['key', 'status', 'client'])
    // 再窄也不会收更多（没有第四列可收）
    expect([...autoHiddenColumns(widthFor(3) - 500, W)]).toEqual(['key', 'status', 'client'])
  })

  it('收无可收时不再报错（容器比最简配置还窄）', () => {
    const hidden = autoHiddenColumns(100, W)
    expect(hidden.size).toBe(3) // 三列全收，但不会试图收别的
  })

  it('宽度未知（0 / 负数 / NaN）时不藏任何列', () => {
    // 首帧还没量到容器宽：闪一下「缺三列」再补回来比晚一点藏更难受
    expect(autoHiddenColumns(0, W).size).toBe(0)
    expect(autoHiddenColumns(-100, W).size).toBe(0)
    expect(autoHiddenColumns(NaN, W).size).toBe(0)
  })

  it('列宽表为空（还没测量）时不藏任何列', () => {
    expect(autoHiddenColumns(800, {}).size).toBe(0)
  })

  it('列宽变了判据跟着变（不再是写死的数字）', () => {
    // 模型列内容变长后，更早开始收列 —— 这正是「阈值算出来」的意义
    const wide = { ...W, model: 400 }
    const wideTotal = Object.values(wide).reduce((a, b) => a + b, 0)
    // 同样的容器宽度，列变宽后要开始收了
    expect(autoHiddenColumns(TOTAL, wide).size).toBeGreaterThan(0)
    expect(autoHiddenColumns(wideTotal, wide).size).toBe(0)
  })

  it('隐藏顺序是 密钥 → 状态 → 客户端（站主指定）', () => {
    expect([...AUTO_HIDE_COLUMNS]).toEqual(['key', 'status', 'client'])
  })

  it('恢复顺序与隐藏顺序相反：客户端 → 状态 → 密钥', () => {
    // 需求原话「恢复时反着来」：最后被收掉的最先回来。
    // 用「收掉 k 列刚好装得下」的宽度验证：宽一点时最先回来的是最后收的那列。
    const widthFor = (hiddenCount: number) => {
      const hidden = AUTO_HIDE_COLUMNS.slice(0, hiddenCount)
      return (Object.keys(W) as ColKey[])
        .filter((k) => !hidden.includes(k))
        .reduce((s, k) => s + W[k], 0)
    }
    // 收 3 列的宽度：三列都藏
    expect(autoHiddenColumns(widthFor(3), W).size).toBe(3)
    // 收 2 列的宽度：client 回来了（它是最后被收的）
    const two = autoHiddenColumns(widthFor(2), W)
    expect(two.has('client')).toBe(false)
    expect(two.has('status')).toBe(true)
    // 收 1 列的宽度：client + status 都回来了
    const one = autoHiddenColumns(widthFor(1), W)
    expect(one.has('client')).toBe(false)
    expect(one.has('status')).toBe(false)
    expect(one.has('key')).toBe(true)
    // 全装得下：key 也回来
    expect(autoHiddenColumns(widthFor(0), W).size).toBe(0)
  })

  it('只要某个被藏的列还能塞回来，就不该有余量在分摊', () => {
    // 核心诉求「先恢复列、最后再拉伸列宽」，准确表述是：
    //   若还有被藏的列能加回（加回后仍装得下），那一定不该在分摊余量。
    // 反过来「藏着列就没有余量」是不成立的：三列全收完之后若仍有剩余空间，
    // 那只能摊给剩下的 8 列 —— 此时没有列可恢复，摊余量才是对的。
    for (let width = 900; width <= TOTAL; width += 1) {
      const h = autoHiddenColumns(width, W)
      const vis = (Object.keys(W) as ColKey[]).filter((k) => !h.has(k))
      const decl = vis.reduce((s, k) => s + W[k], 0)
      const leftover = Math.max(0, width - decl)
      if (h.size === 0) continue
      // 下一个该恢复的列（最后被收掉的那个）
      const nextBack = [...h][h.size - 1] as ColKey
      const canRestore = decl + W[nextBack] <= width
      expect(
        !canRestore || leftover < 2,
        `宽度 ${width}: 藏 ${[...h].join(',')}，但加回 ${nextBack} 后 ${decl + W[nextBack]} ≤ ${width} 仍装得下，却在分摊 ${leftover}px 余量`
      ).toBe(true)
    }
  })


})

describe('mergeColumnVisibility 与用户手动勾选取并集', () => {
  it('宽屏且用户没关任何列 → 全显示', () => {
    const v = mergeColumnVisibility([], 2400, COL_KEYS, W)
    expect(Object.values(v).every(Boolean)).toBe(true)
  })

  it('窄屏自动隐藏的列被隐藏', () => {
    const v = mergeColumnVisibility([], 600, COL_KEYS, W)
    expect(v.client).toBe(false)
    expect(v.key).toBe(false)
    expect(v.status).toBe(false)
    expect(v.model).toBe(true)
  })

  it('用户手动关掉的列，窗口变宽也不会自己回来', () => {
    // 「并集」的关键语义：自动那一份是临时判断，用户的选择优先保留
    const v = mergeColumnVisibility(['speed'], 2400, COL_KEYS, W)
    expect(v.speed).toBe(false)
    expect(v.status).toBe(true)
  })

  it('用户关掉的列不占宽，因此不会因它而多收一列', () => {
    // 全列合计 1190px。容器 1080px 时装不下：
    //   未手动关列 → 先收 key（腾出 100）→ 1090 仍 > 1080 → 再收 status → 1026 够
    //   用户已关 client（少 112）→ 1078 ≤ 1080 → 一列都不用再收
    const width = 1080
    const autoOff = mergeColumnVisibility([], width, COL_KEYS, W)
    expect(autoOff.key).toBe(false)
    expect(autoOff.status).toBe(false)

    const userOff = mergeColumnVisibility(['client'], width, COL_KEYS, W)
    expect(userOff.key).toBe(true) // 合计已够，不该再收
    expect(userOff.status).toBe(true)
  })

  it('锁定列不会被自动隐藏误伤', () => {
    const locked = COL_LOCKED as readonly string[]
    for (const c of AUTO_HIDE_COLUMNS) {
      expect(locked.includes(c)).toBe(false)
    }
  })
})

describe('visibleColumnKeys', () => {
  it('按 COL_KEYS 的顺序返回可见列（顺序即渲染顺序）', () => {
    const v = mergeColumnVisibility([], 2400, COL_KEYS, W)
    expect(visibleColumnKeys(v, COL_KEYS)).toEqual([...COL_KEYS])
  })

  it('窄屏收掉三列后，其余顺序不变', () => {
    const v = mergeColumnVisibility([], 600, COL_KEYS, W)
    const keys = visibleColumnKeys(v, COL_KEYS)
    expect(keys).not.toContain('client')
    expect(keys).not.toContain('key')
    expect(keys).not.toContain('status')
    const expected = (COL_KEYS as readonly ColKey[]).filter(
      (k) => k !== 'client' && k !== 'key' && k !== 'status'
    )
    expect(keys).toEqual(expected)
  })
})
