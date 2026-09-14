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
  stream: boolean
  status_code: number
  error: string
  retry_count: number
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
