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

// SecurityConfig 保管加密主密钥与出站凭据。
type SecurityConfig struct {
	// Secret 用于 AES-GCM 加密上游渠道密钥；生产环境务必通过 RELAY_SECRET 注入。
	Secret string `yaml:"secret"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	Mode string `yaml:"mode"` // debug | release
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
}

type LogConfig struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // text | json
}

// Default 返回内置默认值。
func Default() *Config {
	return &Config{
		Server: ServerConfig{Host: "0.0.0.0", Port: 8888, Mode: "release"},
		Database: DatabaseConfig{
			Host: "127.0.0.1", Port: 5432, User: "llmrelay",
			Password: "", DBName: "llm_relay", SSLMode: "disable", TimeZone: "UTC",
		},
		Security: SecurityConfig{Secret: "llm-relay-dev-secret-change-me"},
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
		},
		Log: LogConfig{Level: "info", Format: "text"},
	}
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

	setStr(&c.Security.Secret, "RELAY_SECRET")

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
		"host":                   "SERVER_HOST",
		"port":                   "SERVER_PORT",
		"mode":                   "GIN_MODE",
		"log_level":              "LOG_LEVEL",
		"log_format":             "LOG_FORMAT",
		"upstream_timeout_sec":   "RELAY_UPSTREAM_TIMEOUT",
		"first_byte_timeout_sec": "RELAY_FIRST_BYTE_TIMEOUT",
		"max_retries":            "RELAY_MAX_RETRIES",
		"max_request_body_mb":    "RELAY_MAX_REQUEST_BODY_MB",
		"log_retention_days":     "RELAY_LOG_RETENTION_DAYS",
		"payload_storage_mode":   "RELAY_PAYLOAD_STORAGE_MODE",
		"payload_max_kb":         "RELAY_PAYLOAD_MAX_KB",
		"max_concurrency":        "RELAY_MAX_CONCURRENCY",
		"default_rpm":            "RELAY_DEFAULT_RPM",
		"secret":                 "RELAY_SECRET",
		"database":               "DB_HOST / DB_PORT / DB_USER / DB_PASSWORD / DB_NAME / DB_SSLMODE / DB_TIMEZONE",
	}
}

// EnvKeys 供 api 层读取环境变量映射（公开出去以便 handler 直接调用）。
func EnvKeys() map[string]string { return envKeys() }

func (c *Config) validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port 非法: %d", c.Server.Port)
	}
	switch c.Relay.PayloadStorageMode {
	case "all", "errors", "none":
	default:
		return fmt.Errorf("relay.payload_storage_mode 必须是 all|errors|none，当前: %q", c.Relay.PayloadStorageMode)
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
