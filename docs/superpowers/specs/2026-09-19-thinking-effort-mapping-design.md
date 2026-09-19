# 思考强度的跨协议映射

日期：2026-09-19
状态：待评审

## 背景

排查「Claude Code 的请求在日志列表里不显示思考等级」时，发现了两层问题：

1. **观察层（已修）**：`relay.ExtractThinkingLevel` 的 Anthropic 分支只认
   `thinking.type = enabled/disabled`，而 Claude Code 发的是新代形状
   `thinking:{type:"adaptive"}` + `output_config:{effort:"max"}`，
   于是整类请求静默落成空档。已修并上线验证。

2. **转发层（本 spec）**：思考强度**从来没有跨协议传递过**。
   全后端搜不到任何 `thinking` → `reasoning_effort` 的映射：入站 Anthropic
   的思考参数在出站非 Anthropic 时被 `stripCacheControl` 清掉。

   实测确认：用户的 5 条渠道全是 `openai-chat` 协议，因此 Claude Code 的
   `effort:max` **从未到达上游**，强度完全由上游默认行为决定。

   另有一个白名单缺口：`carryAnthropicParams` / `takeAnthropicParams` 用固定
   白名单（`thinking`/`top_k`/`metadata`/`service_tier`），Claude Code 新报文里的
   `output_config` 与 `context_management` 不在其中，在
   Anthropic→Anthropic 链路上被丢弃 —— 表现为「思考开着但强度丢失」。

## 目标

让思考强度在整个转发链路上不丢失、不静默降级。

非目标：不改变上游协议语义、不为上游发明它表达不了的能力（见「已知不等价」）。

## 设计

### 核心决策：通用语里 `reasoning_effort` 是唯一权威字段

顺着 `backend/internal/api/protocols.go` 已声明的架构原则：

> 站内以 OpenAI Chat Completions 作为「通用语」……这样新增一个上游协议只需要
> 写一个出站适配器，不必组合 N x M 个转换器。

所以思考强度也走通用语：**每个入站转换器写 `reasoning_effort`，每个出站
转换器读它并翻译成自己协议的形状**。不写 N×M 的协议对映射表。

`reasoning_effort` 同时继续用于「思考」列的观察快照（`ExtractThinkingLevel`），
两者共用同一套档位词表，避免又出现两套词表各自漂移。

### 档位词表（收敛为两段语义）

| 档位 | 语义 | 说明 |
|---|---|---|
| `off` | 明确关 | 用户说了不要 |
| `auto` | 交给模型定 | 与 Gemini `budget:-1`、Anthropic `adaptive` 同义 |
| `minimal` | 极低 | 来自 OpenAI `reasoning_effort` |
| `low` `medium` `high` | 三档常规 | 各家都有 |
| `xhigh` `max` | 两档高档 | Anthropic 4.6+ `effort` 引入 |

### 出站映射表

**上游接受度实测**（openai-chat 渠道，真实上游）：

| 值 | 结果 |
|---|---|
| `low` `medium` `high` `xhigh` `max` | 200 接受 |
| `auto` `minimal` `off` `adaptive` | 400 拒绝 |
| 不带该字段 | 200 正常 |

| 通用语 | → openai-chat | → anthropic | → gemini (`thinkingBudget`) | → responses |
|---|---|---|---|---|
| `off` | 删字段 | `thinking:{type:"disabled"}` | `0` | 删字段 |
| `auto` | 删字段 | `thinking:{type:"adaptive"}` | `-1` | 删字段 |
| `minimal` | `low` | `budget 1024` | `1024` | `low` |
| `low` | `low` | `budget 4096` | `4096` | `low` |
| `medium` | `medium` | `budget 8192` | `8192` | `medium` |
| `high` | `high` | `budget 16384` | `16384` | `high` |
| `xhigh` | `xhigh` | `budget 24576` | `24576` | `xhigh` |
| `max` | `max` | `budget 32768` | `32768` | `max` |

分界值沿用 `relay/thinking.go` 已记录的边界（`budgetMediumFrom = 8192`、
`geminiHighFrom = 24576`），使读（观察）与写（转发）共用同一套刻度。

