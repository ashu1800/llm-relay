// 批量启停（2026-10-11）：渠道页与密钥页共用。
//
// 为什么需要：停掉一批渠道（某个上游整体故障、要下线一批试用渠道）时只能一行
// 一行点开关 —— 20 个渠道就是 20 次「找行 → 找按钮 → 点 → 等重拉」。
// 而这件事本身是**一次决策**：这一批一起停。
//
// ---- 为什么作用在「当前筛选结果」上，而不是勾选框 ----
//
// 第一版做的是勾选列 + 已选 N 项。放弃它的原因是**宽度**：勾选列要占 32px，
// 而渠道表各列声明宽之和 1177 在 1440 视口下正好等于容器宽（1182）——
// 多出来的 32px 会让这一页重新出现横向滚动条，而 `check-table-widths.mjs`
// 查不出这件事（它只比对「声明宽之和 ≤ scroll.x」，勾选列不在声明列里）。
// 要腾出 32px 只能压某一列的内容宽，而「每列至少把内容显示完整」是站主
// 明确定过的要求。为一个批量入口去动列宽，代价比收益大。
//
// 「作用于当前筛选结果」反而更贴合真实用法：某个上游出问题时的动线是
// 「按模型/地址搜出这一批 → 一起停掉」—— 搜索框已经把这个集合描述清楚了，
// 再让人去勾 20 个复选框是多余的一步。代价是必须**说清作用范围**：
// 按钮上写着数量、确认框里重复一遍当前的筛选条件（见 confirmText）。
//
// ---- 为什么是顺序跑而不是 Promise.all ----
//
//   1. 每一条都是一次写库（后端 PUT 会校验/重排），几十个并行 PUT 打过去
//      只会让数据库那条路排队，整体并不更快；
//   2. 失败时要能说清「哪几条没成功」，并行回来顺序是乱的，提示里那串名字
//      就没法按界面顺序读；
//   3. 进度是有意义的：「正在处理 7/20」比一个不知道要转多久的转圈强得多。
//
// ---- 失败不中断 ----
//
// 中途某一条 400/409 时继续跑剩下的，最后一次性汇报失败名单。中断的话
// 「已成功 7 条」这个事实既没被说出来、剩下的也没做 —— 用户面对一个半成品
// 状态，而他不知道是哪一半。这与「静默重取失败要留痕」是同一条原则。
import { ref } from 'vue'
import { message } from '@/utils/feedback'

export interface BatchRow {
  id: number
  enabled: boolean
}

export function useBatchToggle<T extends BatchRow>(opts: {
  /** 取一行的显示名（提示文案用，报名字不报 id） */
  label: (row: T) => string
  /** 对一行执行启用/停用。抛错即视为这一条失败 */
  setEnabled: (row: T, enabled: boolean) => Promise<void>
  /** 全部跑完（无论成败）之后调用：重新拉列表 */
  reload: () => Promise<void> | void
  /** 名词，用于提示文案（「渠道」/「密钥」） */
  noun: string
}) {
  const running = ref(false)
  const progress = ref({ done: 0, total: 0 })

  /**
   * 这批行里真正需要改动的条数。
   *
   * 调用方（确认框的标题、按钮上的数量）也要用它，所以导出成独立函数：
   * 筛选结果里混着**本来就是目标状态**的行是常态（搜「glm」命中 10 条，
   * 其中 6 条早就停用了），而「停用这 10 个」与「实际改了 4 行」不一致时，
   * 用户会以为提示数字是错的 —— 而真正错的是我们报了一个没发生的动作数。
   */
  function needChange(rows: readonly T[], enabled: boolean): T[] {
    return rows.filter((r) => r.enabled !== enabled)
  }

  /**
   * @param rows 这次要作用的所有行（调用方给「当前筛选结果」）
   * @param enabled 目标状态
   */
  async function run(rows: readonly T[], enabled: boolean) {
    if (running.value || rows.length === 0) return
    const verb = enabled ? '启用' : '停用'
    // 只对「状态真的会变」的行发请求（2026-10-11）：PUT 是幂等的，多打几次
    // 数据不会错，但提示里的数字会大于实际执行的状态变更 —— 见 needChange。
    const targets = needChange(rows, enabled)
    const already = rows.length - targets.length
    if (targets.length === 0) {
      // 一行都不用改时说清楚，而不是弹一句「已停用 0 个」（那读起来像失败）
      message.info('这 ' + rows.length + ' 个' + opts.noun + '已经是' + verb + '状态，无需改动')
      return
    }

    running.value = true
    progress.value = { done: 0, total: targets.length }
    const failed: string[] = []
    try {
      for (const row of targets) {
        try {
          await opts.setEnabled(row, enabled)
        } catch (e: any) {
          failed.push(opts.label(row) + '（' + (e?.message || '失败') + '）')
        }
        progress.value = { done: progress.value.done + 1, total: targets.length }
      }
    } finally {
      running.value = false
      await opts.reload()
    }

    const ok = targets.length - failed.length
    // 「另有 N 个本来就是该状态」只在确有跳过时才提一句 ——
    // 不提的话，用户看着自己选中的 10 个只被改动了 4 个会以为漏了 6 个
    const skipped = already > 0 ? '（另有 ' + already + ' 个本来就是' + verb + '状态）' : ''
    if (failed.length === 0) {
      message.success('已' + verb + ' ' + ok + ' 个' + opts.noun + skipped)
      return
    }
    // 部分失败：说清「成了几个、哪几个没成」，而不是笼统一句「操作失败」。
    // 8 秒：这份名单要看得完（默认 3 秒里读不完几个名字）
    message.warning(
      '已' + verb + ' ' + ok + ' 个' + opts.noun + skipped + '，' + failed.length + ' 个失败：' + failed.join('；'),
      8
    )
  }

  return { running, progress, run, needChange }
}
