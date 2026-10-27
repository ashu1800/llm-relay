package store

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shopspring/decimal"
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

// OpenByDSN 用现成的 DSN 连库（测试与命令行工具用；服务本身走 Open）。
func OpenByDSN(dsn string) (*Store, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:                 gormlogger.Default.LogMode(gormlogger.Warn),
		SkipDefaultTransaction: true,
		NowFunc:                func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("连接 PostgreSQL 失败: %w", err)
	}
	return &Store{db: db}, nil
}

// Ready 用于就绪探针（/readyz 借它确认数据库可接受请求）。
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
		&model.Channel{},
		&model.ChannelModel{},
		&model.ModelPrice{},
		&model.APIKey{},
		&model.RequestLog{},
		&model.RequestPayload{},
		&model.Proxy{},
		&model.Setting{},
		&model.ClientAlias{},
	); err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	// 单价从「渠道 × 模型」搬到「模型级」（2026-10-27）。必须在 AutoMigrate
	// 之后：新表得先存在。它读的是 channel_models 上**即将被删掉**的老价格列，
	// 所以顺序是「先建表 → 搬数据 → 再删列」（删列在 migrateLegacySchema 里）。
	if err := s.migratePricesToModelLevel(); err != nil {
		return err
	}
	// 客户端显示映射的启动回填：日志列上线早于映射表，历史行没经过
	// 写路径的自动登记，这里按 DISTINCT 一次性补齐（同样必须在
	// AutoMigrate 之后：两张表都得先存在）
	if err := s.backfillClientAliases(); err != nil {
		return err
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
	// 与 hasTable 同口径限定 current_schema：同库其它 schema 有同名表/列时
	// 会误判「已有列」、迁移跳过补列，运行期 INSERT 报缺列 ——
	// 那种错只会在别人的环境上出现，本地永远复现不了
	if err := s.db.Raw(
		"SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?",
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

	// 价格从「模型名 → 单价」的独立表搬到渠道的模型白名单上。
	//
	// 顺序很讲究：先把列补齐（AutoMigrate 加不了 NOT NULL 无默认的列），
	// 再搬数据，最后才删表 —— 中间任何一步失败都不该丢价格。
	if s.hasTable("channel_models") {
		// 2026-10-27：单价已迁到模型级（见 migratePricesToModelLevel），
		// 这四列**只在还没跑过那次迁移时**才需要补。
		//
		// 为什么必须加这个条件：本函数每次启动都会执行，而删列那段只在
		// 第一次执行（幂等标记）。无条件 ADD COLUMN IF NOT EXISTS 会让
		// 「第二次启动」把刚删掉的价格列原样加回来 —— 实测过，表里会多出
		// 四个恒为 0 的列。它们不参与计价（读路径已不看），但会让
		// 「列已删除」这件事看起来像没做成，也让后来的人把 0 当成真实价格。
		priceColsNeeded := !s.hasTable("model_prices")
		var stmts []string
		if priceColsNeeded {
			stmts = append(stmts,
				// 四个单价与实体上的 default:0 保持一致：0 就是「这个模型没配价」
				"ALTER TABLE channel_models ADD COLUMN IF NOT EXISTS input_per1_m numeric(18,8) NOT NULL DEFAULT 0",
				"ALTER TABLE channel_models ADD COLUMN IF NOT EXISTS output_per1_m numeric(18,8) NOT NULL DEFAULT 0",
				"ALTER TABLE channel_models ADD COLUMN IF NOT EXISTS cache_read_per1_m numeric(18,8) NOT NULL DEFAULT 0",
				"ALTER TABLE channel_models ADD COLUMN IF NOT EXISTS cache_write_per1_m numeric(18,8) NOT NULL DEFAULT 0",
			)
		}
		// peak_rules 仍归渠道（时段规则），永远要保证存在
		stmts = append(stmts, "ALTER TABLE channel_models ADD COLUMN IF NOT EXISTS peak_rules jsonb")
		for _, stmt := range stmts {
			if err := s.db.Exec(stmt).Error; err != nil {
				return fmt.Errorf("迁移 channel_models 价格列失败（%s）: %w", stmt, err)
			}
		}
		// multiplier 单独处理：无默认值的 NOT NULL 列加不进已有数据的表
		// （AutoMigrate 生成的是 ADD COLUMN ... NOT NULL，Postgres 直接拒绝），
		// 所以先带 DEFAULT 0 建列，建完也**保留**默认值 —— 理由见实体上的注释
		if !s.hasColumn("channel_models", "multiplier") {
			for _, stmt := range []string{
				"ALTER TABLE channel_models ADD COLUMN multiplier numeric(10,4) NOT NULL DEFAULT 0",
			} {
				if err := s.db.Exec(stmt).Error; err != nil {
					return fmt.Errorf("迁移 channel_models.multiplier 失败（%s）: %w", stmt, err)
				}
			}
		}
	}

	// proxies.for_update（勾选「用于自动更新」）与 multiplier 是同一类坑：
	// 实体上 not null 且没有 default，AutoMigrate 给**已有数据**的表加这种列
	// 会生成 ADD COLUMN ... NOT NULL，Postgres 直接报
	// "column contains null values"，AutoMigrate 返回错误、应用起不来。
	// 先手工补列，DEFAULT false 就是这个字段的正确语义（默认不用于更新），保留。
	if s.hasTable("proxies") && !s.hasColumn("proxies", "for_update") {
		if err := s.db.Exec(
			"ALTER TABLE proxies ADD COLUMN for_update boolean NOT NULL DEFAULT false").Error; err != nil {
			return fmt.Errorf("迁移 proxies.for_update 失败: %w", err)
		}
		slog.Info("代理表已补充「用于自动更新」列（既有代理默认不用于更新）")
	}

	// model_pricings 是**最早一代**的定价表（模型名 → 单价），比「价格挂渠道」
	// 还早。这段只在极老库上触发，且必须与新结构互斥：一旦 model_prices
	// 已存在，channel_models 上的四个单价列可能已被删掉，此时任何 UPDATE
	// 引用它们都会报「列不存在」，应用起不来。
	legacyPricingApplies := s.hasTable("model_pricings") && s.hasTable("channel_models") &&
		!s.hasTable("model_prices") && s.hasColumn("channel_models", "input_per1_m")
	if legacyPricingApplies {
		// 只填「还没配价」的白名单行：用户在渠道里已经配好的价格不能被
		// 一张要被删掉的旧表覆盖
		res := s.db.Exec(`UPDATE channel_models cm SET
				input_per1_m = mp.input_per1_m,
				output_per1_m = mp.output_per1_m,
				cache_read_per1_m = mp.cache_read_per1_m,
				cache_write_per1_m = mp.cache_write_per1_m,
				multiplier = mp.multiplier,
				peak_rules = mp.peak_rules
			FROM model_pricings mp
			WHERE mp.model_key = cm.public_name
			  AND COALESCE(cm.input_per1_m, 0) = 0
			  AND COALESCE(cm.output_per1_m, 0) = 0
			  AND COALESCE(cm.cache_read_per1_m, 0) = 0
			  AND COALESCE(cm.cache_write_per1_m, 0) = 0
			  AND COALESCE(cm.multiplier, 0) = 0`)
		if res.Error != nil {
			return fmt.Errorf("把定价搬到渠道模型失败: %w", res.Error)
		}
		// 搬不到的必须点名：这些价格在新的结构里没有落脚处，
		// 静默丢掉的话用户只会发现「某个模型突然算 0 元了」
		var orphans []struct{ ModelKey string }
		if err := s.db.Raw(`SELECT mp.model_key FROM model_pricings mp
			WHERE NOT EXISTS (SELECT 1 FROM channel_models cm WHERE cm.public_name = mp.model_key)`).
			Scan(&orphans).Error; err == nil {
			for _, o := range orphans {
				slog.Warn("定价没有对应的渠道模型，已随旧表一并删除，需要在新结构里重新配置", "model", o.ModelKey)
			}
		}
		if res.RowsAffected > 0 {
			slog.Info("定价已搬到渠道模型上", "条数", res.RowsAffected)
		}
	}

	// 模型商彻底退场：表与各表上的归属列一并删掉。
	//
	// ALTER 必须逐张表判断在不在：DROP COLUMN IF EXISTS 只容忍「列不存在」，
	// 容忍不了「表不存在」。而这一步排在 AutoMigrate **之前**，全新库里这些表
	// 还没建出来，于是新装用户第一次启动会直接以
	// 「relation "channel_models" does not exist」失败 —— 与上面 hasTable
	// 那段注释里写的是同一个坑，只是这里漏了守卫。
	// DROP TABLE 不用判断：它自带 IF EXISTS。
	for _, t := range []struct{ table, column string }{
		{"channel_models", "model_id"},
		{"channels", "provider_id"},
		{"request_logs", "provider_id"},
	} {
		if !s.hasTable(t.table) {
			continue
		}
		stmt := "ALTER TABLE " + t.table + " DROP COLUMN IF EXISTS " + t.column
		if err := s.db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("删除模型商结构失败（%s）: %w", stmt, err)
		}
	}
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS pricing_sync_logs",
		// 模板管理整个功能已下线：菜单、接口、实体都删了，
		// 表留着只会在备份/排查时让人以为它还在生效
		"DROP TABLE IF EXISTS channel_templates",
		"DROP TABLE IF EXISTS models",
		"DROP TABLE IF EXISTS providers",
		// 价格已搬到渠道的模型白名单上（见上面那段迁移），
		// 旧表留着只会在备份与排查时让人以为它还在生效
		"DROP TABLE IF EXISTS model_pricings",
	} {
		if err := s.db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("删除模型商结构失败（%s）: %w", stmt, err)
		}
	}

	// 分组整体退场（2026-10-26 破坏性重构，不做旧版本兼容）。
	//
	// 路由顺序改由密钥的每模型渠道列表决定，渠道的 group_id / weight、
	// 分组表与路由策略全部失去存在意义。唯一的回填是密钥路由：
	// 存量密钥按「白名单 → 拥有该模型的启用渠道（weight 序）」生成
	// model_channels，让老配置在新模型下保持原有的转移顺序。
	// **回填必须在删 weight 列之前做**，否则原优先级无从还原。
	//
	// 而且必须在回填之前给 api_keys 补上路由列：真实老库上 AutoMigrate 还没跑
	// （migrateLegacySchema 排在它前面），此时 api_keys 既没有 model_channels
	// 也没有 model_routes —— 回填里的 UPDATE SET model_channels 会以
	// 「column does not exist」失败，Migrate 失败，应用永远起不来。
	// （2026-10-26 评审发现：老库形状的测试从已 AutoMigrate 的新表装回老列
	// 构造，恰好掩盖了这条路径，见 store_test 的真实老库用例。）
	// 此处带 hasTable 守卫，与下面 DROP 循环同一套规则。
	if s.hasTable("api_keys") && !s.hasColumn("api_keys", "model_channels") {
		if err := s.db.Exec("ALTER TABLE api_keys ADD COLUMN model_channels jsonb").Error; err != nil {
			return fmt.Errorf("补密钥路由列失败: %w", err)
		}
	}
	if s.hasTable("channels") && s.hasColumn("channels", "group_id") {
		if err := s.backfillKeyModelRoutes(); err != nil {
			return err
		}
	}
	// DROP COLUMN 必须逐张表判断在不在（与上面删 provider 结构同一条坑：
	// 全新库里这些表还没建，直接跑会以 relation does not exist 失败）。
	// 表存在性对整个循环体成立（循环体不建表也不删表），下面的各分支
	// 不再各自重复 hasTable 判断。
	for _, t := range []struct{ table, column string }{
		{"channels", "group_id"},
		{"channels", "weight"},
		{"request_logs", "group_id"},
		{"api_keys", "allowed_groups"},
	} {
		if !s.hasTable(t.table) {
			continue
		}
		// 唯一索引 idx_group_weight 引着 weight 列，删列前先摘掉；
		// 外键 fk_channels_group 同理（RESTRICT 指着 channel_groups）。
		if t.column == "weight" {
			if err := s.db.Exec("DROP INDEX IF EXISTS idx_group_weight").Error; err != nil {
				return fmt.Errorf("删除分组权重索引失败: %w", err)
			}
		}
		if t.column == "group_id" {
			if err := s.db.Exec("ALTER TABLE channels DROP CONSTRAINT IF EXISTS fk_channels_group").Error; err != nil {
				return fmt.Errorf("删除分组外键失败: %w", err)
			}
		}
		if t.column == "allowed_groups" {
			// 补路由列已统一提前到回填调用点之前（上面的 ADD COLUMN 块）——
			// 走到这里该列必然已存在，这里不再重复补。
			// model_routes 是早期实现按 Go 字段名建的列（实体没显式声明
			// column 时，ModelRoutes → model_routes），与写路径用的
			// model_channels 是两列并存 —— 读路径绑在它上面，表现为
			// 「保存成功、列表永远回旧顺序」（2026-10-26 实测踩过）。
			// 实体已显式 column:model_channels，这里把残留列删掉。
			if s.hasColumn("api_keys", "model_routes") {
				if err := s.db.Exec("ALTER TABLE api_keys DROP COLUMN IF EXISTS model_routes").Error; err != nil {
					return fmt.Errorf("删除密钥路由残留列失败: %w", err)
				}
				slog.Info("api_keys.model_routes 残留列已删除（路由列统一为 model_channels）")
			}
		}
		stmt := "ALTER TABLE " + t.table + " DROP COLUMN IF EXISTS " + t.column
		if err := s.db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("删除分组结构失败（%s）: %w", stmt, err)
		}
	}
	if s.hasTable("channel_groups") {
		if err := s.db.Exec("DROP TABLE IF EXISTS channel_groups").Error; err != nil {
			return fmt.Errorf("删除分组表失败: %w", err)
		}
		slog.Info("分组功能已移除：channel_groups 表已删除")
	}
	return nil
}

