// 前端 API 类型。单一事实源在 Go：backend/internal/model 的结构体经 tygo
// 生成 generated/model.ts（重生成：cd backend && tygo generate，或 make gen-types），
// 本文件把生成实体收口成「接口响应形状」——只在两类地方覆盖生成结果：
//   1. 可空性：Go 指针字段（*time.Time）生成 `field?: string`，但线上 JSON
//      里 nil 指针序列化成 null，前端一律写 `field: string | null`；
//   2. jsonb 窄化：Go 侧存 jsonb 的松散 JSON（JSONMap/JSONList → any），
//      内部结构的知识只存在于前端（SlotRule / RateRule / RetryTrailStep），
//      在这里叠加。Go 新增字段会自动流进这些类型，不再手工双写。
//
// 响应里「实体之外」的聚合字段（today_spent / runtime / models 一类，
// 由列表接口现场计算）也在这里声明 —— 它们不属于任何 Go 实体。
//
// 历史教训（这段别删）：env 变量清单手写 13 条错 6 条、分组策略 label 两处
// 不一致 —— 手工双写漂移在这个项目里发生过不止一次。
import type {
  APIKey as APIKeyEntity,
  Channel as ChannelEntity,
  ChannelGroup as ChannelGroupEntity,
  ChannelModel as ChannelBindingEntity,
  Proxy as ProxyEntity,
  RequestLog as RequestLogEntity
} from './generated/model'
import {
  ProtocolAnthropic,
  ProtocolCustom,
  ProtocolEmbeddings,
  ProtocolGemini,
  ProtocolOpenAIChat,
  ProtocolOpenAIResponses
} from './generated/model'

export interface Channel extends Omit<
  ChannelEntity,
  'available_slots' | 'extra_config' | 'custom_mapping' | 'last_checked_at'
> {
  available_slots: SlotRule[] | null
  extra_config: Record<string, unknown> | null
  custom_mapping: Record<string, unknown> | null
  last_checked_at: string | null
  /** 模型白名单的对外名（列表接口带出，画面上「模型」列用） */
  models?: string[]
  model_count?: number
  /**
   * 最近一次实际承接请求的时间（列表接口从请求日志里聚合出来）。
   * null / 缺省表示日志里查不到记录 —— 可能是这条渠道一直没被用上，
   * 也可能是它的调用早于日志保留期已被清理，两种情况在界面上是同一种呈现。
   */
  last_used_at?: string | null
  /**
   * 运行期状态（列表接口从转发内核的内存状态里取的快照）。
   * health_status 只回答「最近一次成功或失败」，这一块回答
   * 「此刻能不能被路由到」：冷却中的渠道即使 health_status 还是
   * healthy 也不会接活。缺省表示转发内核没在运行（或旧版后端）。
   */
  runtime?: {
    /** 冷却剩余毫秒；0 = 不在冷却中 */
    cooldown_ms: number
    /** 连续失败次数（成功一次清零；达到 3 会触发自动冷却） */
    fail_streak: number
    /** 平滑首包延迟（EWMA 毫秒；0 = 还没有成功观测值） */
    latency_ms: number
    /** 当前在途请求数 */
    inflight: number
  }
}

// SlotRule 描述渠道可用时段。结束时间早于开始时间表示跨午夜。
export interface SlotRule {
  days: number[]
  start: string
  end: string
}

// RateRule 是定价的倍率时段：落在窗口内时用该窗口的 multiplier（优先级高于固定倍率）。
// days 为空表示每天；end 早于 start 表示跨午夜；时间按服务器本地时区判断。
// 多条同时命中时，列表里靠后的那条生效。
export interface RateRule {
  days: number[]
  start: string
  end: string
  multiplier: number
  label: string
}

export interface ChannelGroup extends Omit<ChannelGroupEntity, 'daily_budget'> {
  /**
   * 按币种的日预算（{"CNY": 50}）。逐币种独立判定，不做任何折算
   * （与看板金额同一铁律）。null / 缺币种 = 该币种不限。
   * 只提醒不拦截：80% / 100% 各档每天经 live 推送提醒一次。
   */
  daily_budget?: Record<string, number> | null
  /**
   * 今日各币种已花费金额（列表接口带出，只对配了预算的分组计算）。
   * 与 daily_budget 逐币对照着读。
   */
  today_spent?: Record<string, number>
}

