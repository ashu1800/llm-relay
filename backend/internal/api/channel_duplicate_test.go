package api

import (
	"os"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"llm-relay/internal/model"
)

// 复制渠道的命名必须保证不重名。
//
// 为什么值得单独测：channels.name **没有唯一索引**，重名不会报错，只会
// 让界面上出现两条一模一样的渠道 —— 用户分不清哪条是哪条，删除时更容易
// 删错。而连点两次「复制」是很常见的操作（没看到反馈就会再点一次）。
func TestUniqueChannelName(t *testing.T) {
	dsn := os.Getenv("LLMRELAY_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 LLMRELAY_TEST_DSN，跳过 PG 集成测试")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("连测试库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Channel{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM channels WHERE name LIKE 'dupname-%'`)
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	s := &Server{}

	t.Run("库里没有同名时用「原名（副本）」", func(t *testing.T) {
		if got := s.uniqueChannelName(db, "", "dupname-src"); got != "dupname-src（副本）" {
			t.Errorf("实际 %q", got)
		}
	})

	t.Run("已存在则依次加序号", func(t *testing.T) {
		for _, n := range []string{"dupname-src（副本）", "dupname-src（副本） 2"} {
			if err := db.Create(&model.Channel{Name: n, BaseURL: "http://x"}).Error; err != nil {
				t.Fatal(err)
			}
		}
		if got := s.uniqueChannelName(db, "", "dupname-src"); got != "dupname-src（副本） 3" {
			t.Errorf("应跳到序号 3，实际 %q", got)
		}
	})

	t.Run("用户指定名字优先，但重名仍要避让", func(t *testing.T) {
		if err := db.Create(&model.Channel{Name: "dupname-custom", BaseURL: "http://x"}).Error; err != nil {
			t.Fatal(err)
		}
		if got := s.uniqueChannelName(db, "dupname-custom", "whatever"); got != "dupname-custom 2" {
			t.Errorf("指定重名时应加序号，实际 %q", got)
		}
		if got := s.uniqueChannelName(db, "dupname-fresh", "whatever"); got != "dupname-fresh" {
			t.Errorf("指定不重名时应原样使用，实际 %q", got)
		}
	})

	t.Run("超长名字必须压在列宽内（varchar(128)）", func(t *testing.T) {
		// 这里的上限不是随手写的数字，而是 channels.name 的列宽。
		// 曾经按「varchar(191)」把上限设成 150 —— 实测插入直接报
		// value too long for type character varying(128)，而当时的测试
		// 断言的是 150，所以没能拦住。
		const columnWidth = 128

		long := strings.Repeat("名", 300)
		got := s.uniqueChannelName(db, long, "x")
		if r := []rune(got); len(r) > columnWidth {
			t.Errorf("名字必须 ≤ %d 字符（列宽），实际 %d", columnWidth, len(r))
		}

		// 真正插入一次：只断言长度不够，PostgreSQL 的字符计数口径
		// （按 rune 而非字节）才是最终裁判
		if err := db.Create(&model.Channel{Name: got, BaseURL: "http://x"}).Error; err != nil {
			t.Errorf("截断后的名字应当插得进去，实际失败: %v", err)
		}

		// 加到序号之后仍要放得下
		long2 := strings.Repeat("名", 128)
		if err := db.Create(&model.Channel{Name: long2, BaseURL: "http://x"}).Error; err != nil {
			t.Fatalf("铺满列宽的名字应可插入: %v", err)
		}
		got2 := s.uniqueChannelName(db, long2, "x")
		if r := []rune(got2); len(r) > columnWidth {
			t.Errorf("带序号的名字也必须 ≤ %d 字符，实际 %d", columnWidth, len(r))
		}
	})
}
