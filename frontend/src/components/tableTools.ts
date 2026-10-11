// 四张管理表的本地搜索 / 分页判据（纯函数，有单测）。
//
// 为什么是客户端做：/channels、/keys、/groups、/proxies 四个列表接口都是
// **一次返回全量**（没有 page / page_size 参数，密钥那条接口的 total 就是
// len(items)）—— 这是有意的，它们的数据量级是「几十条到几百条」，
// 而后端加了分页也不会让任何一次请求变快（一次全表扫描 vs 一次带 offset 的
// 全表扫描）。但界面上不处理的话，一屏几十行仍然要滚，而「按名字找一个渠道」
// 在 200 个渠道里靠眼睛扫是最慢的方式。
//
// 判据抽成纯函数而不是直接写在组件里：分页最容易错的两处（搜索后没回第一页、
// 数据变少后当前页越界）都是「只在某些组合下才出现」的，用组件测要挂载、
// 要造 DOM；抽出来就是几行断言。

/**
 * 一条搜索串是否命中一段文本。
 *
 * 按**空白拆成多个词**，全部命中才算命中（AND）。这样「gpt key1」能筛出
 * 「key1 的 gpt 请求」这类跨字段组合，而不用去猜用户想在哪个字段里搜。
 * 大小写不敏感 —— 渠道名、模型名、密钥名在站里三种大小写都出现过。
 */
export function matchQuery(text: string, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  const hay = (text || '').toLowerCase()
  return q.split(/\s+/).every((term) => hay.includes(term))
}

/** 当前页是否已经越界（搜索后行数变少、或删掉了本页最后一条） */
export function clampPage(page: number, totalRows: number, pageSize: number): number {
  const size = Math.max(1, pageSize)
  const last = Math.max(1, Math.ceil(totalRows / size))
  if (page < 1) return 1
  return page > last ? last : page
}

/** 取某一页的数据。page 已保证合法（调用方先过 clampPage） */
export function pageSlice<T>(list: readonly T[], page: number, pageSize: number): T[] {
  const size = Math.max(1, pageSize)
  const start = (Math.max(1, page) - 1) * size
  return list.slice(start, start + size)
}

/**
 * 分页器要不要显示。
 *
 * 只有一页时不显示：这几张表的默认数据量常常不足一页，
 * 底部常驻一条「共 3 条」的分页条既占高度又没有信息量
 * （antd 的 hideOnSinglePage 也是这个意思，这里是它的判据）。
 */
export function showPager(totalRows: number, pageSize: number): boolean {
  return totalRows > Math.max(1, pageSize)
}

/** 可选的每页条数。20 是默认 —— 与日志列表一致，也接近一屏能看全的行数 */
export const PAGE_SIZES = ['10', '20', '50', '100'] as const