// backfillKeyModelRoutes 把存量密钥的分组白名单一次性转换成每模型渠道路由。
//
// 规则（2026-10-26，破坏性重构的一次性回填）：
//   - allowed_models 为空的密钥 → 填入全库 DISTINCT public_name
//     （原语义「不限」，新模型要求非空，等价展开是唯一不丢调用的选择）；
//   - 每个模型的候选渠道 = 白名单含该模型的**启用**渠道，按 (weight, id) 升序
//     —— weight 是当时的组内优先级，这样恢复出的转移顺序与分组时代一致；
//   - 路由键与 allowed_models 对齐：模型没有任何渠道能跑时也保留键（空列表），
//     让请求失败时报错能如实说「这个模型当前没有可用渠道」。
//
// 用 settings 标记做幂等（与 client_alias_backfill_v1 同一套做法），
// 成功后写标记；失败不写，下次启动重试。必须在 weight 列还在时调用
// （调用点见 migrateLegacySchema 末尾）。
func (s *Store) backfillKeyModelRoutes() error {
	const markerKey = "key_model_routes_backfill_v1"
	var marker model.Setting
	err := s.db.Where("key = ?", markerKey).First(&marker).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("读取密钥路由回填标记失败: %w", err)
	}

	// 模型 → 渠道 ID（weight 序）。一次 JOIN 查全，内存里归堆。
	var rows []struct {
		PublicName string
		ChannelID  uint
		Weight     int
	}
	if err := s.db.Raw(`SELECT cm.public_name AS public_name, cm.channel_id AS channel_id,
			COALESCE(ch.weight, 0) AS weight
		FROM channel_models cm JOIN channels ch ON ch.id = cm.channel_id
		WHERE cm.enabled = true AND ch.enabled = true
		ORDER BY cm.public_name, ch.weight, ch.id`).Scan(&rows).Error; err != nil {
		return fmt.Errorf("读取渠道模型白名单失败: %w", err)
	}
	channelsOf := map[string][]uint{}
	for _, r := range rows {
		channelsOf[r.PublicName] = append(channelsOf[r.PublicName], r.ChannelID)
	}
	var allNames []string
	if err := s.db.Raw(`SELECT DISTINCT public_name FROM channel_models WHERE enabled = true
		ORDER BY public_name`).Scan(&allNames).Error; err != nil {
		return fmt.Errorf("读取全量模型名失败: %w", err)
	}

	var keys []model.APIKey
	if err := s.db.Find(&keys).Error; err != nil {
		return fmt.Errorf("读取密钥失败: %w", err)
	}
	updated := 0
	for _, k := range keys {
		models := []string(k.AllowedModels)
		if len(models) == 0 {
			models = allNames // 原「不限」展开成全量
		}
		routes := model.JSONMap{}
		cleanModels := make(model.StringList, 0, len(models))
		for _, name := range models {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			cleanModels = append(cleanModels, name)
			ids, ok := channelsOf[name]
			if !ok {
				ids = []uint{}
			}
			routes[name] = ids
		}
		updates := map[string]any{"model_channels": routes}
		if len(k.AllowedModels) == 0 {
			updates["allowed_models"] = cleanModels
		}
		if err := s.db.Model(&model.APIKey{}).Where("id = ?", k.ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("回填密钥 %d 的模型路由失败: %w", k.ID, err)
		}
		updated++
	}
	if err := s.db.Create(&model.Setting{
		Key:   markerKey,
		Value: model.JSONMap{"value": true},
	}).Error; err != nil {
		return fmt.Errorf("写入密钥路由回填标记失败: %w", err)
	}
	slog.Info("存量密钥已按渠道优先级回填模型路由", "密钥数", updated)
	return nil
}

