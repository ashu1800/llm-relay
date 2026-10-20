package api

// 实时日志推送的回归测试：判据是「这条查询在真实表结构上能跑通」。
//
// 为什么必须连库：2026-10-20 的事故是 hand-written Select 里写了三个
// json tag 名（inbound_protocol / upstream_protocol / stream），真实列名
// 是 inbound_proto / upstream_proto / is_stream。GORM 不校验列名，错误
// 只在执行时出现，又被调用方的 continue 吞掉 —— 单测不连库就永远发现不了。
// 这条用例用 AutoMigrate 建出与生产同一份表结构，再执行同一个查询。
//
// 用 LLMRELAY_TEST_DSN 门控（与 store 包的 PG 集成测试同一套）：
// 没设置时跳过，Windows 侧 go test 不红，WSL 侧 verify-all.sh 全量跑。

import (
	"os"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"llm-relay/internal/model"
)

// TestLiveLogQueryMatchesSchema 证明实时推送的查询在真实表结构上可执行，
// 且推送出去的字段确实是列表要用的那些。
func TestLiveLogQueryMatchesSchema(t *testing.T) {
	dsn := os.Getenv("LLMRELAY_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 LLMRELAY_TEST_DSN，跳过 PG 集成测试")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("连测试库失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, _ := db.DB(); sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	// 表结构以 model 为准：这正是 AutoMigrate 在生产库建出来的形状，
	// 列名与手写字符串对不对得上是本用例要回答的问题。
	if err := db.AutoMigrate(&model.RequestLog{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}

	// 造一行并读回：只查空表的话，列名写错同样会报错，但顺带证明字段映射对。
	row := model.RequestLog{
		TraceID:        "live-query-probe",
		InboundProto:   "anthropic-messages",
		UpstreamProto:  "openai-chat",
		ModelRequested: "probe-model",
		IsStream:       true,
		StatusCode:     200,
		ClientID:       "claude-code",
		ClientUA:       "claude-cli/2.1.299",
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("插入探针行失败: %v", err)
	}
	t.Cleanup(func() { db.Delete(&model.RequestLog{}, row.ID) })

	rows, err := liveLogQuery(db, row.ID-1)
	if err != nil {
		t.Fatalf("实时日志查询在真实表结构上失败（列名写错了？）: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("应查到 1 行，实际 %d", len(rows))
	}
	got := rows[0]
	// 三个曾经写错的列，逐个确认接到了正确的字段
	if got.InboundProto != "anthropic-messages" {
		t.Errorf("inbound_proto 未正确映射，实际 %q", got.InboundProto)
	}
	if got.UpstreamProto != "openai-chat" {
		t.Errorf("upstream_proto 未正确映射，实际 %q", got.UpstreamProto)
	}
	if !got.IsStream {
		t.Error("is_stream 未正确映射")
	}
	// 被 Omit 掉的列必须是零值：它们不进推送帧（client_ua 是 1KB 的原始 UA）
	if got.ClientUA != "" {
		t.Errorf("client_ua 应在 Omit 里（不随推送发出），实际 %q", got.ClientUA)
	}
	if got.PricingSnapshot != nil || got.RetryTrail != nil {
		t.Errorf("pricing_snapshot / retry_trail 应被 Omit，实际 %v / %v", got.PricingSnapshot, got.RetryTrail)
	}
}
