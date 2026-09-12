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
	Redis    RedisConfig    `yaml:"redis"`
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

type RedisConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	Enabled  bool   `yaml:"enabled"`
}

func (r RedisConfig) Addr() string { return fmt.Sprintf("%s:%d", r.Host, r.Port) }

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
		Redis:    RedisConfig{Host: "127.0.0.1", Port: 6379, DB: 0, Enabled: true},
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

	setStr(&c.Redis.Host, "REDIS_HOST")
	setInt(&c.Redis.Port, "REDIS_PORT")
	setStr(&c.Redis.Password, "REDIS_PASSWORD")
	setInt(&c.Redis.DB, "REDIS_DB")
	setBool(&c.Redis.Enabled, "REDIS_ENABLED")

	setInt(&c.Relay.MaxRetries, "RELAY_MAX_RETRIES")
	setStr(&c.Relay.PayloadStorageMode, "RELAY_PAYLOAD_STORAGE_MODE")
	setInt(&c.Relay.PayloadMaxKB, "RELAY_PAYLOAD_MAX_KB")
	setInt(&c.Relay.MaxConcurrency, "RELAY_MAX_CONCURRENCY")
	setInt(&c.Relay.DefaultRPM, "RELAY_DEFAULT_RPM")

	setStr(&c.Security.Secret, "RELAY_SECRET")

	setStr(&c.Log.Level, "LOG_LEVEL")
	setStr(&c.Log.Format, "LOG_FORMAT")
}

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

func setBool(dst *bool, key string) {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			*dst = b
		}
	}
}
