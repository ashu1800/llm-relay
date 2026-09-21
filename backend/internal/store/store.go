package store

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
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
	// 权重回填必须排在 AutoMigrate **之前**：这一版给 (group_id, weight) 加了
	// 唯一索引，而老数据里同分组重名次的渠道是存在的（同一分组下多条渠道
	// 都还是默认的 weight=1），先建索引会直接失败、应用起不来。
	if err := s.normalizeChannelWeights(); err != nil {
		return err
	}
	if err := s.normalizeGroupStrategies(); err != nil {
		return err
	}
	if err := s.db.AutoMigrate(
		&model.ChannelGroup{},
		&model.Channel{},
		&model.ChannelModel{},
		&model.APIKey{},
		&model.RequestLog{},
		&model.RequestPayload{},
		&model.Proxy{},
		&model.Setting{},
	); err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	// 密钥的分组白名单从「存名字」收敛成「存 ID」：必须在 AutoMigrate 之后
	// （keys 表得先存在），放在这里而不是 Seed 里，因为它是一次性的数据
	// 修复，与种子数据无关
	if err := s.normalizeKeyGroupRefs(); err != nil {
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
		for _, stmt := range []string{
			// 四个单价与实体上的 default:0 保持一致：0 就是「这个模型没配价」
			"ALTER TABLE channel_models ADD COLUMN IF NOT EXISTS input_per1_m numeric(18,8) NOT NULL DEFAULT 0",
			"ALTER TABLE channel_models ADD COLUMN IF NOT EXISTS output_per1_m numeric(18,8) NOT NULL DEFAULT 0",
			"ALTER TABLE channel_models ADD COLUMN IF NOT EXISTS cache_read_per1_m numeric(18,8) NOT NULL DEFAULT 0",
			"ALTER TABLE channel_models ADD COLUMN IF NOT EXISTS cache_write_per1_m numeric(18,8) NOT NULL DEFAULT 0",
			"ALTER TABLE channel_models ADD COLUMN IF NOT EXISTS peak_rules jsonb",
		} {
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

	if s.hasTable("model_pricings") && s.hasTable("channel_models") {
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
		{"channel_groups", "provider_id"},
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
	return nil
}

// normalizeChannelWeights 把每个分组内的渠道权重重排成 1..N 连续序号。
//
// 背景：weight 原来是「加权随机」的抽签份额，同一分组下多条渠道都是默认值 1
// 是常态（现有数据里就有）。现在 weight 是组内优先级序号，必须连续唯一，
// 否则故障转移顺序没有定义。
//
// 排序键 (weight ASC, enabled DESC, id ASC)：
//   - weight 优先，尽量保留用户原本表达的先后意图
//   - 同权重时**启用的排前面** —— 停用的渠道不参与路由，让在用的渠道占链头
//     更符合预期（只按 id 排的话，一条早就停用的老渠道会占住第一位）
//
// 只更新与目标值不同的行：这个函数每次启动都会跑，无条件写一遍会把全部渠道的
// updated_at 都刷新掉，用户看到「所有渠道刚刚都被改过」。
func (s *Store) normalizeChannelWeights() error {
	if !s.hasTable("channels") {
		return nil
	}
	var rows []struct {
		ID      uint
		GroupID uint
		Weight  int
	}
	if err := s.db.Raw("SELECT id, group_id, weight FROM channels ORDER BY group_id, weight, enabled DESC, id").
		Scan(&rows).Error; err != nil {
		return fmt.Errorf("读取渠道权重失败: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}

	next := map[uint]int{} // 分组 ID -> 下一个序号
	type fixRow struct {
		id   uint
		want int
	}
	var pending []fixRow
	for _, r := range rows {
		next[r.GroupID]++
		if want := next[r.GroupID]; r.Weight != want {
			pending = append(pending, fixRow{id: r.ID, want: want})
		}
	}
	if len(pending) == 0 {
		return nil
	}
	// 「先改负、后翻正」必须在一个事务里：中途崩溃会留下负权重，
	// 而 weight ASC 排序时负值排最前 —— 组内优先级被静默重排。
	fixed := 0
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, f := range pending {
			// 唯一索引已存在时会中途撞车（把 1 改成 2，而 2 还没让位），
			// 负数不会与任何正序号冲突
			if err := tx.Exec("UPDATE channels SET weight = ? WHERE id = ?", -f.want, f.id).Error; err != nil {
				return fmt.Errorf("重排渠道权重失败（id=%d）: %w", f.id, err)
			}
			fixed++
		}
		if err := tx.Exec("UPDATE channels SET weight = -weight WHERE weight < 0").Error; err != nil {
			return fmt.Errorf("渠道权重翻正失败: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	slog.Info("渠道权重已重排为组内优先级序号", "调整条数", fixed)
	return nil
}

// normalizeGroupStrategies 把已下线的路由策略收敛到 failover。
//
// 「加权随机」（weighted）随 weight 改语义一起下线：它把权重当抽签份额，
// 权重越大越容易被抽中，与「越小越优先」正好相反。留着这个值的话，
// 分组表单的下拉里没有它、界面显示空白，而路由又会走一条已废弃的分支。
//
// 不认识的取值也一并收敛：与其让一个拼错的值静默落到某个默认分支，
// 不如落到一个语义明确的策略上。
func (s *Store) normalizeGroupStrategies() error {
	if !s.hasTable("channel_groups") {
		return nil
	}
	res := s.db.Exec(`UPDATE channel_groups SET strategy = ?
		WHERE COALESCE(strategy, '') NOT IN (?, ?, ?)`,
		model.StrategyFailover, model.StrategyRoundRobin, model.StrategyLeastLatency, model.StrategyFailover)
	if res.Error != nil {
		return fmt.Errorf("迁移分组路由策略失败: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		slog.Info("分组路由策略已从「加权随机」迁移为「顺序故障转移」", "分组数", res.RowsAffected)
	}
	return nil
}

// normalizeKeyGroupRefs 把密钥分组白名单里的**名字**条目归一化成分组 ID。
//
// 老版本的密钥白名单存分组名（前端直接提交 g.name）：分组一改名，引用它的
// 密钥全体 403，界面上毫无征兆。写入侧现在统一走 api.normalizeGroupRefs
// 只存 ID，这段一次性迁移把库里还剩的名字条目换成 ID —— 幂等：条目全是
// 数字时什么都不做，跑多少遍都安全。
//
// 解析不了的名字（分组已删）**保留原样**：把它删掉等于静默放宽该密钥的
// 权限（白名单少了一条限制），运行期 resolveGroupWhitelist 会继续把这种
// 悬空引用如实报成 403。只留一条 Warn 日志，让问题在日志里可见。
func (s *Store) normalizeKeyGroupRefs() error {
	var keys []model.APIKey
	if err := s.db.Find(&keys).Error; err != nil {
		return fmt.Errorf("读取密钥失败: %w", err)
	}
	// 先把分组建一次内存索引，再逐条解析：这段迁移在每次进程启动时都会跑，
	// 逐条 WHERE name = ? 会让启动付出 O(密钥数 × 条目数) 次查询 ——
	// 而分组表很小，一次读全 + map 查找是两次查询的事。
	// 同名分组取 id 最小的一个，与原来 First()（默认按主键升序）一致。
	byName := map[string]uint{}
	var groups []model.ChannelGroup
	if err := s.db.Select("id", "name").Order("id").Find(&groups).Error; err != nil {
		return fmt.Errorf("读取分组失败: %w", err)
	}
	for _, g := range groups {
		if _, ok := byName[g.Name]; !ok {
			byName[g.Name] = g.ID
		}
	}

	for _, k := range keys {
		if len(k.AllowedGroups) == 0 {
			continue
		}
		changed := false
		out := make(model.StringList, 0, len(k.AllowedGroups))
		seen := map[string]bool{}
		keep := func(item string) {
			if !seen[item] {
				seen[item] = true
				out = append(out, item)
			}
		}
		for _, raw := range k.AllowedGroups {
			item := strings.TrimSpace(raw)
			if item == "" {
				changed = true // 空条目顺手清掉
				continue
			}
			// 已是 ID 形态：原样保留，存在性交给运行期解析去查
			// （这里再查一遍库只会让启动多 N 次 SELECT，换不来新信息）
			if _, err := strconv.ParseUint(item, 10, 32); err == nil {
				keep(item)
				continue
			}
			id, ok := byName[item]
			if !ok {
				slog.Warn("密钥分组白名单里的名字解析不了，保留原样（分组可能已删除或改名）",
					"key_id", k.ID, "key", k.Name, "ref", item)
				keep(item)
				continue
			}
			changed = true
			keep(strconv.FormatUint(uint64(id), 10))
		}
		if !changed {
			continue
		}
		if err := s.db.Model(&model.APIKey{}).Where("id = ?", k.ID).
			Update("allowed_groups", out).Error; err != nil {
			return fmt.Errorf("归一化密钥 %d 的分组白名单失败: %w", k.ID, err)
		}
		slog.Info("密钥分组白名单已归一化为分组 ID", "key_id", k.ID, "key", k.Name, "refs", out)
	}
	return nil
}

// Seed 写入首次启动所需的基线数据，可重复执行。
//
// 默认分组只在「一个分组都没有」时创建，也就是全新安装那一次。
//
// 原来写的是 Where(Name: "默认分组").FirstOrCreate(...)：判断依据是**名字**，
// 于是用户在界面上把这个分组改名或删掉之后，下次重启又会凭空冒出一个同名分组、
// 并被设成默认 —— 现象是「我明明删掉了，重启它又回来了」。
// 分组只要存在过，怎么增删都归用户管，这里不再插手。
//
// 唯一的例外是把分组删到一个不剩：那种库与全新安装没有区别（渠道必须挂在分组下，
// 有渠道就不可能一个分组都没有），按全新安装处理。
func (s *Store) Seed() error {
	var groupCount int64
	if err := s.db.Model(&model.ChannelGroup{}).Count(&groupCount).Error; err != nil {
		return fmt.Errorf("统计分组数量失败: %w", err)
	}
	if groupCount == 0 {
		g := model.ChannelGroup{
			Name:      "默认分组",
			Strategy:  model.StrategyFailover,
			IsDefault: true,
			Enabled:   true,
		}
		if err := s.db.Create(&g).Error; err != nil {
			return fmt.Errorf("初始化默认分组失败: %w", err)
		}
		slog.Info("全新安装：已创建默认分组", "id", g.ID)
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
// 必须在 AutoMigrate 之后调用：表得先建出来。
// （这里的顺序注记曾经是「必须在 Seed 之后，因为要回填模板分组」——
// 回填语句随模板管理下线一起删掉了，见下面的注释，别再照着它排顺序。）
//
// 用原生 SQL 而不是 GORM 的 association tag：后者要求给实体加关联字段，
// 会改变实体的 JSON 形状，还会让 AutoMigrate 的行为取决于字段定义；
// 直接把要建的约束写清楚更好读。每条都是 DROP IF EXISTS 后重建，可重复执行。
func (s *Store) EnsureForeignKeys() error {
	// 一个分组都没有时先不加：这时也不会有渠道（渠道必须挂在分组下），
	// 加了拦不住什么，反而可能让「库里存着指向旧分组的渠道」这种历史数据
	// 直接把启动卡死。等有分组了，下次启动自然会建上。
	var grp model.ChannelGroup
	if err := s.db.Order("is_default DESC, id").First(&grp).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("查找分组失败: %w", err)
	}

	// 注意：这里曾经有一段「回填 channel_templates.group_id」的语句
	// （种子数据里的内置模板没写分组，留下 7 行 group_id=0，加外键前要先归到默认分组）。
	// 模板管理下线、表被 DROP 之后，它变成了对已删表的 UPDATE —— 实测直接让
	// 应用启动失败：relation "channel_templates" does not exist。
	// 删功能时要把引用它的地方一起找干净，包括这种「只在迁移里出现一次」的语句。

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
	}
	for _, stmt := range stmts {
		if err := s.db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("建立外键约束失败: %w", err)
		}
	}
	slog.Info("外键约束就绪")
	return nil
}
