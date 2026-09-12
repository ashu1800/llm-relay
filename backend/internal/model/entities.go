package model

import (
	"time"

	"github.com/shopspring/decimal"
)

// Protocol 常量：渠道支持的出站协议类型。
const (
	ProtocolOpenAIChat      = "openai-chat"
	ProtocolOpenAIResponses = "openai-responses"
	ProtocolAnthropic       = "anthropic-messages"
	ProtocolGemini          = "gemini-generateContent"
	ProtocolEmbeddings      = "openai-embeddings"
	ProtocolCustom          = "custom"
)

// 分组路由策略。
const (
	StrategyWeighted     = "weighted"
	StrategyRoundRobin   = "round_robin"
	StrategyLeastLatency = "least_latency"
	StrategyFailover     = "failover"
)

// 报文留存策略。
const (
	PayloadStoreAll    = "all"
	PayloadStoreErrors = "errors"
	PayloadStoreNone   = "none"
)

// Provider 模型商（openai / deepseek / 自定义）。
type Provider struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	Code       string    `gorm:"size:32;uniqueIndex;not null" json:"code"`
	Name       string    `gorm:"size:64;not null" json:"name"`
	PricingURL string    `gorm:"size:512" json:"pricing_url"`
	Enabled    bool      `gorm:"not null;default:true" json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ChannelGroup 渠道分组，决定一组渠道的路由策略。
type ChannelGroup struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:64;uniqueIndex;not null" json:"name"`
	Remark    string    `gorm:"size:255" json:"remark"`
	Strategy  string    `gorm:"size:32;not null;default:weighted" json:"strategy"`
	IsDefault bool      `gorm:"not null;default:false" json:"is_default"`
	Enabled   bool      `gorm:"not null;default:true" json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Channel 上游渠道。APIKeyEnc 存放 AES-GCM 密文，不随 JSON 输出。
//
// ProviderID 是这条渠道对接的模型商，0 表示未指定 —— 聚合站（OpenRouter、
// 各类中转）本来就不专属于某一家，不该硬凑一个模型商。
// 它刻意不留 gorm 的 default 标签：带默认值的字段在值为零时会被 GORM 从 INSERT
// 里省掉、转而落库成列默认值，0 就永远存不进去 —— 结果是「没选过的渠道」
// 全被标成 OpenAI，徽标显示的和实际不符。
type Channel struct {
	ID          uint     `gorm:"primaryKey" json:"id"`
	Name        string   `gorm:"size:128;not null" json:"name"`
	GroupID     uint     `gorm:"index;not null;default:1" json:"group_id"`
	ProviderID  uint     `gorm:"index;not null" json:"provider_id"`
	Protocol    string   `gorm:"size:32;not null;default:openai-chat" json:"protocol"`
	BaseURL     string   `gorm:"size:512;not null" json:"base_url"`
	APIKeyEnc   string   `gorm:"size:2048" json:"-"`
	APIKeyHint  string   `gorm:"size:32" json:"api_key_hint"`
	Weight      int      `gorm:"not null;default:1" json:"weight"`
	Enabled     bool     `gorm:"not null;default:true" json:"enabled"`
	MonitorType string   `gorm:"size:32;not null;default:none" json:"monitor_type"`
	Slots       JSONList `gorm:"type:jsonb" json:"available_slots"`
	ExtraConfig JSONMap  `gorm:"type:jsonb" json:"extra_config"`
	// 列名显式写成 custom_map：字段名与 json 名不一致，
	// 不显式声明的话写原生列映射时极易误用 custom_mapping
	CustomMap     JSONMap    `gorm:"column:custom_map;type:jsonb" json:"custom_mapping"`
	HealthStatus  string     `gorm:"size:32;not null;default:unknown" json:"health_status"`
	LastError     string     `gorm:"size:1024" json:"last_error"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Model 对外暴露的模型名。
type Model struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	PublicName  string    `gorm:"size:128;uniqueIndex;not null" json:"public_name"`
	ProviderID  uint      `gorm:"index;not null;default:1" json:"provider_id"`
	Description string    `gorm:"size:255" json:"description"`
	Enabled     bool      `gorm:"not null;default:true" json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ChannelModel 渠道与模型的绑定，含上游真实模型名（对外名可与原生名不同）。
type ChannelModel struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	ChannelID    uint      `gorm:"uniqueIndex:idx_channel_model;not null" json:"channel_id"`
	ModelID      uint      `gorm:"uniqueIndex:idx_channel_model;not null" json:"model_id"`
	UpstreamName string    `gorm:"size:128;not null" json:"upstream_name"`
	Enabled      bool      `gorm:"not null;default:true" json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ChannelTemplate 渠道模板，用于快速填充常见供应商配置。
type ChannelTemplate struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:128;uniqueIndex;not null" json:"name"`
	Protocol    string    `gorm:"size:32;not null" json:"protocol"`
	BaseURL     string    `gorm:"size:512" json:"base_url"`
	GroupID     uint      `json:"group_id"`
	ExtraConfig JSONMap   `gorm:"type:jsonb" json:"extra_config"`
	CustomMap   JSONMap   `gorm:"column:custom_map;type:jsonb" json:"custom_mapping"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// APIKey 对外下发的调用密钥。只存哈希与展示前缀，明文仅在创建时返回一次。
type APIKey struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	Name          string     `gorm:"size:64;not null" json:"name"`
	KeyHash       string     `gorm:"size:64;uniqueIndex;not null" json:"-"`
	KeyPrefix     string     `gorm:"size:32" json:"key_prefix"`
	Enabled       bool       `gorm:"not null;default:true" json:"enabled"`
	AllowedModels StringList `gorm:"type:jsonb" json:"allowed_models"`
	// RateLimitRPM 是该密钥每分钟允许的请求数；0 表示用全局默认值，负值表示不限
	RateLimitRPM  int        `gorm:"not null;default:0" json:"rate_limit_rpm"`
	AllowedGroups StringList `gorm:"type:jsonb" json:"allowed_groups"`
	LastUsedAt    *time.Time `json:"last_used_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// RequestLog 单次调用明细。金额只做成本感知，不参与任何扣减。
