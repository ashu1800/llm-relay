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
export function fmtTime(t: string | number | Date | null | undefined): string {
  if (t === null || t === undefined || t === '') return '—'
  const d = t instanceof Date ? t : new Date(t)
  if (isNaN(d.getTime())) return String(t)
  const p = (n: number) => (n < 10 ? '0' + n : String(n))
  return (
    d.getFullYear() +
    '-' + p(d.getMonth() + 1) +
    '-' + p(d.getDate()) +
    ' ' + p(d.getHours()) +
    ':' + p(d.getMinutes()) +
    ':' + p(d.getSeconds())
  )
}
