import { describe, expect, it } from 'vitest'
import { filterCommands, type CommandItem } from './commandPalette'

const mockItems: CommandItem[] = [
  {
    id: 'nav-dashboard',
    category: 'nav',
    title: '数据看板',
    subtitle: 'Dashboard 概览卡与实时日志',
    keywords: ['dashboard', 'kanban', 'shuju'],
    action: () => {}
  },
  {
    id: 'nav-channels',
    category: 'nav',
    title: '渠道管理',
    subtitle: '配置上游端点与白名单',
    keywords: ['channels', 'qudao', 'upstream'],
    action: () => {}
  },
  {
    id: 'theme-nord',
    category: 'theme',
    title: 'Nord 主题',
    subtitle: '冷灰蓝极地调',
    keywords: ['nord', 'blue', 'dark'],
    action: () => {}
  },
  {
    id: 'action-error-logs',
    category: 'action',
    title: '只看失败日志',
    subtitle: '过滤 status_class=error',
    keywords: ['error', 'failed', 'shibai'],
    action: () => {}
  },
  {
    id: 'model-claude-3-7-sonnet',
    category: 'model',
    title: 'claude-3-7-sonnet',
    subtitle: '跳转至支持该模型的渠道',
    keywords: ['claude', 'anthropic', 'sonnet'],
    action: () => {}
  }
]

describe('filterCommands', () => {
  it('空查询返回全量命令', () => {
    expect(filterCommands('', mockItems)).toHaveLength(mockItems.length)
    expect(filterCommands('   ', mockItems)).toHaveLength(mockItems.length)
  })

  it('按标题过滤匹配', () => {
    const res = filterCommands('看板', mockItems)
    expect(res).toHaveLength(1)
    expect(res[0].id).toBe('nav-dashboard')
  })

  it('按关键词不区分大小写匹配', () => {
    const res = filterCommands('NORD', mockItems)
    expect(res).toHaveLength(1)
    expect(res[0].id).toBe('theme-nord')
  })

  it('按副标题过滤匹配', () => {
    const res = filterCommands('status_class', mockItems)
    expect(res).toHaveLength(1)
    expect(res[0].id).toBe('action-error-logs')
  })

  it('处理特殊字符时不抛出正则表达式异常', () => {
    expect(() => filterCommands('[*.+\\^$', mockItems)).not.toThrow()
    expect(filterCommands('[*.+\\^$', mockItems)).toEqual([])
  })
})
