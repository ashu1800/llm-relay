import { describe, expect, it } from 'vitest'
import { CELL_BREATH, CELL_PAD } from './logColumns'

// 思考列的下限必须放得下最长的档位词（2026-10-27 实测踩坑）。
//
// 踩到的是什么：去掉胶囊改纯文字那次，我把下限从 88 改到 62，
// 算式里只算「文字 + 内边距 16」，**漏了 CELL_BREATH(6)**，而文字宽又按
// 6.5px/字符 低估。"medium" 实际需要约 63px，62 差 1px —— 页面上的表现
// 是格子里折成两行："mediu" / "m"（站主截图反馈「内容换行显示了」）。
//
// 这条用例把「下限 ≥ 最长档位词所需宽度」钉住。改下限、改字体、加档位词
// 时它会失败，而不是等用户看截图才发现。

/** backend/internal/relay/thinking.go 里的全部档位词 */
const THINKING_LEVELS = ['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'on', 'auto', 'max']

/** 思考列在 RequestLogPanel 的 COL_BOUNDS 里 */
const THINKING_BOUNDS: [number, number] = [72, 84]
/** 思考列的 CONTENT_MEASURE.pad（去掉胶囊后是 0） */
const THINKING_PAD = 0

/**
 * 12px 正文字体下英文小写字母的保守字宽。
 *
 * 为什么用 6.8 而不是实测的 ~6.2：估算必须**偏大**才安全 —— 偏小会让下限
 * 显得够用而实际折行，正是这次的 bug。宁可多留 2px 空白，也不要一个词
 * 折成两行（那是错的信息，不只是难看）。
 */
const CHAR_W = 6.8

function widthNeeded(word: string): number {
  return word.length * CHAR_W + THINKING_PAD + CELL_PAD + CELL_BREATH
}

describe('思考列下限放得下最长的档位词', () => {
  it('每个档位词都不超过下限（不会因为列太窄而折行）', () => {
    const [lo] = THINKING_BOUNDS
    for (const lv of THINKING_LEVELS) {
      const need = widthNeeded(lv)
      expect(need, `"${lv}" 需要 ${need.toFixed(1)}px，下限只有 ${lo}px`).toBeLessThanOrEqual(lo)
    }
  })

  it('下限单独拿出来就放得下最长档位词（不靠测量兜底）', () => {
    // 这条与上一条的区别很关键：原来用 clampColumnWidth(need) 验证，
    // 而 clamp 里是 max(lo, need) —— 下限小的时候 need 会兜底，测试永远绿。
    // 真正要钉的是「下限本身」够：测量要等首帧渲染完才跑，且它量的
    // getBoundingClientRect 是**已经折行后**的盒子（折行后反而更窄），
    // 下限才是折行与否的最后保障。
    const [lo] = THINKING_BOUNDS
    const widest = THINKING_LEVELS.reduce((a, b) => (widthNeeded(a) >= widthNeeded(b) ? a : b))
    expect(
      widthNeeded(widest),
      `最长的档位词是 "${widest}"，需要 ${widthNeeded(widest).toFixed(1)}px，下限 ${lo}px`
    ).toBeLessThanOrEqual(lo)
  })

  it('上限不会被档位词撑爆（内容是封闭集合）', () => {
    const [, hi] = THINKING_BOUNDS
    // 最长的档位词也不需要超过上限 —— 内容固定，再宽只是空白
    expect(widthNeeded('minimal')).toBeLessThanOrEqual(hi)
  })

  it('回归钉：下限 62 那种算法会漏掉 CELL_BREATH', () => {
    // 直接复现当时的错误算式：只算文字 + CELL_PAD，漏了 BREATH
    const wrong = 'medium'.length * 6.5 + CELL_PAD // = 39 + 16 = 55
    // 正确算式比它大 6（BREATH）+ 字宽差
    const right = widthNeeded('medium')
    expect(right).toBeGreaterThan(wrong)
    // 且正确答案已经超过 62 —— 说明那个下限确实差了一点
    expect(right).toBeGreaterThan(62)
  })
})
