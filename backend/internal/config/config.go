package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是服务的全部配置。加载顺序：默认值 -> YAML 文件 -> 环境变量（最高优先级）。
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Relay    RelayConfig    `yaml:"relay"`
	Security SecurityConfig `yaml:"security"`
	Log      LogConfig      `yaml:"log"`
}

// SecurityConfig 保管加密主密钥、管理台登录密钥与出站凭据。
type SecurityConfig struct {
	// Secret 用于 AES-GCM 加密上游渠道密钥；生产环境务必通过 RELAY_SECRET 注入。
	Secret string `yaml:"secret"`
	// AdminKey 是管理后台的登录密钥（无账号模型：全站只有这一把）。
	// 为空表示关闭管理台登录鉴权 —— 仅建议在「只绑回环」的本地部署下使用；
	// 公网部署必须设置，否则管理接口（含渠道密钥与调用日志）对外裸奔。
	// 密钥本身不落库：启动时只算哈希，登录时常数时间比对。
	AdminKey string `yaml:"admin_key"`
	// SessionTTL 是管理台登录会话的有效期，默认 168h（7 天）。
	// 登录签发的会话令牌在签发时刻就带上了绝对过期时间，进程重启不影响它。
	SessionTTL time.Duration `yaml:"session_ttl"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	Mode string `yaml:"mode"` // debug | release
	// TrustedProxies 是可信反向代理的地址列表，交给 gin.SetTrustedProxies。
	// 登录限流按客户端 IP 计数，而经反代部署时请求的远端地址是代理本身，
	// 真实 IP 在 X-Forwarded-For 里 —— 只有来自可信代理的该头部才会被采信，
	// 否则攻击者可以伪造头部、每个请求换一个假 IP 把限流绕成摆设。
	//
	// 默认只信回环：覆盖最常见的「nginx/caddy 与本服务同机」的部署；
	// 反代在别的机器上时在这里列出它的地址。直连部署（无代理）不受影响。
	// 传空列表表示一个都不信，此时客户端 IP 取 TCP 远端地址。
	TrustedProxies []string `yaml:"trusted_proxies"`
}

// Addr 返回 Gin 监听地址。
func (s ServerConfig) Addr() string { return fmt.Sprintf("%s:%d", s.Host, s.Port) }

type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
	SSLMode  string `yaml:"sslmode"`
	TimeZone string `yaml:"timezone"`
}

// DSN 生成 PostgreSQL 连接串。
func (d DatabaseConfig) DSN() string {
	tz := d.TimeZone
	if tz == "" {
		tz = "UTC"
	}
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s TimeZone=%s",
		d.Host, d.Port, d.User, d.Password, d.DBName, d.SSLMode, tz)
}

type RelayConfig struct {
	UpstreamTimeout    time.Duration `yaml:"upstream_timeout"`
	FirstByteTimeout   time.Duration `yaml:"first_byte_timeout"`
	MaxRetries         int           `yaml:"max_retries"`
	MaxRequestBodyMB   int           `yaml:"max_request_body_mb"`
	LogRetentionDays   int           `yaml:"log_retention_days"`
	PayloadStorageMode string        `yaml:"payload_storage_mode"` // all | errors | none
	PayloadMaxKB       int           `yaml:"payload_max_kb"`       // 单条报文留存上限
	MaxConcurrency     int           `yaml:"max_concurrency"`      // 同时进行的上游请求数上限
	DefaultRPM         int           `yaml:"default_rpm"`          // 每个密钥默认的每分钟请求上限

	// RetrySameUpstreamDelay 是故障转移时、**下一次尝试仍会打到同一台上游**
	// 时的等待时长（0 表示不等待，即本功能引入前的行为）。
	//
	// 为什么只对「同一台上游」生效：两条渠道共用同一个 base_url 时，
	// 「换渠道」在上游看来就是同一个端点又被连打了一次 —— 那是上游判定
	// 我们攻击、直接熔断的形态（详见 relay/backoff.go）。而切到另一个
	// 上游时立刻重试不会给任何一方造成压力，白等只是让用户多等。
	RetrySameUpstreamDelay time.Duration `yaml:"retry_same_upstream_delay"`
}

type LogConfig struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // text | json
}

// Default 返回内置默认值。
func Default() *Config {
	cfg := &Config{
		Server: ServerConfig{Host: "0.0.0.0", Port: 8888, Mode: "release"},
		Database: DatabaseConfig{
			Host: "127.0.0.1", Port: 5432, User: "llmrelay",
			Password: "", DBName: "llm_relay", SSLMode: "disable", TimeZone: "UTC",
		},
		Security: SecurityConfig{
			Secret:     "llm-relay-dev-secret-change-me",
			SessionTTL: 168 * time.Hour,
		},
		Relay: RelayConfig{
			UpstreamTimeout:    300 * time.Second,
			FirstByteTimeout:   120 * time.Second,
			MaxRetries:         2,
			MaxRequestBodyMB:   64,
			LogRetentionDays:   30,
			PayloadStorageMode: "errors",
			PayloadMaxKB:       256,
			MaxConcurrency:     64,
			DefaultRPM:         0,
			// 500ms 对齐 sub2api 的同账号重试基线（见 relay/backoff.go）。
			// 不是 0：默认要能护住「两条渠道共用同一上游」的配置，
			// 否则这个功能等于没开。
			RetrySameUpstreamDelay: 500 * time.Millisecond,
		},
		Log: LogConfig{Level: "info", Format: "text"},
	}
	// TrustedProxies 不能写进上面的字面量后统一返回 —— 切片是可变值，
	// 两个调用方拿到同一底层数组会互相改写；每次现造一份。
	cfg.Server.TrustedProxies = []string{"127.0.0.1", "::1"}
	return cfg
}

// Load 读取配置文件并叠加环境变量覆盖。
func Load(path string) (*Config, error) {
	cfg := Default()

	if path != "" {
		raw, err := os.ReadFile(path)
		if err == nil {
			if err := yaml.Unmarshal(raw, cfg); err != nil {
				return nil, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
		}
	}

	applyEnv(cfg)
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyEnv 让容器编排可以通过环境变量覆盖任意关键配置。
//
// 覆盖率是刻意做全的：Dockerfile 里设了 CONFIG_PATH=/app/config.yaml，
// 但仓库里没有这个文件、编排也没挂载它，而 Load 对「文件不存在」是静默
// 跳过的。也就是说容器部署下，凡是这里没有环境变量入口的字段就**无法调整**，
// 且不会有任何提示。新增配置项时请一并在这里补上入口。
func applyEnv(c *Config) {
	setStr(&c.Server.Host, "SERVER_HOST")
	setInt(&c.Server.Port, "SERVER_PORT")
	setStr(&c.Server.Mode, "GIN_MODE")
	setCsv(&c.Server.TrustedProxies, "SERVER_TRUSTED_PROXIES")

	setStr(&c.Database.Host, "DB_HOST")
	setInt(&c.Database.Port, "DB_PORT")
	setStr(&c.Database.User, "DB_USER")
	setStr(&c.Database.Password, "DB_PASSWORD")
	setStr(&c.Database.DBName, "DB_NAME")
	setStr(&c.Database.SSLMode, "DB_SSLMODE")
	setStr(&c.Database.TimeZone, "DB_TIMEZONE")

	setDuration(&c.Relay.UpstreamTimeout, "RELAY_UPSTREAM_TIMEOUT")
	setDuration(&c.Relay.FirstByteTimeout, "RELAY_FIRST_BYTE_TIMEOUT")
	setInt(&c.Relay.MaxRetries, "RELAY_MAX_RETRIES")
	setInt(&c.Relay.MaxRequestBodyMB, "RELAY_MAX_REQUEST_BODY_MB")
	setInt(&c.Relay.LogRetentionDays, "RELAY_LOG_RETENTION_DAYS")
	setStr(&c.Relay.PayloadStorageMode, "RELAY_PAYLOAD_STORAGE_MODE")
	setInt(&c.Relay.PayloadMaxKB, "RELAY_PAYLOAD_MAX_KB")
	setInt(&c.Relay.MaxConcurrency, "RELAY_MAX_CONCURRENCY")
	setInt(&c.Relay.DefaultRPM, "RELAY_DEFAULT_RPM")
	setDuration(&c.Relay.RetrySameUpstreamDelay, "RELAY_RETRY_SAME_UPSTREAM_DELAY")

	setStr(&c.Security.Secret, "RELAY_SECRET")
	// 管理台登录密钥与会话时长。密钥缺失不报错（保持旧部署可升级），
	// 但「鉴权未启用」会体现在启动日志与 /api/admin/system/info 里，
	// 让界面能持续提醒 —— 与默认加密密钥的处理是同一个思路。
	setStr(&c.Security.AdminKey, "RELAY_ADMIN_KEY")
	setDuration(&c.Security.SessionTTL, "RELAY_SESSION_TTL")

	setStr(&c.Log.Level, "LOG_LEVEL")
	setStr(&c.Log.Format, "LOG_FORMAT")
}

// envKeys 列出所有被 applyEnv 识别的环境变量。
//
// 供 /api/admin/system/info 回给前端渲染「修改位置」提示。原来这份清单在
// SettingsView.vue 里手写了一份，13 条里有 6 条写的是根本不存在的变量名
// （RELAY_LOG_LEVEL、RELAY_UPSTREAM_TIMEOUT…），用户照着设完全没有反应
// 也不会报错。清单放在这里就不会再漂移。
func envKeys() map[string]string {
	return map[string]string{
		"host":                         "SERVER_HOST",
		"port":                         "SERVER_PORT",
		"mode":                         "GIN_MODE",
		"trusted_proxies":              "SERVER_TRUSTED_PROXIES",
		"log_level":                    "LOG_LEVEL",
		"log_format":                   "LOG_FORMAT",
		"upstream_timeout_sec":         "RELAY_UPSTREAM_TIMEOUT",
		"first_byte_timeout_sec":       "RELAY_FIRST_BYTE_TIMEOUT",
		"max_retries":                  "RELAY_MAX_RETRIES",
		"max_request_body_mb":          "RELAY_MAX_REQUEST_BODY_MB",
		"log_retention_days":           "RELAY_LOG_RETENTION_DAYS",
		"payload_storage_mode":         "RELAY_PAYLOAD_STORAGE_MODE",
		"payload_max_kb":               "RELAY_PAYLOAD_MAX_KB",
		"max_concurrency":              "RELAY_MAX_CONCURRENCY",
		"default_rpm":                  "RELAY_DEFAULT_RPM",
		"retry_same_upstream_delay_ms": "RELAY_RETRY_SAME_UPSTREAM_DELAY",
		"secret":                       "RELAY_SECRET",
		"console_auth_enabled":         "RELAY_ADMIN_KEY",
		"session_ttl_hours":            "RELAY_SESSION_TTL",
		"database":                     "DB_HOST / DB_PORT / DB_USER / DB_PASSWORD / DB_NAME / DB_SSLMODE / DB_TIMEZONE",
	}
}

// EnvKeys 供 api 层读取环境变量映射（公开出去以便 handler 直接调用）。
func EnvKeys() map[string]string { return envKeys() }

func (c *Config) validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port 非法: %d", c.Server.Port)
	}
	// 管理密钥的强度在启动时就地卡死，而不是等到被暴力破解才后悔：
	// 公网上的登录接口面对的是无限次的自动化尝试，太短的密钥（哪怕有
	// 限流）在时间尺度上仍然守不住。install.sh 生成的 48 位随机值远超门槛。
	if key := c.Security.AdminKey; key != "" {
		if key == "CHANGE_ME" {
			return fmt.Errorf("security.admin_key 还是占位值 CHANGE_ME，请设置真实的管理密钥（deploy/.env 中 RELAY_ADMIN_KEY）")
		}
		if len(key) < 16 {
			return fmt.Errorf("security.admin_key 长度不足 16 位（当前 %d 位），公网部署会被暴力破解；建议使用随机长字符串", len(key))
		}
		if key == c.Security.Secret {
			return fmt.Errorf("security.admin_key 不能与加密主密钥（security.secret）相同：登录密钥出现在每个请求里，泄露面更大，两者共用等于把主密钥交给网络")
		}
	}
	switch {
	case c.Security.SessionTTL <= 0:
		return fmt.Errorf("security.session_ttl 必须为正，当前: %s", c.Security.SessionTTL)
	case c.Security.SessionTTL > 720*time.Hour:
		return fmt.Errorf("security.session_ttl 不应超过 720h（30 天），当前: %s", c.Security.SessionTTL)
	}
	switch c.Relay.PayloadStorageMode {
	case "all", "errors", "none":
	default:
		return fmt.Errorf("relay.payload_storage_mode 必须是 all|errors|none，当前: %q", c.Relay.PayloadStorageMode)
	}
	// 负值会让退避变成「立即返回」而不是报错，静默违背配置意图；
	// 上限也拦一下，避免一次请求被拖进分钟级（与 relay 包的封顶同口径）
	if c.Relay.RetrySameUpstreamDelay < 0 {
		return fmt.Errorf("relay.retry_same_upstream_delay 不能为负: %s", c.Relay.RetrySameUpstreamDelay)
	}
	if c.Relay.RetrySameUpstreamDelay > 30*time.Second {
		return fmt.Errorf("relay.retry_same_upstream_delay 不应超过 30s: %s", c.Relay.RetrySameUpstreamDelay)
	}
	return nil
}

func setStr(dst *string, key string) {
	if v, ok := os.LookupEnv(key); ok {
		*dst = strings.TrimSpace(v)
	}
}

func setInt(dst *int, key string) {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			*dst = n
		}
	}
}

// setDuration 接受 Go 的 duration 写法（300s / 5m / 2h）。
// 解析失败时保留原值 —— 配置项写错不该让服务起不来，
// 但要能看出来它没生效。
func setDuration(dst *time.Duration, key string) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		// 纯数字按秒处理，"300" 比 "300s" 更符合直觉
		if n, err2 := strconv.Atoi(strings.TrimSpace(v)); err2 == nil {
			*dst = time.Duration(n) * time.Second
		}
		return
	}
	*dst = d
}

// setCsv 读取逗号分隔的字符串列表（空白项剔除）。
// 变量未设置时不改动默认值；设为空串则清空列表（= 一个代理都不信）。
func setCsv(dst *[]string, key string) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return
	}
	items := []string{}
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			items = append(items, p)
		}
	}
	*dst = items
}
