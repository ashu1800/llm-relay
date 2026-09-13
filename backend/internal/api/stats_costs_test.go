package api

import (
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"

	"llm-relay/internal/model"
)

// 金额折叠必须按币种分开：混币相加会得到一个既不是人民币也不是美元的数。
func TestCostsByCurrencyKeepsCurrenciesApart(t *testing.T) {
	c := costsByCurrency{}
	c.add(model.CurrencyCNY, decimal.RequireFromString("12.5"))
	c.add(model.CurrencyCNY, decimal.RequireFromString("0.5"))
	c.add(model.CurrencyUSD, decimal.RequireFromString("3"))

	out := c.json()
	if got := out[model.CurrencyCNY]; got != "13.00000000" {
		t.Errorf("人民币合计 = %s，期望 13.00000000", got)
	}
	if got := out[model.CurrencyUSD]; got != "3.00000000" {
		t.Errorf("美元合计 = %s，期望 3.00000000", got)
	}
	// 金额用字符串传：JSON 浮点会把 0.15 变成 0.14999999999999999
	buf, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if string(buf) != `{"CNY":"13.00000000","USD":"3.00000000"}` {
		t.Errorf("序列化结果 = %s", buf)
	}
}

// 空币种不能随手并进某个币种里 —— 那正是「把钱挂到错误的币种上」。
// 历史上真出现过空值（列默认值之前的老库），所以这里固化成美元并如实说明。
func TestCostsByCurrencyFallsBackOnEmpty(t *testing.T) {
	c := costsByCurrency{}
	c.add("", decimal.RequireFromString("1"))
	c.add("  ", decimal.RequireFromString("2"))
	if len(c) != 1 {
		t.Fatalf("期望只归到一个币种，实际 %d 个: %v", len(c), c)
	}
	if got := c.json()[model.CurrencyUSD]; got != "3.00000000" {
		t.Errorf("空币种应归一到美元，实际 %s", got)
	}
}
