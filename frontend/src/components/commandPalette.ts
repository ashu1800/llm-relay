export type CommandCategory = 'nav' | 'theme' | 'action' | 'model'

export interface CommandItem {
  id: string
  category: CommandCategory
  title: string
  subtitle?: string
  keywords?: string[]
  icon?: string
  action: () => void | Promise<void>
}

export const CATEGORY_LABELS: Record<CommandCategory, string> = {
  nav: '页面导航',
  action: '快捷操作与排障',
  theme: '主题外观',
  model: '模型管理'
}

/**
 * 纯函数：根据输入字符串过滤命令项列表。
 *
 * 匹配范围：标题、副标题、预置拼音/英文关键词。
 * 不采用复杂正则，避免用户键入特殊字符时异常。
 */
export function filterCommands(query: string, items: CommandItem[]): CommandItem[] {
  const q = (query || '').trim().toLowerCase()
  if (!q) return items

  return items.filter((item) => {
    if (item.title && item.title.toLowerCase().includes(q)) return true
    if (item.subtitle && item.subtitle.toLowerCase().includes(q)) return true
    if (item.keywords && item.keywords.some((k) => k.toLowerCase().includes(q))) return true
    return false
  })
}
