// 表格列显隐（持久化）。
//
// 为什么需要：请求日志表有 11 列，宽窗口下正好铺满，窄窗口下必须横向滚动；
// 而「看日志」这个动作本身是有取向的 —— 排障的人盯的是状态/耗时/渠道，
// 看成本的人只关心费用与词元。让人把自己不看的那几列关掉，
// 比让他每次横向拖来拖去更实际。
//
// 存的是**被隐藏的列**而不是「显示的列」：以后给表加一列时，
// 老用户的本地记录里没有它 → 默认可见，不会出现「升级后新列莫名不见了」。
//
// 与 persistedChoice 同一口径：不引 pinia，键名带 llm-relay- 前缀，
// 读的时候必须容错（旧版本残留的键名、手改的脏数据、隐私模式下 localStorage
// 直接抛异常）—— 拿到不认识的值不能塞给界面，那会得到一张缺列的表。
//
// 锁定列（locked）：不允许隐藏的列。日志表锁住「请求时间」与「操作」：
// 前者是行的身份，后者是打开详情的唯一入口 —— 两者都能关掉的话，
// 用户会得到一张「看不出这行是什么、也点不开」的表，而且不知道怎么恢复。

import { computed, ref } from 'vue'

const PREFIX = 'llm-relay-cols-'

function readHidden(tableId: string, keys: readonly string[], locked: readonly string[]): string[] {
  try {
    const raw = localStorage.getItem(PREFIX + tableId)
    if (!raw) return []
    return raw
      .split(',')
      .map((s) => s.trim())
      .filter((k) => k !== '' && keys.includes(k) && !locked.includes(k))
  } catch {
    return []
  }
}

function writeHidden(tableId: string, hidden: string[]): void {
  try {
    if (hidden.length === 0) localStorage.removeItem(PREFIX + tableId)
    else localStorage.setItem(PREFIX + tableId, hidden.join(','))
  } catch {
    /* 忽略：写失败只影响「下次还记不记得」，本次勾选照常生效 */
  }
}

/**
 * 一次性列预设（2026-10-11）。
 *
 * 语义是「给这张表定一个初值，但只在他还没有自己的偏好时」：
 *   · 已经存过列显隐记录（用户手动调过）→ 什么都不改，只记标记；
 *   · 没存过 → 写入预设的隐藏列表；
 *   · 已经应用过预设 → 直接返回，避免「用户改回来之后又被预设覆盖」
 *     （reset() 会删掉列记录，那时标记还在，所以不会被重新套上）。
 *
 * 为什么不复用 writeHidden：那个函数不认识 keys / locked，
 * 而预设是**代码里写死的一份列表**，将来列改名或删除之后它可能引用到
 * 不存在的列 —— 脏值必须在写进去之前滤掉（readHidden 读的时候也会滤，
 * 但写的时候就干净，能少一次「为什么本地存了个奇怪的键」）。
 */
export function applyPresetOnce<K extends string>(
  tableId: string,
  preset: readonly string[],
  keys: readonly K[],
  locked: readonly K[] = []
): boolean {
  const marker = PREFIX + 'preset-' + tableId
  try {
    if (localStorage.getItem(marker) !== null) return false
    if (localStorage.getItem(PREFIX + tableId) === null) {
      const clean = preset.filter((k) => keys.includes(k as K) && !locked.includes(k as K))
      if (clean.length) localStorage.setItem(PREFIX + tableId, clean.join(','))
    }
    localStorage.setItem(marker, '1')
    return true
  } catch {
    // 隐私模式：存不下就不做预设，界面上是全列可见（安全的那一侧）
    return false
  }
}

/**
 * @param tableId 存储键后缀，各表唯一（如 'logs' / 'channels'）
 * @param keys    全部列，顺序即表格里的顺序（勾选面板按这个顺序列）
 * @param locked  不允许隐藏的列
 */
export function useColumnVisibility<K extends string>(
  tableId: string,
  keys: readonly K[],
  locked: readonly K[] = []
) {
  const hidden = ref<string[]>(readHidden(tableId, keys, locked))
  const visible = computed<Record<K, boolean>>(() => {
    const out = {} as Record<K, boolean>
    for (const k of keys) out[k] = !hidden.value.includes(k)
    return out
  })
  /** 当前可见的列，顺序与 keys 一致 —— 列宽合计、测量循环都按它遍历 */
  const visibleKeys = computed<K[]>(() => keys.filter((k) => !hidden.value.includes(k)))

  function toggle(key: K): void {
    if (locked.includes(key)) return
    // 不允许把最后一列也关掉：一张只有表头的表没有意义，
    // 而且用户会以为「数据没了」。面板里对应的勾选框是禁用态，
    // 这里是第二道闸（键盘/程序化调用也拦得住）。
    if (!hidden.value.includes(key) && visibleKeys.value.length <= 1) return
    hidden.value = hidden.value.includes(key)
      ? hidden.value.filter((k) => k !== key)
      : [...hidden.value, key]
    writeHidden(tableId, hidden.value)
  }

  /** 恢复默认：清掉本地记录（而不是写一个「全可见」的值） */
  function reset(): void {
    hidden.value = []
    try {
      localStorage.removeItem(PREFIX + tableId)
    } catch {
      /* 忽略 */
    }
  }

  return { visible, visibleKeys, toggle, reset }
}
