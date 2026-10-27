// 请求日志表的列显隐状态（单例）。
//
// 为什么要单例：按钮在**看板工具栏**（与主题按钮、刷新按钮同一排），
// 而表在 RequestLogPanel 里 —— 两处必须看到同一份状态，否则点了没反应。
// 而按钮不放在表格面板自己的标题栏里是有原因的：那一条 40px 的标题栏
// 2026-09-26 被特意删掉（表体刚好能多放一整行，见 RequestLogPanel 模板注释），
// 为了一个列设置按钮把它加回来，等于把那次优化退回去。
//
// 直接在模块作用域建实例：useColumnVisibility 只用 ref / computed / localStorage，
// 不依赖组件实例与生命周期，所以不需要「首次调用时懒建」那一层包装。
import { ref, watch } from 'vue'
import { useColumnVisibility, applyPresetOnce } from '@/composables/useColumnVisibility'
import { COL_KEYS, COL_LOCKED, type ColKey } from '@/components/logColumns'
import { readStoredChoice, writeStoredChoice } from '@/utils/persistedChoice'

/**
 * 窄屏一次性列预设（2026-10-11）。
 *
 * 为什么需要：日志表 12 列（2026-10-27 加思考强度列后）在宽屏下刚好铺满，
 * 而 ≤900px（侧栏收成图标栏那一档）可视宽度只剩 1000 出头 —— 一进页面就要
 * 横向拖，而「请求时间」与「操作」是左右两个固定列，中间那几列被挤在一条缝里，
 * 等于什么都看不到。
 *
 * 收起来的是这三列，理由是**排障视角**（这也是窄屏下最可能的场景）：
 *   速度 / 费用 / 密钥 —— 它们分别回答「快不快」「贵不贵」「谁在调」，
 *   都是第二眼的问题；而模型、渠道、词元、耗时、状态留下来之后，
 *   「这一次调用出了什么事」仍然一眼读得完。
 * （客户端列 2026-10-14 加入时**没有**进这份预设：它回答「这次是哪家客户端发的」，
 * 对排障是一眼信息而不是第二眼；且它只有 112px 宽，不是窄屏的主要矛盾。）
 *
 * （客户端列 2026-10-14 加入时**没有**进这份预设：它回答「这次是哪家客户端发的」，
 * 对排障是一眼信息而不是第二眼；且它只有 112px 宽，不是窄屏的主要矛盾。
 * 思考强度列 2026-10-27 加入时同样**没有**进预设：它回答「这次按什么强度思考」，
 * 与模型/渠道同属一眼信息，而且它只有 88px —— 为了省这点宽把排障信息收掉
 * 是本末倒置。）
 *
 * 三个约束：
 *   · 只在**用户从未调整过**列显隐时应用（applyPresetOnce 会记一个标记）——
 *     覆盖用户自己选过的列，比让他横向滚一次更烦人；
 *   · 只在窄屏那一档应用：宽屏用户不该因为「某次在手机上看过」而少三列；
 *   · 应用之后用户照样能在列设置里改回来（就是一份普通的隐藏列表）。
 *
 * 放在模块作用域执行（而不是某个 onMounted 里）：它就是「首次读 localStorage
 * 之前定下初值」，晚一拍就会先渲染一遍全列再收起来 —— 用户看得见那一下闪。
 */
if (window.matchMedia('(max-width: 900px)').matches) {
  applyPresetOnce('logs', ['speed', 'cost', 'key'], COL_KEYS, COL_LOCKED)
}

export const logColumns = useColumnVisibility<ColKey>('logs', COL_KEYS, COL_LOCKED)

/**
 * 日志表的行密度（2026-10-11）。
 *
 * `comfortable` 是原来唯一的形态（每行 44px，实测）：一行里装得下两排词元、
 * 三档耗时竖条与模型胶囊，扫列表时不会看串行。
 * `compact` 把单元格的上下内边距从 8px 收到 3px —— **实测行高 44px → 39px**
 * （1440 视口，`.shots/verify-ux-2026-10-11.mjs` 的 A7 组量的就是这两个数），
 * 同一块表体下的可视行数 13.4 → 15.2 行。
 *
 * **为什么只改内边距**：行高的下限由内容决定 —— 词元那一格是上下两排共用一个
 * 网格（`.token-cell` 实测 30px），内边距压到 0 也只能再省 6px，而这个档位
 * 剩下那 39px 里已经有 30px 是内容。想再矮就得动那一格的结构，而它的高度与
 * 测量锚点都是实测出来的（见 RequestLogPanel 的 CONTENT_MEASURE 与
 * 「两排共用一个网格」那段注释）。与其造一个「紧凑了但不明显」的档，
 * 不如把这一档能省的部分（内边距）省干净，并把真实数字写下来。
 *
 * 与之相关的几何**全部是运行时量出来的**（表体高度走弹性分配、
 * 扫光亮带按 getBoundingClientRect 定位、JS 锚点补偿也是量出来的），
 * 所以换密度不需要同步改任何像素常量 —— 唯一要重新确认的是可视行数，
 * 而那是量出来的结果，不是写死的期望。
 *
 * `.shots/verify-ux-2026-10-11.mjs` 的 A7 组量的是**实际行高**（44 → 39），
 * 而不是「类名加上了」。
 */
export type LogDensity = 'comfortable' | 'compact'

const density = ref<LogDensity>(
  readStoredChoice('log-density', ['comfortable', 'compact'], 'comfortable') as LogDensity
)
watch(density, (v) => writeStoredChoice('log-density', v))

export const logDensity = density
