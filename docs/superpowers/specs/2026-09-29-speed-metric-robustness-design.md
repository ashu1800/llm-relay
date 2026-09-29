# 输出速度列的「几千 tok/s」假值：首字窗口塌缩（2026-09-29）

> 站主反馈：「仔细分析一下 token 速度计算，有些时候 token 速度达到了离谱的几千 token/s」。
> 本文记录根因、证据链、修法与未做的事。复算用的 SQL 见第七节（只读查询，未改任何后端代码）。

## 一、问题

请求日志列表的「速度」列（`frontend/src/components/RequestLogPanel.vue`，该列由 commit
`54df67a` 加入）在生产库里的分布（2026-09-29，流式可算行 n=2265，未取整的分位）：

| 口径 | p50 | p90 | p99 | 最大 | >1000 的行数 |
|---|---|---|---|---|---|
| 修前（`总耗时 − 首字` 一律做分母） | 162.1 | 347.2 | **12100** | **52286** | 109 |
| 修后（窗口塌缩时退回总耗时） | 112.6 | 278.9 | **370.5** | **1069** | 5 |

p99 与最大值差两个数量级 —— 这不是「上游偶尔很快」，是同一批行在两种分母下算出来的。

## 二、速度是怎么算出来的

前端的算法只有一处（列表与详情弹窗共用同一个函数）：

```js
const ms = row.stream ? row.total_ms - (row.first_byte_ms || 0) : row.total_ms
const v = row.completion_tokens / (ms / 1000)   // 显示时一律取整
```

两个时间戳都来自后端，来源口径不同：

- `total_ms`：基准是处理器入口的 `started := time.Now()`（`backend/internal/api/relay_handler.go:127`），
  落库处 `int(time.Since(started).Milliseconds())`；
- `first_byte_ms`：
  - 流式分支（`backend/internal/api/relay_handler.go:305-423` `streamToClient`）基准是
    **本次上游尝试**的 `att.StartedAt`（`backend/internal/relay/forward.go:169`），在读循环
    首次读到正文时打点（`relay_handler.go:340-342`）—— 也就是「首个正文正文字节到达时刻」；
  - 非流式分支（`relay_handler.go:273-290`）落库的是 `att.HeaderMs`，即响应头到达时刻，
    那时整段生成已经完成。
- 落库时原样写进 `first_byte_ms` / `total_ms`（`backend/internal/relay/logwriter.go:276-277`）。

还有一个容易混淆的点：**日志里的 `stream` 字段是「客户端要不要流式」，而分不分流式分支
看的是上游响应的 Content-Type**（`forward.go:276` `isEventStream`）。两者不是一回事 ——
客户端要流式、上游却整体回 JSON 时，`stream=true` 但走的是非流式分支，此时
`first_byte_ms` 就是 `upstream_ms`（同一个 `att.HeaderMs`，逐整数相等，见 `logwriter.go:276,291`）。
这条恒等式是本事故里区分两条分支的判据。

## 三、证据链（生产库实测）

1. **不是估算在作怪**：109 行 >1000 tok/s 全部是 `is_stream = true`，其中只有 1 行
   `usage_estimated`；分子（完成词元）中位数 76，分母（总耗时 − 首字）中位数只有 **16ms**、
   最小 **4ms**。
2. **形态是 `first_byte_ms ≈ total_ms`**：id 1427（智谱Pro / glm-5.3-flash）输出 366 词元、
   总耗时 7395ms、首字 7388ms → 分母 7ms → **52286 tok/s**；同类的还有 id 1715（677 / 12743 / 12730）、
   id 1428（303 / 6627 / 6621）、id 1564（601 / 7397 / 7385）、id 1571（864 / 18084 / 18005）。
3. **规模**：全库流式行里 323 行（14.5%）的首字落在总耗时 95% 之后，也就是首字之后
   只剩不到 5% 的时间。这批行的「输出 ÷ 总耗时」落在 **1–81 tok/s**，与同渠道同模型的
   正常行一致 —— 分子没变，是分母塌了。
4. **判别两条分支**（用第 2 节的恒等式）：这 109 行里 45 行 `first_byte_ms = upstream_ms`
   （两种解释都成立），**64 行 `first_byte_ms = upstream_ms + ≥1ms`** —— 非流式分支不可能出现
   这个差值（落库的 first_byte 就是 `att.HeaderMs`），所以这些行确实是流式分支，
   **上游回的确实是 SSE**，只是正文整块到达。
5. **不是站上的出口代理在缓冲**：全部渠道 `proxy_id = 0`（没有出口代理）；而
   `backend/internal/relay/convert/upstream.go:235-246` 的 `UpstreamStream` 对 openai-chat
   上游是**零拷贝透传**（default 分支直接返回 `resp.Body`），首字节打点就在这个 body 的
   读循环上 —— 打点是忠实的，链路里没有中间层。
6. **报文留存样本印证「响应头也晚到」**：id 749（502，总耗时 3460ms，上游握手 3266ms）
   与 id 1115（502，总耗时 28583ms，上游握手 28443ms）的响应 `Content-Type` 都是
   `text/event-stream`、正文为空 —— 上游的**响应头本身**就压到很晚才发，不是正文传输被缓冲。
7. **与「没有推理词元」强相关**：智谱Pro / glm-5.3-flash 的 84 行塌缩行
   `reasoning_tokens` **全为 0**，而同渠道同模型正常的 216 行里 208 行有推理词元；
   全库塌缩行里 323 行有 319 行推理词元为 0。同渠道、同模型、同一分钟内两种形态交替出现，
   且这些行没有任何同渠道并发重叠（109 行 **0 行**有并发；正常行 19.5% 有）——
   说明是上游侧对每次请求的路由差异，不是本站的并发或排队。

