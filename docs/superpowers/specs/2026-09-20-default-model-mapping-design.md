# 渠道级「默认模型映射」（兜底映射）

日期：2026-09-20
状态：已实现

## 背景

用 Claude Code 时经常出现 502，日志显示：**耗时 1–3ms、输入/输出 token 全为 0、渠道列为
DeepSeek**。这三个特征一起指向「上游在参数校验阶段就拒绝了，压根没进推理」。

从代码链路逐环确认的根因：

1. Claude Code 发的是 Anthropic 协议侧的模型名（`claude-sonnet-4-6` / `claude-opus-4-7`）。
2. 路由器 `Router.Candidates` 按 `channel_models.public_name = ?` **精确等值**匹配候选渠道
   —— 没有通配、没有前缀、没有兜底。
3. 转发时 `RewriteModel` 把 `model` 换成该白名单行的 `upstream_name`；而 `normalizeWhitelist`
   规定**上游名留空即归一成对外名**。
4. 于是 `claude-sonnet-4-6` 被原样 POST 给 DeepSeek。DeepSeek 不认这个名字，当场拒绝 ——
   **token 全 0 正是「没进推理」的证据**。

渠道表单里的 tooltip 早就写着「写错这里是最常见的 502 原因」，但那要求**逐个模型名手工维护**：
Claude Code 每换一次模型名或版本号，都要回渠道补一行白名单。

## 目标

给每条渠道加一个可开关的「默认模型映射」：打开后，**白名单没精确命中的请求**统一改写为
指定模型发给上游。以后客户端换任何模型名都不必再改配置。

非目标：不做「无条件全接」。白名单为空的渠道仍不参与路由 —— 兜底是「接不准的」，
不是「接所有的」。

## 决策

| 决策 | 取值 | 理由 |
|---|---|---|
| 接单范围 | 白名单精确命中优先，没命中的落到兜底 | 保留精细控制，已配好的映射不被破坏 |
| 路由次序 | 兜底候选**永远排最后** | 容错通道，不该抢精确渠道的流量 |
| 计费口径 | 按**兜底目标**的单价 | 客户端的模型名在渠道里通常没配价，按它查只会记 0 元 |
| 日志模型名 | 请求名记客户端原名 + 兜底标记 | 写错模型名从此不报错，这个标记是唯一发现途径 |
| 密钥白名单 | **不豁免** | 「静默放宽权限」比「需要多点一次」糟得多 |

## 设计

### 存储：复用 `extra_config`

`Channel.ExtraConfig["default_model_enabled"]` + `["default_model"]`，不新增表、不用迁移
（`ExtraConfig` 已存在）。读取收敛在一处：`relay.ChannelDefaultModel`，照
`ChannelMaxConcurrency` 的容错写法（jsonb 里的布尔可能是 `bool` 或字符串 `"true"`）。

**「开关开着但模型名没填」等同于没开** —— 半残配置若被当成开启，路由会构造出上游名为空的
候选，请求带着客户端原名发出去，又变成那个 2ms 的 502。

判据只有一处，三处共用：路由（`Router.buildCandidates`）、列表回显（`fallbackModelOf`）、
写入校验（`api.validateDefaultModel`）。三处各写一份迟早漂移，而漂移的表现正是
「界面上开着、路由里没开」。

### 路由：两轮查询 + 稳定分区

兜底查询的落点是一句 **INNER JOIN**：

```sql
JOIN channel_models ON channel_models.channel_id = channels.id
                   AND channel_models.enabled = true
                   AND channel_models.public_name = channels.extra_config->>'default_model'
```

这一个 JOIN 同时完成三件事：校验「兜底目标确实在这条渠道的白名单里」、取回那一行的
`binding_id`、取回它自己的 `proxy_id`。于是**计价、单价快照、时段倍率、模型级代理覆盖
全部自动生效，计价引擎一行都不用改**；同时保证请求不会带着上游不认识的名字发出去
（指定一个上游不认的模型只是把 502 从 A 挪到 B）。

