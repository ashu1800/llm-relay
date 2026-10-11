// 行级操作后的焦点归还（2026-10-11）。
//
// 用 happy-dom 真建一小段 DOM 来测，而不是只测选择器字符串 —— 这段逻辑
// 唯一会出错的地方就是「查不到」与「查错了」，两者都只有在真实 DOM 上
// 才看得出来（选择器写错时 querySelector 返回 null 而不是报错）。
import { describe, expect, it, beforeEach } from 'vitest'
import { actionSelector, escapeAttr, focusRowAction, rowSelector } from './focusRow'

describe('选择器拼装', () => {
  it('行选择器用 data-row-key（antd 写在 <tr> 上的行身份）', () => {
    expect(rowSelector(42)).toBe('tr[data-row-key="42"]')
  })

  it('动作选择器是前缀匹配，不是全等', () => {
    expect(actionSelector('停用密钥 ')).toBe('button[aria-label^="停用密钥 "]')
  })

  it('转义反斜杠与双引号：rowKey 是外部数据，不假设它干净', () => {
    expect(escapeAttr('a"b')).toBe('a\\"b')
    expect(escapeAttr('a\\b')).toBe('a\\\\b')
    expect(rowSelector('x"y')).toBe('tr[data-row-key="x\\"y"]')
  })
})

describe('focusRowAction 焦点归还', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  function buildTable() {
    const wrap = document.createElement('div')
    wrap.setAttribute('tabindex', '-1')
    wrap.innerHTML = `
      <table>
        <tbody>
          <tr data-row-key="7">
            <td><button aria-label="编辑渠道 A">e</button></td>
            <td><button aria-label="删除渠道 A">d</button></td>
          </tr>
          <tr data-row-key="9">
            <td><button aria-label="编辑渠道 B">e</button></td>
            <td><button aria-label="删除渠道 B">d</button></td>
          </tr>
        </tbody>
      </table>`
    document.body.appendChild(wrap)
    return wrap
  }

  it('找到同一行的同一个动作并聚焦（不是同一行的别的动作）', () => {
    const wrap = buildTable()
    expect(focusRowAction(wrap, 9, '编辑渠道 ')).toBe(true)
    expect(document.activeElement?.getAttribute('aria-label')).toBe('编辑渠道 B')
  })

  it('重排之后仍按行身份找回（不依赖列位置）', () => {
    const wrap = buildTable()
    const tbody = wrap.querySelector('tbody')!
    // 把第 9 行挪到最前面，模拟一次重排
    tbody.insertBefore(tbody.querySelector('tr[data-row-key="9"]')!, tbody.firstChild)
    expect(focusRowAction(wrap, 9, '删除渠道 ')).toBe(true)
    expect(document.activeElement?.getAttribute('aria-label')).toBe('删除渠道 B')
  })

  it('行不在了（刚被删掉）时焦点落到容器兜底，而不是掉回 body', () => {
    const wrap = buildTable()
    expect(focusRowAction(wrap, 999, '删除渠道 ')).toBe(false)
    expect(document.activeElement).toBe(wrap)
  })

  it('scope 为空时安全返回 false（组件已卸载的竞态）', () => {
    expect(focusRowAction(null, 1, '删除渠道 ')).toBe(false)
  })
})
