// 客户端显示的统一解析层：把「用户映射（对照表）> 内置 CLIENT_META >
// 落库快照」的优先级收口在一处。请求日志列表、详情抽屉、设置面板的
// 预览共用这里 —— 三处各自实现一遍必然漂移出三个叫法。
//
// 与后端的对应关系：
//   · clientKey 是 relay.ClientKey 的第三份实现（另两份：Go 权威版 +
//     store.backfillClientAliases 的 SQL 版），三处口径必须一致，
//     否则登记与解析对不上键；
//   · 优先级与后端 exportLogs 的 clientCell 同一条（映射名 > 快照名），
//     CSV 与界面才不会是两个名字。
import { CLIENT_META, ICON_LIBRARY } from './clientMeta'
import type { ClientAlias } from '@/api/types'

/** 显示映射的查找键：id 非空取 id，否则取观测到的 name token。
 *  口径同 relay.ClientKey（Go 权威版），改口径三处一起动。 */
export function clientKey(id: string, name: string): string {
  return id || name
}

/** 图标的渲染形态。builtin 走 CSS mask（随主题变色），img 与 text
 *  是用户自带的位图/字符（形状即颜色，与渠道图标同一种三态）。 */
export type ClientIconSpec =
  | { kind: 'builtin'; file: string }
  | { kind: 'img'; src: string }
  | { kind: 'text'; ch: string }

/** 显示名：映射名 > 内置当前叫法 > 落库快照名。三级都空由调用方渲染 —。 */
export function resolveClientLabel(
  id: string,
  name: string,
  aliases: Map<string, ClientAlias>
): string {
  const alias = aliases.get(clientKey(id, name))
  if (alias && alias.name) return alias.name
  if (id && CLIENT_META[id]) return CLIENT_META[id].name
  return name
}

/** 图标：映射的非空 icon 按 content 识别形态；否则回落内置链路
 *  （CLIENT_META 的品牌图标 → default.svg）。待设置的映射行
 *  （icon 空）不参与 —— 它还没配，不该把显示压成默认图标。 */
export function resolveClientIcon(
  id: string,
  name: string,
  aliases: Map<string, ClientAlias>
): ClientIconSpec {
  const alias = aliases.get(clientKey(id, name))
  if (alias && alias.icon) {
    const spec = aliasIconSpec(alias.icon)
    if (spec) return spec
  }
  return { kind: 'builtin', file: (id && CLIENT_META[id]?.file) || 'default.svg' }
}

/** 映射行的三种状态（设置面板分组用；判据只有这一处）：
 *   · mapped —— 用户配过名字；
 *   · builtin —— 没配，但 CLIENT_META 认识它（显示本身就好，配不配随意）；
 *   · pending —— 没配、内置表也不认识（列上显示的是原始 token，
 *     真正等用户来配的那一档）。
 *
 *  为什么放在这里而不是视图里：三态判定同时被「待设置计数」与「排序」
 *  消费，写两份必然漂移（2026-10-19 审查发现视图与 composable 各有一份
 *  且口径不同）。纯函数 + 单测，与 logLive / tableTools 同一做法。 */
export type ClientAliasStatus = 'mapped' | 'builtin' | 'pending'

export function clientAliasStatus(row: { key: string; name: string }): ClientAliasStatus {
  if (row.name) return 'mapped'
  if (CLIENT_META[row.key]) return 'builtin'
  return 'pending'
}

/** 待设置行数。口径与 clientAliasStatus 严格一致 ——
 *  内置客户端会被自动登记成 name 空的行，但它们不需要人管。 */
export function pendingAliasCount(rows: ReadonlyArray<{ key: string; name: string }>): number {
  return rows.reduce((n, r) => (clientAliasStatus(r) === 'pending' ? n + 1 : n), 0)
}

/** 把映射里的 icon 字符串识别成渲染形态（映射面板的预览也用它）。
 *  认不出（拼错的文件名、空白）返回 null，调用方回落默认链路 ——
 *  mask 引一个不存在的 url 不报错、只渲染一枚看不见的空白，
 *  静默空白比显式回落更糟。 */
export function aliasIconSpec(icon: string): ClientIconSpec | null {
  const trimmed = icon.trim()
  if (trimmed.startsWith('data:') || isRemoteIcon(trimmed)) {
    return { kind: 'img', src: trimmed }
  }
  // 一两个字符按文字/emoji 渲染（与渠道图标的单字符约定一致）
  if (trimmed !== '' && Array.from(trimmed).length <= 2) return { kind: 'text', ch: trimmed }
  // 其余按内置图标文件名引用；不在清单里的一律回落（不猜）。
  // 比对用 trimmed：备份导入会按原样写 icon（见 admin_backup.go），
  // 手改过的 JSON 里带首尾空白时，用原始值比对会静默落回默认图标。
  if (ICON_LIBRARY.includes(trimmed)) return { kind: 'builtin', file: trimmed }
  return null
}

/** 是不是「图片外链」（http(s) 地址）。
 *
 *  这类图标保存时会被后端抓下来缓存成 data URI（口径同渠道图标，见
 *  api.cacheClientIcon），因为存地址等于日志页每打开一次就去请求一次
 *  那个外链。抓不到时按原样入库 —— 设置面板据此标出「还没缓存」的行，
 *  并让「保存」保持可点（再点一次就是重试抓取）。 */
export function isRemoteIcon(icon: string): boolean {
  return /^https?:\/\//i.test(icon.trim())
}