type RequestLog struct {
	ID             uint   `gorm:"primaryKey" json:"id"`
	TraceID        string `gorm:"size:64;index" json:"trace_id"`
	APIKeyID       uint   `gorm:"index" json:"api_key_id"`
	APIKeyName     string `gorm:"size:64" json:"api_key_name"`
	ChannelID      uint   `gorm:"index" json:"channel_id"`
	ChannelName    string `gorm:"size:128" json:"channel_name"`
	GroupID        uint   `gorm:"index" json:"group_id"`
	ProviderID     uint   `gorm:"index" json:"provider_id"`
	InboundProto   string `gorm:"size:32" json:"inbound_protocol"`
	UpstreamProto  string `gorm:"size:32" json:"upstream_protocol"`
	ModelRequested string `gorm:"size:128;index" json:"model_requested"`
	ModelUpstream  string `gorm:"size:128" json:"model_upstream"`
	IsStream       bool   `json:"stream"`
	StatusCode     int    `json:"status_code"`
	Error          string `gorm:"size:1024" json:"error"`
	RetryCount     int    `json:"retry_count"`

	PromptTokens        int  `json:"prompt_tokens"`
	CompletionTokens    int  `json:"completion_tokens"`
	TotalTokens         int  `json:"total_tokens"`
	CachedTokens        int  `json:"cached_tokens"`
	CacheCreationTokens int  `json:"cache_creation_tokens"`
	ReasoningTokens     int  `json:"reasoning_tokens"`
	UsageEstimated      bool `json:"usage_estimated"`

	EstimatedCost   decimal.Decimal `gorm:"type:numeric(18,8);default:0" json:"estimated_cost"`
	PricingSnapshot JSONMap         `gorm:"type:jsonb" json:"pricing_snapshot"`

	FirstByteMs int       `json:"first_byte_ms"`
	TotalMs     int       `json:"total_ms"`
	UpstreamMs  int       `json:"upstream_ms"`
	ClientIP    string    `gorm:"size:64" json:"client_ip"`
	CreatedAt   time.Time `gorm:"index:idx_log_created,priority:1" json:"created_at"`
}

// RequestPayload 请求/响应原始报文，按策略留存。
type RequestPayload struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	LogID           uint      `gorm:"uniqueIndex;not null" json:"log_id"`
	RequestBody     string    `gorm:"type:text" json:"request_body"`
	ResponseBody    string    `gorm:"type:text" json:"response_body"`
	RequestHeaders  JSONMap   `gorm:"type:jsonb" json:"request_headers"`
	ResponseHeaders JSONMap   `gorm:"type:jsonb" json:"response_headers"`
	CreatedAt       time.Time `json:"created_at"`
}

// ModelPricing 模型单价。金额按每 100 万 token 的美元价存储，便于与官网口径对齐。
// PeakRules 描述峰谷时段，例如 DeepSeek 的 peak 时段价格翻倍。
type ModelPricing struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	ProviderID uint   `gorm:"index;not null" json:"provider_id"`
	ModelKey   string `gorm:"size:128;uniqueIndex;not null" json:"model_key"`
	MatchType  string `gorm:"size:16;not null;default:exact" json:"match_type"`
	Currency   string `gorm:"size:8;not null;default:USD" json:"currency"`
	// 列名见 columns.go：GORM 推导出的是 per1_m，与 json 名 per_1m 不同，
	// 这里显式声明以便和原生映射对得上
	InputPer1M      decimal.Decimal `gorm:"column:input_per1_m;type:numeric(18,8);default:0" json:"input_per_1m"`
	OutputPer1M     decimal.Decimal `gorm:"column:output_per1_m;type:numeric(18,8);default:0" json:"output_per_1m"`
	CacheReadPer1M  decimal.Decimal `gorm:"column:cache_read_per1_m;type:numeric(18,8);default:0" json:"cache_read_per_1m"`
	CacheWritePer1M decimal.Decimal `gorm:"column:cache_write_per1_m;type:numeric(18,8);default:0" json:"cache_write_per_1m"`
	PeakRules       JSONList        `gorm:"type:jsonb" json:"peak_rules"`
	Source          string          `gorm:"size:16;not null;default:litellm" json:"source"`
	SourceURL       string          `gorm:"size:512" json:"source_url"`
	Priority        int             `gorm:"not null;default:10" json:"priority"`
	Active          bool            `gorm:"not null;default:true" json:"active"`
	UpdatedAt       time.Time       `json:"updated_at"`
	CreatedAt       time.Time       `json:"created_at"`
}

// PricingSyncLog 定价同步历史。
type PricingSyncLog struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	Source        string     `gorm:"size:32;not null" json:"source"`
	Status        string     `gorm:"size:16;not null" json:"status"`
	Added         int        `json:"added"`
	Updated       int        `json:"updated"`
	Unchanged     int        `json:"unchanged"`
	SkippedManual int        `json:"skipped_manual"`
	Error         string     `gorm:"size:1024" json:"error"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
}

// Setting 键值型系统设置。
type Setting struct {
	Key       string    `gorm:"primaryKey;size:64" json:"key"`
	Value     JSONMap   `gorm:"type:jsonb" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}
