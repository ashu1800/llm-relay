package store

// PG 集成测试：覆盖迁移幂等、老库补列、权重重排。
//
// 用 LLMRELAY_TEST_DSN 门控（指向一个**专用测试库**，测试会建表、写测试行）：
// 没设置时整组跳过 —— Windows 侧 go test 不红，WSL 侧 verify-all.sh
// export 后全量跑。此前 internal/store 零测试，迁移逻辑只被 60+ 个
// E2E 脚本间接覆盖，出问题定位链路很长。
//
// 用法（WSL）：
//	export LLMRELAY_TEST_DSN="host=127.0.0.1 port=5432 user=llmrelay password=... dbname=llm_relay_test sslmode=disable"
//	go test ./internal/store/ -v

import (
	"os"
	"strings"
	"testing"
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
func TestMigrateFreshDatabase(t *testing.T) {
	s := testStore(t)
	// 清掉本测试库里的所有表（专用测试库才允许这么干）
	tables := []string{"request_payloads", "request_logs", "channel_models",
		"channels", "channel_groups", "api_keys", "proxies", "settings"}
	for _, tbl := range tables {
		s.db.Exec("DROP TABLE IF EXISTS " + tbl + " CASCADE")
	}
	if err := s.Migrate(); err != nil {
		t.Fatalf("全新库迁移失败: %v", err)
	}
	for _, tbl := range tables {
		if !s.hasTable(tbl) {
			t.Errorf("迁移后应存在表 %s", tbl)
		}
	}
}

// TestNormalizeChannelWeightsRepairsDuplicates 同分组重名次被重排为连续序号，
// 且重跑一遍不再改动（收敛）。
func TestNormalizeChannelWeightsRepairsDuplicates(t *testing.T) {
	s := testStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	// 造一个分组，两条渠道故意同 weight=1（老库的常态）
	db := s.db
	db.Exec("DELETE FROM channels WHERE group_id IN (SELECT id FROM channel_groups WHERE name = ?)", "w-test")
	db.Exec("DELETE FROM channel_groups WHERE name = ?", "w-test")
	var gid uint
	if err := db.Exec("INSERT INTO channel_groups (name, strategy, created_at, updated_at) VALUES (?, 'failover', now(), now()) RETURNING id", "w-test").Error; err != nil {
		t.Skipf("RETURNING 语法不可用，跳过: %v", err)
	}
	if err := db.Raw("SELECT id FROM channel_groups WHERE name = ?", "w-test").Scan(&gid).Error; err != nil || gid == 0 {
		t.Skipf("取分组 id 失败: %v", err)
	}
	db.Exec("INSERT INTO channels (name, base_url, protocol, group_id, weight, enabled, created_at, updated_at) VALUES ('wa', 'http://a', 'openai-chat', ?, 1, true, now(), now())", gid)
	db.Exec("INSERT INTO channels (name, base_url, protocol, group_id, weight, enabled, created_at, updated_at) VALUES ('wb', 'http://b', 'openai-chat', ?, 1, true, now(), now())", gid)
	t.Cleanup(func() {
		db.Exec("DELETE FROM channels WHERE group_id = ?", gid)
		db.Exec("DELETE FROM channel_groups WHERE id = ?", gid)
	})

	if err := s.normalizeChannelWeights(); err != nil {
		t.Fatalf("重排失败: %v", err)
	}
	var weights []int
	db.Raw("SELECT weight FROM channels WHERE group_id = ? ORDER BY weight", gid).Scan(&weights)
	if len(weights) != 2 || weights[0] != 1 || weights[1] != 2 {
		t.Fatalf("重排后应为 [1 2]，实际 %v", weights)
	}
	// 唯一索引在 Migrate 里建 —— 重排后建索引必须不撞车
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_channels_group_weight ON channels (group_id, weight)").Error; err != nil {
		t.Fatalf("重排后建唯一索引失败（说明序号仍有重复）: %v", err)
	}
	// 收敛：重跑不再改动
	if err := s.normalizeChannelWeights(); err != nil {
		t.Fatal(err)
	}
	var again []int
	db.Raw("SELECT weight FROM channels WHERE group_id = ? ORDER BY weight", gid).Scan(&again)
	if len(again) != 2 || again[0] != 1 || again[1] != 2 {
		t.Fatalf("重跑后应保持 [1 2]，实际 %v", again)
	}
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

// TestForeignKeyConstraints 钉住迁移建立的两条外键真的在拦截。
//
// 这两条约束原先由 scripts/test-foreign-keys.sh 验证，但那是 Docker 时代的
// 脚本，随部署形态改成「裸二进制 + systemd」一起退役（fe2da29）—— 验证没有
// 跟着搬过来，于是 store.go 里的两条 FOREIGN KEY 一直处于「没有任何自动化
// 证明它生效」的状态。迁移是 ALTER TABLE ... ADD CONSTRAINT，一旦哪次改动
// 让它没跑到、或建表顺序变化导致它失败，约束会静默消失，而坏数据要等到
// 用户删分组或导入备份时才暴露出来。
//
// 断言的是**具名约束 + 拦截行为**，不是「外键总数」：原先的脚本数
// count(*) = 2，它自己的注释就写着「每删一个功能都要回来改这个数字，
// 否则会以外键数量不对误报」。按名字断言没有这个维护尾巴，而且直接覆盖
// ON DELETE 的两种语义（CASCADE 带走 / RESTRICT 拦住）—— 这两个才是
// 应用层真正依赖的契约。
func TestForeignKeyConstraints(t *testing.T) {
	s := testStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	db := s.db

	for _, name := range []string{"fk_channel_models_channel", "fk_channels_group"} {
		var n int64
		db.Raw("SELECT count(*) FROM pg_constraint WHERE conname = ? AND contype = 'f'", name).Scan(&n)
		if n != 1 {
			t.Fatalf("外键 %s 不存在：迁移里的 ADD CONSTRAINT 没有生效", name)
		}
	}

	// 反向断言：channel_templates 已随功能下线 DROP，不该被迁移建回来。
	// 它曾经让应用起不来 —— 迁移里有一句 UPDATE 指向这张已删的表。
	if s.hasTable("channel_templates") {
		t.Error("channel_templates 应已删除；建回它会让「删表」前的那句迁移 UPDATE 重新炸掉启动")
	}

	// ---- 造一套自己的数据：分组 → 渠道 → 白名单 ----
	// 用能一眼认出的名字，绝不碰测试库里的既有行。
	const gname = "__fk_probe_group__"
	const cname = "__fk_probe_channel__"
	cleanup := func() {
		db.Exec("DELETE FROM channel_models WHERE channel_id IN (SELECT id FROM channels WHERE name = ?)", cname)
		db.Exec("DELETE FROM channels WHERE name = ?", cname)
		db.Exec("DELETE FROM channel_groups WHERE name = ?", gname)
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := db.Exec(`INSERT INTO channel_groups (name, strategy, enabled, created_at, updated_at)
		VALUES (?, 'failover', true, now(), now())`, gname).Error; err != nil {
		t.Skipf("造测试分组失败，跳过: %v", err)
	}
	var gid uint
	db.Raw("SELECT id FROM channel_groups WHERE name = ?", gname).Scan(&gid)
	if gid == 0 {
		t.Skip("取分组 id 失败，跳过")
	}
	if err := db.Exec(`INSERT INTO channels (name, base_url, protocol, group_id, weight, enabled, created_at, updated_at)
		VALUES (?, 'http://127.0.0.1:1', 'openai-chat', ?, 1, true, now(), now())`, cname, gid).Error; err != nil {
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

	// 2. 仍有渠道的分组删不掉（channels.group_id → channel_groups.id，RESTRICT）
	if err := db.Exec("DELETE FROM channel_groups WHERE id = ?", gid).Error; err == nil {
		t.Error("删除仍有渠道的分组应被外键拒绝，实际删成功（渠道会变成悬挂引用）")
	} else if !strings.Contains(err.Error(), "foreign key") {
		t.Errorf("应因外键被拒，实际错误: %v", err)
	}

	// 3. 渠道没了，它的白名单行跟着走（ON DELETE CASCADE）
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

	// 4. 分组空了之后必须删得掉 —— 证明 RESTRICT 是「拦住」不是「锁死」
	if err := db.Exec("DELETE FROM channel_groups WHERE id = ?", gid).Error; err != nil {
		t.Errorf("渠道已全部删除，空分组应可删除，实际: %v", err)
	}
}
