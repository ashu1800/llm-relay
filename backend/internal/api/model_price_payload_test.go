package api

import (
	"slices"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"llm-relay/internal/model"
)

// mustDec 是测试里的 decimal 速写。
func mustDec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return d
}

// buildModelPrice 的解析规则：八个单价（美元四档 + 人民币四档）各自独立。
//
// 这一层挡的是「填了但没生效」：金额走字符串而不是 JSON 浮点（0.15 会变成
// 0.14999999999999999），空串必须是 0 而不是解析失败 —— 一个模型只填输入价
// 是完全合法的（很多中转站只按输入计价）。
func TestBuildModelPrice(t *testing.T) {
	t.Run("两币种八档各自独立落值", func(t *testing.T) {
		row, errMsg := buildModelPrice(modelPricePayload{
			ModelName:          "gpt-5",
			USDInputPer1M:      "1.25",
			USDOutputPer1M:     "10",
			USDCacheReadPer1M:  "0.125",
			USDCacheWritePer1M: "1.5",
			RMBInputPer1M:      "8",
			RMBOutputPer1M:     "64",
			RMBCacheReadPer1M:  "0.8",
			RMBCacheWritePer1M: "9.6",
		})
		if errMsg != "" {
			t.Fatalf("不该报错: %s", errMsg)
		}
		// 按字符串比对是为了盯住精度：单价要落进 numeric(18,8)，
		// 走 float 中转的话 0.125 这类值会漂
		if got := row.USDInputPer1M.String(); got != "1.25" {
			t.Errorf("美元输入价应为 1.25，实际 %s", got)
		}
		if got := row.RMBOutputPer1M.String(); got != "64" {
			t.Errorf("人民币输出价应为 64，实际 %s", got)
		}
		if got := row.RMBCacheWritePer1M.String(); got != "9.6" {
			t.Errorf("人民币缓存写价应为 9.6，实际 %s", got)
		}
	})

	t.Run("空串落成 0 而不是解析失败", func(t *testing.T) {
		row, errMsg := buildModelPrice(modelPricePayload{
			ModelName:     "only-input",
			USDInputPer1M: "0.5",
			// 其余七档都不填
		})
		if errMsg != "" {
			t.Fatalf("只填输入价是合法状态，不该报错: %s", errMsg)
		}
		if row.USDOutputPer1M.String() != "0" {
			t.Errorf("未填的输出价应为 0，实际 %s", row.USDOutputPer1M)
		}
		if !row.RMBInputPer1M.IsZero() {
			t.Errorf("未填的人民币档应为 0，实际 %s", row.RMBInputPer1M)
		}
	})

	t.Run("非法数字与负数都要报错并指明是哪一档", func(t *testing.T) {
		cases := []struct {
			name string
			in   modelPricePayload
			want string
		}{
			{"非法数字", modelPricePayload{ModelName: "m", USDInputPer1M: "abc"}, "美元输入单价"},
			{"负数", modelPricePayload{ModelName: "m", RMBOutputPer1M: "-1"}, "人民币输出单价"},
			{"人民币档非法", modelPricePayload{ModelName: "m", RMBCacheReadPer1M: "1e"}, "人民币缓存读单价"},
		}
		for _, c := range cases {
			_, errMsg := buildModelPrice(c.in)
			if errMsg == "" {
				t.Errorf("%s：应当报错", c.name)
				continue
			}
			if !strings.Contains(errMsg, c.want) {
				t.Errorf("%s：错误信息应指明「%s」，实际 %s", c.name, c.want, errMsg)
			}
		}
	})

	t.Run("空白字符串按未填处理", func(t *testing.T) {
		row, errMsg := buildModelPrice(modelPricePayload{
			ModelName:     "m",
			USDInputPer1M: "   ",
		})
		if errMsg != "" {
			t.Fatalf("空白应按未填处理: %s", errMsg)
		}
		if !row.USDInputPer1M.IsZero() {
			t.Errorf("空白应落成 0，实际 %s", row.USDInputPer1M)
		}
	})
}

