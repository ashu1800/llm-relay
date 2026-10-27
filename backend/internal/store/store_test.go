package store

// PG 集成测试：覆盖迁移幂等、老库补列、分组结构的破坏性退场。
//
// 用 LLMRELAY_TEST_DSN 门控（指向一个**专用测试库**，测试会建表、写测试行）：
// 没设置时整组跳过 —— Windows 侧 go test 不红，WSL 侧 verify-all.sh
// export 后全量跑。
//
// 用法（WSL）：
//	export LLMRELAY_TEST_DSN="host=127.0.0.1 port=5432 user=llmrelay password=... dbname=llm_relay_test sslmode=disable"
//	go test ./internal/store/ -v

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// testStore 按环境变量连库；没配置就跳过。
func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("LLMRELAY_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 LLMRELAY_TEST_DSN，跳过 PG 集成测试")
	}
	s, err := OpenByDSN(dsn)
	if err != nil {
		t.Fatalf("连测试库失败: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := s.db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return s
}

// TestMigrateIdempotent 连续迁移两遍不报错、不产生重复列/索引。
// Migrate 在每次启动都会跑，非幂等会让第二次重启直接起不来。
func TestMigrateIdempotent(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 2; i++ {
		if err := s.Migrate(); err != nil {
			t.Fatalf("第 %d 遍迁移失败: %v", i+1, err)
		}
	}
}

// TestMigrateFreshDatabase 全新建库（无表）上迁移成功并建齐表。
// 全新克隆后的第一次启动就是这条路径，migrateLegacySchema 里的老结构
// 查询必须能在「什么表都没有」的库上安全返回。
// channel_groups 已随分组功能移除：迁移后**不该**存在。
func TestMigrateFreshDatabase(t *testing.T) {
	s := testStore(t)
	// 清掉本测试库里的所有表（专用测试库才允许这么干）
	// model_prices 必须一起 DROP：它是 2026-10-27 新增的表，
	// 且迁移用 settings 里的 model_prices_migration_v1 做幂等标记 ——
	// 只删其它表的话，残留的 model_prices 会让「全新库」这个前提不成立，
	// 表现是本测试在整套测试里跑时失败、单独跑时通过（实测过）。
	tables := []string{"request_payloads", "request_logs", "channel_models",
		"channels", "channel_groups", "api_keys", "proxies", "settings", "model_prices"}
	for _, tbl := range tables {
		s.db.Exec("DROP TABLE IF EXISTS " + tbl + " CASCADE")
	}
	if err := s.Migrate(); err != nil {
		t.Fatalf("全新库迁移失败: %v", err)
	}
	for _, tbl := range tables {
		if tbl == "channel_groups" {
			continue
		}
		if !s.hasTable(tbl) {
			t.Errorf("迁移后应存在表 %s", tbl)
		}
	}
	if s.hasTable("channel_groups") {
		t.Error("分组已移除，迁移后不应存在 channel_groups 表")
	}
	if s.hasColumn("channels", "group_id") || s.hasColumn("channels", "weight") {
		t.Error("channels 不应再有 group_id / weight 列")
	}
	if s.hasColumn("request_logs", "group_id") {
		t.Error("request_logs 不应再有 group_id 列")
	}
	if s.hasColumn("api_keys", "allowed_groups") {
		t.Error("api_keys 不应再有 allowed_groups 列")
	}
	if !s.hasColumn("api_keys", "model_channels") {
		t.Error("api_keys 应有 model_channels 列")
	}
}

// TestMigrateRemovesGroupSchemaAndBackfillsKeyRoutes 模拟分组时代的老库升级。
//
// 老库形状：channel_groups 表 + channels.group_id/weight + api_keys.allowed_groups，
// 存量密钥没有 model_channels。升级后：
//   - 分组结构与渠道的 group_id/weight 列被删除；
//   - 密钥按「白名单 → 拥有该模型的启用渠道（weight 序）」生成路由；
//   - 老语义「不限模型」（allowed_models 空）展开成全量模型；
//   - 再跑一遍迁移（幂等）不报错、不重复回填。
func TestMigrateRemovesGroupSchemaAndBackfillsKeyRoutes(t *testing.T) {
	s := testStore(t)
	// 先跑到新结构，再手工把老结构「装回去」，保证测试不依赖具体的库历史
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	db := s.db
	db.Exec("DROP TABLE IF EXISTS channel_groups CASCADE")
	db.Exec("ALTER TABLE channels ADD COLUMN IF NOT EXISTS group_id bigint NOT NULL DEFAULT 0")
	db.Exec("ALTER TABLE channels ADD COLUMN IF NOT EXISTS weight int NOT NULL DEFAULT 1")
	db.Exec("ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS allowed_groups jsonb")
	// model_routes 残留列：早期实现按 Go 字段名建的列，与 model_channels 并存
	// 时读路径绑在它上面（「保存成功、列表回旧顺序」的根因，2026-10-26 实测）。
	// 装回一根带旧值的，迁移后必须被删掉。
	db.Exec("ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS model_routes jsonb")
	// 真实老库形状（2026-10-26 评审 C1）：api_keys 上**没有任何路由列** ——
	// AutoMigrate 还没跑过，老库升级时回填的 UPDATE SET model_channels
	// 必须先有列才能执行。把两口列都删掉，逼着迁移在回填前自己补列。
	db.Exec("ALTER TABLE api_keys DROP COLUMN IF EXISTS model_channels")
	db.Exec("ALTER TABLE api_keys DROP COLUMN IF EXISTS model_routes")

	// 造数据：两条渠道（weight 2 的先建、weight 1 的后建，验证按 weight 而非 id 排序）
	//
	// cleanup 分两层 —— **拆掉老结构的那种清理只能进 t.Cleanup**：
	// 曾把 DROP COLUMN 放在 setup 里立刻执行，INSERT 时列已经没了，
	// 用例恒以「造老结构数据失败」SKIP，而 SKIP 在汇总里是绿的 ——
	// 小组形状/回填/残留列的断言一次都没真的跑过，C1（回填补列缺失）
	// 正是靠这个盲区存活到评审。setup 只清数据，不碰结构。
	cleanupData := func() {
		db.Exec("DELETE FROM channel_models WHERE channel_id IN (SELECT id FROM channels WHERE name LIKE '__mig\\_%' ESCAPE '\\')")
		db.Exec("DELETE FROM api_keys WHERE name IN ('__mig_probe_key__', '__mig_open_key__')")
		db.Exec("DELETE FROM channels WHERE name LIKE '__mig\\_%' ESCAPE '\\'")
		db.Exec("DELETE FROM settings WHERE key = 'key_model_routes_backfill_v1'")
	}
	cleanup := func() {
		cleanupData()
		db.Exec("DROP TABLE IF EXISTS channel_groups CASCADE")
		db.Exec("ALTER TABLE channels DROP COLUMN IF EXISTS group_id")
		db.Exec("ALTER TABLE channels DROP COLUMN IF EXISTS weight")
		db.Exec("ALTER TABLE api_keys DROP COLUMN IF EXISTS allowed_groups")
		db.Exec("ALTER TABLE api_keys DROP COLUMN IF EXISTS model_routes")
	}
	cleanupData()
	t.Cleanup(cleanup)

	if err := db.Exec(`INSERT INTO channels (name, base_url, protocol, group_id, weight, enabled, created_at, updated_at)
		VALUES ('__mig_ch_b__', 'http://b', 'openai-chat', 1, 2, true, now(), now())`).Error; err != nil {
		// 模式升级失败不 SKIP：SKIP 在 CI 汇总里等于绿，老形状断言会被静默掩盖
		//（本用例正是靠「恒 SKIP」的盲区漏掉了回填补列缺失，2026-10-26）。
		t.Fatalf("造老结构数据失败: %v", err)
	}
	if err := db.Exec(`INSERT INTO channels (name, base_url, protocol, group_id, weight, enabled, created_at, updated_at)
		VALUES ('__mig_ch_a__', 'http://a', 'openai-chat', 1, 1, true, now(), now())`).Error; err != nil {
		t.Fatal(err)
	}
	var chA, chB uint
	db.Raw("SELECT id FROM channels WHERE name = '__mig_ch_a__'").Scan(&chA)
	db.Raw("SELECT id FROM channels WHERE name = '__mig_ch_b__'").Scan(&chB)
	for _, m := range []struct {
		ch   uint
		name string
	}{{chA, "mig-model"}, {chB, "mig-model"}} {
		if err := db.Exec(`INSERT INTO channel_models (channel_id, public_name, upstream_name, enabled, created_at, updated_at)
			VALUES (?, 'mig-model', 'mig-model', true, now(), now())`, m.ch).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 一把只选了 mig-model 的密钥 + 一把「不限模型」的密钥
	if err := db.Exec(`INSERT INTO api_keys (name, key_hash, enabled, allowed_models, created_at, updated_at)
		VALUES ('__mig_probe_key__', '__mig_hash__', true, '["mig-model"]', now(), now())`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO api_keys (name, key_hash, enabled, allowed_models, created_at, updated_at)
		VALUES ('__mig_open_key__', '__mig_hash_open__', true, '[]', now(), now())`).Error; err != nil {
		t.Fatal(err)
	}

	if err := s.Migrate(); err != nil {
		t.Fatalf("老库升级迁移失败: %v", err)
	}

	if s.hasTable("channel_groups") {
		t.Error("升级后 channel_groups 应被删除")
	}
	if s.hasColumn("channels", "group_id") || s.hasColumn("channels", "weight") {
		t.Error("升级后 channels.group_id / weight 应被删除")
	}
	if s.hasColumn("api_keys", "allowed_groups") {
		t.Error("升级后 api_keys.allowed_groups 应被删除")
	}
	if s.hasColumn("api_keys", "model_routes") {
		t.Error("升级后 model_routes 残留列应被删除（路由列统一为 model_channels）")
	}

	// jsonb 列招聘不进 GORM 的普通 Go 类型（[]string / map 都会以
	// unsupported Scan 失败）：先扫成文本再手工解开 —— 断言口径不受影响，
	// 也不依赖专用 Valuer 类型
	var probeRaw struct {
		AllowedModels string `gorm:"column:allowed_models"`
		ModelChannels string `gorm:"column:model_channels"`
	}
	if err := db.Raw(`SELECT allowed_models, model_channels FROM api_keys WHERE name = '__mig_probe_key__'`).
		Scan(&probeRaw).Error; err != nil {
		t.Fatal(err)
	}
	var probeModels []string
	_ = json.Unmarshal([]byte(probeRaw.AllowedModels), &probeModels)
	probeChannels := parseRouteJSON(t, probeRaw.ModelChannels)
	if len(probeModels) != 1 || probeModels[0] != "mig-model" {
		t.Errorf("模型白名单应保持 [mig-model]，实际 %v", probeModels)
	}
	// weight=1 的渠道 a 必须排在 weight=2 的 b 前面
	if got := probeChannels["mig-model"]; len(got) != 2 || got[0] != chA || got[1] != chB {
		t.Errorf("路由应按 weight 排序 [%d %d]，实际 %v", chA, chB, probeChannels["mig-model"])
	}

	openChannels := parseRouteJSON(t, scanText(t, db,
		`SELECT model_channels FROM api_keys WHERE name = '__mig_open_key__'`))
	if len(openChannels) == 0 {
		t.Error("「不限模型」的密钥应展开为全量模型路由")
	}
	if _, ok := openChannels["mig-model"]; !ok {
		t.Errorf("展开的路由应包含 mig-model，实际 %v", openChannels)
	}

	// 幂等：再跑一遍不报错，且路由不被二次改写
	if err := s.Migrate(); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}
	routesAgain := parseRouteJSON(t, scanText(t, db,
		`SELECT model_channels FROM api_keys WHERE name = '__mig_probe_key__'`))
	if len(routesAgain["mig-model"]) != 2 {
		t.Errorf("重复迁移不应改写已有路由，实际 %v", routesAgain)
	}
}

// scanText 把单行单列查询扫成文本（jsonb/数字列都要走文本，见上面注释）。
func scanText(t *testing.T, db *gorm.DB, query string) string {
	t.Helper()
	var out string
	if err := db.Raw(query).Scan(&out).Error; err != nil {
		t.Fatalf("扫描 %q 失败: %v", query, err)
	}
	return out
}

// parseRouteJSON 把 model_channels 的 jsonb 文本解成 map；NULL/空返回空 map。
func parseRouteJSON(t *testing.T, raw string) map[string][]uint {
	t.Helper()
	out := map[string][]uint{}
	if strings.TrimSpace(raw) == "" || raw == "null" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("路由不是合法 jsonb: %v（原文 %q）", err, raw)
	}
	return out
}

// TestMigrateAddsProxyForUpdateColumn 存量库补 proxies.for_update 列。
//
// 「not null 且没有 default」的列交给 AutoMigrate 去给非空表加，Postgres
// 会报 "column contains null values"、应用起不来（见 migrateLegacySchema
// 里 multiplier 那段注释）。这条测试模拟升级前的老库（有数据、无该列），
// 钉住「补列成功 + 既有行回填 false」的升级路径。
func TestMigrateAddsProxyForUpdateColumn(t *testing.T) {
	s := testStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	// 还原成升级前的样子：没有这一列，且表里至少有一行数据
	s.db.Exec("ALTER TABLE proxies DROP COLUMN IF EXISTS for_update")
	if err := s.db.Exec(`INSERT INTO proxies (name, protocol, host, port, enabled, last_status)
		VALUES ('__mig_probe__', 'socks5', '10.0.0.1', 1080, true, 'unknown')
		ON CONFLICT (name) DO NOTHING`).Error; err != nil {
		t.Skipf("造数据失败，跳过: %v", err)
	}
	t.Cleanup(func() { s.db.Exec("DELETE FROM proxies WHERE name = '__mig_probe__'") })

	if err := s.Migrate(); err != nil {
		t.Fatalf("老库补列迁移失败: %v", err)
	}
	if !s.hasColumn("proxies", "for_update") {
		t.Fatal("迁移后应存在 for_update 列")
	}
	var nulls int64
	s.db.Raw("SELECT count(*) FROM proxies WHERE for_update IS NULL").Scan(&nulls)
	if nulls != 0 {
		t.Fatalf("NOT NULL 列补齐后不应有 NULL 行，实际 %d", nulls)
	}
	var checked int64
	s.db.Raw("SELECT count(*) FROM proxies WHERE name = '__mig_probe__' AND for_update = true").Scan(&checked)
	if checked != 0 {
		t.Fatal("既有代理补列后默认应为 false（不用于自动更新）")
	}
}

// TestHasColumnScopedToCurrentSchema 列探测限定当前 schema。
// （只验证正向：本 schema 的列能查到。跨 schema 的隔离依赖库的部署形态，
// 有条件的测试库可以用 search_path 制造第二个 schema 验证反向。）
func TestHasColumnScopedToCurrentSchema(t *testing.T) {
	s := testStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if !s.hasTable("channels") {
		t.Fatal("channels 表应存在")
	}
	if !s.hasColumn("channels", "base_url") {
		t.Fatal("channels.base_url 列应能探测到")
	}
	if s.hasColumn("channels", "definitely_not_a_column_xyz") {
		t.Fatal("不存在的列不应被探测到")
	}
}

// TestForeignKeyConstraints 钉住迁移建立的外键真的在拦截。
//
// 迁移是 ALTER TABLE ... ADD CONSTRAINT，一旦哪次改动让它没跑到、
// 或建表顺序变化导致它失败，约束会静默消失，而坏数据要等到
// 用户删渠道或导入备份时才暴露出来。
//
// 断言的是**具名约束 + 拦截行为**，不是「外键总数」：按名字断言没有
// 维护尾巴，而且直接覆盖 ON DELETE 的语义（CASCADE 带走）——
// 这才是应用层真正依赖的契约。
func TestForeignKeyConstraints(t *testing.T) {
	s := testStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	// 约束由 EnsureForeignKeys 建（main.go 在 Migrate 后单独调它）。
	// 这里显式补一次：测试库上没有服务跑过的话（fresh schema/全新库），
	// 本用例原来会「等外部环境建好约束」，环境空了就必 FAIL ——
	// 自给自足后两边都能跑（DROP IF EXISTS + ADD 幂等）。
	if err := s.EnsureForeignKeys(); err != nil {
		t.Fatalf("EnsureForeignKeys 失败: %v", err)
	}
	db := s.db

	// 断言只看**本 schema** 的约束：pg_constraint 里 conname 不区分 schema，
	// 测试库若与别的 schema 共库（scratch search_path 正是这么跑的），
	// 同名约束的行可能不止一条 —— 不限定的话断言会数错行（2026-10-26 评审时修复）。
	var n int64
	db.Raw(`SELECT count(*) FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace ns ON ns.oid = t.relnamespace
		WHERE c.conname = ? AND c.contype = 'f'
		  AND ns.nspname = ANY(current_schemas(true))`, "fk_channel_models_channel").Scan(&n)
	if n != 1 {
		t.Fatalf("当前 schema 的外键 fk_channel_models_channel 不存在：迁移里的 ADD CONSTRAINT 没有生效（count=%d）", n)
	}

	// 反向断言：channel_templates 已随功能下线 DROP，不该被迁移建回来。
	// 它曾经让应用起不来 —— 迁移里有一句 UPDATE 指向这张已删的表。
	if s.hasTable("channel_templates") {
		t.Error("channel_templates 应已删除；建回它会让「删表」前的那句迁移 UPDATE 重新炸掉启动")
	}

	// ---- 造一条自己的渠道 ----
	// 用能一眼认出的名字，绝不碰测试库里的既有行。
	const cname = "__fk_probe_channel__"
	cleanup := func() {
		db.Exec("DELETE FROM channel_models WHERE channel_id IN (SELECT id FROM channels WHERE name = ?)", cname)
		db.Exec("DELETE FROM channels WHERE name = ?", cname)
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := db.Exec(`INSERT INTO channels (name, base_url, protocol, enabled, created_at, updated_at)
		VALUES (?, 'http://127.0.0.1:1', 'openai-chat', true, now(), now())`, cname).Error; err != nil {
		t.Fatalf("造测试渠道失败: %v", err)
	}
	var cid uint
	db.Raw("SELECT id FROM channels WHERE name = ?", cname).Scan(&cid)
	if cid == 0 {
		t.Fatal("取渠道 id 失败")
	}

	// 1. 孤儿白名单行必须被拒（channel_models.channel_id → channels.id）
	if err := db.Exec(`INSERT INTO channel_models (channel_id, public_name, upstream_name, enabled, created_at, updated_at)
		VALUES (999999999, 'ghost', 'ghost', true, now(), now())`).Error; err == nil {
		db.Exec("DELETE FROM channel_models WHERE channel_id = 999999999")
		t.Error("指向不存在渠道的白名单行应被外键拒绝，实际插入成功（约束没生效）")
	} else if !strings.Contains(err.Error(), "foreign key") {
		// 区分「被外键拒绝」与「因别的约束失败」：只看 err != nil 的话，
		// 万一撞的是 NOT NULL 或唯一索引，测试会假绿成「外键生效了」
		t.Errorf("应因外键被拒，实际错误: %v", err)
	}

	// 2. 渠道没了，它的白名单行跟着走（ON DELETE CASCADE）
	if err := db.Exec(`INSERT INTO channel_models (channel_id, public_name, upstream_name, enabled, created_at, updated_at)
		VALUES (?, '__fk_probe_model__', '__fk_probe_model__', true, now(), now())`, cid).Error; err != nil {
		t.Fatalf("造测试白名单行失败: %v", err)
	}
	if err := db.Exec("DELETE FROM channels WHERE id = ?", cid).Error; err != nil {
		t.Fatalf("删除渠道失败（CASCADE 下这一步应只删渠道本身）: %v", err)
	}
	var left int64
	db.Raw("SELECT count(*) FROM channel_models WHERE channel_id = ?", cid).Scan(&left)
	if left != 0 {
		t.Errorf("渠道删除后其白名单行应被 CASCADE 带走，实际残留 %d 行", left)
	}
}
