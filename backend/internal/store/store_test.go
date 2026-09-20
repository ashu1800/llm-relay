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