// PriceFor 按币种选档：这是「同一模型同时有美元价和人民币价」的落点。
// 选错档的表现是「人民币渠道按美元数字记账」，账面上完全看不出异常。
func TestModelPriceForCurrency(t *testing.T) {
	mp := model.ModelPrice{
		ModelName:          "gpt-5",
		USDInputPer1M:      mustDec("1"),
		USDOutputPer1M:     mustDec("10"),
		RMBInputPer1M:      mustDec("7"),
		RMBOutputPer1M:     mustDec("70"),
		USDCacheReadPer1M:  mustDec("0.1"),
		RMBCacheReadPer1M:  mustDec("0.7"),
		USDCacheWritePer1M: mustDec("1.1"),
		RMBCacheWritePer1M: mustDec("7.7"),
	}

	usd := mp.PriceFor(model.CurrencyUSD)
	if usd.InputPer1M.String() != "1" || usd.OutputPer1M.String() != "10" ||
		usd.CacheReadPer1M.String() != "0.1" || usd.CacheWritePer1M.String() != "1.1" {
		t.Errorf("美元档取错: %+v", usd)
	}

	cny := mp.PriceFor(model.CurrencyCNY)
	if cny.InputPer1M.String() != "7" || cny.OutputPer1M.String() != "70" ||
		cny.CacheReadPer1M.String() != "0.7" || cny.CacheWritePer1M.String() != "7.7" {
		t.Errorf("人民币档取错: %+v", cny)
	}

	// 空币种 / 认不出的币种回落到美元：与计价引擎的兜底口径一致
	if got := mp.PriceFor("").InputPer1M.String(); got != "1" {
		t.Errorf("空币种应回落美元，实际 %s", got)
	}
	if got := mp.PriceFor("JPY").InputPer1M.String(); got != "1" {
		t.Errorf("未知币种应回落美元，实际 %s", got)
	}
	// 币种大小写要容忍（用户在渠道表单里可能填小写）
	if got := mp.PriceFor("cny").InputPer1M.String(); got != "7" {
		t.Errorf("小写 cny 应取人民币档，实际 %s", got)
	}
}

// 只有一档配价时，另一档必须是「全 0」——引擎据此提示「该币种没配价」。
func TestModelPriceForCurrencyPartial(t *testing.T) {
	mp := model.ModelPrice{ModelName: "only-usd", USDInputPer1M: mustDec("2")}
	if mp.PriceFor(model.CurrencyCNY).IsZero() != true {
		t.Error("只配了美元价时，人民币档应判定为未定价")
	}
	if mp.PriceFor(model.CurrencyUSD).IsZero() {
		t.Error("美元档已配价，不应判定为未定价")
	}
}

// used_currencies 的输出形状是跨语言契约。
//
// 曾经后端返回 map[string]map[string]bool，序列化成 {"CNY":true,"USD":true}，
// 而前端 modelPricing.ts 按 string[] 调 .includes() —— 打开「编辑定价」
// 弹窗时抛 TypeError: usedCurrencies.includes is not a function，页面白屏。
// 两侧代码各自都合法、Go 与 TS 的类型检查都看不出来，所以钉一条形状断言。
func TestSortedCurrencyLists(t *testing.T) {
	got := sortedCurrencyLists(map[string]map[string]bool{
		"both":   {"USD": true, "CNY": true},
		"usd":    {"USD": true},
		"cny":    {"CNY": true},
		"unused": {},
	})

	// 1) 两个币种齐全，且顺序按 SupportedCurrencies（CNY 在前）——
	//    顺序不稳定会让界面的「美元 / 人民币」在两次刷新之间自己换位置
	if want := []string{model.CurrencyCNY, model.CurrencyUSD}; !slices.Equal(got["both"], want) {
		t.Errorf("both 应为 %v（按 SupportedCurrencies 排序），实际 %v", want, got["both"])
	}
	if want := []string{model.CurrencyUSD}; !slices.Equal(got["usd"], want) {
		t.Errorf("usd 应为 %v，实际 %v", want, got["usd"])
	}
	if want := []string{model.CurrencyCNY}; !slices.Equal(got["cny"], want) {
		t.Errorf("cny 应为 %v，实际 %v", want, got["cny"])
	}

	// 2) 空集合要产出空数组而不是 nil：
	//    JSON 里 nil 是 null，前端 `res.used_currencies[name] || []` 之后
	//    仍然要对它调 .includes()，而 null 会让那一步抛错。
	if got["unused"] == nil {
		t.Error("空币种集合应产出空数组，不能是 nil")
	} else if len(got["unused"]) != 0 {
		t.Errorf("空币种集合应为空，实际 %v", got["unused"])
	}

	// 3) 输出形状必须是「模型 -> 数组」，不是「模型 -> 对象」。
	//    编译期它是 map[string][]string，这条断言防的是有人把它改回 map 值类型。
	if _, isSlice := any(got["both"]).([]string); !isSlice {
		t.Error("值必须是 []string（前端按数组用）")
	}
}
