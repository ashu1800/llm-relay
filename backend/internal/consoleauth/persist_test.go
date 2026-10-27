package consoleauth

import (
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// 持久化往返测试：Load/Save 走真实 PostgreSQL。
//
// 为什么必须有一条真库测试：内存版 Store 绕开了 Load/Save，而这两条
// 路径曾经同时错得很隐蔽 —— 写入成功、重启后读不回来（jsonb 扫进
// []byte 时驱动报错），认证状态静默退回 unset，用户以为 2FA 已生效，
// 实际引导密钥又活了。任何用 fake 的测试都发现不了。
//
// 用 LLMRELAY_TEST_DSN 门控（与 store 包的集成测试同一套约定），
// 没设置时跳过。
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("LLMRELAY_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 LLMRELAY_TEST_DSN，跳过 PG 集成测试")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("连测试库失败: %v", err)
	}
	// settings 表可能不存在（单独跑本包时）：按 model.Setting 的形状建一张
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS settings (
		key varchar(64) PRIMARY KEY,
		value jsonb,
		updated_at timestamptz
	)`).Error; err != nil {
		t.Fatalf("建 settings 表失败: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM settings WHERE key = ?`, StateKey).Error
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestStatePersistsAcrossStoreReopen(t *testing.T) {
	db := openTestDB(t)
	sealer, err := NewSealer("test-master")
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Exec(`DELETE FROM settings WHERE key = ?`, StateKey).Error

	store, err := NewStore(db, sealer)
	if err != nil {
		t.Fatalf("首次打开状态失败: %v", err)
	}
	if store.Snapshot().Method != MethodUnset {
		t.Fatal("空库应为 unset")
	}

	now := time.Unix(1_800_000_000, 0).UTC()
	// 绑 2FA：confirm 时先拿 pending 的明文算码
	if _, _, err := store.SetupTOTPInit(); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingPlainForTest()
	if err != nil {
		t.Fatal(err)
	}
	code, err := Code(pending, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetupTOTPConfirm(code, now); err != nil {
		t.Fatalf("确认绑定失败: %v", err)
	}
	versionBefore := store.Snapshot().CredVersion

	// 模拟进程重启：重新打开一个 Store，状态必须原样回来
	reopened, err := NewStore(db, sealer)
	if err != nil {
		t.Fatalf("重启后读取状态失败（持久化 bug）: %v", err)
	}
	st := reopened.Snapshot()
	if st.Method != MethodTOTP {
		t.Fatalf("重启后认证方式 = %q，期望 totp（状态没有真正落库）", st.Method)
	}
	if st.TOTPSecretEnc == "" {
		t.Fatal("重启后 2FA 密钥密文丢失")
	}
	if st.CredVersion != versionBefore {
		t.Fatalf("重启后会话版本 = %q，期望 %q（旧会话会全部失效）", st.CredVersion, versionBefore)
	}
	// 重启后用原来的密钥仍能解出 TOTP（密文与主密钥一致）
	plain, err := reopened.TOTPSecretPlainForTest()
	if err != nil || plain != pending {
		t.Fatalf("重启后解出的 2FA 密钥不一致: %v", err)
	}
	// 且仍能验证动态码（防重放缓存是进程内的，重启后同一码可再用 ——
	// 这是可接受的语义：缓存的作用窗口本来只有 30 秒）
	next := now.Add(totpStep)
	code2, _ := Code(plain, next)
	if err := reopened.VerifyTOTP(code2, next); err != nil {
		t.Fatalf("重启后动态码验证失败: %v", err)
	}
}

func TestStateKeyChangePersists(t *testing.T) {
	db := openTestDB(t)
	sealer, _ := NewSealer("test-master")
	_ = db.Exec(`DELETE FROM settings WHERE key = ?`, StateKey).Error

	store, err := NewStore(db, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetupKey("0123456789abcdef-key"); err != nil {
		t.Fatal(err)
	}
	if err := store.ChangeKey("0123456789abcdef-key", "new-key-0123456789"); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewStore(db, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.MatchKey("new-key-0123456789") {
		t.Fatal("重启后新密钥应生效")
	}
	if reopened.MatchKey("0123456789abcdef-key") {
		t.Fatal("重启后旧密钥不应再生效")
	}
}
