package api

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/netstat"
)

// 带宽累计流量的持久化（2026-11-05 站主要求）。
//
// 为什么需要：看板展开带宽格看到的「↓ / ↑ 累计」原本是进程内计数，
// 每次重启归零 —— 站主要的是一个能跨重启对账的数字。
//
// 为什么不直接用网卡自开机以来的计数：那是「这台机器开机以来」，
// 换网卡 / 重启机器都会倒着跳，而且和本服务的观测窗口不是一回事。
// 这里保存的是服务自己的累计值（见 netstat.Clock），同时带上最后一次
// 读到的原始计数器，让进程重启后能把「上次保存 → 本次启动」的增量补回来。
//
// 存在 settings 表（key = bandwidth_totals_v1），与其余单行配置同一条路：
// 备份 / 恢复时跟着库走，不需要新表。多实例共用一个库时会互相覆盖 ——
// 本项目是单实例部署（systemd / SCM），这个前提与其余运行期状态一致。
const bandwidthSettingKey = "bandwidth_totals_v1"

// bandwidthPersistInterval 是落盘间隔。代价是一行 UPDATE；崩溃时最多丢
// 30 秒的增量（优雅退出会在 ctx.Done 时补一次，正常重启不丢）。
//
// 是 var 而不是 const：PG 集成测试会把它改小，否则一个用例要真等 30 秒。
// 与 heartbeatInterval / bandwidthInterval 同一套做法。
var bandwidthPersistInterval = 30 * time.Second

// bandwidthSettingValue 把累计状态编码成 settings 行的 jsonb 值。
//
// 数字用字符串而不是 JSON number：累计字节会到 GB / TB 量级，jsonb 的
// number 在解码端是 float64，超过 2^53 就不再精确；而这是一个要对账的
// 数字，不该有精度损失。原始计数器只在完整时才写 —— 缺了它只是补不了
// 停机期间的增量，不影响总数。
func bandwidthSettingValue(p netstat.Persisted) model.JSONMap {
	v := model.JSONMap{
		"rx_total": strconv.FormatUint(p.RxTotal, 10),
		"tx_total": strconv.FormatUint(p.TxTotal, 10),
	}
	if p.Interface != "" && !p.SavedAt.IsZero() {
		v["rx_counter"] = strconv.FormatUint(p.RxCounter, 10)
		v["tx_counter"] = strconv.FormatUint(p.TxCounter, 10)
		v["interface"] = p.Interface
		v["saved_at"] = p.SavedAt.UTC().Format(time.RFC3339Nano)
	}
	return v
}

// bandwidthStateFromValue 解析 settings 行。第二个返回值表示「这是不是一个
// 可用的累计状态」：rx_total / tx_total 缺一个就整行作废（宁可从头计数，
// 也不要把半个状态当成真的接着用）。原始计数器不完整时只恢复总数。
func bandwidthStateFromValue(v model.JSONMap) (netstat.Persisted, bool) {
	var p netstat.Persisted
	rx, ok := jsonUint(v["rx_total"])
	if !ok {
		return p, false
	}
	tx, ok := jsonUint(v["tx_total"])
	if !ok {
		return p, false
	}
	p.RxTotal, p.TxTotal = rx, tx

	// 原始计数器是可选增强：缺一个就整组忽略（半组数据补不了账，
	// 还会把「上一拍」的基线设错）。
	rc, okRC := jsonUint(v["rx_counter"])
	tc, okTC := jsonUint(v["tx_counter"])
	iface, _ := v["interface"].(string)
	savedAt, okAt := jsonTime(v["saved_at"])
	if okRC && okTC && iface != "" && okAt {
		p.RxCounter, p.TxCounter = rc, tc
		p.Interface, p.SavedAt = iface, savedAt
	}
	return p, true
}

// jsonUint 接受字符串（本服务的写入口径）与数字（手工改库 / 旧数据兜底）。
func jsonUint(v any) (uint64, bool) {
	switch x := v.(type) {
	case string:
		n, err := strconv.ParseUint(x, 10, 64)
		return n, err == nil
	case float64:
		// jsonb number 解码成 float64；负数 / 超出 uint64 的一律拒绝。
		if x < 0 || x >= math.MaxUint64 {
			return 0, false
		}
		return uint64(x), true
	default:
		return 0, false
	}
}

func jsonTime(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// loadBandwidthState 读上一次保存的累计状态。
//
// 返回 found=false 且 err=nil 只表示「还没有这一行」（首次启动）。
// 真错误（库不可用 / 内容损坏）必须与它区分开：调用方在真错误时
// **不写盘**，否则一次启动期的读失败会把历史累计清零。
func loadBandwidthState(ctx context.Context, db *gorm.DB) (netstat.Persisted, bool, error) {
	if db == nil {
		return netstat.Persisted{}, false, nil
	}
	var row model.Setting
	// 用 Find 而不是 Take：首次启动没有这一行是正常状态，Take 会返回
	// ErrRecordNotFound 并让 GORM 在每次启动时打一行「record not found」。
	res := db.WithContext(ctx).Where("key = ?", bandwidthSettingKey).Limit(1).Find(&row)
	if res.Error != nil {
		return netstat.Persisted{}, false, res.Error
	}
	if res.RowsAffected == 0 {
		return netstat.Persisted{}, false, nil
	}
	p, ok := bandwidthStateFromValue(row.Value)
	if !ok {
		return netstat.Persisted{}, false, fmt.Errorf("设置 %s 的内容无法解析", bandwidthSettingKey)
	}
	return p, true, nil
}

// saveBandwidthState 写回累计状态（upsert）。
func saveBandwidthState(ctx context.Context, db *gorm.DB, p netstat.Persisted) error {
	if db == nil {
		return nil
	}
	row := model.Setting{Key: bandwidthSettingKey, Value: bandwidthSettingValue(p)}
	return db.WithContext(ctx).Save(&row).Error
}
