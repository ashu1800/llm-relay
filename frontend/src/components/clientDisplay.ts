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

/** 把映射里的 icon 字符串识别成渲染形态（映射面板的预览也用它）。
 *  认不出（拼错的文件名、空白）返回 null，调用方回落默认链路 ——
 *  mask 引一个不存在的 url 不报错、只渲染一枚看不见的空白，
 *  静默空白比显式回落更糟。 */
export function aliasIconSpec(icon: string): ClientIconSpec | null {
  if (icon.trim().startsWith('data:') || isRemoteIcon(icon)) {
    return { kind: 'img', src: icon.trim() }
  }
  // 一两个字符按文字/emoji 渲染（与渠道图标的单字符约定一致）
  const trimmed = icon.trim()
  if (trimmed !== '' && Array.from(trimmed).length <= 2) return { kind: 'text', ch: trimmed }
  // 其余按内置图标文件名引用；不在清单里的一律回落（不猜）
  if (ICON_LIBRARY.includes(icon)) return { kind: 'builtin', file: icon }
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
