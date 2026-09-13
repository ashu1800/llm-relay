package api

import (
	"encoding/json"
	"strings"
	"testing"

	"llm-relay/internal/model"
)

// applyPriceFields 的单价、倍率与时段规则处理。
//
// 这里挡的是三类「静默失效」：
//   - 固定倍率「没传」与「显式传 1」要能区分（指针语义），且 0 不能落成「免费」；
//   - 写错的时段窗口必须在保存时报错，否则它只会永不命中；
//   - 空字符串必须落成 0 而不是解析失败 —— 白名单是整表提交的，不填就是不算钱。
func TestApplyPriceFieldsMultiplier(t *testing.T) {
	in := whitelistItem{PublicName: "m", InputPer1M: "1", OutputPer1M: "2"}

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

func TestApplyPriceFieldsDecimals(t *testing.T) {
	in := whitelistItem{
		PublicName: "m",
		InputPer1M: "0.15", OutputPer1M: "0.6",
		CacheReadPer1M: "0.003", CacheWritePer1M: "0.3",
	}
	row := model.ChannelModel{}
	if err := applyPriceFields(&row, in); err != nil {
		t.Fatal(err)
	}
	got := []string{
		row.InputPer1M.String(), row.OutputPer1M.String(),
		row.CacheReadPer1M.String(), row.CacheWritePer1M.String(),
	}
	want := []string{"0.15", "0.6", "0.003", "0.3"}
	for i := range want {
		if got[i] != want[i] {
			// 按字符串比对是为了盯住精度：单价要落进 numeric(18,8)，
			// 走 float 中转的话 0.15 会变成 0.14999999999999999
			t.Errorf("第 %d 个单价应为 %s，实际 %s", i+1, want[i], got[i])
		}
	}

	// 空字符串 = 0（不填就是不算钱），不是解析失败
	row = model.ChannelModel{}
	if err := applyPriceFields(&row, whitelistItem{PublicName: "m"}); err != nil {
		t.Fatalf("全空不该报错: %v", err)
	}
	if !row.InputPer1M.IsZero() || !row.OutputPer1M.IsZero() {
		t.Errorf("空值应落成 0，实际 %s / %s", row.InputPer1M, row.OutputPer1M)
	}

	// 非法数字与负数要报错，并带上字段名（界面上要能对上哪一格填错了）
	for _, bad := range []whitelistItem{
		{PublicName: "m", InputPer1M: "abc"},
		{PublicName: "m", OutputPer1M: "-1"},
	} {
		err := applyPriceFields(&model.ChannelModel{}, bad)
		if err == nil {
			t.Errorf("非法单价 %+v 应当报错", bad)
			continue
		}
		if !strings.Contains(err.Error(), "单价") {
			t.Errorf("错误信息应指明是哪个单价，实际 %v", err)
		}
	}
}

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

// 渠道白名单载荷必须能带上价格：漏掉任何一个字段都表现为
// 「界面填了、保存也成功，但库里是 0」——账面上完全看不出异常。
func TestWhitelistItemCarriesPrice(t *testing.T) {
	body := `{"public_name":"m","upstream_name":"u","enabled":true,
		"input_per_1m":"0.15","output_per_1m":"0.6",
		"cache_read_per_1m":"0.003","cache_write_per_1m":"0.3",
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
	if row.InputPer1M.String() != "0.15" || row.Multiplier != 1.5 || len(row.PeakRules) != 1 {
		t.Errorf("载荷里的价格没有完整落到实体上: %+v", row)
	}
}
