package model

import (
	"testing"

	"gorm.io/gorm/schema"
)

// 定价列名必须与 GORM 由字段名推导的结果一致。
//
// 这组断言防的是一个真实踩过的坑：InputPer1M 推出的是 input_per1_m，
// 而代码里照 json 名写成了 input_per_1m，更新语句于是报「列不存在」。
// 它只在更新已有行时才暴露，因此定价同步只写首次插入、官方调价再也进不来，
// 手工改价也一直静默失败——都不会报错给用户看。
func TestPricingColumnNamesMatchGORM(t *testing.T) {
	ns := schema.NamingStrategy{}
	cases := []struct {
		field string
		want  string
	}{
		{"InputPer1M", ColPricingInputPer1M},
		{"OutputPer1M", ColPricingOutputPer1M},
		{"CacheReadPer1M", ColPricingCacheReadPer1M},
		{"CacheWritePer1M", ColPricingCacheWritePer1M},
	}
	for _, c := range cases {
		got := ns.ColumnName("", c.field)
		if got != c.want {
			t.Errorf("%s 的 GORM 列名是 %q，常量却写成 %q —— 原生列映射会报「列不存在」",
				c.field, got, c.want)
		}
	}
}

// 把结论直接固化下来：json 名与列名确实不同，两者不可互相替代。
// 若哪天 GORM 改了命名策略让二者相同，这里会跳过并提示更新注释。
func TestPricingColumnDiffersFromJSONName(t *testing.T) {
	ns := schema.NamingStrategy{}
	if ns.ColumnName("", "InputPer1M") == "input_per_1m" {
		t.Skip("GORM 命名策略已变化，columns.go 的说明需要同步更新")
	}
	if ColPricingInputPer1M == "input_per_1m" {
		t.Error("常量不应等于 json 名，否则说明写错了")
	}
}
