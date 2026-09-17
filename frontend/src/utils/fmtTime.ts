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
