package update

// resolveUpdateProxyURL 的集成测试：验证「勾选代理优先，手填兜底」的完整优先级。
//
// 与 store 包同一套门控（LLMRELAY_TEST_DSN 指向专用测试库），没配置就跳过
// —— Windows 侧 go test 不红，WSL 侧 verify-all.sh export 后全量跑。
// 不拿 sqlite 凑合：for_update/enabled 的布尔落库行为要按真实库验证，
// 这是 store 包立下的既有约定。

import (
	"log/slog"
	"os"
	"strings"
	"testing"

	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/secure"
	"llm-relay/internal/store"
)

func testProxyDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("LLMRELAY_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 LLMRELAY_TEST_DSN，跳过 PG 集成测试")
	}
	s, err := store.OpenByDSN(dsn)
	if err != nil {
		t.Fatalf("连测试库失败: %v", err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	db := s.DB()
	// 勾选状态是全局唯一的，测试之间必须互不残留
	db.Exec("DELETE FROM proxies")
	t.Cleanup(func() {
		db.Exec("DELETE FROM proxies")
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// newResolveService 直构 Service：只测代理解析这一条路径，
// 不走 NewService（那会读 settings 表，与本测试无关）。
func newResolveService(db *gorm.DB, cipher *secure.Cipher) *Service {
	return &Service{db: db, logger: slog.Default(), cipher: cipher}
}

func insertProxy(t *testing.T, db *gorm.DB, row model.Proxy) {
	t.Helper()
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("插入代理失败: %v", err)
	}
}

func TestResolveUpdateProxyURLPriority(t *testing.T) {
	db := testProxyDB(t)
	cipher, err := secure.NewCipher("update-proxy-test-secret")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("没有勾选代理时用手填兜底", func(t *testing.T) {
		db.Exec("DELETE FROM proxies")
		s := newResolveService(db, cipher)
		if got := s.resolveUpdateProxyURL("socks5://fallback:1080"); got != "socks5://fallback:1080" {
			t.Fatalf("应回退手填地址，实际 %q", got)
		}
	})

	t.Run("勾选且启用的代理优先于手填", func(t *testing.T) {
		db.Exec("DELETE FROM proxies")
		insertProxy(t, db, model.Proxy{Name: "upd-test-a", Protocol: "socks5", Host: "10.0.0.1", Port: 1080, Enabled: true, ForUpdate: true})
		s := newResolveService(db, cipher)
		if got := s.resolveUpdateProxyURL("socks5://fallback:1080"); got != "socks5://10.0.0.1:1080" {
			t.Fatalf("应优先勾选代理，实际 %q", got)
		}
	})

	t.Run("勾选但停用的代理不生效", func(t *testing.T) {
		db.Exec("DELETE FROM proxies")
		insertProxy(t, db, model.Proxy{Name: "upd-test-b", Protocol: "socks5", Host: "10.0.0.2", Port: 1080, Enabled: false, ForUpdate: true})
		s := newResolveService(db, cipher)
		if got := s.resolveUpdateProxyURL(""); got != "" {
			t.Fatalf("停用的勾选代理应回退手填（此处为空即直连），实际 %q", got)
		}
	})

	t.Run("密码用主密钥解密后拼进 URL", func(t *testing.T) {
		db.Exec("DELETE FROM proxies")
		enc, err := cipher.Encrypt("pa55")
		if err != nil {
			t.Fatal(err)
		}
		insertProxy(t, db, model.Proxy{Name: "upd-test-c", Protocol: "http", Host: "192.168.1.1", Port: 7890, Username: "u", PasswordEnc: enc, Enabled: true, ForUpdate: true})
		s := newResolveService(db, cipher)
		if got := s.resolveUpdateProxyURL(""); got != "http://u:pa55@192.168.1.1:7890" {
			t.Fatalf("密码应解密并写入 userinfo，实际 %q", got)
		}
	})

	t.Run("解不开密码时回退手填而不是报错", func(t *testing.T) {
		db.Exec("DELETE FROM proxies")
		enc, err := cipher.Encrypt("pa55")
		if err != nil {
			t.Fatal(err)
		}
		insertProxy(t, db, model.Proxy{Name: "upd-test-d", Protocol: "socks5", Host: "10.0.0.3", Port: 1080, Username: "u", PasswordEnc: enc, Enabled: true, ForUpdate: true})
		// nil cipher：主密钥缺失的部署形态
		s := newResolveService(db, nil)
		got := s.resolveUpdateProxyURL("socks5://fallback:1080")
		if !strings.HasPrefix(got, "socks5://fallback") {
			t.Fatalf("解密失败应回退手填地址，实际 %q", got)
		}
	})
}
