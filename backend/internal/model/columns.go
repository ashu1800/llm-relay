package model

// 定价表的列名常量。
//
// 为什么需要这个文件：GORM 由 Go 字段名推导列名，
// InputPer1M 推出的是 input_per1_m，不是 input_per_1m ——
// 数字 1 跟着 per，下划线加在末尾那个 M 之前。
// 这跟 json 名（input_per_1m）不一样，而写原生列映射时人自然会照 json 名写，
// 结果是「列不存在」的运行期错误，且只在真的发生更新时才暴露。
//
// 实际后果：定价同步过去只写首次插入，官方调价再也同步不进来；
// 定价页的手工改价也一直静默失败。
//
// 集中声明在这里，实体标签与原生映射共用一份，并由 columns_test.go
// 直接拿 GORM 的命名策略做校验，改错会立刻测出来。
const (
	ColPricingInputPer1M      = "input_per1_m"
	ColPricingOutputPer1M     = "output_per1_m"
	ColPricingCacheReadPer1M  = "cache_read_per1_m"
	ColPricingCacheWritePer1M = "cache_write_per1_m"
)