/**
 * Proxy 出站代理。密码只进不出：接口返回的是 has_password，
 * 编辑时留空即表示沿用原密码。
 */
export interface Proxy extends Omit<ProxyEntity, 'last_tested_at'> {
  last_tested_at: string | null
}

/** 代理连通性测试结果 */
export interface ProxyTestResult {
  ok: boolean
  latency_ms: number
  status_code: number
  error: string
  tested_at: string
}

/** 渠道的模型白名单条目：对外名 → 上游名（留空则同名） */
export interface ChannelBinding extends Omit<ChannelBindingEntity, 'peak_rules'> {
  // 价格挂在这一行上：同一个模型名在不同渠道成本不同，
  // 全局一份价只能取其一（原来的做法），账就对不上了
  /** 倍率时段：命中时用该时段的倍率（优先于固定倍率） */
  peak_rules: RateRule[] | null
}

export interface APIKey
  extends Omit<APIKeyEntity, 'allowed_models' | 'allowed_groups' | 'last_used_at'> {
  allowed_models: string[] | null
  allowed_groups: string[] | null
  last_used_at: string | null
}

/** 故障转移链路里的一次失败尝试（后端 relay.AttemptTrail 的落库形态） */
export interface RetryTrailStep {
  channel_id: number
  channel_name: string
  status_code: number
  error: string
  /**
   * 在这一次失败之后、换到下一个渠道之前等待的毫秒数。
   *
   * 只有「下一个候选打在同一台上游」时才会有等待（避免对同一端点连打，
   * 那会被上游当成攻击）。0 或缺失表示没等过 —— 换到了不同上游。
   */
  wait_before_ms?: number
  /** 该次失败尝试上游回报的用量；上游没回报就没有这个字段 */
  usage?: { prompt_tokens: number; completion_tokens: number; total_tokens: number }
}

export interface RequestLog
  extends Omit<RequestLogEntity, 'retry_trail' | 'pricing_snapshot'> {
  /**
   * 故障转移链路的逐次失败尝试（渠道/状态码/错误/用量），随日志快照。
   * 失败尝试的 token 上游可能照收（context-length-exceeded 的 400 就是典型），
   * 详情里看得见它，账面对不上上游账单时才有线索。没有失败尝试时为空。
   */
  retry_trail?: { steps?: RetryTrailStep[] } | null
  /** 计价快照：命中时刻的单价与倍率，日后改价不会影响历史账目 */
  pricing_snapshot?: Record<string, any> | null
}

export interface Paged<T> {
  items: T[]
  total: number
  page?: number
  page_size?: number
}

// Pricing 已删除：价格改为挂在渠道模型的绑定上（见 ChannelBinding 的价格字段）。
// 原来那张「模型名 → 单价」的独立表已经下线，全局一份价没法表达
// 「同一个模型名在不同渠道成本不同」。

// 渠道协议选项。value 不再手写字符串 —— 直接取生成文件里的后端常量，
// 加协议时改 Go 侧（model.Protocol*），这里补 label 即可，值永远对得上。
export const PROTOCOLS: { value: string; label: string }[] = [
  { value: ProtocolOpenAIChat, label: 'OpenAI Chat Completions' },
  { value: ProtocolOpenAIResponses, label: 'OpenAI Responses' },
  { value: ProtocolAnthropic, label: 'Anthropic Messages' },
  { value: ProtocolGemini, label: 'Gemini generateContent' },
  { value: ProtocolEmbeddings, label: 'OpenAI Embeddings' },
  { value: ProtocolCustom, label: '自定义（可配置适配器）' }
]

// 分组路由策略。全站只有这一份定义：GroupsView 以前自己抄了一份，
// 两边的 label 已经不一致（'最低延迟' vs '延迟优先'），同一个策略两个名字。
//
// 「加权随机」（weighted）已下线：那时的 weight 是抽签份额，权重越大越容易被
// 抽中；现在 weight 是渠道在分组内的优先级序号（越小越优先），
// 抽签语义与它正好相反。老数据由后端迁移改成 failover。
export const STRATEGIES = [
  { value: 'failover', label: '顺序故障转移' },
  { value: 'round_robin', label: '轮询' },
  { value: 'least_latency', label: '最低延迟' }
]
