package model

import (
	"sync"
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

// 价格列的**类型**也必须被 GORM 正确解析出来。
//
// 防的是另一个真实踩过的坑：CacheWritePer1M 的 gorm tag 把 `type:` 写成了 `type=`，
// 而 GORM 的 tag 解析只认冒号 —— 那个字段于是没有类型，退回按 Go 类型推断：
// decimal.Decimal 的 Value() 返回 string，列就被建成 text。
//
// 这类错误不会立刻炸：GORM 把 text 读进 decimal 照样能读，计费看起来一切正常。
// 但任何拿这一列和 0 比较的原生 SQL 都会报「operator does not exist: text <> integer」——
// 也就是 GET /api/admin/settings 直接 500、旧备份导入时按该列过滤的 WHERE 整句失败，
// 备份里的价格一条都恢复不了。
func TestPricingColumnTypesAreParsed(t *testing.T) {
	s, err := schema.Parse(&ChannelModel{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("解析 ChannelModel 失败: %v", err)
	}
	for _, name := range []string{"InputPer1M", "OutputPer1M", "CacheReadPer1M", "CacheWritePer1M"} {
		field := s.LookUpField(name)
		if field == nil {
			t.Fatalf("找不到字段 %s", name)
		}
		if field.DataType != "numeric(18,8)" {
			t.Errorf("%s 的类型被解析成 %q，期望 numeric(18,8)：tag 里要写 type:（冒号），"+
				"写成 type= 时 GORM 读不到，会按 Go 类型推断成 text", name, field.DataType)
		}
	}
}
