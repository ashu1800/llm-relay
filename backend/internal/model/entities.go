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

// ChannelGroup 渠道分组：一组渠道的路由与权限范围。
//
// 「这个分组能用哪些模型」不由分组自己声明，而由组内渠道的模型白名单决定
// （见 ChannelModel）。分组只回答「走哪批渠道、按什么策略排序、密钥能不能用它」。
type ChannelGroup struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	Name      string `gorm:"size:64;uniqueIndex;not null" json:"name"`
	Remark    string `gorm:"size:255" json:"remark"`
	Strategy  string `gorm:"size:32;not null;default:weighted" json:"strategy"`
	IsDefault bool   `gorm:"not null;default:false" json:"is_default"`
	Enabled   bool   `gorm:"not null" json:"enabled"`
	// Color 是分组在界面上的胶囊颜色（#rgb / #rrggbb）。
	// 空值表示按分组名派生一个稳定颜色（见前端 utils/groupStyle.ts）——
	// 不强制每个人都去挑颜色，但挑过就必须全站一致地用它。
	Color string `gorm:"size:32" json:"color"`
	// RPM / TPM 是这个分组整体的每分钟请求数 / token 数上限，0 表示不限制。
	// 语义见 relay.GroupLimiter：RPM 统计发往上游的请求（含重试），
	// TPM 统计上游回报的实际 token 数。
	//
	// 这两个列有 default 标签，AutoMigrate 给已有数据的表加列时会带上
	// DEFAULT 0，因此不会像 multiplier 那样在升级时把应用卡死。
	RPM       int       `gorm:"not null;default:0" json:"rpm"`
	TPM       int       `gorm:"not null;default:0" json:"tpm"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Channel 上游渠道。APIKeyEnc 存放 AES-GCM 密文，不随 JSON 输出。
//
// 两条约定：
//   - 渠道自己声明能跑哪些模型（ChannelModel = 对外名 → 上游名的白名单与映射），
//     不依赖任何「模型目录」表；
//   - Protocol 是**上游**协议：客户端无论用哪种协议请求，都会先归一成
//     OpenAI Chat，再按这里声明的协议转成上游格式（见 relay/forward.go）。
type Channel struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	Name       string `gorm:"size:128;not null" json:"name"`
	GroupID    uint   `gorm:"index;not null;default:1" json:"group_id"`
	Protocol   string `gorm:"size:32;not null;default:openai-chat" json:"protocol"`
	BaseURL    string `gorm:"size:512;not null" json:"base_url"`
	APIKeyEnc  string `gorm:"size:2048" json:"-"`
	APIKeyHint string `gorm:"size:32" json:"api_key_hint"`
	Weight     int    `gorm:"not null;default:1" json:"weight"`
	// ProxyID 指定这个渠道走哪个出站代理（见 model.Proxy）；0 表示直连。
	// 用 0 而不是 NULL：零值就等于默认行为，能少一层判空；带 default 的列在
	// 已有数据上加列也安全（非空且无默认值时 AutoMigrate 会直接失败）
	ProxyID     uint     `gorm:"not null;default:0" json:"proxy_id"`
	Enabled     bool     `gorm:"not null" json:"enabled"`
	MonitorType string   `gorm:"size:32;not null;default:none" json:"monitor_type"`
	Slots       JSONList `gorm:"type:jsonb" json:"available_slots"`
	ExtraConfig JSONMap  `gorm:"type:jsonb" json:"extra_config"`
	// 列名显式写成 custom_map：字段名与 json 名不一致，
	// 不显式声明的话写原生列映射时极易误用 custom_mapping
	CustomMap JSONMap `gorm:"column:custom_map;type:jsonb" json:"custom_mapping"`
	// Icon 是渠道图标：可以是 data URI（从上游抓来的 favicon）、
	// 任意 http(s) 图片地址，或者用户直接写的一两个字符。
	// 用 text 而不是定长：favicon 转成 base64 动辄几 KB，size:512 会在
	// 保存时被数据库直接拒绝（或静默截断成一个坏图片）
	Icon          string     `gorm:"type:text" json:"icon"`
	HealthStatus  string     `gorm:"size:32;not null;default:unknown" json:"health_status"`
	LastError     string     `gorm:"size:1024" json:"last_error"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// ChannelModel 是渠道的模型白名单条目，同时承担模型映射：
