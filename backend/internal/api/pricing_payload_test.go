package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// buildPricingRow 的倍率与时段规则处理。
//
// 这里挡的是两类「静默失效」：
//   - 固定倍率「没传」与「显式传 1」要能区分（指针语义）；
//   - 写错的时段窗口必须在保存时报错，否则它只会永不命中。
func TestBuildPricingRowMultiplier(t *testing.T) {
	payload := pricingPayload{ModelKey: "m", InputPer1M: "1", OutputPer1M: "2"}

	// 没传倍率 -> 1（按原价），不能落成 0（0 看起来像「免费」）
	row, err := buildPricingRow(payload)
	if err != nil {
		t.Fatal(err)
	}
	if row.Multiplier != 1 {
		t.Errorf("没传倍率时应为 1，实际 %v", row.Multiplier)
	}

	// 显式传 1
	one := 1.0
	payload.Multiplier = &one
	if row, err = buildPricingRow(payload); err != nil {
		t.Fatal(err)
	}
	if row.Multiplier != 1 {
		t.Errorf("显式传 1 时应为 1，实际 %v", row.Multiplier)
	}

	// 传 0 视同没填，归一到 1
	zero := 0.0
	payload.Multiplier = &zero
	if row, err = buildPricingRow(payload); err != nil {
		t.Fatal(err)
	}
	if row.Multiplier != 1 {
		t.Errorf("传 0 应归一到 1，实际 %v", row.Multiplier)
	}

	// 负值与超上限要报错
	for _, bad := range []float64{-1, 1000} {
		v := bad
		payload.Multiplier = &v
		if _, err := buildPricingRow(payload); err == nil {
			t.Errorf("倍率 %v 应当报错", bad)
		}
	}
}

func TestBuildPricingRowPeakRules(t *testing.T) {
	payload := pricingPayload{ModelKey: "m"}
	rules := `[{"days":[1,2,3,4,5],"start":"09:00","end":"12:00","multiplier":2}]`
	if err := json.Unmarshal([]byte(rules), &payload.PeakRules); err != nil {
		t.Fatal(err)
	}
	row, err := buildPricingRow(payload)
	if err != nil {
		t.Fatalf("合法规则不该报错: %v", err)
	}
	if len(row.PeakRules) != 1 {
		t.Fatalf("规则应被保留，实际 %v", row.PeakRules)
	}

	// 时间格式错的规则必须被拦下，并指明第几条
	bad := pricingPayload{ModelKey: "m"}
	if err := json.Unmarshal([]byte(`[{"start":"9点","end":"12:00","multiplier":2}]`), &bad.PeakRules); err != nil {
		t.Fatal(err)
	}
	_, err = buildPricingRow(bad)
	if err == nil || !strings.Contains(err.Error(), "第 1 条") {
		t.Errorf("非法规则应报错并指明第几条，实际 %v", err)
	}
}

// match_type 只认 exact / prefix：写错会让定价永远匹配不上。
func TestBuildPricingRowMatchType(t *testing.T) {
	row, err := buildPricingRow(pricingPayload{ModelKey: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if row.MatchType != "exact" {
		t.Errorf("默认应为 exact，实际 %s", row.MatchType)
	}
	row, err = buildPricingRow(pricingPayload{ModelKey: "m", MatchType: "prefix"})
	if err != nil {
		t.Fatal(err)
	}
	if row.MatchType != "prefix" {
		t.Errorf("应保留 prefix，实际 %s", row.MatchType)
	}
}
