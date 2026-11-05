package api

import (
	"context"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"llm-relay/internal/config"
	"llm-relay/internal/model"
	"llm-relay/internal/netstat"
	"llm-relay/internal/store"
)

// 带宽累计值落库的 PG 集成测试（LLMRELAY_TEST_DSN 门控）。
//
// 真库才能暴露的两件事：settings 行的 upsert 在真实主键约束下的行为
// （Save 的 UPDATE → 无行时 INSERT 分支），以及 jsonb 往返之后数字的形态。
func openBandwidthTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("LLMRELAY_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 LLMRELAY_TEST_DSN，跳过 PG 集成测试")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("连测试库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Setting{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM settings WHERE key = ?`, bandwidthSettingKey)
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestBandwidthTotalsPersistInSettings(t *testing.T) {
	db := openBandwidthTestDB(t)
	ctx := context.Background()

	// 首次启动：没有行 → found=false（不是错误）
	if _, found, err := loadBandwidthState(ctx, db); err != nil || found {
		t.Fatalf("空库应返回 found=false 且无错误，实际 found=%v err=%v", found, err)
	}

	p := netstat.Persisted{
		RxTotal:   5_000_000_000, // > 2^32，覆盖大数精度
		TxTotal:   7_000_000_001,
		RxCounter: 123_456,
		TxCounter: 654_321,
		Interface: "eth0",
		SavedAt:   time.Unix(1_700_000_000, 0).UTC(),
	}
	if err := saveBandwidthState(ctx, db, p); err != nil {
		t.Fatalf("首次保存失败: %v", err)
	}
	got, found, err := loadBandwidthState(ctx, db)
	if err != nil || !found {
		t.Fatalf("保存后应能读回，实际 found=%v err=%v", found, err)
	}
	if got.RxTotal != p.RxTotal || got.TxTotal != p.TxTotal {
		t.Fatalf("累计值往返不一致：rx=%d tx=%d", got.RxTotal, got.TxTotal)
	}
	if got.RxCounter != p.RxCounter || got.TxCounter != p.TxCounter || got.Interface != p.Interface {
		t.Fatalf("计数器信息往返不一致：%+v", got)
	}

	// 第二次保存走 UPDATE 分支：总数必须被覆盖，而不是插入第二行
	p.RxTotal += 1_000
	if err := saveBandwidthState(ctx, db, p); err != nil {
		t.Fatalf("覆盖保存失败: %v", err)
	}
	got, _, err = loadBandwidthState(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if got.RxTotal != p.RxTotal {
		t.Fatalf("覆盖保存后应读到新值，期望 %d，实际 %d", p.RxTotal, got.RxTotal)
	}
	var n int64
	if err := db.Model(&model.Setting{}).Where("key = ?", bandwidthSettingKey).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("同一 key 只应有一行，实际 %d 行", n)
	}
}

// 内容损坏（手工改库写错）要报错，而不是静默从 0 开始 —— 调用方据此
// 关闭本次运行的写盘，避免把历史累计覆盖掉。
func TestBandwidthCorruptSettingIsAnError(t *testing.T) {
	db := openBandwidthTestDB(t)
	ctx := context.Background()

	if err := db.Save(&model.Setting{
		Key:   bandwidthSettingKey,
		Value: model.JSONMap{"rx_total": "oops", "tx_total": "2"},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadBandwidthState(ctx, db); err == nil {
		t.Fatal("内容损坏应返回错误")
	}
}

// 端到端接线：启动时读回、运行中按间隔落盘、退出时补写。
//
// 单测覆盖了 Resume / Snapshot 的算术，这里钉住「采样循环真的调了它们」——
// 漏掉任何一个，用户看到的现象都正是「重启后归零」。
func TestBandwidthLoopPersistsAcrossRestart(t *testing.T) {
	dsn := os.Getenv("LLMRELAY_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 LLMRELAY_TEST_DSN，跳过 PG 集成测试")
	}
	st, err := store.OpenByDSN(dsn)
	if err != nil {
		t.Fatalf("连测试库失败: %v", err)
	}
	db := st.DB()
	t.Cleanup(func() {
		db.Exec(`DELETE FROM settings WHERE key = ?`, bandwidthSettingKey)
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	ctx := context.Background()
	if err := saveBandwidthState(ctx, db, netstat.Persisted{RxTotal: 12_345, TxTotal: 67_890}); err != nil {
		t.Fatal(err)
	}
	var before model.Setting
	if err := db.Where("key = ?", bandwidthSettingKey).Take(&before).Error; err != nil {
		t.Fatal(err)
	}

	oldSample, oldPersist := bandwidthInterval, bandwidthPersistInterval
	bandwidthInterval = 20 * time.Millisecond
	bandwidthPersistInterval = 50 * time.Millisecond
	defer func() { bandwidthInterval, bandwidthPersistInterval = oldSample, oldPersist }()

	s := &Server{deps: &Deps{Config: &config.Config{}, Live: NewLiveHub(nil), Store: st}}
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.liveBandwidthLoop(runCtx)
	}()

	// 等到运行期真的写了一次（UpdatedAt 前进），而不是只在启动时读到了旧行。
	deadline := time.Now().Add(3 * time.Second)
	for {
		var row model.Setting
		if err := db.Where("key = ?", bandwidthSettingKey).Take(&row).Error; err == nil &&
			row.UpdatedAt.After(before.UpdatedAt) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("运行期没有把累计值落盘")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("采样循环未在 ctx 取消后退出")
	}

	// 退出补写：数字不能比启动时读回的更小。
	got, found, err := loadBandwidthState(ctx, db)
	if err != nil || !found {
		t.Fatalf("退出后应仍有一行状态：found=%v err=%v", found, err)
	}
	if got.RxTotal < 12_345 || got.TxTotal < 67_890 {
		t.Fatalf("累计值在退出流程中倒退了：rx=%d tx=%d", got.RxTotal, got.TxTotal)
	}
}
