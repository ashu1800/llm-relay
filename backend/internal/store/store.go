package store

import (
	"fmt"
	"log/slog"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"llm-relay/internal/config"
	"llm-relay/internal/model"
)

// Store 封装数据库句柄与迁移/种子逻辑。
type Store struct {
	db *gorm.DB
}

// Open 建立连接并配置连接池。此时不校验可用性，避免拖慢启动。
func Open(cfg *config.Config) (*Store, error) {
	logLevel := gormlogger.Warn
	if cfg.Log.Level == "debug" {
		logLevel = gormlogger.Info
	}

	db, err := gorm.Open(postgres.Open(cfg.Database.DSN()), &gorm.Config{
		Logger:                 gormlogger.Default.LogMode(logLevel),
		SkipDefaultTransaction: true,
		NowFunc:                func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("连接 PostgreSQL 失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层连接失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(40)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(time.Hour)
	sqlDB.SetConnMaxIdleTime(10 * time.Minute)

	return &Store{db: db}, nil
}

// DB 暴露原始句柄，供各业务仓储使用。
func (s *Store) DB() *gorm.DB { return s.db }

// Ready 用于容器健康检查。
func (s *Store) Ready() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Ping()
}

// Migrate 建表。实体变更时自动补齐列，适合个人自用场景。
func (s *Store) Migrate() error {
	if err := s.db.AutoMigrate(
		&model.Provider{},
		&model.ChannelGroup{},
		&model.Channel{},
		&model.Model{},
		&model.ChannelModel{},
		&model.ChannelTemplate{},
		&model.APIKey{},
		&model.RequestLog{},
		&model.RequestPayload{},
		&model.ModelPricing{},
		&model.PricingSyncLog{},
		&model.Setting{},
	); err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	slog.Info("数据库迁移完成")
	return nil
}

// Seed 写入首次启动所需的基线数据，可重复执行。
func (s *Store) Seed() error {
	providers := []model.Provider{
		{Code: "openai", Name: "OpenAI", PricingURL: "https://developers.openai.com/api/docs/pricing", Enabled: true},
		{Code: "deepseek", Name: "DeepSeek", PricingURL: "https://api-docs.deepseek.com/quick_start/pricing", Enabled: true},
	}
	for i := range providers {
		p := providers[i]
		if err := s.db.Where(model.Provider{Code: p.Code}).
			Attrs(model.Provider{Name: p.Name, PricingURL: p.PricingURL, Enabled: true}).
			FirstOrCreate(&p).Error; err != nil {
			return fmt.Errorf("初始化模型商 %s 失败: %w", p.Code, err)
		}
	}

	group := model.ChannelGroup{}
	if err := s.db.Where(model.ChannelGroup{Name: "默认分组"}).
		Attrs(model.ChannelGroup{Strategy: model.StrategyWeighted, IsDefault: true, Enabled: true}).
		FirstOrCreate(&group).Error; err != nil {
		return fmt.Errorf("初始化默认分组失败: %w", err)
	}

	defaults := []model.Setting{
		{Key: "payload_storage_mode", Value: model.JSONMap{"value": model.PayloadStoreErrors}},
		{Key: "log_retention_days", Value: model.JSONMap{"value": 30}},
	}
	for i := range defaults {
		st := defaults[i]
		if err := s.db.Where(model.Setting{Key: st.Key}).
			Attrs(model.Setting{Value: st.Value}).
			FirstOrCreate(&st).Error; err != nil {
			return fmt.Errorf("初始化设置 %s 失败: %w", st.Key, err)
		}
	}

	slog.Info("基线数据就绪")
	return nil
}

// ProviderByCode 按编码取模型商，供定价同步使用。
func (s *Store) ProviderByCode(code string) (model.Provider, error) {
	var p model.Provider
	err := s.db.Where(model.Provider{Code: code}).First(&p).Error
	return p, err
}