SQL 只粗筛「有 `default_model` 这个键」，开关状态由 Go 侧判定 —— 避免
「前端写 true / 后端读 bool / SQL 读字符串」三处漂移。

**排序**：先按来源稳定分区，只对精确段套用分组策略，兜底段拼在尾部。
必须在分区**之前**分两段来排 —— 三种策略都会主动把任意候选挪到最前
（RR 是整段左移、least_latency 按延迟重排），排在最后再分区来不及。副作用是兜底段
**不参与轮询**，RR 语义变成「精确命中的几条渠道怎么轮流」，这正是它该回答的问题。

### 来源必须显式传递，不能从行内容推断

`candidateRow.fromFallbackQuery` 由查询这一层钉死。**曾经想用
`public_name == default_model` 推断**，那是个真 bug：精确查询也带着 `default_model` 列，
当客户端请求的模型名恰好就是渠道的默认模型名时（请求 `deepseek-chat`、渠道默认也是它），
一次精确命中会被误判成兜底 —— 日志标错、候选还被排到末尾。
`TestBuildCandidatesExactMatchNotMisjudgedAsFallback` 钉住这一条。

### 计费：用候选的 Binding，不用请求名

`BillingModel` 返回 `res.Candidate.Binding.PublicName`：精确命中时它本就等于请求名
（候选查询的条件就是 `public_name = 请求名`），是一次**行为无变化的等价替换**；
兜底时它等于兜底目标名，才能命中那条真实存在的价。两种情况同一个表达式覆盖，
不加分支、不加列。

## 已知行为边界（不得静默，也不得当作 bug 修掉）

1. **写错模型名从此不再报错**：以前拼错模型名会得到 502 + 可用模型提示，现在会被兜底
   悄悄接走。这是本特性最大的可用性代价 —— 靠日志的「兜底」标记与渠道列表的胶囊让人发现。
2. **响应里的模型名是上游回显的那个**（即兜底目标），不是客户端请求的名字。
   这是**既有行为、与兜底无关**：响应改写器 `OpenAIChatToAnthropicResponse`
   （以及 Gemini / Responses 的同名函数）优先取**上游响应体**里的 `model` 字段，
   取不到才回落到传入的入站模型名 —— 所以任何配了「上游模型名」的渠道都这样，
   端到端测试里精确映射那条路径同样回显 `exact-mapped-name`。
   **不要**把它当成兜底引入的 bug 去「修」。
   （早期版本的本文档写的是「客户端始终看到自己请求的模型名」，那是错的：
   只看了 `relay_handler` 传 `publicModel` 就下了结论，没有追进 translator 内部。
   端到端断言把这条纠了出来。）
3. **兜底目标必须在该渠道白名单里且启用**：关掉或删掉那一行，兜底随之失效。
4. **兜底目标未配价时该笔调用记 0 元**，且渠道列表的 `unpriced_count` 不会把它算进去
   （它只遍历白名单行）。保存时给 warning 提示。
5. **`/v1/models` 与 `availableModelsHint` 不暴露兜底目标**：它们的语义是「白名单并集」，
   加入兜底目标会变成「兜底模型的别名集合」。
6. **「测试连通性」不受影响**：`Service.Probe` 直接用传入的候选，不走 `Candidates`。

## 验证

`scripts/test-default-model.sh`（9 组端到端断言，上游用 `proto-upstream.js` mock ——
它记录**实际收到的请求体**，于是「模型名换没换」是可断言的）：

