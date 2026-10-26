/**
 * 模型名分段：拆解为厂商/路径前缀（如 anthropic/）、核心型号（如 claude-3-7-sonnet）、版本日期后缀（如 -20250219）
 */
export type ModelNameSegments = {
  prefix?: string
  core: string
  suffix?: string
}

const SUFFIX_REGEX = /(-(?:\d{8}|\d{4}-\d{2}-\d{2}|latest|preview|exp|turbo|instruct))$/i

/**
 * 解析模型名称的语义层级，用于提升密集列表的排版节奏与可读性
 */
export function parseModelName(name?: string | null): ModelNameSegments {
  if (!name || typeof name !== 'string') {
    return { core: '' }
  }

  let rest = name
  let prefix: string | undefined

  // 1. 提取路径前缀（如 "anthropic/", "deepseek-ai/", "meta-llama/"）
  const slashIdx = rest.indexOf('/')
  if (slashIdx > 0 && slashIdx < rest.length - 1) {
    prefix = rest.slice(0, slashIdx + 1)
    rest = rest.slice(slashIdx + 1)
  }

  let suffix: string | undefined
  // 2. 提取日期或版本修饰后缀（如 -20250219, -preview, -latest）
  const match = rest.match(SUFFIX_REGEX)
  if (match && match.index && match.index > 0) {
    suffix = match[1]
    rest = rest.slice(0, match.index)
  }

  return {
    prefix,
    core: rest,
    suffix
  }
}
