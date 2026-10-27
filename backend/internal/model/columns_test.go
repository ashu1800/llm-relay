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
//
// 2026-10-27：定价搬到 model.ModelPrice，且它的列名**全部显式声明**了
// gorm:"column:..."（因为字段名带 USD/RMB 前缀，推导出来的名字不可读）。
// 显式声明的风险更高：少写一个 column: 就退回推导名，而原生 SQL
// （upsert、迁移、备份导入都手写 SQL）会立刻「列不存在」。
func TestModelPriceColumnNames(t *testing.T) {
	ns := schema.NamingStrategy{}
	cases := []struct {
		field string
		want  string
	}{
		{"USDInputPer1M", "usd_input_per1_m"},
		{"USDOutputPer1M", "usd_output_per1_m"},
		{"USDCacheReadPer1M", "usd_cache_read_per1_m"},
		{"USDCacheWritePer1M", "usd_cache_write_per1_m"},
		{"RMBInputPer1M", "rmb_input_per1_m"},
		{"RMBOutputPer1M", "rmb_output_per1_m"},
		{"RMBCacheReadPer1M", "rmb_cache_read_per1_m"},
		{"RMBCacheWritePer1M", "rmb_cache_write_per1_m"},
	}
	for _, c := range cases {
		got := ns.ColumnName("", c.field)
		if got != c.want {
			t.Errorf("%s 的 GORM 推导列名是 %q，与手写 SQL 用的 %q 不一致 —— "+
				"原生 upsert / 迁移会报「列不存在」", c.field, got, c.want)
		}
	}
}

// 显式 column: 标签必须真的生效：GORM 解析出的列名要等于常量，
// 而不是仅仅「推导名与常量相同」（这层防护针对标签写错的情形）。
func TestModelPriceExplicitColumnTags(t *testing.T) {
	s, err := schema.Parse(&ModelPrice{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("解析 ModelPrice 失败: %v", err)
	}
	want := map[string]string{
		"USDInputPer1M":      "usd_input_per1_m",
		"USDOutputPer1M":     "usd_output_per1_m",
		"USDCacheReadPer1M":  "usd_cache_read_per1_m",
		"USDCacheWritePer1M": "usd_cache_write_per1_m",
		"RMBInputPer1M":      "rmb_input_per1_m",
		"RMBOutputPer1M":     "rmb_output_per1_m",
		"RMBCacheReadPer1M":  "rmb_cache_read_per1_m",
		"RMBCacheWritePer1M": "rmb_cache_write_per1_m",
	}
	for field, col := range want {
		f := s.LookUpField(field)
		if f == nil {
			t.Fatalf("找不到字段 %s", field)
		}
		if f.DBName != col {
			t.Errorf("%s 实际列名是 %q，期望 %q —— 检查 gorm tag 里的 column:", field, f.DBName, col)
		}
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
// 而本次改动恰恰在迁移与备份导入里都手写了这样的原生 SQL。
func TestModelPriceColumnTypesAreParsed(t *testing.T) {
	s, err := schema.Parse(&ModelPrice{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("解析 ModelPrice 失败: %v", err)
	}
	for _, name := range []string{
		"USDInputPer1M", "USDOutputPer1M", "USDCacheReadPer1M", "USDCacheWritePer1M",
		"RMBInputPer1M", "RMBOutputPer1M", "RMBCacheReadPer1M", "RMBCacheWritePer1M",
	} {
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

// ModelName 是这张表的身份：必须有唯一索引，否则 upsert 的
// ON CONFLICT (model_name) 会直接报「没有匹配的唯一约束」。
func TestModelPriceNameIsUnique(t *testing.T) {
	s, err := schema.Parse(&ModelPrice{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("解析 ModelPrice 失败: %v", err)
	}
	f := s.LookUpField("ModelName")
	if f == nil {
		t.Fatal("找不到 ModelName 字段")
	}
	// GORM 的两个字段语义不同：
	//   Unique      —— 列级 UNIQUE 约束（gorm:"unique"）
	//   UniqueIndex —— 命名唯一索引（gorm:"index:xxx,unique"）
	// 两者都满足 ON CONFLICT (model_name) 的前提，任一非空即可。
	// 只判 UniqueIndex 会把 gorm:"unique" 误判成「没有唯一约束」。
	if f.UniqueIndex == "" && !f.Unique {
		t.Error("ModelName 需要唯一约束（unique 或 index:...,unique）：upsert 依赖 ON CONFLICT (model_name)")
	}
}
