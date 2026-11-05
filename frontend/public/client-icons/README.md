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

图标有两条渲染支路（见 `ClientIcon.vue`）：

- **单色轮廓**（多数）：CSS mask —— SVG 只提供形状（alpha），颜色由 CSS 变量
  给（`--color-text-secondary`，随主题走）。图标文件里写什么 fill 都不影响
  形状，但**不要**把它当彩色资产维护；
- **官方配色**（`components/clientMeta.ts` 的 `COLOR_ICONS`，43 个）：文件本身
  就是品牌彩色版，浅色主题直接 `<img>` 显示原色；深色主题回落到上面那条
  mask 单色轮廓 —— 彩色 logo 里的深色部分（axios 深紫、curl 近黑蓝）压在
  深色卡片上会糊掉，而黑白标的官方形态本来就是随背景反色。

哪些进 COLOR_ICONS：lobe-icons 有 `<slug>-color.svg` 的（取其彩色版），
加上少数品牌官方色公认、但 lobe-icons 没出彩色版的（按官方色注入 fill）。
**整幅颜色太浅的会被排除**（对白底不到 2:1，压在白卡片上等于隐形）：
openrouter、yi、trae、qoder、junie、langchain 这几个就属于这种情况，
它们继续走单色。OpenAI / Cursor / Copilot 这类官方就是黑白标的，**故意**
留在单色档。

## 来源

| 文件 | 来源 | 说明 |
|---|---|---|
| 品牌图标（客户端 / 厂商 / 网关，完整清单见 `ICON_LIBRARY`） | [lobe-icons](https://github.com/lobehub/lobe-icons) `packages/static-svg/icons/`（MIT） | 每个品牌取官方轮廓，统一 24×24 viewBox。2026-11-05 起，能取到 `<slug>-color.svg` 的换成彩色版（进 `COLOR_ICONS`），取不到的留单色 |
| zcode | ZCode 官网 hero logo `zcode.z.ai/images/hero-visual/zcode-logo@2x.png` | 官方站点未公开 SVG（应用图标只有带底色的 PNG），这里取透明底的标志本体，二值化 alpha 后缩到 128px 内嵌进 SVG —— 18px 下与矢量无差别 |
| continue / chatbox / nextchat / librechat | 各自官方仓库的矢量资产：`continuedev/continue` 的 `docs/logo/light.svg`、`chatboxai/chatbox` 的 `src/renderer/static/icons/icon-chatbox.svg`、`ChatGPTNextWeb/NextChat` 的 `app/icons/logo.svg`、`danny-avila/LibreChat` 的 `client/public/assets/logo.svg` | 这几个客户端在两个图标库里都没有收录，直接从官方仓库取（ChatBox 取的是符号图标，不是带底色的应用图标） |
| 通用工具图标（curl / go-http-client / python-urllib / python-requests / aiohttp / axios / postman / insomnia / browser / deepseek） | [simple-icons](https://github.com/simple-icons/simple-icons) `icons/<slug>.svg`（CC0） | 无品牌方 UI 资产可用的库，取其官方 logo 轮廓（python-urllib 用 Python 官方 logo —— 标准库与语言同源）；deepseek 同时是映射面板常用的厂商图标 |
| default.svg | 本项目自绘 | 终端提示符「>_」：认不出的客户端绝大多数是 CLI/脚本；用问号会暗示「出错了」，而未知是完全正常的状态 |

## 暂用默认图标的客户端

以下客户端暂时落到 `default.svg`（识别与显示名都不受影响，只是图标是通用款）：

- `aider` —— 官方 logo 是 200×60 的字标（`<text>` + 内嵌字体），缩到 18px
  不可读，不适合当图标；
- `wget`、`httpx`、`node-fetch`、`undici`、`okhttp`
  （五个库没有品牌 logo —— 它们的设计目标就是「不带身份地发请求」）

补图标的流程：把 SVG 放进本目录、文件名等于 `client_id`，然后在
`components/clientMeta.ts` 的 `CLIENT_META` 里把该 id 的 `file` 指过去
（现在它们指向 `default.svg`），并把文件名加进 `ICON_LIBRARY`。
`frontend/scripts/check-contracts.mjs` 会校验「映射表里写的文件都存在」，
以及 `ICON_LIBRARY` 与目录双向一致，指错/漏记会当场报错。
