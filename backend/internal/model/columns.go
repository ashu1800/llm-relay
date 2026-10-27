package model

// 定价表的列名常量。
//
// GORM 由 Go 字段名推导列名：InputPer1M → input_per1_m（不是 input_per_1m），
// 写原生 SQL 时人容易照 json 名写，结果只在更新时才报「列不存在」。
// 集中在这里，由 columns_test.go 直接拿 GORM 命名策略校验。
//
// 本文件的包级注释会被 tygo 复制到生成产物里（每个 const 一遍），
// 所以正文只写这一段，细节在 columns_test.go。
const (
	// 老 channel_models 上的单价列，只被一次性迁移使用
	// （store.migratePricesToModelLevel）：读这些列、搬进 model_prices、再删掉。
	// 迁移后代码不再引用，但常量保留 —— 那是「老库升级」路径唯一的列名来源。
	ColPricingInputPer1M      = "input_per1_m"
	ColPricingOutputPer1M     = "output_per1_m"
	ColPricingCacheReadPer1M  = "cache_read_per1_m"
	ColPricingCacheWritePer1M = "cache_write_per1_m"
)

// model.ModelPrice 的列名。实体上用 column: 显式声明，这里供原生 SQL
// （model.UpsertModelPrice / InsertModelPriceIfAbsent）引用；两处必须逐字一致。
const (
	ColPriceModel             = "model_name"
	ColPriceUSDInputPer1M     = "usd_input_per1_m"
	ColPriceUSDOutputPer1M    = "usd_output_per1_m"
	ColPriceUSDCacheReadPer1M = "usd_cache_read_per1_m"
	ColPriceUSDWritePer1M     = "usd_cache_write_per1_m"
	ColPriceRMBInputPer1M     = "rmb_input_per1_m"
	ColPriceRMBOutputPer1M    = "rmb_output_per1_m"
	ColPriceRMBCacheReadPer1M = "rmb_cache_read_per1_m"
	ColPriceRMBWritePer1M     = "rmb_cache_write_per1_m"
)
