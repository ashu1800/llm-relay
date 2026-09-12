package store

import (
	"errors"
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
//
// 顺序不能颠倒：AutoMigrate 会按实体定义去建列，而 channel_models.public_name
// 是 not null 且没有默认值 —— 表里已经有行时，直接 ADD COLUMN 会被 Postgres 拒绝。
// 所以先把老结构迁到新结构（补列、回填、去重、置非空、删旧列），
// 再让 AutoMigrate 去补索引与其余表。
func (s *Store) Migrate() error {
	if err := s.migrateLegacySchema(); err != nil {
		return err
	}
	if err := s.db.AutoMigrate(
		&model.ChannelGroup{},
		&model.Channel{},
		&model.ChannelModel{},
		&model.ChannelTemplate{},
		&model.APIKey{},
		&model.RequestLog{},
		&model.RequestPayload{},
		&model.ModelPricing{},
		&model.Setting{},
	); err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	slog.Info("数据库迁移完成")
	return nil
}

// hasTable / hasColumn 用于判断老结构还在不在。
//
// 迁移语句必须在「老库」与「全新库」上都能跑：全新库上 channel_models 里
// 根本没有 model_id，照搬那条 UPDATE ... FROM models 会因为表不存在直接报错，
// 于是新装用户第一次启动就失败。
func (s *Store) hasTable(table string) bool {
	var n int64
	if err := s.db.Raw(
		"SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?",
		table).Scan(&n).Error; err != nil {
		return false
	}
	return n > 0
}

func (s *Store) hasColumn(table, column string) bool {
	var n int64
	if err := s.db.Raw(
		"SELECT count(*) FROM information_schema.columns WHERE table_name = ? AND column_name = ?",
		table, column).Scan(&n).Error; err != nil {
		return false
	}
	return n > 0
}

// migrateLegacySchema 把「模型商 + 模型目录 + 渠道绑定」那套老结构收敛成
// 「渠道自带模型白名单」，并删掉模型商相关的一切。
//
// 背景：模型目录（models）曾是一张独立的表，渠道绑定只存 model_id。
// 这让「模型建好了但没绑渠道」变成一种界面上看不出来的死状态（请求直接没有候选），
// 所以白名单改为直接存在渠道上。迁移要保证已有绑定不丢：
// public_name 从 models 回填，回填不到的（模型已被删）才丢弃。
func (s *Store) migrateLegacySchema() error {
	if s.hasTable("channel_models") && s.hasColumn("channel_models", "model_id") {
		if err := s.db.Exec(
			"ALTER TABLE channel_models ADD COLUMN IF NOT EXISTS public_name varchar(128)").Error; err != nil {
			return fmt.Errorf("迁移 channel_models.public_name 失败: %w", err)
		}
		if s.hasTable("models") {
			if err := s.db.Exec(`UPDATE channel_models cm SET public_name = m.public_name
				FROM models m WHERE m.id = cm.model_id AND COALESCE(cm.public_name, '') = ''`).Error; err != nil {
				return fmt.Errorf("回填 channel_models.public_name 失败: %w", err)
			}
		}
		// 同一个渠道内对外名必须唯一（新索引是 (channel_id, public_name)），
		// 回填后可能出现重复，保留 id 最小的那条
		for _, stmt := range []string{
			`DELETE FROM channel_models a USING channel_models b
			 WHERE a.id > b.id AND a.channel_id = b.channel_id AND a.public_name = b.public_name`,
			"DELETE FROM channel_models WHERE COALESCE(public_name, '') = ''",
			"ALTER TABLE channel_models ALTER COLUMN public_name SET NOT NULL",
			"ALTER TABLE channel_models DROP CONSTRAINT IF EXISTS fk_channel_models_model",
		} {
			if err := s.db.Exec(stmt).Error; err != nil {
				return fmt.Errorf("迁移 channel_models 失败: %w", err)
			}
		}
		slog.Info("渠道绑定已迁移为渠道模型白名单")
	}

	// model_pricings.multiplier（固定倍率）在实体上是 not null 且没有 default。
	//
	// 这类列**不能在表里已有数据时交给 AutoMigrate 去加**：它生成的是
	// ALTER TABLE ... ADD COLUMN multiplier numeric(10,4) NOT NULL，
	// Postgres 会直接报 "column contains null values"，AutoMigrate 返回错误，
	// 应用连不上库就起不来（实测就是在 3552 行定价上启动失败，
	// 清空后才起来 —— 用户升级时不该靠这种巧合）。
	//
	// 所以这里先建列并给已有行兜底 1（1 = 原价，语义上正是「没配倍率」），
	// 再立刻把 DEFAULT 去掉：留着它会让「忘记填倍率」被列默认值静默补上。
	if s.hasTable("model_pricings") && !s.hasColumn("model_pricings", "multiplier") {
		for _, stmt := range []string{
			"ALTER TABLE model_pricings ADD COLUMN multiplier numeric(10,4) NOT NULL DEFAULT 1",
			"ALTER TABLE model_pricings ALTER COLUMN multiplier DROP DEFAULT",
		} {
			if err := s.db.Exec(stmt).Error; err != nil {
				return fmt.Errorf("迁移 model_pricings.multiplier 失败（%s）: %w", stmt, err)
			}
		}
		slog.Info("定价表已补充固定倍率列（既有记录按 1 倍原价处理）")
	}

	// 模型商彻底退场：表与各表上的归属列一并删掉
	for _, stmt := range []string{
		"ALTER TABLE channel_models DROP COLUMN IF EXISTS model_id",
		"ALTER TABLE channels DROP COLUMN IF EXISTS provider_id",
		"ALTER TABLE channel_groups DROP COLUMN IF EXISTS provider_id",
		"ALTER TABLE model_pricings DROP COLUMN IF EXISTS provider_id",
		// 定价改为纯手工：自动同步的来源、来源地址与优先级都不再有意义
		"ALTER TABLE model_pricings DROP COLUMN IF EXISTS source",
		"ALTER TABLE model_pricings DROP COLUMN IF EXISTS source_url",
		"ALTER TABLE model_pricings DROP COLUMN IF EXISTS priority",
		"ALTER TABLE request_logs DROP COLUMN IF EXISTS provider_id",
		"DROP TABLE IF EXISTS pricing_sync_logs",
		"DROP TABLE IF EXISTS models",
		"DROP TABLE IF EXISTS providers",
	} {
		if err := s.db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("删除模型商结构失败（%s）: %w", stmt, err)
		}
	}
	return nil
}

