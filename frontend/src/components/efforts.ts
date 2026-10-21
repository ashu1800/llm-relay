// 思考档位词表（白名单编辑器的「档位」一列、/v1/models 的 supported_efforts）。
//
// 值在这里手写而不是取生成文件：档位常量在 backend/internal/relay/convert
// 包里，tygo 只生成 internal/model 的实体类型，取不到它们。两边的一致性
// 由 scripts/check-contracts.mjs 的「档位词表前后端一致」一条静态比对兜住 ——
// 改后端词表而忘了这里，npm run check 会直接红。
//
// 顺序就是强度序（从关到最强），也是下拉里的选项顺序：用户看这一列时想的
// 是「从关到最强」，不是字母序。与后端 convert.AllEfforts 的注释同一条理由。
export const EFFORT_LEVELS = ['off', 'auto', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'] as const

/**
 * 归一一份档位声明，口径与后端 convert.NormalizeEfforts 完全一致：
 * 去空白、小写、none 归成 off、重复的只留一个，已知档位按强度序排，
 * 认不得的档位**保留**在末尾（它是用户的声明，可能对应上游将来新增的档位）。
 *
 * 为什么前端也要有一份：下拉的候选项是固定的八档，而库里可能存着这份
 * 词表之外的条目（从备份导入、或后端将来加了一档而前端没跟上）。不归一
 * 就会被 antd 的 select 当成「没有值的 tag」悄悄丢掉，用户改别的档位时
 * 顺手把它们一起抹了 —— 而界面上看不出任何变化。
 */
export function normalizeEfforts(list?: string[] | null): string[] {
  if (!list || list.length === 0) return []
  const want = new Set<string>()
  for (const raw of list) {
    const v = String(raw ?? '').trim().toLowerCase()
    if (!v) continue
    want.add(v === 'none' ? 'off' : v)
  }
  const out: string[] = EFFORT_LEVELS.filter((e) => want.has(e))
  for (const v of want) {
    if (!out.includes(v)) out.push(v)
  }
  return out
}

/** 下拉选项：八档全出，外加声明里有、词表里没有的条目（排在后面） */
export function effortOptions(declared?: string[] | null): { value: string; label: string }[] {
  const extra = normalizeEfforts(declared).filter((e) => !(EFFORT_LEVELS as readonly string[]).includes(e))
  return [...EFFORT_LEVELS, ...extra].map((v) => ({ value: v, label: v }))
}
