export interface Channel {
  id: number
  name: string
  group_id: number
  provider_id: number
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
}

// SlotRule 描述渠道可用时段。结束时间早于开始时间表示跨午夜。
export interface SlotRule {
  days: number[]
  start: string
  end: string
}

export interface ChannelGroup {
  id: number
  name: string
  remark: string
  strategy: string
  is_default: boolean
  enabled: boolean
}

export interface ModelItem {
  id: number
  public_name: string
  provider_id: number
  description: string
  enabled: boolean
  created_at: string
}

export interface Provider {
  id: number
  code: string
  name: string
}

export interface ChannelBinding {
  id: number
  channel_id: number
  model_id: number
  upstream_name: string
  public_name: string
  enabled: boolean
}

export interface APIKey {
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
  provider_id: number
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