// Seed 写入首次启动所需的基线数据，可重复执行。
func (s *Store) Seed() error {
	group := model.ChannelGroup{}
	if err := s.db.Where(model.ChannelGroup{Name: "默认分组"}).
		Attrs(model.ChannelGroup{Strategy: model.StrategyWeighted, IsDefault: true, Enabled: true}).
		FirstOrCreate(&group).Error; err != nil {
		return fmt.Errorf("初始化默认分组失败: %w", err)
	}

	// 常见厂商的接入参数做成内置模板，建渠道时不必手抄 base_url 与协议。
	// 只预置公开的接口地址，不含任何凭据。
	// 模板必须落在某个分组上：group_id=0 不是合法分组，
	// 有外键之后这种行根本插不进去，没有外键时则会变成一个查不到归属的悬挂值
	templates := []model.ChannelTemplate{
		{Name: "OpenAI 官方", GroupID: group.ID, Protocol: model.ProtocolOpenAIChat, BaseURL: "https://api.openai.com/v1"},
		{Name: "DeepSeek 官方", GroupID: group.ID, Protocol: model.ProtocolOpenAIChat, BaseURL: "https://api.deepseek.com/v1"},
		{Name: "Anthropic 官方", GroupID: group.ID, Protocol: model.ProtocolAnthropic, BaseURL: "https://api.anthropic.com/v1"},
		{Name: "Google Gemini 官方", GroupID: group.ID, Protocol: model.ProtocolGemini, BaseURL: "https://generativelanguage.googleapis.com/v1beta"},
		{Name: "OpenRouter", GroupID: group.ID, Protocol: model.ProtocolOpenAIChat, BaseURL: "https://openrouter.ai/api/v1"},
		{Name: "本地 Ollama", GroupID: group.ID, Protocol: model.ProtocolOpenAIChat, BaseURL: "http://host.docker.internal:11434/v1"},
		{Name: "本地 vLLM", GroupID: group.ID, Protocol: model.ProtocolOpenAIChat, BaseURL: "http://host.docker.internal:8000/v1"},
	}
	for i := range templates {
		t := templates[i]
		if err := s.db.Where(model.ChannelTemplate{Name: t.Name}).
			Attrs(model.ChannelTemplate{Protocol: t.Protocol, BaseURL: t.BaseURL}).
			FirstOrCreate(&t).Error; err != nil {
			return fmt.Errorf("初始化渠道模板 %s 失败: %w", t.Name, err)
		}
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

// EnsureForeignKeys 补齐数据库层面的外键约束，并修复加约束前必须先修好的数据。
//
// 为什么会缺这些关系：它们原本只靠应用代码维护，全库零外键。
// 应用代码一旦漏掉一处（例如备份导入时分组映射失败却保留了旧 ID），
// 库里就会留下指向不存在行的引用，而且没有任何东西会报错 ——
// 往后的表现是「渠道莫名其妙不参与路由」这类查不到原因的问题。
//
// 必须在 Seed 之后调用：回填模板分组需要默认分组已经存在。
// 也必须在 AutoMigrate 之后：表得先建出来。
//
// 用原生 SQL 而不是 GORM 的 association tag：后者要求给实体加关联字段，
// 会改变实体的 JSON 形状，还会让 AutoMigrate 的行为取决于字段定义；
// 直接把要建的约束写清楚更好读。每条都是 DROP IF EXISTS 后重建，可重复执行。
func (s *Store) EnsureForeignKeys() error {
	var grp model.ChannelGroup
	if err := s.db.Order("is_default DESC, id").First(&grp).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 一个分组都没有，此时加外键也拦不住什么，留到有分组之后再建
			return nil
		}
		return fmt.Errorf("查找默认分组失败: %w", err)
	}

	// 种子数据里的内置模板没写分组，留下 7 行 group_id=0。
	// 0 不是合法分组，直接加外键会被这些行挡住，所以先归到默认分组。
	if err := s.db.Exec(
		"UPDATE channel_templates SET group_id = ? WHERE group_id = 0 OR group_id NOT IN (SELECT id FROM channel_groups)",
		grp.ID).Error; err != nil {
		return fmt.Errorf("回填模板分组失败: %w", err)
	}

	// ON DELETE 的选择：
	//   channel_models 是纯关联表，宿主没了就该跟着走 —— CASCADE
	//   分组被删要拦住而不是连带删除渠道 —— RESTRICT（应用层已有守卫，这里兜底）
	stmts := []string{
		"ALTER TABLE channel_models DROP CONSTRAINT IF EXISTS fk_channel_models_channel",
		`ALTER TABLE channel_models ADD CONSTRAINT fk_channel_models_channel
			FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE`,
		"ALTER TABLE channels DROP CONSTRAINT IF EXISTS fk_channels_group",
		`ALTER TABLE channels ADD CONSTRAINT fk_channels_group
			FOREIGN KEY (group_id) REFERENCES channel_groups(id) ON DELETE RESTRICT`,
		"ALTER TABLE channel_templates DROP CONSTRAINT IF EXISTS fk_channel_templates_group",
		`ALTER TABLE channel_templates ADD CONSTRAINT fk_channel_templates_group
			FOREIGN KEY (group_id) REFERENCES channel_groups(id) ON DELETE RESTRICT`,
	}
	for _, stmt := range stmts {
		if err := s.db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("建立外键约束失败: %w", err)
		}
	}
	slog.Info("外键约束就绪")
	return nil
}
