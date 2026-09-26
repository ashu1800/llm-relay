# 出站 schema 引用归一化（$ref / $defs 提升）

日期：2026-09-26
状态：已实现（`backend/internal/relay/convert/schema_refs.go`，接线在
`upstream.go` 的 `UpstreamRequest`，测试 `schema_refs_test.go`）

## 背景

ZCode 调 `gemini-3.8-flash`（渠道为 OpenAI 兼容的 Vercel AI Gateway）时报
`stream_initialization_failed`，上游 400：

> Google schema conversion only supports references to direct children of
> root-level Defs or definitions.（`AI_UnsupportedFunctionalityError`，不可重试）

链路：ZCode → 中转站（渠道协议 `openai-chat`，透传分支）→ Vercel AI Gateway →
Gemini。网关在把工具参数转成 Google 格式前要自己做一次 schema 转换，而它的
转换器只接受「指向根级 `$defs`（或 `definitions`）**直接子节点**」的 `$ref`。
ZCode 挂载的 MCP 服务器派生的工具 schema 里嵌套 `$defs`、深层
`#/$defs/A/properties/B` 很常见，这类请求被整条拒绝，换渠道也无解。

中转站原先对 `tools[].function.parameters` 全程原样透传；唯一的 schema 清洗
是 Gemini 原生协议的 `sanitizeGeminiSchema`（丢弃式白名单过滤，不是解引用），
覆盖不到这条路径。

## 方案

出站前把引用整理成「最保守」形状：**所有 `$defs`/`definitions` 提升到 schema
根级，所有 `$ref` 一律写成 `#/$defs/<名>`**。三原则：

1. **语义保持** —— 只挪位置、改引用，不删任何关键字。组合关键字、
   `additionalProperties` 等交给上游转换器自己取舍；对纯 OpenAI 上游，
   提升后的 schema 与原 schema 语义等价。
2. **零扰动** —— 请求体不含 `"$ref"`/`"$defs"`/`"definitions"` 子串直接返回；
   schema 没有引用问题（含根级 `$defs` + 简单引用的合规形状、以及属性名恰好叫
   `definitions`/`$ref` 的陷阱形状）字节级原样返回，不重新序列化。
3. **兜底** —— 解析不了的引用（外部 URL、失效指针）就地降级：去掉 `$ref`、
   缺 `type` 时补 `object`，其余键（如 description）保留。丢一个参数的精确
   约束，好过整条请求被拒。

### 算法（collect → plan → apply 三段）

- **collect**：遍历整棵 schema 找出所有定义容器；根级容器条目保留原名，
  嵌套容器条目撞名时让位加 `_2` 后缀，全部收进统一的 defs 表，并登记
  「容器条目指针 → 新指针」的改写表（含恒等映射）。
- **plan**：对原始树（先于任何改动）解析每个 `$ref`：
  - 已是登记过的根级直接子节点 → 恒等或改指向；
  - 指向深层节点（`#/$defs/A/properties/B`、`#/properties/x`）→
    **目标整体提升**为新定义（不就地展开，天然免疫引用环）；
  - `$ref: "#"` → 整根深拷贝提升为定义（直接引用原根会造出自包含环，
    序列化栈溢出）；
  - 解析不了 → 不登记，留给 apply 降级。
- **apply**：按改写表换 `$ref` 值；删除已提升的嵌套容器（根级的删掉后统一
  重建 `$defs`）；定义节点再走一遍（清掉 `"#"` 拷贝里的原样容器、把降级
  同步回定义表）。

### 关键陷阱：命名映射

`properties` / `patternProperties` / `dependentSchemas` 的值是「名字 → schema」
映射，**键是用户起的属性名** —— 属性完全可以叫 `"$defs"` 甚至 `"$ref"`
（GitHub API 的 schema 就有叫 `$ref` 的属性）。落在这些映射里的同名键不是
定义容器，绝不能当容器提升/删除，也不能当引用改写。三个遍历（collect/plan/
apply）都带 `inNamed` 上下文，且子层性质只在「当前层是 schema」时按键名推导
—— 否则定义恰好叫 `properties` 时，它内部真正的 `$defs` 会被漏收。

### 接线

`UpstreamRequest` 在 `isChatPath` 守卫之后、协议 `switch` 之前调用
`normalizeSchemaRefs(body)`：此时请求体还是 OpenAI Chat 通用语形状，一处覆盖
全部出站协议（openai-chat / custom 透传、openai-responses 拍平后的扁平
`parameters`、anthropic `input_schema`、gemini 出站）。改写发生时按渠道/模型
记一条 Info 日志，便于核对生效情况。

顶层解析用 `map[string]json.RawMessage` 浅解析：`messages` 等大块内容保持
原字节，只有真正改写过的 `tools` / `response_format` 重新序列化。

## 覆盖范围与已知边界

- 只修 `$ref`/`$defs` 这一个报错面：`oneOf`/`allOf`/`additionalProperties`
  等关键字原样保留，是否被上游接受由上游转换器决定，不在本次战线内。
- Gemini 原生协议（`gemini-generateContent`）出站仍走 `sanitizeGeminiSchema`
  的丢弃式过滤，引用会被丢弃 —— 该路径本来就不会 400，暂不升级。
- 超过 2^53 的 schema 数值经 float64 往返可能改写表示形式（与
  `stripCacheControlFromJSON` 同一取舍），工具 schema 里实际不会出现。
- 不可重试的 400（如本次）不会消耗重试次数，修复只能靠改写出站报文本身。