| 验证项 | 手段 |
|---|---|
| 兜底改写生效（本次修复的核心） | 请求 `claude-opus-4-7`，断言 mock 收到的是兜底目标名、`rejected == 0` |
| 响应体回显客户端原名 | 断言响应 `model` 仍是请求名 |
| 日志口径 | 断言 `model_requested` / `model_upstream` / `fallback_mapped` 三者 |
| 精确优先 | 把请求名也登记进白名单，断言走映射名且 `fallback_mapped=false` |
| 密钥白名单不豁免 | 配 `allowed_models` 后断言 403，且白名单内的名字仍 200 |
| 关掉开关 | 回到 502，且**明确不算半残**（不误报）；上游一次都没被打到 |
| 半残配置点名 | 开关开着但兜底目标被停用 → 502 且提示里出现「默认模型映射」 |
| 保存校验 | 开关开着没填名字 / 指向白名单外的模型 → 400 |
| 计费口径 | 断言 `pricing_snapshot->>'model_key'` 是兜底目标名 |

**实测结果：26 项全部通过（2026-09-20）。**

单测（仓库没有 DB 测试环境，故逻辑尽量抽成纯函数）：
`relay.ChannelDefaultModel` 容错读取、`sortCandidates` 分区（4 条）、
`mergeCandidates` 去重、`buildCandidates` 来源判定（3 条）、`BuildLog` 兜底标记、
`BillingModel`、`modelsHintText`、`api.validateDefaultModel`。

## 实现中改掉的三处初始设计

1. **`buildCandidates` 的来源必须显式传入，不能从行内容推断**（设计初稿写的是
   `public_name == default_model`）。那是真 bug：精确查询也带 `default_model` 列，
   当请求名恰好等于渠道默认模型名时，一次精确命中会被误判成兜底。
   改为 `candidateRow.fromFallbackQuery`，由查询层钉死。
2. **`brokenFallbackCount` 只数「开关真的开着」的**。初版用
   `jsonb_exists(extra_config, 'default_model_enabled')`，只看键存不存在 ——
   于是「用户主动关掉开关、模型又不在白名单里」也会被算成半残并反复报警。
   误报会让人开始整体忽略这条提示，所以判据收紧成
   `lower(btrim(extra_config->>'default_model_enabled')) = 'true'`，与 Go 侧的
   `truthyFlag` 对齐。
3. **计价改用一个 `BillingModel` 纯函数**，而不是在 `pricing_hook.go` 里加
   `if FallbackMapped` 分支：`res.Candidate.Binding.PublicName` 在精确与兜底两种
   情形下都是正确的计费键，一处覆盖，不必新增日志列、也不必有分支。

## 上线后实战暴露的两个问题（2026-09-20，均已修）

1. **「配好的兜底映射过一会儿自己没了」** —— 不是兜底功能的错，是渠道表单的
   一个**既有隐患被它放大**：`extra_config` 保存时整块覆盖，而表单用
   `rows.value` 里的快照做基底，快照一过期，保存就会把期间由别处写进去的
   `default_model` 键静默删掉。修复与回归闸见提交 15c3aa1。
   教训：**整块覆盖写的字段，基底绝不能来自可能过期的快照**。
2. **所有候选（精确 + 兜底）都在冷却时，请求仍然失败** —— 这是设计使然
   （冷却中的渠道不该被继续打），但提示原先只数精确命中的渠道，没说
   「兜底那条也因冷却没顶上」，用户无法区分「兜底没生效」与「兜底也被挡住」。
   现在 coolingCount 连兜底一起数并在提示里分开说。
3. （部署侧）**宿主端口发布动作会打断 Windows↔WSL 的 localhost 转发**，
   与兜底无关但同日连续发作两次，app 改为 host 网络模式根治
   （见 deploy/docker-compose.yml app 段的长注释与提交 d8452a0）。

## 风险

| 风险 | 缓解 |
|---|---|
| 兜底掩盖模型名拼写错误 | 日志「兜底」标记 + 渠道列表胶囊；文档写明这是设计取舍 |
| 兜底目标被移出白名单后静默失效 | 保存时校验；`availableModelsHint` 在失败提示里点名半残配置 |
| 同一条渠道既精确命中又开兜底，候选重复 | `mergeCandidates` 按渠道 ID 去重，保留精确那条 |
| 改变全站路由行为 | 未开开关的渠道行为完全不变；`sortCandidates` 的既有 7 个测试全绿，证明是等价重构 |
