// 客户端显示映射（对照表）的管理接口。后端在 admin_client_alias.go，
// 没有创建接口 —— 行只能由日志观测产生（自动登记 + 启动回填），
// 管理动作是「看 / 改 / 删」。
import { api } from './client'
import type { ClientAlias, Paged } from './types'

/** PUT 的返回：回读的整行（保存后直接刷新本地列表用） */
interface AliasUpdateResult {
  id: number
  updated: number
  item: ClientAlias
}

export const clientAliasApi = {
  /** 全量列表（后端按「待设置在前、更新时间倒序」排好，量级几十到几百） */
  list: () => api.get<Paged<ClientAlias>>('/client-aliases'),

  /**
   * 局部更新显示名 / 图标。与更新配置同一个三态约定：
   * 字段缺失 = 不改，空串 = 清掉（显示名清空即回到「待设置」）。
   * 注意清除必须传 `''` 而不是 null —— Go 把 null 也解析成「不修改」。
   */
  update: (id: number, patch: { name?: string; icon?: string }) =>
    api.put<AliasUpdateResult>(`/client-aliases/${id}`, patch),

  /** 删除这条映射：该客户端下次被观测到会重新登记一行待设置 */
  remove: (id: number) => api.del<{ id: number; deleted: boolean }>(`/client-aliases/${id}`)
}
