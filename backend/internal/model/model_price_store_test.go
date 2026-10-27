package model

import (
	"slices"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

// 共享 SQL 里的列名必须与实体标签逐字一致。
//
// 这条测试针对的是本次改动中真实踩过的坑：把 5 处手写 upsert 收敛成
// 一个函数时，我在新函数里又手抄了一遍列名，抄成了 usd_input_per_1_m
// （人更容易想到的写法），而实际列名是 usd_input_per1_m。
// 结果是保存接口 500、备份导入失败 —— 而这在单测里完全看不出来，
// 因为 SQL 只有打到真库才会被解析。
//
// 现在函数引用 columns.go 的常量，这条测试验证常量确实与实体解析出的
// 列名一致，且列清单里没有多余/重复项。
func TestModelPriceUpsertColumnsMatchEntity(t *testing.T) {
	s, err := schema.Parse(&ModelPrice{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("解析 ModelPrice 失败: %v", err)
	}

	// 1) SQL 用到的每个列名都必须是实体上真实存在的列
	actual := map[string]bool{}
	for _, f := range s.Fields {
		actual[f.DBName] = true
	}
	for _, col := range modelPriceColumns {
		if !actual[col] {
			t.Errorf("SQL 里的列 %q 在 ModelPrice 上不存在（实际列：%v）", col, keysOf(actual))
		}
	}

	// 2) 八个价格列一个都不能少：漏一列的表现是「保存成功但那一档永远是 0」
	for field, col := range map[string]string{
		"USDInputPer1M": ColPriceUSDInputPer1M, "USDOutputPer1M": ColPriceUSDOutputPer1M,
		"USDCacheReadPer1M": ColPriceUSDCacheReadPer1M, "USDCacheWritePer1M": ColPriceUSDWritePer1M,
		"RMBInputPer1M": ColPriceRMBInputPer1M, "RMBOutputPer1M": ColPriceRMBOutputPer1M,
		"RMBCacheReadPer1M": ColPriceRMBCacheReadPer1M, "RMBCacheWritePer1M": ColPriceRMBWritePer1M,
	} {
		f := s.LookUpField(field)
		if f == nil {
			t.Fatalf("找不到字段 %s", field)
		}
		if f.DBName != col {
			t.Errorf("%s 实际列名 %q，常量却是 %q", field, f.DBName, col)
		}
		found := false
		for _, c := range modelPriceColumns {
			if c == col {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("列 %q 没出现在 SQL 列清单里", col)
		}
	}

	// 3) 列清单不许重复（重复会让占位符数量与参数错位）
	seen := map[string]bool{}
	for _, c := range modelPriceColumns {
		if seen[c] {
			t.Errorf("列 %q 在清单里出现了不止一次", c)
		}
		seen[c] = true
	}

	// 4) 列清单长度必须等于参数数量：SQL 里每列一个 ?，
	// 少给一个参数会静默错位（写进相邻列），比缺列更难查。
	if len(modelPriceColumns) != len(modelPriceArgs(ModelPrice{})) {
		t.Errorf("列 %d 个，参数 %d 个 —— 占位符与参数必须一一对应",
			len(modelPriceColumns), len(modelPriceArgs(ModelPrice{})))
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// 排序：错误信息里列出实际列名时，map 迭代顺序每次都不同 ——
	// 同一处失败两次跑出两种报错，比对 CI 日志和本地结果时会以为是新问题。
	slices.Sort(out)
	return out
}
