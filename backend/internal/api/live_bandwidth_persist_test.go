package api

import (
	"testing"
	"time"

	"llm-relay/internal/model"
	"llm-relay/internal/netstat"
)

// 带宽累计值编解码的单元测试（2026-11-05 持久化）。
//
// 这些断言看起来琐碎，但每一条都对应一个会让「重启后数字变样」的坑：
// jsonb 数字精度、半组计数器、旧数据的手工改库形态。

func TestBandwidthSettingRoundtrip(t *testing.T) {
	p := netstat.Persisted{
		RxTotal:   1<<40 + 7, // 超过 2^53 的量级也要精确
		TxTotal:   3,
		RxCounter: 99,
		TxCounter: 100,
		Interface: "eth0",
		SavedAt:   time.Unix(1_700_000_000, 123).UTC(),
	}
	got, ok := bandwidthStateFromValue(bandwidthSettingValue(p))
	if !ok {
		t.Fatal("自己写出的值必须能解析回来")
	}
	if got.RxTotal != p.RxTotal || got.TxTotal != p.TxTotal {
		t.Fatalf("累计值不一致：rx=%d tx=%d", got.RxTotal, got.TxTotal)
	}
	if got.RxCounter != p.RxCounter || got.TxCounter != p.TxCounter || got.Interface != p.Interface {
		t.Fatalf("计数器信息不一致：%+v", got)
	}
	if !got.SavedAt.Equal(p.SavedAt) {
		t.Fatalf("SavedAt 不一致：%v vs %v", got.SavedAt, p.SavedAt)
	}
}

// 总数缺一个 / 非法 → 整行作废。宁可从头计数，也不要把半个状态当真。
func TestBandwidthStateRejectsIncompleteTotals(t *testing.T) {
	for _, v := range []model.JSONMap{
		{},
		{"rx_total": "1"},
		{"rx_total": "1", "tx_total": "x"},
		{"rx_total": "-1", "tx_total": "2"},
	} {
		if _, ok := bandwidthStateFromValue(v); ok {
			t.Errorf("不完整/非法的值不该被接受：%#v", v)
		}
	}
}

// 原始计数器不完整（手工改库、旧版本数据）时只恢复总数 —— 不能拿半组
// 数据当基线，否则重启后会补出一笔凭空的增量。
func TestBandwidthStateWithoutCounterOnlyRestoresTotals(t *testing.T) {
	got, ok := bandwidthStateFromValue(model.JSONMap{
		"rx_total":   "10",
		"tx_total":   "20",
		"rx_counter": "5", // 只有一半
		"interface":  "eth0",
	})
	if !ok {
		t.Fatal("总数完整就应接受")
	}
	if got.RxTotal != 10 || got.TxTotal != 20 {
		t.Fatalf("总数不对：rx=%d tx=%d", got.RxTotal, got.TxTotal)
	}
	if got.RxCounter != 0 || got.TxCounter != 0 || got.Interface != "" || !got.SavedAt.IsZero() {
		t.Fatalf("计数器信息不完整时应整组丢弃：%+v", got)
	}
}

// 旧数据 / 手工改库可能写成 JSON number：能读就接着用，别把历史清零。
func TestBandwidthStateAcceptsJSONNumbers(t *testing.T) {
	got, ok := bandwidthStateFromValue(model.JSONMap{
		"rx_total": float64(1024),
		"tx_total": float64(2048),
	})
	if !ok || got.RxTotal != 1024 || got.TxTotal != 2048 {
		t.Fatalf("数字形态应能解析，实际 ok=%v %+v", ok, got)
	}
}
