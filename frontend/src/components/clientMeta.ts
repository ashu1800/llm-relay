// 客户端 id → 显示名 + 图标文件的映射。
//
// 后端 relay.ExtractClient 的规则表是这份映射的**数据源**：那边认出的 id
// 原样落进日志的 client_id 列，前端用 id 在这里找回图标与当前显示名。
// 两边的 id 集合由 frontend/scripts/check-contracts.mjs 静态比对
// （后端规则表 ⊆ 本表），新增客户端时漏了这边会当场报错，而不是静默显示
// 一个默认图标。
//
// 为什么显示名也留一份（后端已经落了 client_name）：
//   · client_name 是**识别当时**的快照，用于历史行的正确显示 —— 与
//     CostCurrency / PricingSnapshot 同一个道理，不允许被后来的改名改写；
//   · 这份 name 是**当前**的统一叫法，让同一个客户端在一屏里不会出现
//     两个名字（例如某个客户端改名之后，老行与今天的新行都显示新名）。
// 渲染时的优先级（判据只有一处，见 clientDisplay.resolveClientLabel）：
//   用户映射名 → 本表的 name → 落库的 client_name 快照
// 注意反过来不成立：本表的 name **不能**被 client_name 覆盖 ——
// 快照是「识别当时它自称什么」，当前统一叫法才是这里这份。
//
// file 允许指向同一个文件（openai-python / openai-node 共用 openai.svg）：
// 它们是同一家 SDK 的两个语言版本，品牌图标本来就该是同一个。
//
// 加一个客户端要动的地方（照抄现有条目的形状即可）：
//   1. 后端 relay/client.go 的 clientRules（识别规则）；
//   2. 本表的 id → name + file；
//   3. frontend/public/client-icons/<id>.svg（没有官方资产的指 default.svg，
//      并在那个目录的 README 里记一笔）。
export interface ClientMeta {
  /** 界面上显示的名字（当前统一叫法） */
  name: string
  /** public/client-icons/ 下的文件名 */
  file: string
}

export const CLIENT_META: Record<string, ClientMeta> = {
  // ---- AI 编码 CLI / 代码代理 ----
  'claude-code': { name: 'Claude Code', file: 'claude-code.svg' },
  codex: { name: 'Codex CLI', file: 'codex.svg' },
  cursor: { name: 'Cursor', file: 'cursor.svg' },
  windsurf: { name: 'Windsurf', file: 'windsurf.svg' },
  cline: { name: 'Cline', file: 'cline.svg' },
  'roo-code': { name: 'Roo Code', file: 'roo-code.svg' },
  'kilo-code': { name: 'Kilo Code', file: 'kilo-code.svg' },
  continue: { name: 'Continue', file: 'default.svg' },
  aider: { name: 'Aider', file: 'default.svg' },
  opencode: { name: 'OpenCode', file: 'opencode.svg' },
  'gemini-cli': { name: 'Gemini CLI', file: 'gemini-cli.svg' },
  'qwen-code': { name: 'Qwen Code', file: 'qwen-code.svg' },
  trae: { name: 'Trae', file: 'trae.svg' },
  zcode: { name: 'ZCode', file: 'default.svg' },

  // ---- 桌面 / Web 聊天客户端 ----
  'cherry-studio': { name: 'Cherry Studio', file: 'cherry-studio.svg' },
  lobechat: { name: 'LobeChat', file: 'lobechat.svg' },
  chatbox: { name: 'ChatBox', file: 'default.svg' },
  nextchat: { name: 'NextChat', file: 'default.svg' },
  sillytavern: { name: 'SillyTavern', file: 'sillytavern.svg' },
  'open-webui': { name: 'Open WebUI', file: 'open-webui.svg' },
  'lm-studio': { name: 'LM Studio', file: 'lm-studio.svg' },

  // ---- SDK ----
  'openai-python': { name: 'OpenAI Python SDK', file: 'openai-python.svg' },
  'openai-node': { name: 'OpenAI Node SDK', file: 'openai-node.svg' },
  'anthropic-python': { name: 'Anthropic Python SDK', file: 'anthropic-python.svg' },
  'anthropic-ts': { name: 'Anthropic TypeScript SDK', file: 'anthropic-ts.svg' },
  'google-genai': { name: 'Google GenAI SDK', file: 'google-genai.svg' },
  langchain: { name: 'LangChain', file: 'langchain.svg' },
  'vercel-ai-sdk': { name: 'Vercel AI SDK', file: 'vercel-ai-sdk.svg' },
  'github-copilot': { name: 'GitHub Copilot', file: 'github-copilot.svg' },

  // ---- 通用 HTTP 库 ----
  curl: { name: 'curl', file: 'curl.svg' },
  wget: { name: 'Wget', file: 'default.svg' },
  'go-http-client': { name: 'Go HTTP 客户端', file: 'go-http-client.svg' },
  'python-urllib': { name: 'Python urllib', file: 'python-urllib.svg' },
  'python-requests': { name: 'Python requests', file: 'python-requests.svg' },
  httpx: { name: 'HTTPX', file: 'default.svg' },
  aiohttp: { name: 'AIOHTTP', file: 'aiohttp.svg' },
  axios: { name: 'Axios', file: 'axios.svg' },
  'node-fetch': { name: 'node-fetch', file: 'default.svg' },
  undici: { name: 'undici', file: 'default.svg' },
  okhttp: { name: 'OkHttp', file: 'default.svg' },
  postman: { name: 'Postman', file: 'postman.svg' },
  insomnia: { name: 'Insomnia', file: 'insomnia.svg' },
  browser: { name: '浏览器', file: 'browser.svg' }
}

/** 图标文件名：未知 id（含空串）一律回落到默认图标。
 *  不在这里判文件存在性 —— 那是 check-contracts.mjs 的静态职责，
 *  运行时的 onerror 会静默失败、反而藏住「注册了 id 没放文件」。 */
export function clientIconFile(id: string | null | undefined): string {
  return (id && CLIENT_META[id]?.file) || 'default.svg'
}

/**
 * 内置图标库的完整文件清单（= public/client-icons/ 下全部 .svg）。
 * 两类消费者：映射面板的图标下拉选项，与 clientDisplay 对自定义图标
 * 「按文件名引用内置图标」的合法性判定。与目录的一致性由
 * check-contracts.mjs 双向钉住（清单多了文件得放，文件多了清单得记），
 * 所以这里的手工清单不会静默漂移。
 */
export const ICON_LIBRARY: string[] = [
  'aiohttp.svg',
  'anthropic-python.svg',
  'anthropic-ts.svg',
  'axios.svg',
  'browser.svg',
  'cherry-studio.svg',
  'claude-code.svg',
  'cline.svg',
  'codex.svg',
  'curl.svg',
  'cursor.svg',
  'default.svg',
  'deepseek.svg',
  'gemini-cli.svg',
  'github-copilot.svg',
  'go-http-client.svg',
  'google-genai.svg',
  'insomnia.svg',
  'kilo-code.svg',
  'langchain.svg',
  'lm-studio.svg',
  'lobechat.svg',
  'open-webui.svg',
  'openai-node.svg',
  'openai-python.svg',
  'opencode.svg',
  'postman.svg',
  'python-requests.svg',
  'python-urllib.svg',
  'qwen-code.svg',
  'roo-code.svg',
  'sillytavern.svg',
  'trae.svg',
  'vercel-ai-sdk.svg',
  'windsurf.svg'
]
