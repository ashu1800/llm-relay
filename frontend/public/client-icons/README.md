# 客户端图标

请求日志「客户端」列的图标资产（`RequestLogPanel.vue` 的客户端列 +
`components/ClientIcon.vue`）。对**已识别的客户端**，文件名即后端
`relay.ExtractClient` 规则表里的 `client_id`（`<id>.svg`）；`default.svg`
是认不出客户端时的统一默认图标。

这套目录同时是**客户端显示映射**（系统设置 → 客户端显示映射）的内置图标库：
`components/clientMeta.ts` 的 `ICON_LIBRARY` 是本目录的完整清单（下拉选项、
自定义图标按文件名引用的合法性判定都取它），与目录的一致性由
`check-contracts.mjs` 双向钉住 —— 加图标要两处一起动。

内置库不只服务「已识别的客户端」：映射面板里给任意观测到的客户端挑图标时，
用户还需要厂商与网关的官方 logo（自建工具常以厂商命名，如
`deepseek-harness`）。所以库里同时收了三类（共 90 个）：

- **客户端软件**：编码 agent / IDE（Amp、Kiro、Qoder、CodeBuddy、Goose、
  OpenHands、Junie、Devin、JetBrains、Neovim、Zed…）、桌面与 Web 聊天客户端
  （Cherry Studio、LobeChat、ChatBox、NextChat、LibreChat、Open WebUI、
  LM Studio、SillyTavern、Dify、FastGPT、Poe、Monica…）；
- **模型厂商**：OpenAI、Claude、Gemini、Grok、Mistral、DeepSeek、Kimi、
  Doubao、Yuanbao、Qwen、Zhipu、MiniMax、Hunyuan、Wenxin、Spark、Baichuan、
  Yi、StepFun、SenseNova、Cohere、NVIDIA；
- **网关 / 平台**：OpenRouter、SiliconCloud、Ollama、vLLM、New API、
  Together、Groq、Fireworks、Hugging Face、ModelScope。

## 渲染方式

图标以 **CSS mask** 渲染（见 `ClientIcon.vue`）：SVG 只提供轮廓（alpha），
颜色由 CSS 变量给（`--color-text-secondary`，随主题走）。因此：

- 图标**不需要**是单色文件，任何 fill（品牌色或 currentColor）在 mask 下
  都只是「不透明的形状」——换色与主题无关；
- 反过来，**不要**把图标当成彩色资产来维护：浅色主题下是深灰、深色主题下
  是浅灰，12 套主题共用一套文件。

## 来源

| 文件 | 来源 | 说明 |
|---|---|---|
| 品牌图标（客户端 / 厂商 / 网关，完整清单见 `ICON_LIBRARY`） | [lobe-icons](https://github.com/lobehub/lobe-icons) `packages/static-svg/icons/`（MIT） | 每个品牌取官方单色轮廓（不带 `-text` / `-color` 后缀的那份），统一 24×24 viewBox |
| continue / chatbox / nextchat / librechat | 各自官方仓库的矢量资产：`continuedev/continue` 的 `docs/logo/light.svg`、`chatboxai/chatbox` 的 `src/renderer/static/icons/icon-chatbox.svg`、`ChatGPTNextWeb/NextChat` 的 `app/icons/logo.svg`、`danny-avila/LibreChat` 的 `client/public/assets/logo.svg` | 这几个客户端在两个图标库里都没有收录，直接从官方仓库取（ChatBox 取的是符号图标，不是带底色的应用图标） |
| 通用工具图标（curl / go-http-client / python-urllib / python-requests / aiohttp / axios / postman / insomnia / browser / deepseek） | [simple-icons](https://github.com/simple-icons/simple-icons) `icons/<slug>.svg`（CC0） | 无品牌方 UI 资产可用的库，取其官方 logo 轮廓（python-urllib 用 Python 官方 logo —— 标准库与语言同源）；deepseek 同时是映射面板常用的厂商图标 |
| default.svg | 本项目自绘 | 终端提示符「>_」：认不出的客户端绝大多数是 CLI/脚本；用问号会暗示「出错了」，而未知是完全正常的状态 |

## 暂用默认图标的客户端

以下客户端暂时落到 `default.svg`（识别与显示名都不受影响，只是图标是通用款）：

- `aider` —— 官方 logo 是 200×60 的字标（`<text>` + 内嵌字体），缩到 18px
  不可读，不适合当图标；
- `zcode` —— 应用图标只有 PNG，官方站点未公开 SVG，暂不转描；
- `wget`、`httpx`、`node-fetch`、`undici`、`okhttp`
  （五个库没有品牌 logo —— 它们的设计目标就是「不带身份地发请求」）

补图标的流程：把 SVG 放进本目录、文件名等于 `client_id`，然后在
`components/clientMeta.ts` 的 `CLIENT_META` 里把该 id 的 `file` 指过去
（现在它们指向 `default.svg`），并把文件名加进 `ICON_LIBRARY`。
`frontend/scripts/check-contracts.mjs` 会校验「映射表里写的文件都存在」，
以及 `ICON_LIBRARY` 与目录双向一致，指错/漏记会当场报错。
