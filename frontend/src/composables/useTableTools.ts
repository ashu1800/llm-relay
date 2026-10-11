// 四张管理表共用的「本地搜索 + 客户端分页」（2026-10-11）。
//
// 用法：
//   const { q, rows, pagination, total, searching } = useTableTools(source, {
//     storageKey: 'keys-page-size',
//     searchText: (k) => [k.name, k.key_prefix].join(' ')
//   })
// 模板里把 `rows` 交给 :data-source、`pagination` 交给 :pagination，
// 搜索框绑 `q`。
//
// 为什么值得抽出来（而不是每页各写一份）：四张表的接口都是一次返回全量，
// 所以四处的判据完全一样 —— 搜索后回第 1 页、数据变少后把越界页码夹回来、
// 只有一页时不渲染分页条。这类「只在某些组合下才出错」的逻辑抄四遍，
// 迟早有一处漏掉夹页，表现是「搜出来的结果明明是有的，表格却空着」。
//
// 判据本身在 components/tableTools.ts（纯函数 + 单测），这里只负责响应式接线。
import { computed, ref, watch, type Ref } from 'vue'
import { clampPage, matchQuery, pageSlice, PAGE_SIZES, showPager } from '@/components/tableTools'
import { readStoredChoice, writeStoredChoice } from '@/utils/persistedChoice'

export interface TableToolsOptions<T> {
  /** localStorage 键后缀：每张表各自记住每页条数 */
  storageKey: string
  /** 组装一行的可搜索文本（把要搜的字段拼起来即可，大小写不敏感） */
  searchText: (row: T) => string
}

export function useTableTools<T>(source: Ref<T[]>, opts: TableToolsOptions<T>) {
  const q = ref('')
  const pageSize = ref(
    Number(readStoredChoice(opts.storageKey, [...PAGE_SIZES], '20')) || 20
  )
  const page = ref(1)

  const filtered = computed(() => {
    const query = q.value
    if (!query.trim()) return source.value
    return source.value.filter((row) => matchQuery(opts.searchText(row), query))
  })

  // 夹页放在 computed 里而不是 watch 里写 page：watch 是渲染之后才跑的，
  // 中间那一帧表格是空的（搜索时肉眼可见地闪一下）。
  const safePage = computed(() => clampPage(page.value, filtered.value.length, pageSize.value))
  const rows = computed(() => pageSlice(filtered.value, safePage.value, pageSize.value))
  const total = computed(() => filtered.value.length)

  /**
   * 当前视图是否**只显示了数据的一部分**（有搜索词、或者数据多到分了页）。
   *
   * 渠道页要用它来决定「能不能拖拽排序」：拖拽提交的是屏幕上看到的顺序，
   * 而屏幕上只有一部分行 —— 提交一个子集上去，后端会把它们重排成 1..N，
   * 没显示的那些行还留着旧序号，整组的顺序就乱了（这是数据损坏，不只是显示问题）。
   * 所以「视图被切开时不许拖」这条判断必须由这里给出，而不是各页自己拼。
   */
  const isSplit = computed(() => !!q.value.trim() || showPager(total.value, pageSize.value))

  // 搜索词一变就回第 1 页：停在第 5 页搜出来的东西通常不足一页，
  // 不回去就是一张空表（夹页虽然能兜住，但「回到第 1 页」才是用户的预期）
  watch(q, () => { page.value = 1 })

  const pagination = computed(() => ({
    current: safePage.value,
    pageSize: pageSize.value,
    total: total.value,
    // 只有一页时整条不渲染（见 tableTools.showPager）
    hideOnSinglePage: !showPager(total.value, pageSize.value),
    showSizeChanger: showPager(total.value, pageSize.value),
    pageSizeOptions: [...PAGE_SIZES],
    size: 'small' as const,
    showTotal: (t: number) => '共 ' + t + ' 条',
    onChange: (p: number, ps: number) => {
      page.value = p
      if (ps !== pageSize.value) {
        pageSize.value = ps
        writeStoredChoice(opts.storageKey, String(ps))
        // 换每页条数后页码的意义变了，回第 1 页（夹页也能兜，但夹出来的位置
        // 是「最后一页」，用户找不到自己刚才看的那一行）
        page.value = 1
      }
    }
  }))

  return { q, rows, pagination, total, filtered, page, pageSize, isSplit }
}
