package model

import "testing"

// 币种代码的归一化：大小写与空白都该容忍，认不出来时必须返回 false。
//
// 为什么专门测「认不出来」：这是唯一一个「静默错」不可接受的输入 ——
// 退回默认值会让金额挂到另一个币种上，而界面上一切都正常。
func TestNormalizeCurrency(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"CNY", CurrencyCNY, true},
		{"cny", CurrencyCNY, true},
		{"  Usd  ", CurrencyUSD, true},
		{"USD", CurrencyUSD, true},
		{"", "", false},
		{"  ", "", false},
		{"RMB", "", false}, // 常见误写：人民币的另一种叫法，但不是 ISO 代码
		{"$", "", false},
		{"CNYX", "", false},
	}
	for _, c := range cases {
		got, ok := NormalizeCurrency(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("NormalizeCurrency(%q) = (%q, %v)，期望 (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