### 出站 anthropic 的分代处理

`budget_tokens` 在 Opus 5 / Sonnet 5 这类新模型上会 **400**，因此不能无条件发：

1. **有 `anthropic_params`**（链路 anthropic→anthropic）：原样搬运，**无损**。
   这是既有的正确行为，保留。
2. **无 `anthropic_params`**（上游是 anthropic 但入站是别的协议）：只发
   `output_config.effort`，**不发** `budget_tokens`。这样在新老模型上都安全。
3. `off` → `thinking:{type:"disabled"}`；`auto` → `thinking:{type:"adaptive"}`。

### 顺带修掉的缺口

`convert/anthropic.go` 与 `convert/upstream_anthropic.go` 两处白名单
（**必须同步修改**）加入 `output_config`、`context_management`。

- `context_management`：纯 Anthropic 概念，无跨协议对应物，只在
  anthropic→anthropic 无损往返。
- `output_config`：有对应物（`effort`），由上面的映射表处理；但作为
  Anthropic 私有参数仍需往返保留，避免其他子字段丢失。

## 已知不等价（必须记录，不得静默）

**`off` 在 openai-chat 上游无法表达**：实测 `reasoning_effort:"none"` 返回 400，
故只能删字段，让上游用默认行为。用户说「不要思考」，上游可能仍然思考。
这是上游能力的边界，不是本设计能解决的。**不得**改用某个会被接受的档位
（如 `low`）来假装关闭 —— 那会让「关」变成「低」，是更隐蔽的欺骗。

**`minimal` 在上游无法表达**：实测同样 400。处理方式是**向上收敛到 `low`**
（而非删字段）：用户明确要了「最低档」，保留这个意图比丢掉它更接近用户所要，
代价只是略高于所要求。这与上面 `off` 的处理不同 —— `off` 是「不要」，
用任何正档位冒充它都是语义反转；`minimal` 与 `low` 只是程度差异，收敛是安全的。

**gemini 的 token 预算待真实定标**：`effort` 是定性档位、`thinkingBudget` 是
定量 token 数，本表的数值按文档边界推定，**无真实 Gemini 上游可验证**。
代码中须标注「待真实上游定标」。

## 验证计划

`scripts/proto-upstream.js` 是能记录「上游实际收到什么」的 mock
（`/stats` 返回 `last_request`、`rejected`），且同时支持 anthropic / gemini /
openai 三种协议。`scripts/test-upstream-protocol.sh` 已有用它做字段级断言的
先例（`chk "system 提到顶层" ...`）。

验证方式：为本地产物临时建一条指向 mock 的渠道，逐格断言
`last_request` 里的思考字段，并断言 `rejected == 0`。

| 验证项 | 手段 |
|---|---|
| anthropic → openai-chat | 真实上游实测（已可复现）+ mock 断言字段值 |
| anthropic → anthropic | mock 断言 `output_config.effort` 往返存活 |
| anthropic → gemini | mock 断言 `thinkingConfig.thinkingBudget` 数值 |
| 入站 gemini → 通用语 | mock 断言 |
| 出站 anthropic 的分代 | mock 断言新模型请求体**不含** `budget_tokens` |
| `off`/`auto` 删字段 | 断言请求体里没有 `reasoning_effort` |

单测为主（`convert` 包已有 `semantics_test.go`、`upstream_*_test.go` 的成熟模式），
mock 集成测试补齐字段值断言。

## 风险

| 风险 | 缓解 |
|---|---|
| 出站 anthropic 发 `budget_tokens` 导致新模型 400 | 默认不发；仅 `anthropic_params` 无损往返时保留 |
| 上游对未知 `reasoning_effort` 值 400 | 映射表只产出实测接受的值；不为上游发明档位 |
| gemini 预算数值拍错 | 标注待定标；有真实上游后重测 |
| 改变全站转发行为 | 你的 5 条渠道全是 openai-chat，是回归重点；上线后看「思考」列与实际强度是否一致 |
