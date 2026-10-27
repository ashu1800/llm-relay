package api

import (
	"encoding/json"
	"strings"
	"testing"

	"llm-relay/internal/model"
)

// applyPriceFields 的倍率与时段规则处理。
//
// 这里挡的是两类「静默失效」：
//   - 固定倍率「没传」与「显式传 1」要能区分（指针语义），且 0 不能落成「免费」；
//   - 写错的时段窗口必须在保存时报错，否则它只会永不命中。
//
// 单价已于 2026-10-27 迁到模型级，它的解析与校验测试在
// model_price_payload_test.go（buildModelPrice）。
func TestApplyPriceFieldsMultiplier(t *testing.T) {
	in := whitelistItem{PublicName: "m"}

	// 没传倍率 -> 1（按原价），不能落成 0（0 看起来像「免费」）
	row := model.ChannelModel{}
	if err := applyPriceFields(&row, in); err != nil {
		t.Fatal(err)
	}
	if row.Multiplier != 1 {
		t.Errorf("没传倍率时应为 1，实际 %v", row.Multiplier)
	}

	// 显式传 1
	one := 1.0
	in.Multiplier = &one
	row = model.ChannelModel{}
	if err := applyPriceFields(&row, in); err != nil {
		t.Fatal(err)
	}
	if row.Multiplier != 1 {
		t.Errorf("显式传 1 时应为 1，实际 %v", row.Multiplier)
	}

	// 传 0 视同没填，归一到 1
	zero := 0.0
	in.Multiplier = &zero
	row = model.ChannelModel{}
	if err := applyPriceFields(&row, in); err != nil {
		t.Fatal(err)
	}
	if row.Multiplier != 1 {
		t.Errorf("传 0 应归一到 1，实际 %v", row.Multiplier)
	}

	// 折扣倍率必须原样保留（曾经被夹到 >= 1，打折规则静默失效）
	half := 0.5
	in.Multiplier = &half
	row = model.ChannelModel{}
	if err := applyPriceFields(&row, in); err != nil {
		t.Fatal(err)
	}
	if row.Multiplier != 0.5 {
		t.Errorf("折扣倍率应保留 0.5，实际 %v", row.Multiplier)
	}

	// 负值与超上限要报错
	for _, bad := range []float64{-1, 1000} {
		v := bad
		in.Multiplier = &v
		if err := applyPriceFields(&model.ChannelModel{}, in); err == nil {
			t.Errorf("倍率 %v 应当报错", bad)
		}
	}
}

// 四位小数倍率（2026-09-24）：channel_models.multiplier 是 numeric(10,4)，
// 上游按折扣率报价时常见 0.1875 / 0.1234 这种四位值。
//
// 这条测试盯的是「API 层别把它舍掉」：曾经前端输入框的 precision=2 会在
// 失焦时把 0.1875 四舍五入成 0.19（那是前端问题，已修），但**后端这条路径
// 同样不能有隐式收窄** —— 一旦有人在这里加了 round/toFixed 之类的
// 「顺手归一」，四位倍率就会再次静默变成两位，而账单上完全看不出。
func TestApplyPriceFieldsMultiplierFourDecimals(t *testing.T) {
	for _, v := range []float64{0.1875, 0.1234, 0.0001, 1.2345, 0.0625} {
		want := v
		in := whitelistItem{PublicName: "m", Multiplier: &want}
		var row model.ChannelModel
		if err := applyPriceFields(&row, in); err != nil {
			t.Fatalf("倍率 %v 不应报错: %v", v, err)
		}
		if row.Multiplier != v {
			t.Errorf("四位倍率 %v 应原样保留，实际 %v", v, row.Multiplier)
		}
	}
}

// 计价引擎侧的四位倍率精度在 internal/pricing 包内测
//（TestFourDecimalMultiplierExact，见 pricing/engine_multiplier_test.go）——
// 那条链路要直接填 Engine 的缓存才能不依赖数据库，而 cache 是包内字段。
// 这里只守 API 层：别在 applyPriceFields 这条路上把四位值收窄。

func TestApplyPriceFieldsPeakRules(t *testing.T) {
	in := whitelistItem{PublicName: "m"}
	rules := `[{"days":[1,2,3,4,5],"start":"09:00","end":"12:00","multiplier":2}]`
	if err := json.Unmarshal([]byte(rules), &in.PeakRules); err != nil {
		t.Fatal(err)
	}
	row := model.ChannelModel{}
	if err := applyPriceFields(&row, in); err != nil {
		t.Fatalf("合法规则不该报错: %v", err)
	}
	if len(row.PeakRules) != 1 {
		t.Fatalf("规则应被保留，实际 %v", row.PeakRules)
	}

	// 时间格式错的规则必须被拦下，并指明第几条
	bad := whitelistItem{PublicName: "m"}
	if err := json.Unmarshal([]byte(`[{"start":"9点","end":"12:00","multiplier":2}]`), &bad.PeakRules); err != nil {
		t.Fatal(err)
	}
	err := applyPriceFields(&model.ChannelModel{}, bad)
	if err == nil || !strings.Contains(err.Error(), "第 1 条") {
		t.Errorf("非法规则应报错并指明第几条，实际 %v", err)
	}
}

// 渠道白名单载荷必须能带上倍率与时段规则：漏掉任何一项都表现为
// 「界面填了、保存也成功，但库里是 0」——账面上完全看不出异常。
//
// 2026-10-27：单价已不在这个载荷里（见 model_price_payload_test.go）。
func TestWhitelistItemCarriesChannelPricing(t *testing.T) {
	body := `{"public_name":"m","upstream_name":"u","enabled":true,
		"multiplier":1.5,
		"peak_rules":[{"days":[1],"start":"09:00","end":"12:00","multiplier":2}]}`
	var item whitelistItem
	if err := json.Unmarshal([]byte(body), &item); err != nil {
		t.Fatal(err)
	}
	row := model.ChannelModel{PublicName: item.PublicName}
	if err := applyPriceFields(&row, item); err != nil {
		t.Fatal(err)
	}
	if row.Multiplier != 1.5 || len(row.PeakRules) != 1 {
		t.Errorf("载荷里的渠道定价属性没有完整落到实体上: %+v", row)
	}
}