// backfillClientAliases 把日志里已出现过的客户端一次性登记进显示映射表。
//
// 映射表的行为模型（见 model.ClientAlias 的注释）：客户端第一次被观测到
// 就落一行「待设置」，等用户来配名字与图标。映射表晚于日志的客户端列
// 上线，历史行不会经过写路径的自动登记（logwriter.logClientAlias），
// 所以启动时按 DISTINCT 补一次。
//
// **只在第一次跑，之后每次启动都直接返回**（2026-10-19 审查发现）：
// 这条语句对 request_logs 做全表顺序扫描 + DISTINCT，而 client_id /
// client_name 都没有索引，ON CONFLICT DO NOTHING 也不会让计划器跳过源行
// （它只抑制插入）。默认保留 30 天、日 10 万行时，每次重启与自更新都要在
// 监听端口之前扫数百万宽行 —— 而它的产出在第一次之后恒为空。
// 这个回填本来就是一次性的数据修复：新客户端由写路径的 logClientAlias
// 持续登记，不需要每次启动再扫一遍。
//
// 标记写进 settings 表而不是「判断表里有没有行」：后者在用户删光全部映射
// 之后会误判成「还没回填过」，又把日志里的客户端全插回来。
//
// Key 的派生与 relay.ClientKey 同口径：id 非空取 id，否则取 name token。
// 两边各写一份是因为 SQL 里复用不了 Go 函数；改口径必须两处一起动
// （前端 clientDisplay.ts 还有第三份，三处注释互指）。
func (s *Store) backfillClientAliases() error {
	if !s.hasTable("request_logs") {
		return nil
	}
	// 已跑过：幂等空返回，不查日志表。用 settings 里的一行标记，
	// 而不是「client_aliases 非空」——用户删光映射是合法状态。
	const markerKey = "client_alias_backfill_v1"
	var marker model.Setting
	err := s.db.Where("key = ?", markerKey).First(&marker).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("读取客户端映射回填标记失败: %w", err)
	}

	res := s.db.Exec(`INSERT INTO client_aliases ("key", created_at, updated_at)
		SELECT DISTINCT COALESCE(NULLIF(client_id, ''), NULLIF(client_name, '')), now(), now()
		FROM request_logs
		WHERE COALESCE(NULLIF(client_id, ''), NULLIF(client_name, ''), '') <> ''
		ON CONFLICT ("key") DO NOTHING`)
	if res.Error != nil {
		return fmt.Errorf("回填客户端显示映射失败: %w", res.Error)
	}
	// 成功之后才写标记：失败了下次启动还要重试（那时多半是库暂时不可用，
	// 不该把「没做成」永久记成「做过了」）
	if err := s.db.Create(&model.Setting{
		Key:   markerKey,
		Value: model.JSONMap{"value": true},
	}).Error; err != nil {
		return fmt.Errorf("写入客户端映射回填标记失败: %w", err)
	}
	if res.RowsAffected > 0 {
		slog.Info("已把日志中出现过的客户端登记进显示映射表", "条数", res.RowsAffected)
	}
	return nil
}

