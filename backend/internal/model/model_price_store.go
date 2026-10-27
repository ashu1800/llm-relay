package model

import (
	"strings"

	"gorm.io/gorm"
)

// model_prices 的 upsert SQL 只有一份，因为它在五个地方要用：
// 管理接口保存、备份导入（新格式与老格式各一条路径）、以及启动迁移。
//
// 为什么必须共享：这段 SQL 手写 8 个列名 × 5 处 = 40 个必须逐字一致的点。
// 加一列或改一次名要同时改五处，漏掉的那处不会报错 —— 表现是
// 「接口返回 200、库里那几列没变」，只能靠人肉对账发现。
//
// 更关键的是列名本身反直觉：GORM 由 USDInputPer1M 推导出的是
// usd_input_per1_m（数字 1 跟着 per），写成人更容易想到的
// usd_input_per_1_m 就会「列不存在」。所以这里连字面量都不写，
// 全部引用 columns.go 的常量 —— 列名只有一处定义，改错会被
// columns_test.go 当场拦下。
//
// 两个变体共用同一份列清单：
//   - UpsertModelPrice        冲突时覆盖（管理接口保存、新格式备份导入）；
//   - InsertModelPriceIfAbsent 冲突时跳过（启动迁移、老格式备份导入 ——
//     那两条路径的语义是「补缺」，不能用旧值盖掉用户已经改好的价）。
var modelPriceColumns = []string{
	ColPriceModel,
	ColPriceUSDInputPer1M, ColPriceUSDOutputPer1M, ColPriceUSDCacheReadPer1M, ColPriceUSDWritePer1M,
	ColPriceRMBInputPer1M, ColPriceRMBOutputPer1M, ColPriceRMBCacheReadPer1M, ColPriceRMBWritePer1M,
}

// modelPriceColumnList 生成 「(a, b, c)」 形式的列清单（含 created_at/updated_at）。
func modelPriceColumnList() string {
	return "(" + strings.Join(modelPriceColumns, ", ") + ", created_at, updated_at)"
}

// modelPriceArgs 按列顺序取值，保证参数与占位符一一对应。
// 少给一个参数会静默错位 —— 那比缺列更难查，所以顺序只写在这里。
func modelPriceArgs(p ModelPrice) []any {
	return []any{
		p.ModelName,
		p.USDInputPer1M, p.USDOutputPer1M, p.USDCacheReadPer1M, p.USDCacheWritePer1M,
		p.RMBInputPer1M, p.RMBOutputPer1M, p.RMBCacheReadPer1M, p.RMBCacheWritePer1M,
	}
}

// UpsertModelPrice 写入或覆盖一条模型价（按 model_name 唯一键）。
func UpsertModelPrice(db *gorm.DB, p ModelPrice) error {
	// DO UPDATE 的赋值列表同样引用常量：手写一遍就等于又把列名抄了一次。
	set := make([]string, 0, len(modelPriceColumns))
	for _, c := range modelPriceColumns {
		if c == ColPriceModel {
			continue // 主键冲突列不赋值
		}
		set = append(set, c+" = EXCLUDED."+c)
	}
	sql := "INSERT INTO model_prices " + modelPriceColumnList() +
		" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, now(), now())" +
		" ON CONFLICT (" + ColPriceModel + ") DO UPDATE SET " +
		strings.Join(set, ", ") + ", updated_at = now()"
	return db.Exec(sql, modelPriceArgs(p)...).Error
}

// InsertModelPriceIfAbsent 只在模型还没有价时插入，返回是否真的插入了。
//
// 用于「补缺」语义的路径（启动迁移、老备份导入）：库里已有的价一律不动，
// 否则用旧备份或迁移结果覆盖掉用户后来改的值。
func InsertModelPriceIfAbsent(db *gorm.DB, p ModelPrice) (bool, error) {
	sql := "INSERT INTO model_prices " + modelPriceColumnList() +
		" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, now(), now())" +
		" ON CONFLICT (" + ColPriceModel + ") DO NOTHING"
	res := db.Exec(sql, modelPriceArgs(p)...)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// NegativePriceField 返回这条价格里第一个为负的字段名，没有则返回空串。
//
// 负单价进统计比没有统计更糟：看板上会出现「省了钱」的假象，而账面
// 完全看不出异常。
//
// 备份导入路径用这个（JSON 直接给出 decimal，没有字符串解析这一步）；
// 管理接口保存走 buildModelPrice → parseDecimal，那条路径在解析时
// 就拒绝负数并带上字段名。两处的判断条件必须一致，改动时一起看。
func (m ModelPrice) NegativePriceField() string {
	for _, f := range []struct {
		label string
		v     interface{ IsNegative() bool }
	}{
		{"美元输入单价", m.USDInputPer1M}, {"美元输出单价", m.USDOutputPer1M},
		{"美元缓存读单价", m.USDCacheReadPer1M}, {"美元缓存写单价", m.USDCacheWritePer1M},
		{"人民币输入单价", m.RMBInputPer1M}, {"人民币输出单价", m.RMBOutputPer1M},
		{"人民币缓存读单价", m.RMBCacheReadPer1M}, {"人民币缓存写单价", m.RMBCacheWritePer1M},
	} {
		if f.v.IsNegative() {
			return f.label
		}
	}
	return ""
}
