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
//
// 曾经还有一个 StrategyWeighted（"weighted"，加权随机）：那时 weight 是抽签
// 份额，权重越大越容易被抽中。现在 weight 改为**组内优先级序号**（1..N 连续
// 唯一，越小越优先，见 Channel.Weight），抽签语义与「越靠前越优先」正好相反，
// 已经去掉。老库里 strategy='weighted' 的行由迁移改成 failover。
const (
	StrategyRoundRobin   = "round_robin"
	StrategyLeastLatency = "least_latency"
	StrategyFailover     = "failover"
)

// LegacyStrategyWeighted 是已废弃的策略值，只用于识别老数据。
// 迁移会把它改成 failover；normalizeStrategy 也会把任何不认识的值收敛到
// failover —— 否则库里留着一个前端下拉里没有的值，分组表单会显示空白。
const LegacyStrategyWeighted = "weighted"

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
	RPM int `gorm:"not null;default:0" json:"rpm"`
	TPM int `gorm:"not null;default:0" json:"tpm"`
	// DailyBudget 是分组每日花费预算，按币种分别设额：
	// {"CNY": 50, "USD": 10}。空 / 缺币种 = 该币种不限。
	//
	// 预算直接作为分组的属性而不是独立的一张表：分组本来就是计费的
	// 自然边界（渠道价格按渠道币种记账，渠道挂在分组下），而且这样
	// 编辑走分组现有的表单与接口，不需要一整套 CRUD。
	//
	// 金额**不做跨币种折算**（与看板金额同一铁律：人民币和美元之间
	// 没有汇率就不该有相加），超支判定逐币种独立进行。目前它只是
	// 一个提醒（80% / 100% 时经 live 推送各喊一次，每天每档一次），
	// 不拦截请求 —— 预算配错一刀切断会全站瘫痪，还会腰斩流式响应。
	DailyBudget JSONMap   `gorm:"type:jsonb" json:"daily_budget"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
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
	GroupID    uint   `gorm:"index;not null;default:1;uniqueIndex:idx_group_weight,priority:1" json:"group_id"`
	Protocol   string `gorm:"size:32;not null;default:openai-chat" json:"protocol"`
	BaseURL    string `gorm:"size:512;not null" json:"base_url"`
	APIKeyEnc  string `gorm:"size:2048" json:"-"`
	APIKeyHint string `gorm:"size:32" json:"api_key_hint"`
	// Weight 是**组内优先级序号**：1..N 连续且唯一，值越小越优先。
	//
	// 它不再是「加权随机」的抽签份额（那个策略已下线），而是故障转移顺序：
	// 转发时每轮取候选链首个（见 relay.Service.Relay），候选按本列升序排列，
	// 于是 weight=1 先试、失败换 weight=2，依此类推。
	//
	// 由此产生两条不变量，不要绕过它们直接写这一列：
	//   - 同一分组内 weight 唯一且连续 —— 由 idx_group_weight 这个唯一索引
	//     在数据库层兜底（AutoMigrate 创建，回填见 store.normalizeChannelWeights）
	//   - 增删渠道、换分组之后要重排，别留空洞。统一走
	//     api.renumberGroupChannels / api.applyChannelOrder，它们在事务里
	//     先整体错位再赋 1..N，避免唯一索引在中途瞬时冲突
	Weight int `gorm:"not null;default:1;uniqueIndex:idx_group_weight,priority:2" json:"weight"`
	// Currency 是这家上游给用户开账单用的币种（见 model.NormalizeCurrency）。
	// 价格、日志金额、看板金额都按它计量 —— 同一个模型名在人民币渠道与
	// 美元渠道上本来就是两笔不同的钱，不能合成一个数。
	//
	// default:USD 是给已有数据兜底：加这一列之前，全站金额都按美元口径
	// 录入与展示，老行保持 USD 才是它们本来的语义；新渠道用什么币种由
	// 用户在渠道表单里显式选择。
	Currency string `gorm:"size:8;not null;default:USD" json:"currency"`
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
	Enabled bool `gorm:"not null" json:"enabled"`

	// ---- 价格 ----
	//
	// 价格随模型条目一起配置，而不是单独维护一张「模型 → 价格」的表：
	// 同一个模型名在不同渠道的成本本来就不一样（官网直连 vs 中转站），
	// 全局一份价只能取其一，用户还得自己在脑子里记住哪条渠道该按哪个价算。
	//
	// 单位是每 100 万 token 的价格，**币种由所属渠道的 Currency 决定**
	// （人民币渠道就填人民币数字），日志与看板都按它显示。
	// 四个单价全为 0 表示「这个模型还没配价」，界面上会点名提醒 ——
	// 缺价的直接后果是这笔调用被记成 0 元，而账面上完全看不出异常。
	InputPer1M      decimal.Decimal `gorm:"column:input_per1_m;type:numeric(18,8);default:0" json:"input_per_1m"`
	OutputPer1M     decimal.Decimal `gorm:"column:output_per1_m;type:numeric(18,8);default:0" json:"output_per_1m"`
	CacheReadPer1M  decimal.Decimal `gorm:"column:cache_read_per1_m;type:numeric(18,8);default:0" json:"cache_read_per_1m"`
	CacheWritePer1M decimal.Decimal `gorm:"column:cache_write_per1_m;type:numeric(18,8);default:0" json:"cache_write_per_1m"`
	// Multiplier 是固定倍率（1 = 原价），0 表示「没配」，由代码归一到 1。
	//
	// 这一列**要**保留 default:0，与 ModelPricing 当年相反：那一列的问题是
	// 「用户填的 0 会被列默认值顶掉」，而这一列的 0 恰好就是想要的语义
	// （引擎只在 multiplier > 0 且 != 1 时才用它）。
	// 反过来，NOT NULL 且无默认会留下一个很难查的坑：每一条原生 INSERT
	// 都必须显式给值，漏写就直接违反约束 —— 实测踩过：级联删除的测试用例
	// 插白名单时没写 multiplier，插入静默失败，最后报出来的却是
	// 「删除前绑定数 期望 1 实际 0」这种指错方向的断言。
	Multiplier float64 `gorm:"column:multiplier;type:numeric(10,4);not null;default:0" json:"multiplier"`
	// PeakRules 是时段倍率规则（命中时段时以它为准，优先于固定倍率）
	PeakRules JSONList `gorm:"type:jsonb" json:"peak_rules"`

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

// APIKey 对外下发的调用密钥。鉴权只认哈希，明文另外用主密钥加密存一份。
//
// 为什么不只存哈希：那是最安全的做法，但用户一旦丢了明文就只能重建密钥 ——
// 而「回看一眼自己发出去的密钥」是本地自用工具里最常见的需求，
// 每次都要重建会逼着人把明文记在别处，反而更不安全。
// 与渠道密钥（Channel.APIKeyEnc）用同一套加密与同一条原则：
// 密文不随列表接口下发，只有显式查看时才解密，且带 json:"-" 防止误泄漏。
type APIKey struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	Name    string `gorm:"size:64;not null" json:"name"`
	KeyHash string `gorm:"size:64;uniqueIndex;not null" json:"-"`
	// KeyEnc 是加密后的密钥明文。老数据没有这一列（升级前创建的密钥），
	// 那种情况下明文确实找不回来了，界面上如实说明而不是显示半截。
	KeyEnc        string     `gorm:"type:text" json:"-"`
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
	// FallbackMapped 表示这次调用是被渠道的「默认模型映射」接下的：
	// ModelRequested 是客户端请求的名字（没命中任何白名单），
	// ModelUpstream 才是真正发给上游的默认模型名。
	//
	// 这个标记是**唯一的发现途径**：开了兜底之后，客户端把模型名写错
	// 也不再得到 502（会被兜底悄悄接走），只有靠它才能回答
	// 「有多少请求其实没命中白名单」。**刻意不带 gorm default** ——
	// 带默认值的 bool 在零值时会被 GORM 从 INSERT 里整列省掉（见
	// api/channels_test.go 的 TestBooleanFieldsHaveNoGormDefault）。
	FallbackMapped bool `json:"fallback_mapped"`
	// ThinkingLevel 是入站请求体里思考参数的归一档位
	// （off/minimal/low/medium/high/on/auto，见 relay.ExtractThinkingLevel）。
	// 空串 = 请求没带思考参数 —— 不是「关」，是「没说」，前端据此显示 —。
	ThinkingLevel string `gorm:"size:16" json:"thinking_level"`
	IsStream      bool   `json:"stream"`
	StatusCode    int    `json:"status_code"`
	Error         string `gorm:"size:1024" json:"error"`
	RetryCount    int    `json:"retry_count"`

	PromptTokens        int  `json:"prompt_tokens"`
	CompletionTokens    int  `json:"completion_tokens"`
	TotalTokens         int  `json:"total_tokens"`
	CachedTokens        int  `json:"cached_tokens"`
	CacheCreationTokens int  `json:"cache_creation_tokens"`
	ReasoningTokens     int  `json:"reasoning_tokens"`
	UsageEstimated      bool `json:"usage_estimated"`

	EstimatedCost decimal.Decimal `gorm:"type:numeric(18,8);default:0" json:"estimated_cost"`
	// CostCurrency 是这笔账的币种：取下发请求时那条渠道的 Currency，随日志
	// 一起快照下来。**不能靠 join channels 现查** —— 渠道换个币种，历史账目
	// 就被整体改写了（与 PricingSnapshot 是同一个道理）。
	// default:USD 同上：加列之前的历史行都是按美元口径算出来的。
	CostCurrency    string  `gorm:"size:8;not null;default:USD" json:"cost_currency"`
	PricingSnapshot JSONMap `gorm:"type:jsonb" json:"pricing_snapshot"`

	FirstByteMs int       `json:"first_byte_ms"`
	TotalMs     int       `json:"total_ms"`
	UpstreamMs  int       `json:"upstream_ms"`
	ClientIP    string    `gorm:"size:64" json:"client_ip"`
	CreatedAt   time.Time `gorm:"index:idx_log_created,priority:1" json:"created_at"`
	// RetryTrail 是故障转移链路的逐次尝试摘要（渠道/状态码/错误/用量），
	// 与 PricingSnapshot 同理存快照。失败尝试的 token 上游可能照收
	// （context-length-exceeded 的 400 就是典型），详情里看得见，
	// 账面对不上上游账单时才有线索。没有失败尝试时为空。
	RetryTrail JSONMap `gorm:"type:jsonb" json:"retry_trail"`
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

// Setting 键值型系统设置。
type Setting struct {
	Key       string    `gorm:"primaryKey;size:64" json:"key"`
	Value     JSONMap   `gorm:"type:jsonb" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}