// migratePricesToModelLevel 把单价从「渠道 × 模型」搬到模型级（2026-10-27）。
//
// 背景：单价原先挂在 channel_models 上（同一条价格列在每条渠道里各存一份），
// 现在唯一归属是 model_prices，渠道只留倍率。存量数据必须搬过去，
// 否则升级后所有模型都变成「未定价」，账单从有价突然掉到 0 元。
//
// 合并规则：同一模型名在多个渠道里有不同单价时**取各字段的最大值**
// （站主拍板）。理由：宁可高估成本也不让账单看上去比实际便宜 ——
// 低估会让人以为这条渠道很划算，而高估只是保守。合并后用户可以在
// 新菜单里按模型改回准确值。
//
// 币种归属：老数据的价格数字是「跟着所属渠道币种」的。搬迁时按各渠道的
// 币种，把它的数字写进新表对应的那一档（USD 渠道 → USD 四列，
// CNY 渠道 → RMB 四列）。同一模型既有美元渠道又有人民币渠道时，
// 两档各自取该币种下的最大值 —— 这正是「同一模型同时有美元价和人民币价」
// 这一需求的来源。
//
// 幂等：用 settings 标记，与 backfillKeyModelRoutes / backfillClientAliases
// 同一套做法。成功后写标记，失败下次启动重试。
func (s *Store) migratePricesToModelLevel() error {
	const markerKey = "model_prices_migration_v1"
	var marker model.Setting
	err := s.db.Where("key = ?", markerKey).First(&marker).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("读取模型定价迁移标记失败: %w", err)
	}

	// 老价格列还在时才搬。全新库里这些列从来不存在 —— 迁移直接跳过，
	// 只写标记，避免反复判断。
	if s.hasTable("channel_models") && s.hasColumn("channel_models", model.ColPricingInputPer1M) {
		// 一次查出「模型名 + 币种 + 四档单价」，内存里按 (模型, 币种) 归并取最大。
		var rows []struct {
			PublicName      string
			Currency        string
			InputPer1M      decimal.Decimal
			OutputPer1M     decimal.Decimal
			CacheReadPer1M  decimal.Decimal
			CacheWritePer1M decimal.Decimal
		}
		if err := s.db.Raw(`SELECT cm.public_name AS public_name,
				COALESCE(NULLIF(ch.currency, ''), 'USD') AS currency,
				cm.input_per1_m AS input_per1_m,
				cm.output_per1_m AS output_per1_m,
				cm.cache_read_per1_m AS cache_read_per1_m,
				cm.cache_write_per1_m AS cache_write_per1_m
			FROM channel_models cm
			JOIN channels ch ON ch.id = cm.channel_id`).Scan(&rows).Error; err != nil {
			return fmt.Errorf("读取存量单价失败: %w", err)
		}

		byName := map[string]*model.ModelPrice{}
		for _, r := range rows {
			// 四档全 0 的渠道不构成「有价」，跳过：否则一个没配价的渠道
			// 会把 0 当成「最高价」参与比较（虽然 max 会取到非 0，但语义上
			// 它本来就不该出现在候选里）
			if r.InputPer1M.IsZero() && r.OutputPer1M.IsZero() &&
				r.CacheReadPer1M.IsZero() && r.CacheWritePer1M.IsZero() {
				continue
			}
			p, ok := byName[r.PublicName]
			if !ok {
				p = &model.ModelPrice{ModelName: r.PublicName}
				byName[r.PublicName] = p
			}
			cny := false
			if c, valid := model.NormalizeCurrency(r.Currency); valid && c == model.CurrencyCNY {
				cny = true
			}
			if cny {
				p.RMBInputPer1M = maxDec(p.RMBInputPer1M, r.InputPer1M)
				p.RMBOutputPer1M = maxDec(p.RMBOutputPer1M, r.OutputPer1M)
				p.RMBCacheReadPer1M = maxDec(p.RMBCacheReadPer1M, r.CacheReadPer1M)
				p.RMBCacheWritePer1M = maxDec(p.RMBCacheWritePer1M, r.CacheWritePer1M)
			} else {
				p.USDInputPer1M = maxDec(p.USDInputPer1M, r.InputPer1M)
				p.USDOutputPer1M = maxDec(p.USDOutputPer1M, r.OutputPer1M)
				p.USDCacheReadPer1M = maxDec(p.USDCacheReadPer1M, r.CacheReadPer1M)
				p.USDCacheWritePer1M = maxDec(p.USDCacheWritePer1M, r.CacheWritePer1M)
			}
		}

		migrated := 0
		for _, p := range byName {
			// 用户可能已经在新表里填过价（例如先升级了二进制、又在界面里改过）：
			// 不覆盖已有的行，只补缺的。InsertModelPriceIfAbsent 就是补缺语义
			// （SQL 本体在 model 包里，列名只出现一次）。
			inserted, err := model.InsertModelPriceIfAbsent(s.db, *p)
			if err != nil {
				return fmt.Errorf("迁移模型定价失败: %w", err)
			}
			if inserted {
				migrated++
			}
		}
		if migrated > 0 {
			slog.Info("已把渠道单价迁移为模型级定价", "模型数", migrated)
		}

		// 搬完就删旧列：留着它们会有两个真相，而读路径已经不再看它们。
		// DROP COLUMN 可重复执行（IF EXISTS），全程在迁移内部，不接受外部写入。
		for _, col := range []string{
			model.ColPricingInputPer1M, model.ColPricingOutputPer1M,
			model.ColPricingCacheReadPer1M, model.ColPricingCacheWritePer1M,
		} {
			if err := s.db.Exec("ALTER TABLE channel_models DROP COLUMN IF EXISTS " + col).Error; err != nil {
				return fmt.Errorf("删除旧单价列 %s 失败: %w", col, err)
			}
		}
	}

	if err := s.db.Create(&model.Setting{
		Key:   markerKey,
		Value: model.JSONMap{"value": true},
	}).Error; err != nil {
		return fmt.Errorf("写入模型定价迁移标记失败: %w", err)
	}
	return nil
}

