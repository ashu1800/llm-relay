# 客户端图标

请求日志「客户端」列的图标资产（`RequestLogPanel.vue` 的客户端列 +
`components/ClientIcon.vue`）。文件名即后端 `relay.ExtractClient` 规则表里的
`client_id`（`<id>.svg`）；`default.svg` 是认不出客户端时的统一默认图标。

## 渲染方式

图标以 **CSS mask** 渲染（见 `ClientIcon.vue`）：SVG 只提供轮廓（alpha），
颜色由 CSS 变量给（`--color-text-secondary`，随主题走）。因此：

- 图标**不需要**是单色文件，任何 fill（品牌色或 currentColor）在 mask 下
  都只是「不透明的形状」——换色与主题无关；
- 反过来，**不要**把图标当成彩色资产来维护：浅色主题下是深灰、深色主题下
  是浅灰，22 套主题共用一套文件。

## 来源

| 文件 | 来源 | 说明 |
|---|---|---|
| AI 品牌图标（claude-code / codex / cursor / windsurf / cline / roo-code / kilo-code / opencode / gemini-cli / qwen-code / trae / cherry-studio / lobechat / sillytavern / open-webui / lm-studio / openai-* / anthropic-* / google-genai / langchain / vercel-ai-sdk / github-copilot） | [lobe-icons](https://github.com/lobehub/lobe-icons) `packages/static-svg/icons/`（MIT） | 每个品牌取官方单色轮廓（不带 `-text` / `-color` 后缀的那份） |
| 通用工具图标（curl / go-http-client / python-urllib / python-requests / aiohttp / axios / postman / insomnia / browser） | [simple-icons](https://github.com/simple-icons/simple-icons) `icons/<slug>.svg`（CC0） | 无品牌方 UI 资产可用的库，取其官方 logo 轮廓（python-urllib 用 Python 官方 logo —— 标准库与语言同源） |
| default.svg | 本项目自绘 | 终端提示符「>_」：认不出的客户端绝大多数是 CLI/脚本；用问号会暗示「出错了」，而未知是完全正常的状态 |

## 暂用默认图标的客户端

以下客户端在两个图标库里都没有收录、官方也未公开 SVG 资产，暂时落到
`default.svg`（识别与显示名都不受影响，只是图标是通用款）：

- `continue`、`aider`、`chatbox`、`nextchat`、`zcode`
  （五个客户端本身无公开向量资产；ZCode 的应用图标只有 PNG，暂不转描）
- `wget`、`httpx`、`node-fetch`、`undici`、`okhttp`
  （五个库没有品牌 logo —— 它们的设计目标就是「不带身份地发请求」）

补图标的流程：把 SVG 放进本目录、文件名等于 `client_id`，然后在
`components/clientMeta.ts` 的 `CLIENT_META` 里把该 id 的 `file` 指过去
（现在它们指向 `default.svg`）。`frontend/scripts/check-contracts.mjs`
会校验「映射表里写的文件都存在」，指错会当场报错。