## 四、结论

**不是中继算错，也不是词元数算错，是上游对部分请求没有逐帧返回。** 上游把整段正文
（连同响应头）压到生成结束才一次性发出，于是「总耗时 − 首字」量到的是尾包传输（几毫秒），
而分子是整段输出的词元数 —— 速度被放大了三到四个数量级。中继无法控制上游的交付方式
（那是上游侧的内部行为，需要上游探针才能进一步定位），能做的是**让指标自身对这种形态鲁棒**。

## 五、决策：显示层加「首字窗口塌缩」判据

`窗口 < 总耗时的 1/20` 时不用窗口做分母，退回总耗时，并且悬停说明改写为
「整段耗时 X（上游未逐帧返回正文）」。

- **为什么不显示「—」（当不可测）**：非流式行早就有同类取舍 —— 生成时段量不到，
  就用总耗时近似 + 悬停说明（组件里原注释：「口径不同的分叉必须让用户看得见」）。
  塌缩行沿用同一条路：整段耗时口径给出的数一点不假，只是分母不是生成时段。
  改成「—」会让 14.5% 的流式行失去速度信息。
- **为什么不限幅**（把 >N 的值截到 N）：那会造出一个没有定义的数，比不显示更误导。
- **阈值为什么是比例而不是绝对毫秒**：生产数据上「窗口 < 总耗时 5%」（命中 323 行）
  与「窗口 < 200ms」（命中 221 行）两种规则给出的 p99 / 最大值**完全一致**
  （370 / 1069 tok/s），结论不依赖阈值取在哪一刀。取 5% 是因为它恰好是「首字落在
  总耗时 95% 之后」这个可解释的形态边界，而三者时间戳的分辨率就是毫秒。
- **窗口非正**（首字与收尾记在同一毫秒，或重试链路里首字晚于落库的总耗时）也按塌缩处理：
  那时窗口里没有任何可用的生成时段。

## 六、实现

- 新增 `frontend/src/components/speed.ts`：纯函数 `speedOf(row)` / `speedText(row)`，
  三种分母用 `kind` 区分（`firstByte` / `total` / `burst`），阈值只有一处定义。
  理由与 `latency.ts`、`logColumns.ts` 一致：判据埋在组件里就测不到。
- 新增 `frontend/src/components/speed.spec.ts`：14 个用例，**用例数据直接抄生产库真实行**
  （id 1427 / 1715 / 1428 / 1564 / 1571 …），另钉边界（窗口恰好 = 总耗时 5% 不收、
  少 1ms 就收）、非流式、算不出的情形。
- `frontend/src/components/RequestLogPanel.vue` 的 `tokPerSec` / `speedTitle` 改为调用该模块，
  三种分母对应三种悬停文案；列表与详情两个渲染点都不用动。
- 速度列的宽度契约不受影响：`.spd` 的 66px 是按四位数定的（`docs/ui-spec.md` 第 18 条），
  修后最大值 1069 正好是四位数 —— 修前的 52286 反而超了这一档。

## 七、验证

```bash
cd frontend && npm run verify      # type-check + 契约检查 + vitest + 构建，全通过
```

复算（服务器上只读查询，`request_logs` 的流式可算行）：

```sql
WITH s AS (
  SELECT completion_tokens AS out, total_ms AS tot, COALESCE(first_byte_ms,0) AS fb,
         CASE WHEN (total_ms - COALESCE(first_byte_ms,0)) >= 0.05 * total_ms
              THEN total_ms - COALESCE(first_byte_ms,0) ELSE total_ms END AS denom
  FROM request_logs WHERE is_stream AND completion_tokens > 0 AND total_ms > 0
)
SELECT count(*), percentile_disc(0.99) WITHIN GROUP (ORDER BY out/(denom/1000.0)),
       round(max(out/(denom/1000.0))) FROM s;
-- 修后：n=2265, p99=370.5, max=1069   （把 denom 换成 tot-fb 即修前口径：p99=12100, max=52286）
```

## 八、已知边界（未修）

1. **部分缓冲分辨不出来**：上游分几次大块 flush（而不是一次）时，窗口占比可能仍然很大，
   而前端只有「首字」与「总耗时」两个数，分不出「分块缓冲」与「真的这么快」。修后剩下的
   5 行 >400 tok/s 就是这一类：id 1219（GOAT / deepseek-v4.1-flash，7344 词元 / 10.715s，
   首字 3.845s，窗口占 64% → 1069 tok/s）与 WorkBuddy / deepseek-v4.1-flash 的 4 行
   （461 / 446 / 417 / 407，窗口占 26%–57%）。要根治得让后端在流式读循环里记**尾包时刻
   或分块数**并落库（库表 + 接口 + 前端三处连锁改造），本次不做。
2. **分母偏大的一侧仍在**：`total_ms` 的基准是处理器入口（`relay_handler.go:127`），
   早于本次上游尝试的 `att.StartedAt`（`forward.go:169`），所以并发闸门排队与重试退避
   的时间被算进了「首字后生成」窗口 —— 重试过的请求速度被系统性算低。方向与本次事故
   相反（算低不算高），且改动要动基准口径，留作后续。

## 九、不做的事

- 不在前端「猜」上游是否逐帧返回：那不是前端能测到的事实，只能按可观测的两个时间戳近似。
- 不动后端打点：`first_byte_ms` 是忠实的观测值，问题在解读方式。
- 不改 `docs/ui-spec.md` 第 18 条：它记的是当时的实测（最大 7000、p99 268），仍然成立。
