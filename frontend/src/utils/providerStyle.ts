// 模型商的视觉标识。
//
// 设计约束（三条来自 UI/UX 规范，改这里时请一并守住）：
//   1. 颜色只在这一处定义，组件里不写死 hex —— 换色或加模型商只改这个文件
//   2. 标签文字对底色的对比度不低于 4.5:1（WCAG AA 正文标准）
//   3. 不靠颜色单独区分 —— 每个标签都带名称与首字母，
//      色盲用户与黑白打印同样能分辨
//
// 色相用 oklch 的 H 分量：oklch 是感知均匀的，
// 同一个 L 在不同色相下看起来一样亮，因此各模型商的对比度天然一致；
// 换成 hsl 的话黄色会比蓝色亮一大截，对比度就没法统一保证。

export interface ProviderStyle {
  /** 展示名。API 会返回名称，这里只作为拿不到时的兜底 */
  label: string
  /** 首字母，用于不依赖颜色的形状识别 */
  short: string
  /** oklch 色相角（0-360） */
  hue: number
  /** oklch 彩度。品牌色越鲜艳取值越大，过高会溢出 sRGB 色域 */
  chroma: number
}

// 已知模型商色相取自各自品牌色，再统一到可读的明度上
const KNOWN: Record<string, ProviderStyle> = {
  openai:    { label: 'OpenAI',    short: 'O', hue: 165, chroma: 0.11 },
  deepseek:  { label: 'DeepSeek',  short: 'D', hue: 268, chroma: 0.15 },
  anthropic: { label: 'Anthropic', short: 'A', hue: 42,  chroma: 0.13 },
  google:    { label: 'Google',    short: 'G', hue: 232, chroma: 0.14 },
  azure:     { label: 'Azure',     short: 'Z', hue: 210, chroma: 0.12 },
  moonshot:  { label: 'Moonshot',  short: 'M', hue: 300, chroma: 0.11 },
  zhipu:     { label: '智谱',      short: '智', hue: 195, chroma: 0.11 },
  qwen:      { label: '通义千问',  short: '通', hue: 20,  chroma: 0.13 },
  ollama:    { label: 'Ollama',    short: 'L', hue: 120, chroma: 0.10 }
}

// 从名称稳定派生色相：同一个 code 永远得到同一种颜色。
// 这样以后接入任意第三方模型商，不用改代码就有可区分的样式，
// 而不是全部灰成一片。
function deriveHue(seed: string): number {
  let h = 0
  for (let i = 0; i < seed.length; i++) {
    h = (h * 31 + seed.charCodeAt(i)) % 360
  }
  return h
}

const FALLBACK_CHROMA = 0.12

/** 取模型商样式。code 为空或未知时按名称派生，不会没有样式。 */
export function providerStyle(code?: string | null, name?: string | null): ProviderStyle {
  const key = (code || '').trim().toLowerCase()
  const known = key ? KNOWN[key] : undefined
  if (known) {
    return name ? { ...known, label: name } : known
  }
  const seed = key || (name || '').trim() || 'unknown'
  const short = (name || key || '?').trim().charAt(0).toUpperCase() || '?'
  return {
    label: name || code || '未知',
    short,
    hue: deriveHue(seed),
    chroma: FALLBACK_CHROMA
  }
}

/** 把样式转成可内联的 CSS 变量，颜色本身仍由 CSS 决定（便于适配深色主题）。 */
export function providerVars(s: ProviderStyle): Record<string, string> {
  return { '--pt-h': String(s.hue), '--pt-c': String(s.chroma) }
}
