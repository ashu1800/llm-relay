export interface Channel {
  id: number
  name: string
  group_id: number
  /** 上游协议：客户端无论用哪种协议，都会按它转成上游格式 */
  protocol: string
  base_url: string
  api_key_hint: string
  weight: number
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

/** 渠道的模型白名单条目：对外名 → 上游名（留空则同名） */
export interface ChannelBinding {
  id: number
  channel_id: number
  public_name: string
  upstream_name: string
  enabled: boolean
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

export interface Pricing {
  id: number
  model_key: string
  match_type: string
  currency: string
  input_per_1m: string
  output_per_1m: string
  cache_read_per_1m: string
  cache_write_per_1m: string
  /** 倍率时段：命中时用该时段的倍率（优先于固定倍率） */
  peak_rules: RateRule[] | null
  /** 固定倍率，1 = 原价；没有时段命中时用它 */
  multiplier: number
  active: boolean
  updated_at: string
}

// 渠道协议选项，与后端 model.Protocol* 常量保持一致
export const PROTOCOLS = [
  { value: 'openai-chat', label: 'OpenAI Chat Completions' },
  { value: 'openai-responses', label: 'OpenAI Responses' },
  { value: 'anthropic-messages', label: 'Anthropic Messages' },
  { value: 'gemini-generateContent', label: 'Gemini generateContent' },
  { value: 'openai-embeddings', label: 'OpenAI Embeddings' },
  { value: 'custom', label: '自定义（可配置适配器）' }
]

export const STRATEGIES = [
  { value: 'weighted', label: '加权随机' },
  { value: 'round_robin', label: '轮询' },
  { value: 'least_latency', label: '延迟优先' },
  { value: 'failover', label: '顺序故障转移' }
]
