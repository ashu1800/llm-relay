export interface Channel {
  id: number
  name: string
  group_id: number
  /** 上游协议：客户端无论用哪种协议，都会按它转成上游格式 */
  protocol: string
  base_url: string
  /** 记账币种（CNY / USD）：价格按它录入，日志与看板也按它统计 */
  currency: string
  api_key_hint: string
  weight: number
  /** 走哪个出站代理转发；0 = 直连 */
  proxy_id: number
  /** 渠道图标：data URI / 图片地址 / 一两个字符；空表示用默认图标 */
  icon: string
  enabled: boolean
  monitor_type: string
  available_slots: SlotRule[] | null
  extra_config: Record<string, unknown> | null
  custom_mapping: Record<string, unknown> | null
  health_status: string
  last_error: string
  last_checked_at: string | null
  created_at: string
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

export interface ChannelGroup {
  id: number
  name: string
  remark: string
  strategy: string
  is_default: boolean
  enabled: boolean
  /** 胶囊颜色（#rrggbb）；空表示按分组名自动配色 */
  color: string
  /** 每分钟请求数上限，0 = 不限制 */
  rpm: number
  /** 每分钟 token 数上限，0 = 不限制 */
  tpm: number
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
export interface Proxy {
  id: number
  name: string
  /** socks5 / http / https；https 表示用 TLS 连到代理本身 */
  protocol: string
  host: string
  port: number
  username: string
  has_password: boolean
  enabled: boolean
  /** 勾选「用于自动更新」：版本检测与更新下载走这个代理（单选互斥，后端保证） */
  for_update: boolean
  /** unknown / ok / fail —— 最近一次连通性测试的结论 */
  last_status: string
  last_latency_ms: number
  last_error: string
  last_tested_at: string | null
  created_at: string
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
export interface ChannelBinding {
  id: number
  channel_id: number
  public_name: string
  upstream_name: string
  enabled: boolean
  /** 这一个模型走哪个代理；0 = 跟随渠道 */
  proxy_id: number
  // 价格挂在这一行上：同一个模型名在不同渠道成本不同，
  // 全局一份价只能取其一（原来的做法），账就对不上了
  input_per_1m: string
  output_per_1m: string
  cache_read_per_1m: string
  cache_write_per_1m: string
  /** 固定倍率，1 = 原价；没有时段命中时用它 */
  multiplier: number
  /** 倍率时段：命中时用该时段的倍率（优先于固定倍率） */
  peak_rules: RateRule[] | null
}

export interface APIKey {
  // 0 表示沿用全局默认；负数表示这把密钥完全不限流
  rate_limit_rpm: number
  id: number
  name: string
  key_prefix: string
  enabled: boolean
  allowed_models: string[] | null
  allowed_groups: string[] | null
  last_used_at: string | null
  created_at: string
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

export interface RequestLog {
  id: number
  trace_id: string
  api_key_name: string
  channel_id: number
  channel_name: string
  group_id: number
  inbound_protocol: string
  upstream_protocol: string
  model_requested: string
  model_upstream: string
  /**
   * 这次调用是否被渠道的「默认模型映射」接下的：model_requested 是客户端
   * 请求的名字（没命中任何白名单），model_upstream 才是真正发给上游的默认模型。
   * 开了兜底之后客户端把模型名写错也不再报错，这个标记是唯一的发现途径。
   */
  fallback_mapped?: boolean
  /** 入站思考参数的归一档位（off/minimal/low/medium/high/on/auto）；空 = 请求没带思考参数 */
  thinking_level?: string
  stream: boolean
  status_code: number
  error: string
  retry_count: number
  /**
   * 故障转移链路的逐次失败尝试（渠道/状态码/错误/用量），随日志快照。
   * 失败尝试的 token 上游可能照收（context-length-exceeded 的 400 就是典型），
   * 详情里看得见它，账面对不上上游账单时才有线索。没有失败尝试时为空。
   */
  retry_trail?: { steps?: RetryTrailStep[] } | null
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cached_tokens: number
  cache_creation_tokens: number
  reasoning_tokens: number
  usage_estimated: boolean
  estimated_cost: string
  /** 这笔账的币种，随日志一起快照（渠道改币种不会改写历史） */
  cost_currency: string
  /** 计价快照：命中时刻的单价与倍率，日后改价不会影响历史账目 */
  pricing_snapshot?: Record<string, any> | null
  first_byte_ms: number
  total_ms: number
  upstream_ms: number
  client_ip: string
  created_at: string
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

// 渠道协议选项，与后端 model.Protocol* 常量保持一致
export const PROTOCOLS = [
  { value: 'openai-chat', label: 'OpenAI Chat Completions' },
  { value: 'openai-responses', label: 'OpenAI Responses' },
  { value: 'anthropic-messages', label: 'Anthropic Messages' },
  { value: 'gemini-generateContent', label: 'Gemini generateContent' },
  { value: 'openai-embeddings', label: 'OpenAI Embeddings' },
  { value: 'custom', label: '自定义（可配置适配器）' }
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