// maxDec 返回两个 decimal 中较大的一个（用于迁移时取「各渠道最高价」）。
func maxDec(a, b decimal.Decimal) decimal.Decimal {
	if b.GreaterThan(a) {
		return b
	}
	return a
}

// Seed 写入首次启动所需的基线数据，可重复执行。//
// 分组已移除，这里只剩系统设置：首次启动写入报文留存与日志保留的默认值，
// 已存在的键不覆盖。
func (s *Store) Seed() error {
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

// EnsureForeignKeys 补齐数据库层面的外键约束。
//
// 为什么会缺这些关系：它们原本只靠应用代码维护，全库零外键。
// 应用代码一旦漏掉一处（例如备份导入时引用映射失败却保留了旧 ID），
// 库里就会留下指向不存在行的引用，而且没有任何东西会报错 ——
// 往后的表现是「渠道莫名其妙不参与路由」这类查不到原因的问题。
//
// 必须在 AutoMigrate 之后调用：表得先建出来。
//
// 用原生 SQL 而不是 GORM 的 association tag：后者要求给实体加关联字段，
// 会改变实体的 JSON 形状，还会让 AutoMigrate 的行为取决于字段定义；
// 直接把要建的约束写清楚更好读。每条都是 DROP IF EXISTS 后重建，可重复执行。
func (s *Store) EnsureForeignKeys() error {
	// channel_models 是纯关联表，宿主渠道没了就该跟着走 —— CASCADE
	stmts := []string{
		"ALTER TABLE channel_models DROP CONSTRAINT IF EXISTS fk_channel_models_channel",
		`ALTER TABLE channel_models ADD CONSTRAINT fk_channel_models_channel
			FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE`,
	}
	for _, stmt := range stmts {
		if err := s.db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("建立外键约束失败: %w", err)
		}
	}
	slog.Info("外键约束就绪")
	return nil
}
