// 全站统一的本地时间格式：YYYY-MM-DD HH:mm:ss（手工补零，空值 '—'）。
//
// 为什么不用 toLocaleString('zh-CN')：出来的是 2026/9/12 13:06:02 ——
// 斜杠分隔、月日不补零，同一列里宽度还会随月份变化而抖动，
// 且受运行环境区域设置影响（别的区域下变成 09/14/2026）。
// 手工补零是为了不受任何区域设置影响。
//
// 原来有四份实现各写一套（日志列表这份最完整，密钥/设置/渠道页还是
// toLocaleString，代理页又是 YYYY/MM/DD HH:mm）—— 同一个「最后使用时间」
// 在不同页面是两种长相。这里收拢成单一出口。

// pad2 单独导出：补零不止时间戳在用（时区偏移量、月-日相对时间等），
// 别再各写一份内联的了。
export function pad2(n: number): string {
  return n < 10 ? '0' + n : String(n)
}

export function fmtTime(t: string | null | undefined): string {
  if (t === null || t === undefined || t === '') return '—'
  const d = new Date(t)
  if (isNaN(d.getTime())) return t
  return (
    d.getFullYear() +
    '-' + pad2(d.getMonth() + 1) +
    '-' + pad2(d.getDate()) +
    ' ' + pad2(d.getHours()) +
    ':' + pad2(d.getMinutes()) +
    ':' + pad2(d.getSeconds())
  )
}

/**
 * 日志列表用的紧凑时间：**今天只显示时分秒**，省掉天天重复的那半截日期。
 *
 * 为什么要有这个函数（2026-09-24 UI 审评）：
 * 「请求时间」列宽 155px，是这张表里最宽的一列，也是横向滚动的最大单一贡献者
 * （10 列下限合计 1111px > 笔记本常见的可视宽度）。而「今天」这一档下，
 * 一屏 50 行的日期部分**逐行完全相同** —— 「2026-09-24 」十个字符，
 * 占了这一列近一半的宽度，一个字节的信息量都没有。
 * 省掉它之后这一列可以收到 90px 上下，在 1440/1280 上基本不再触发横向滚动。
 *
 * 分档：
 *   今天 → HH:mm:ss
 *   昨天 → 昨天 HH:mm:ss
 *   今年 → MM-DD HH:mm:ss
 *   更早 → 完整 YYYY-MM-DD HH:mm:ss（跨年时年份才是有效信息）
 * 完整时间始终保留在单元格的 title 上 —— 需要精确到年的场合（对账、
 * 报障截图）悬停就能拿到，不用为了那种低频需求让 50 行都背上年月日。
 */
export function fmtTimeCompact(t: string | null | undefined, now: Date = new Date()): string {
  if (t === null || t === undefined || t === '') return '—'
  const d = new Date(t)
  if (isNaN(d.getTime())) return t
  const hm = pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds())
  // 按「自然日」比较，不按 24 小时差：凌晨 00:30 看 23:50 的记录该说「昨天」，
  // 说「今天」会让人以为是自己刚发的
  const day = (x: Date) => x.getFullYear() * 10000 + (x.getMonth() + 1) * 100 + x.getDate()
  const gap = day(now) - day(d)
  if (gap === 0) return hm
  if (gap === 1) return '昨天 ' + hm
  if (d.getFullYear() === now.getFullYear()) return pad2(d.getMonth() + 1) + '-' + pad2(d.getDate()) + ' ' + hm
  return fmtTime(t)
}
