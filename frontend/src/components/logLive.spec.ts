// 实时插行的处置判据逐条钉住（2026-10-11 修「插行破坏排序」时立）。
//
// 这个判据坏过的表现是静默的：列表看着一切正常，只是用户刚点的「耗时降序」
// 不再成立 —— 新日志一律被插到最前面。而排序偏好是持久化的，
// 所以刷新页面后错序还在，用户只会觉得「这个排序坏了」。
// 四种输入组合 × 两种排序方向都测到，免得下次改守卫时漏掉一支。
import { describe, expect, it } from 'vitest'
import { decideLiveInsert, pendingLogText, type LiveInsertState } from './logLive'

/** 默认视角：第 1 页、无排序、无深链 —— 唯一该直接插的情形 */
const plain: LiveInsertState = { page: 1, sort: '', traceId: '', statusClass: '' }

describe('decideLiveInsert 处置判据', () => {
  it('默认视角直接插（这是列表存在的理由）', () => {
    expect(decideLiveInsert(plain)).toBe('insert')
  })

  it('有排序时不插，改为按当前视角静默重取 —— 无论升序还是降序', () => {
    for (const sort of ['elapsed', '-elapsed', 'cost', '-cost', 'status', '-status']) {
      expect(decideLiveInsert({ ...plain, sort })).toBe('reload')
    }
  })

  it('有排序时即使不在第 1 页也走重取（页码判断不能盖过取数条件）', () => {
    expect(decideLiveInsert({ ...plain, page: 3, sort: '-elapsed' })).toBe('reload')
  })

  it('排障深链走重取：新日志符不符合条件只有服务端知道', () => {
    expect(decideLiveInsert({ ...plain, traceId: 'abc123' })).toBe('reload')
    expect(decideLiveInsert({ ...plain, statusClass: 'error' })).toBe('reload')
  })

  it('第 1 页之外只计数：新行属于第 1 页，插进来会让当前页错位', () => {
    expect(decideLiveInsert({ ...plain, page: 2 })).toBe('count')
    expect(decideLiveInsert({ ...plain, page: 1260 })).toBe('count')
  })

  it('深链优先于页码：深链 + 第 3 页仍是重取', () => {
    expect(decideLiveInsert({ ...plain, page: 3, statusClass: 'error' })).toBe('reload')
  })

  it('空串深链等于没有条件（props 传下来的是空串而不是 undefined）', () => {
    expect(decideLiveInsert({ page: 1, sort: '', traceId: '', statusClass: '' })).toBe('insert')
  })
})

describe('pendingLogText 提示文案', () => {
  it('第 1 页不提示（新行已经直接进列表了）', () => {
    expect(pendingLogText(5, true)).toBe('')
  })

  it('没有攒下的新行不提示', () => {
    expect(pendingLogText(0, false)).toBe('')
  })

  it('第 1 页之外给出「回第 1 页」的下一步，而不只是一个数字', () => {
    expect(pendingLogText(3, false)).toBe('有 3 条新日志，回第 1 页查看')
  })
})