// 客户端请求 PublicName，转发时替换成 UpstreamName（留空则同名）。
//
// 对外模型目录不再单独维护一张表：/v1/models 与路由都直接看这里的白名单，
// 避免出现「模型建好了但没绑渠道」这种两边对不上的状态。
type ChannelModel struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	ChannelID    uint   `gorm:"uniqueIndex:idx_channel_model;not null" json:"channel_id"`
	PublicName   string `gorm:"size:128;uniqueIndex:idx_channel_model;not null" json:"public_name"`
	UpstreamName string `gorm:"size:128;not null" json:"upstream_name"`
	// ProxyID 让单个模型走自己的代理，覆盖渠道级的设置；0 = 跟随渠道。
	// 「同一个渠道里，便宜的模型直连、贵的走代理」这种需求靠渠道级代理做不到
	ProxyID uint `gorm:"not null;default:0" json:"proxy_id"`
	// 不带 gorm default：带默认值的字段在值为零（false）时会被 GORM 从 INSERT
	// 里省掉，转而去用列默认值 true —— 「停用」就永远存不进去，界面上点了保存、
	// 库里依然是启用。与渠道的 ProviderID 是同一个坑。
	Enabled   bool      `gorm:"not null" json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Proxy 出站代理。渠道可以指定走某个代理转发（见 Channel.ProxyID）。
//
// 只支持这三种：socks5 / http / https，其中 https 表示「用 TLS 连到代理本身」。
// 之所以不做「代理池 / 自动切换」：那是另一个量级的东西，
// 而这里的实际需求是「某些上游在国内直连不通」。
type Proxy struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;uniqueIndex;not null" json:"name"`
	// Protocol 见 model.ProxyProtocol* 常量
	Protocol string `gorm:"size:16;not null;default:socks5" json:"protocol"`
	Host     string `gorm:"size:255;not null" json:"host"`
	Port     int    `gorm:"not null" json:"port"`
	Username string `gorm:"size:128" json:"username"`
	// PasswordEnc 是 AES-GCM 密文，不随 JSON 输出。
	// 与渠道密钥同一套加密：代理密码同样是凭据，落到备份文件里必须是密文。
	PasswordEnc string `gorm:"size:512" json:"-"`
	// HasPassword 让界面知道「已经配了密码」而不必回传密文（gorm 不存这一列）
	HasPassword bool `gorm:"-" json:"has_password"`
	// 与渠道同一类坑：带 gorm default 标签的布尔字段存不进 false
	Enabled bool `gorm:"not null" json:"enabled"`
	// 最近一次连通性测试的结果。缓存下来，打开页面就能看到上次的结果，
	// 不必每次进页面都去拨一遍（拨号要花时间，代理不通时更慢）
	LastStatus    string     `gorm:"size:16;not null;default:unknown" json:"last_status"`
	LastLatencyMs int        `gorm:"not null;default:0" json:"last_latency_ms"`
	LastError     string     `gorm:"size:512" json:"last_error"`
	LastTestedAt  *time.Time `json:"last_tested_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// 代理协议常量。
const (
	ProxyProtocolSOCKS5 = "socks5"
	ProxyProtocolHTTP   = "http"
	ProxyProtocolHTTPS  = "https"
)

// APIKey 对外下发的调用密钥。只存哈希与展示前缀，明文仅在创建时返回一次。
type APIKey struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	Name          string     `gorm:"size:64;not null" json:"name"`
	KeyHash       string     `gorm:"size:64;uniqueIndex;not null" json:"-"`
	KeyPrefix     string     `gorm:"size:32" json:"key_prefix"`
	Enabled       bool       `gorm:"not null" json:"enabled"`
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

// ModelPricing 模型单价，全部由用户手工录入。
// 金额按每 100 万 token 的美元价存储，便于与官网口径对齐。
// PeakRules 描述倍率时段（如工作日 9:00-12:00 双倍）。
type ModelPricing struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	ModelKey  string `gorm:"size:128;uniqueIndex;not null" json:"model_key"`
	MatchType string `gorm:"size:16;not null;default:exact" json:"match_type"`
	Currency  string `gorm:"size:8;not null;default:USD" json:"currency"`
	// 列名见 columns.go：GORM 推导出的是 per1_m，与 json 名 per_1m 不同，
	// 这里显式声明以便和原生映射对得上
	InputPer1M      decimal.Decimal `gorm:"column:input_per1_m;type:numeric(18,8);default:0" json:"input_per_1m"`
	OutputPer1M     decimal.Decimal `gorm:"column:output_per1_m;type:numeric(18,8);default:0" json:"output_per_1m"`
	CacheReadPer1M  decimal.Decimal `gorm:"column:cache_read_per1_m;type:numeric(18,8);default:0" json:"cache_read_per_1m"`
	CacheWritePer1M decimal.Decimal `gorm:"column:cache_write_per1_m;type:numeric(18,8);default:0" json:"cache_write_per_1m"`
	PeakRules       JSONList        `gorm:"type:jsonb" json:"peak_rules"`
	// Multiplier 是固定倍率（1 = 原价）。它与时段倍率是「或」的关系：
	// 命中时段规则时用时段倍率，否则用固定倍率（见 pricing.Resolve）。
	// 与 Enabled/Active 同一类坑：不加 gorm default 标签，
	// 0 由代码统一归一到 1，免得「没填」被列默认值悄悄变成别的数
	Multiplier float64 `gorm:"column:multiplier;type:numeric(10,4);not null" json:"multiplier"`
	// 同 ChannelModel.Enabled：带 default 的布尔字段存不进 false
	Active    bool      `gorm:"not null" json:"active"`
	UpdatedAt time.Time `json:"updated_at"`
	CreatedAt time.Time `json:"created_at"`
}

// Setting 键值型系统设置。
type Setting struct {
	Key       string    `gorm:"primaryKey;size:64" json:"key"`
	Value     JSONMap   `gorm:"type:jsonb" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}
