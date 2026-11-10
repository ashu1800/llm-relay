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
`deepseek-harness`）。所以库里同时收了三类（共 93 个）：

- **客户端软件**：编码 agent / IDE（Pi、Oh My Pi、Amp、Kiro、Qoder、CodeBuddy、Goose、
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
- **官方配色**（`components/clientMeta.ts` 的 `COLOR_ICONS`，46 个）：文件本身
  就是品牌彩色版，浅色主题直接 `<img>` 显示原色；深色主题回落到上面那条
  mask 单色轮廓 —— 彩色 logo 里的深色部分（axios 深紫、curl 近黑蓝）压在
  深色卡片上会糊掉，而黑白标的官方形态本来就是随背景反色。

哪些进 COLOR_ICONS：lobe-icons 有 `<slug>-color.svg` 的（取其彩色版），
加上少数品牌官方色公认、但 lobe-icons 没出彩色版的（按官方色注入 fill）。
**整幅颜色太浅的会被排除**（对白底不到 2:1，压在白卡片上等于隐形）：
openrouter、yi、trae、qoder、junie、langchain 这几个就属于这种情况，
它们继续走单色。OpenAI / Cursor / Copilot 这类官方就是黑白标的，**故意**
留在单色档。

例外是 `COLOR_ONLY_ICONS`（目前的 zcode、pi 与 omp）：这三个在深色主题也画
原图，但原因不同：

- zcode 是**自带底色**：它的 alpha 是一整个圆角方块，回落成 mask 会得到
  一块实心方块、Z 直接消失 —— 比「深色下糊掉」更糟，所以它没有回落档；
- pi 是**三色官方标志**（珊瑚 `#F09082` / 蓝 `#4D9ABF` / 黄 `#F1BE58`）：
  三色在深色卡片（`--color-fg` #303030）上的对比度都在 4:1 以上
  （5.7:1 / 4.2:1 / 7.7:1），不存在「深色部分糊掉」的问题，回落成单色
  反而丢掉了用户要的彩色形态。浅色主题按官方原色画：黄色块对白底只有
  1.7:1，但它是三色标志的一部分、形状主体由珊瑚/蓝承担，整幅颜色不算
  「太浅」，所以保留官方三色而不是拆掉黄色；
- omp 也是**自带底色**：官方 favicon 是深色圆角方块 + 品红/紫/青渐变 Pi，
  alpha 同样是一整个方块，回落成 mask 只会得到一块实心方块、Pi 消失，
  所以与 zcode 同档，任何主题都直接画原图。

共同点：它们都同时登记在 `COLOR_ICONS`（浅色主题走彩色）与
`COLOR_ONLY_ICONS`（深色主题也走彩色，不渲染 mask 回落档）。

## 来源

| 文件 | 来源 | 说明 |
|---|---|---|
| 品牌图标（客户端 / 厂商 / 网关，完整清单见 `ICON_LIBRARY`） | [lobe-icons](https://github.com/lobehub/lobe-icons) `packages/static-svg/icons/`（MIT） | 每个品牌取官方轮廓，统一 24×24 viewBox。2026-11-05 起，能取到 `<slug>-color.svg` 的换成彩色版（进 `COLOR_ICONS`），取不到的留单色 |
| zcode | ZCode 官网应用图标 `zcode.z.ai/favicon-192x192.png` | 官方站点未公开 SVG。站主确认要的就是这张「黑色圆角方块 + 白色 Z」的应用图标（不是 hero 里那个灰色钩形标记），缩到 96px 内嵌进 SVG；因为自带底色，它同时登记在 `COLOR_ICONS` 与 `COLOR_ONLY_ICONS` |
| pi | Pi 官网 `pi.dev/logo.svg`（`logo-auto.svg` 是同一份三色标志） | 官方 800×800 viewBox，图形只占中间 469.43×469.43；坐标按 24×24 重排后正好落在 6 的网格上。三色在深色卡片上对比度都 ≥4:1，所以它同时登记在 `COLOR_ICONS` 与 `COLOR_ONLY_ICONS`，深色主题也保持彩色（浅色主题的黄色块对白底 1.7:1，但形状主体由珊瑚/蓝承担，整体不属「颜色太浅」，保留官方三色） |
| omp | Oh My Pi 官网 `omp.sh/favicon.svg`（与仓库 `packages/collab-web/public/favicon.svg` 同一份） | 深色圆角方块 + 品红/紫/青渐变 Pi，官方 64×64 viewBox；坐标等比缩到 24×24 后落盘。自带底色，mask 回落会变成实心方块，所以与 zcode 一样同时登记在 `COLOR_ICONS` 与 `COLOR_ONLY_ICONS` |
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
